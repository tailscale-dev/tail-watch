package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func idsOf(t *testing.T, f *fakeSink) []string {
	t.Helper()
	var out []string
	for _, c := range f.snapshot() {
		var body struct {
			Events []Event `json:"events"`
		}
		if err := json.Unmarshal(c.Body, &body); err != nil {
			t.Fatal(err)
		}
		for _, e := range body.Events {
			out = append(out, e.ID)
		}
	}
	return out
}

func TestDownThenRecoveredDelivered(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{
			sinks:    []SinkConfig{testSink("a"), testSink("b")},
			stateDir: "",
			tags:     nil,
			intake:   0,
			scripts:  nil,
			clock:    nil,
		},
	)
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	h.n.ObserveTransition(upTransition(2, h.clock.Now()))
	for _, name := range []string{"a", "b"} {
		f := h.sinks[name]
		eventually(t, func() bool { return f.callCount() == 2 })
		got := f.eventTypes(t)
		if got[0] != EventDown || got[1] != EventRecovered {
			t.Fatalf("%s got %v", name, got)
		}
	}
	if n := testutil.ToFloat64(h.n.m.sentC.WithLabelValues("a", "target.down")); n != 1 {
		t.Fatalf("sent down = %v", n)
	}
}

func TestSilentStartupAndSuppression(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{testSink("a")}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	tr := upTransition(1, h.clock.Now())
	tr.From = StateUnknown
	h.n.ObserveTransition(tr)
	sup := downTransition(2, h.clock.Now())
	sup.To = StateSuppressed
	h.n.ObserveTransition(sup)
	close(h.alert.first)
	eventually(t, func() bool { return h.sinks["a"].callCount() == 1 })
	if got := h.sinks["a"].eventTypes(t); len(got) != 1 || got[0] != EventStarted {
		t.Fatalf("got %v, want only collector.started", got)
	}
}

func TestCollectorStartedOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{testSink("a")}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	close(h.alert.first)
	eventually(t, func() bool { return h.sinks["a"].callCount() == 1 })
	h.clock.Advance(time.Minute)
	time.Sleep(30 * time.Millisecond)
	if got := h.sinks["a"].callCount(); got != 1 {
		t.Fatalf("collector.started emitted %d times", got)
	}
}

func TestRetryScheduleUnderClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOpts{
		sinks: []SinkConfig{testSink("a")},
		scripts: map[string][]deliveryResult{"a": {
			statusRes(500), statusRes(500), statusRes(503),
		}}, stateDir: "", tags: nil, intake: 0, clock: nil,
	})
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	f := h.sinks["a"]
	for i, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		before := h.clock.timers.Load()
		eventually(t, func() bool { return f.callCount() == i+1 })
		h.clock.waitTimers(t, before)
		h.clock.Advance(wait - time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		if f.callCount() != i+1 {
			t.Fatalf("retry %d fired before %s", i+1, wait)
		}
		h.clock.Advance(time.Millisecond)
	}
	eventually(t, func() bool { return f.callCount() == 4 })
	calls := f.snapshot()
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if got := calls[i+1].At.Sub(calls[i].At); got != want {
			t.Fatalf("attempt %d delay %s want %s", i+2, got, want)
		}
	}
	if calls[0].ID != calls[3].ID || string(calls[0].Body) != string(calls[3].Body) {
		t.Fatal("retries must resend identical bytes")
	}
	if n := testutil.ToFloat64(h.n.m.failedC.WithLabelValues("a", failHTTP5xx)); n != 3 {
		t.Fatalf("failed_total = %v", n)
	}
}

func TestRetryAfterHonoured(t *testing.T) {
	t.Parallel()
	limited := deliveryResult{
		Class:      resultRetry,
		Status:     429,
		Reason:     failHTTP429,
		RetryAfter: 30 * time.Second,
		Err:        nil,
	}
	h := newHarness(t, harnessOpts{
		sinks: []SinkConfig{
			testSink("a"),
		},
		scripts:  map[string][]deliveryResult{"a": {limited}},
		stateDir: "",
		tags:     nil,
		intake:   0,
		clock:    nil,
	})
	h.start(t)
	before := h.clock.timers.Load()
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	f := h.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 1 })
	h.clock.waitTimers(t, before)
	h.clock.Advance(29 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if f.callCount() != 1 {
		t.Fatal("retried before Retry-After")
	}
	h.clock.Advance(time.Second)
	eventually(t, func() bool { return f.callCount() == 2 })
}

func TestRetryDelayCap(t *testing.T) {
	t.Parallel()
	want := []int64{1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300, 300}
	for i, w := range want {
		if got := retryDelay(i+1, 0.5, 0); got != time.Duration(w)*time.Second {
			t.Fatalf("attempt %d: %s want %s", i+1, got, time.Duration(w)*time.Second)
		}
	}
	if got := retryDelay(1, 0.999, 0); got < 1400*time.Millisecond || got > 1500*time.Millisecond {
		t.Fatalf("max jitter %s", got)
	}
	if got := retryDelay(1, 0, 0); got != 500*time.Millisecond {
		t.Fatalf("min jitter %s", got)
	}
	if got := retryDelay(20, 1.4, 0); got != maxRetryDelay {
		t.Fatalf("jittered delay must stay capped, got %s", got)
	}
	if got := retryDelay(1, 0.5, time.Hour); got != maxRetryDelay {
		t.Fatalf("Retry-After must be capped, got %s", got)
	}
}

func TestOrderingDownRetriedRecoveryQueued(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOpts{
		sinks: []SinkConfig{
			testSink("a"),
		},
		scripts:  map[string][]deliveryResult{"a": {statusRes(503)}},
		stateDir: "",
		tags:     nil,
		intake:   0,
		clock:    nil,
	})
	h.start(t)
	before := h.clock.timers.Load()
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	f := h.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 1 })
	h.clock.waitTimers(t, before)
	h.n.ObserveTransition(upTransition(2, h.clock.Now()))
	time.Sleep(30 * time.Millisecond)
	if f.callCount() != 1 {
		t.Fatal("recovery must wait behind the retrying down")
	}
	h.clock.Advance(time.Second)
	eventually(t, func() bool { return f.callCount() == 3 })
	got := f.eventTypes(t)
	if got[0] != EventDown || got[1] != EventDown || got[2] != EventRecovered {
		t.Fatalf("order %v", got)
	}
}

func TestBlockedReceiverDoesNotBlockOtherSink(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{
			sinks:    []SinkConfig{testSink("slow"), testSink("fast")},
			stateDir: "",
			tags:     nil,
			intake:   0,
			scripts:  nil,
			clock:    nil,
		},
	)
	release := make(chan struct{})
	h.sinks["slow"].block = release
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool { return h.sinks["fast"].callCount() == 1 })
	if h.sinks["slow"].callCount() != 0 {
		t.Fatal("slow sink should still be blocked")
	}
	h.n.ObserveTransition(upTransition(2, h.clock.Now()))
	eventually(t, func() bool { return h.sinks["fast"].callCount() == 2 })
	close(release)
	eventually(t, func() bool { return h.sinks["slow"].callCount() == 2 })
}

func TestIntakeNeverBlocks(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{testSink("a")}, stateDir: "", tags: nil, scripts: nil, clock: nil, intake: 2},
	)
	done := make(chan struct{})
	go func() {
		for i := range 50 {
			h.n.ObserveTransition(downTransition(uint64(i+1), h.clock.Now()))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ObserveTransition blocked with no coordinator running")
	}
	if n := testutil.ToFloat64(h.n.m.droppedC.WithLabelValues(sinkIntake, reasonQueueFull)); n != 48 {
		t.Fatalf("intake drops = %v", n)
	}
	h.alert.mu.Lock()
	defer h.alert.mu.Unlock()
	if !h.alert.dirty["nyc-nas"] {
		t.Fatal("dropped transition must mark the target dirty")
	}
}

func TestDroppedRecoveryReconciledFromSnapshot(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{testSink("a")}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	f := h.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 1 })

	snap := alertSnapshot{
		Sequence:  5,
		Name:      "nyc-nas",
		State:     StateUp,
		At:        h.clock.Now(),
		Target:    nasTarget(),
		DownSince: h.clock.Now(),
		Flapping:  false,
		Check:     "",
		Layer:     layerNone,
		Err:       nil,
		Since:     time.Time{},
		Failures:  0,
		Path:      pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
		Removed:   false,
		Dirty:     false,
	}
	h.alert.setSnapshot(snap)
	h.clock.Advance(reconcileInterval)
	eventually(t, func() bool { return f.callCount() == 2 })
	if got := f.eventTypes(t); got[1] != EventRecovered {
		t.Fatalf("got %v", got)
	}
	h.alert.mu.Lock()
	acked := h.alert.acked["nyc-nas"]
	dirty := h.alert.dirty["nyc-nas"]
	h.alert.mu.Unlock()
	if acked != 5 || dirty {
		t.Fatalf("snapshot not acknowledged: acked=%d dirty=%v", acked, dirty)
	}
	// A queued transition at or below the reconciled sequence is ignored.
	h.n.ObserveTransition(downTransition(4, h.clock.Now()))
	time.Sleep(30 * time.Millisecond)
	if f.callCount() != 2 {
		t.Fatal("stale transition must not open an incident")
	}
}

func TestDroppedDownReconciledFromSnapshot(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{testSink("a")}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	snap := alertSnapshot{
		Sequence:  3,
		Name:      "nyc-nas",
		State:     StateDown,
		Check:     "tcp:445",
		Layer:     layerService,
		At:        h.clock.Now(),
		Target:    nasTarget(),
		DownSince: h.clock.Now(),
		Flapping:  false,
		Err:       nil,
		Since:     time.Time{},
		Failures:  0,
		Path:      pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
		Removed:   false,
		Dirty:     false,
	}
	h.alert.setSnapshot(snap)
	h.clock.Advance(reconcileInterval)
	f := h.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 1 })
	if got := f.eventTypes(t); got[0] != EventDown {
		t.Fatalf("got %v", got)
	}
}

func TestNewerDirtyStatePreserved(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{testSink("a")}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	h.alert.setSnapshot(
		alertSnapshot{
			Sequence:  9,
			Name:      "nyc-nas",
			State:     StateUp,
			Target:    nasTarget(),
			Flapping:  false,
			Check:     "",
			Layer:     layerNone,
			Err:       nil,
			Since:     time.Time{},
			At:        time.Time{},
			Failures:  0,
			Path:      pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
			DownSince: time.Time{},
			Removed:   false,
			Dirty:     false,
		})
	h.n.ObserveTransition(downTransition(4, h.clock.Now()))
	time.Sleep(30 * time.Millisecond)
	h.alert.mu.Lock()
	dirty := h.alert.dirty["nyc-nas"]
	h.alert.mu.Unlock()
	if !dirty {
		t.Fatal("acknowledging seq 4 must leave the newer seq 9 mark dirty")
	}
}

func TestRemovedTargetClosesIncident(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{testSink("a")}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	f := h.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 1 })
	h.alert.setSnapshot(alertSnapshot{
		Sequence:  6,
		Name:      "nyc-nas",
		State:     StateDown,
		Removed:   true,
		Target:    nasTarget(),
		Flapping:  false,
		Check:     "",
		Layer:     layerNone,
		Err:       nil,
		Since:     time.Time{},
		At:        time.Time{},
		Failures:  0,
		Path:      pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
		DownSince: time.Time{},
		Dirty:     false,
	})
	h.clock.Advance(reconcileInterval)
	eventually(t, func() bool { return f.callCount() == 2 })
	var body struct {
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(f.snapshot()[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Events[0].Type != EventRecovered || body.Events[0].Reason != reasonRemoved {
		t.Fatalf("got %+v", body.Events[0])
	}
}

func TestFlappingNoticeDoesNotTouchIncident(t *testing.T) {
	t.Parallel()
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{testSink("a")}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	flap := downTransition(2, h.clock.Now())
	flap.Flapping = true
	h.n.ObserveTransition(flap)
	f := h.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 2 })
	if got := f.eventTypes(t); got[1] != EventFlapping {
		t.Fatalf("got %v", got)
	}
	h.n.mu.Lock()
	defer h.n.mu.Unlock()
	if h.n.b.openIncidentFor("nyc-nas") == nil {
		t.Fatal("flapping notice must leave the incident open")
	}
}

func TestRoutingUsesTags(t *testing.T) {
	t.Parallel()
	tagged := testSink("tagged")
	tagged.Match.Tags = []string{"tag:nas"}
	other := testSink("other")
	other.Match.Tags = []string{"tag:printer"}
	h := newHarness(t, harnessOpts{
		sinks: []SinkConfig{tagged, other},
		tags:  func(string) []string { return []string{"tag:nas"} }, stateDir: "", intake: 0, scripts: nil, clock: nil,
	})
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool { return h.sinks["tagged"].callCount() == 1 })
	time.Sleep(30 * time.Millisecond)
	if h.sinks["other"].callCount() != 0 {
		t.Fatal("tag filter should exclude the printer sink")
	}
}

func TestLateFlagFixedAtFirstAttempt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOpts{
		sinks: []SinkConfig{
			testSink("a"),
		},
		scripts:  map[string][]deliveryResult{"a": {statusRes(503)}},
		stateDir: "",
		tags:     nil,
		intake:   0,
		clock:    nil,
	})
	tr := downTransition(1, h.clock.Now())
	h.clock.Advance(5 * time.Minute)
	h.start(t)
	before := h.clock.timers.Load()
	h.n.ObserveTransition(tr)
	f := h.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 1 })
	h.clock.waitTimers(t, before)
	h.clock.Advance(time.Second)
	eventually(t, func() bool { return f.callCount() == 2 })
	calls := f.snapshot()
	var body struct {
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if !body.Events[0].Late {
		t.Fatal("event first attempted 5 minutes after creation must be late")
	}
	if string(calls[0].Body) != string(calls[1].Body) {
		t.Fatal("retry body changed")
	}
	if time.Time(body.Events[0].Time).Equal(h.clock.Now()) {
		t.Fatal("late event must keep its original time")
	}
}

func TestExpiredWhileRetrying(t *testing.T) {
	t.Parallel()
	sc := testSink("a")
	sc.MaxAge = time.Minute
	fail := make([]deliveryResult, 20)
	for i := range fail {
		fail[i] = statusRes(503)
	}
	h := newHarness(
		t,
		harnessOpts{
			sinks:    []SinkConfig{sc},
			scripts:  map[string][]deliveryResult{"a": fail},
			stateDir: "",
			tags:     nil,
			intake:   0,
			clock:    nil,
		},
	)
	h.start(t)
	before := h.clock.timers.Load()
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool { return h.sinks["a"].callCount() == 1 })
	h.clock.waitTimers(t, before)
	h.clock.Advance(2 * time.Minute)
	eventually(t, func() bool {
		return testutil.ToFloat64(h.n.m.droppedC.WithLabelValues("a", reasonExpired)) == 1
	})
}

func TestPermanentFailureDropsOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOpts{
		sinks: []SinkConfig{
			testSink("a"),
		},
		scripts:  map[string][]deliveryResult{"a": {statusRes(400)}},
		stateDir: "",
		tags:     nil,
		intake:   0,
		clock:    nil,
	})
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool {
		return testutil.ToFloat64(h.n.m.droppedC.WithLabelValues("a", reasonPermanent)) == 1
	})
	time.Sleep(30 * time.Millisecond)
	if h.sinks["a"].callCount() != 1 {
		t.Fatal("permanent failure must not retry")
	}
	if testutil.ToFloat64(h.n.m.failedC.WithLabelValues("a", failHTTP4xx)) != 1 {
		t.Fatal("permanent failure should count as failed")
	}
}
