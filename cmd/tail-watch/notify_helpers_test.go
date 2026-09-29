package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/tstest"
	"tailscale.com/tstime"
)

func testNow() time.Time { return time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC) }

// countingClock wraps a tstest.Clock and counts timers so a test can wait for a
// goroutine to arm its timer before advancing time.
type countingClock struct {
	*tstest.Clock

	timers  atomic.Int64
	tickers atomic.Int64
}

func newCountingClock() *countingClock {
	return &countingClock{Clock: tstest.NewClock(tstest.ClockOpts{
		Start: testNow(), Step: 0, TimerChannelSize: 0, FollowRealTime: false,
	}), timers: atomic.Int64{}, tickers: atomic.Int64{}}
}

func (c *countingClock) NewTimer(d time.Duration) (tstime.TimerController, <-chan time.Time) {
	c.timers.Add(1)
	return c.Clock.NewTimer(d)
}

func (c *countingClock) NewTicker(d time.Duration) (tstime.TickerController, <-chan time.Time) {
	c.tickers.Add(1)
	return c.Clock.NewTicker(d)
}

func (c *countingClock) AfterFunc(d time.Duration, f func()) tstime.TimerController {
	return c.Clock.AfterFunc(d, f)
}

// waitTimers blocks until the clock has handed out more than prev timers.
func (c *countingClock) waitTimers(t *testing.T, prev int64) {
	t.Helper()
	eventually(t, func() bool { return c.timers.Load() > prev })
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// fakeSink is a scripted receiver.
type fakeSink struct {
	name     string
	interval time.Duration
	mu       sync.Mutex
	script   []deliveryResult
	calls    []fakeCall
	block    chan struct{}
}

type fakeCall struct {
	ID   string
	Body []byte
	At   time.Time
}

func (f *fakeSink) Name() string { return f.name }

func (f *fakeSink) MinInterval() time.Duration { return f.interval }

func (f *fakeSink) Render(evs []Event, id string) ([]byte, error) {
	return marshalJSON(map[string]any{"id": id, "events": evs})
}

func (f *fakeSink) Send(ctx context.Context, body []byte, m sendMeta) deliveryResult {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return deliveryResult{Class: resultRetry, Status: 0, Reason: failTimeout, RetryAfter: 0, Err: ctx.Err()}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{ID: m.ID, Body: append([]byte(nil), body...), At: m.At})
	if len(f.script) > 0 {
		res := f.script[0]
		f.script = f.script[1:]
		return res
	}
	return deliveryResult{Class: resultOK, Status: 200, Reason: "", RetryAfter: 0, Err: nil}
}

func (f *fakeSink) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSink) snapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// eventTypes decodes the event types of every delivered call in order.
func (f *fakeSink) eventTypes(t *testing.T) []EventType {
	t.Helper()
	var out []EventType
	for _, c := range f.snapshot() {
		var body struct {
			Events []Event `json:"events"`
		}
		if err := json.Unmarshal(c.Body, &body); err != nil {
			t.Fatal(err)
		}
		for _, e := range body.Events {
			out = append(out, e.Type)
		}
	}
	return out
}

func statusRes(status int) deliveryResult { return classifyStatus(status, nil, testNow()) }

type notifyHarness struct {
	n     *notifier
	clock *countingClock
	sinks map[string]*fakeSink
	reg   *prometheus.Registry
	alert *fakeAlerts
}

type fakeAlerts struct {
	mu    sync.Mutex
	snaps map[string]alertSnapshot
	acked map[string]uint64
	dirty map[string]bool
	first chan struct{}
}

func newFakeAlerts() *fakeAlerts {
	return &fakeAlerts{
		mu:    sync.Mutex{},
		snaps: map[string]alertSnapshot{},
		acked: map[string]uint64{},
		dirty: map[string]bool{},
		first: make(chan struct{}),
	}
}

func (f *fakeAlerts) Snapshots() []alertSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []alertSnapshot
	for name, s := range f.snaps {
		s.Dirty = f.dirty[name]
		out = append(out, s)
	}
	return out
}

func (f *fakeAlerts) Acknowledge(name string, seq uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.snaps[name]; ok && seq < s.Sequence {
		return
	}
	f.acked[name] = seq
	f.dirty[name] = false
	if s, ok := f.snaps[name]; ok && s.Removed {
		delete(f.snaps, name)
	}
}

func (f *fakeAlerts) MarkDirty(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirty[name] = true
}

func (f *fakeAlerts) FirstRoundDone() <-chan struct{} { return f.first }

func (f *fakeAlerts) setSnapshot(s alertSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snaps[s.Name] = s
	f.dirty[s.Name] = true
}

type harnessOpts struct {
	sinks    []SinkConfig
	stateDir string
	tags     func(string) []string
	intake   int
	scripts  map[string][]deliveryResult
	clock    *countingClock
}

func testSink(name string) SinkConfig {
	return SinkConfig{
		Name:          name,
		Type:          sinkWebhook,
		URL:           secretRef{Env: "", File: ""},
		Secret:        secretRef{Env: "", File: ""},
		HeadersEnv:    nil,
		MinSeverity:   SeverityInfo,
		Events:        nil,
		Match:         MatchConfig{Sites: nil, Targets: nil, Tags: nil},
		MaxAge:        time.Hour,
		Queue:         queueMemory,
		QueueSize:     1000,
		Batch:         false,
		GroupWait:     10 * time.Second,
		MaxBatch:      100,
		MentionOnPage: "",
		StatusURL:     "",
	}
}

func newHarness(t *testing.T, o harnessOpts) *notifyHarness {
	t.Helper()
	h := &notifyHarness{
		n: nil, clock: o.clock, sinks: map[string]*fakeSink{}, reg: prometheus.NewRegistry(), alert: newFakeAlerts(),
	}
	if h.clock == nil {
		h.clock = newCountingClock()
	}
	for _, sc := range o.sinks {
		h.sinks[sc.Name] = &fakeSink{
			name:     sc.Name,
			script:   o.scripts[sc.Name],
			interval: 0,
			mu:       sync.Mutex{},
			calls:    nil,
			block:    nil,
		}
	}
	n, err := newNotifier(notifierConfig{
		Clock: h.clock, Log: slog.New(slog.DiscardHandler), Sinks: o.sinks,
		Collector: CollectorInfo{Name: "monitor-nyc", TailscaleIP: "100.101.102.103"},
		StateDir:  o.stateDir, Alerts: h.alert, Tags: o.tags, Registry: h.reg,
		NewSink: func(sc SinkConfig) (sink, error) {
			f, ok := h.sinks[sc.Name]
			if !ok {
				return nil, errors.New("no fake sink " + sc.Name)
			}
			return f, nil
		},
		Env:       nil,
		Jitter:    func() float64 { return 0.5 },
		IntakeCap: o.intake,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.n = n
	return h
}

func (h *notifyHarness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.n.Start(ctx)
	eventually(t, func() bool { return h.clock.tickers.Load() > 0 })
	t.Cleanup(func() { cancel(); h.n.Stop() })
}

func nasTarget() Target {
	return Target{
		Name: "nyc-nas", Addr: "10.20.0.50", Site: "nyc", Severity: SeverityPage, Parent: "nyc-router",
		InsecureTLS: false, Interval: 0, Timeout: 0, FailThreshold: 0, RecoverThreshold: 0,
		FlapLimit: 0, FlapWindow: 0, Network: "", Checks: nil, Implicit: false, StableID: "", NodeID: 0,
	}
}

func downTransition(seq uint64, at time.Time) Transition {
	return Transition{
		Sequence:   seq,
		Target:     nasTarget(),
		From:       StateUp,
		To:         StateDown,
		Flapping:   false,
		Check:      "tcp:445",
		Layer:      layerService,
		Err:        errors.New("connection refused"),
		Since:      at.Add(-90 * time.Second),
		At:         at,
		Failures:   3,
		Path:       pathInfo{Kind: "direct", Endpoint: "", DERP: "", Latency: 12 * time.Millisecond},
		Dependents: nil,
		DownSince:  at.Add(-90 * time.Second),
	}
}

func upTransition(seq uint64, at time.Time) Transition {
	return Transition{
		Sequence:   seq,
		Target:     nasTarget(),
		From:       StateDown,
		To:         StateUp,
		Flapping:   false,
		Check:      "",
		Layer:      "",
		Err:        nil,
		Since:      time.Time{},
		At:         at,
		Failures:   0,
		Path:       pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
		Dependents: nil,
		DownSince:  time.Time{},
	}
}

// realSinkHarness builds the notifier around the real webhook and Slack sinks
// instead of fakes.
func realSinkHarness(t *testing.T, o harnessOpts) *notifyHarness {
	t.Helper()
	h := &notifyHarness{
		n: nil, clock: o.clock, sinks: map[string]*fakeSink{}, reg: prometheus.NewRegistry(), alert: newFakeAlerts(),
	}
	if h.clock == nil {
		h.clock = newCountingClock()
	}
	col := CollectorInfo{Name: "monitor-nyc", TailscaleIP: "100.101.102.103"}
	n, err := newNotifier(notifierConfig{
		Clock: h.clock, Log: slog.New(slog.DiscardHandler), Sinks: o.sinks, Collector: col,
		StateDir: o.stateDir, Alerts: h.alert, Tags: o.tags, Registry: h.reg,
		NewSink: nil, Env: nil, Jitter: func() float64 { return 0.5 }, IntakeCap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.n = n
	return h
}
