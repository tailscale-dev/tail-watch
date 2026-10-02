package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:gochecknoglobals // Flag must be a package variable.
var update = flag.Bool("update", false, "rewrite golden files")

func assertGolden(t *testing.T, path string, raw []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		t.Fatalf("golden %s: %v", path, err)
	}
	buf.WriteByte('\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Fatalf("golden %s differs:\n--- want\n%s\n--- got\n%s", path, want, buf.Bytes())
	}
}

func TestNewIDShapeAndOrder(t *testing.T) {
	t.Parallel()
	clk := newCountingClock()
	a, err := newID(prefixEvent, clk.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Millisecond)
	b, err := newID(prefixEvent, clk.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		body, ok := strings.CutPrefix(id, prefixEvent)
		if !ok || len(body) != 26 {
			t.Fatalf("bad id %q", id)
		}
		for _, r := range body {
			if !strings.ContainsRune(idAlphabet, r) {
				t.Fatalf("id %q has character %q outside Crockford base32", id, r)
			}
		}
	}
	if a >= b {
		t.Fatalf("ids do not sort by time: %s >= %s", a, b)
	}
	batch, err := newID(prefixBatch, clk.Now(), rand.Reader)
	if err != nil || !strings.HasPrefix(batch, "bat_") {
		t.Fatalf("batch id %q, %v", batch, err)
	}
}

func TestNewIDKnownVector(t *testing.T) {
	t.Parallel()
	zero := bytes.NewReader(make([]byte, 10))
	id, err := newID(prefixEvent, time.UnixMilli(0), zero)
	if err != nil {
		t.Fatal(err)
	}
	if id != "evt_00000000000000000000000000" {
		t.Fatalf("zero id %q", id)
	}
	ones := bytes.NewReader(bytes.Repeat([]byte{0xff}, 10))
	id, err = newID(prefixEvent, time.UnixMilli(1<<48-1), ones)
	if err != nil {
		t.Fatal(err)
	}
	if id != "evt_7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Fatalf("max id %q", id)
	}
}

func goldenCollector() CollectorInfo {
	return CollectorInfo{Name: "monitor-nyc", TailscaleIP: "100.101.102.103"}
}

func goldenTransition() Transition {
	at := time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC)
	return Transition{
		Sequence:   7,
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
		Path:       pathInfo{Kind: "direct", Endpoint: "203.0.113.5:41641", DERP: "", Latency: 12 * time.Millisecond},
		Dependents: nil,
		DownSince:  at.Add(-90 * time.Second),
	}
}

func marshalEvent(t *testing.T, ev Event) []byte {
	t.Helper()
	b, err := marshalJSON(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPayloadGolden(t *testing.T) {
	t.Parallel()
	tr := goldenTransition()
	down := newDownEvent("evt_01JAB3K9T6M4X2Q8R7N5W0YZCD", goldenCollector(), tr)
	inc := &incident{
		ID: "inc_test", Status: incidentOpen, Target: targetDefOf(nasTarget()), Down: down,
		DownSince: tr.At.Add(-90 * time.Second), Recovery: nil, Sinks: map[string]*sinkDelivery{},
	}
	recAt := tr.At.Add(12 * time.Minute)

	implicit := goldenTransition()
	implicit.Target = Target{
		Dynamic: false,
		Name:    "nyc-router", Addr: "100.64.0.9", Site: "nyc", Severity: SeverityPage, Implicit: true,
		Parent: "", InsecureTLS: false, Interval: 0, Timeout: 0, FailThreshold: 0, RecoverThreshold: 0,
		FlapLimit: 0, FlapWindow: 0, Network: "", Checks: nil, StableID: "", NodeID: 0,
	}
	implicit.Layer = layerUnrouted
	implicit.Dependents = []string{"nyc-nas", "nyc-cam"}

	flap := goldenTransition()
	flap.Flapping = true

	warn := goldenTransition()
	warn.Target.Severity = SeverityWarn
	warn.Layer = layerLAN

	cases := map[string]Event{
		"target.down":          down,
		"target.down.implicit": newDownEvent("evt_01JAB3K9T6M4X2Q8R7N5W0YZCE", goldenCollector(), implicit),
		"target.down.warn":     newDownEvent("evt_01JAB3K9T6M4X2Q8R7N5W0YZCF", goldenCollector(), warn),
		"target.recovered": newRecoveredEvent(
			"evt_01JAB3K9T6M4X2Q8R7N5W0YZCG",
			goldenCollector(),
			inc,
			recAt,
			[]string{"nyc-cam"},
			"",
		),
		"target.recovered.removed": newRecoveredEvent(
			"evt_01JAB3K9T6M4X2Q8R7N5W0YZCH",
			goldenCollector(),
			inc,
			recAt,
			nil,
			reasonRemoved,
		),
		"target.flapping":   newFlappingEvent("evt_01JAB3K9T6M4X2Q8R7N5W0YZCJ", goldenCollector(), flap),
		"collector.started": newStartedEvent("evt_01JAB3K9T6M4X2Q8R7N5W0YZCK", goldenCollector(), tr.At),
		"test": newTestEvent(
			"evt_01JAB3K9T6M4X2Q8R7N5W0YZCM",
			CollectorInfo{Name: "monitor-nyc", TailscaleIP: ""},
			tr.At,
		),
	}
	for name, ev := range cases {
		assertGolden(t, filepath.Join("testdata", "payload", name+".json"), marshalEvent(t, ev))
	}
}

func TestPayloadContract(t *testing.T) {
	t.Parallel()
	tr := goldenTransition()
	down := newDownEvent("evt_A", goldenCollector(), tr)
	decode := func(ev Event) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(marshalEvent(t, ev), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := decode(down)
	if m["dependents"] == nil {
		t.Fatal("down payload must carry a non-nil dependents array")
	}
	if _, ok := m["duration_seconds"]; ok {
		t.Fatal("down payload must omit duration_seconds")
	}
	rec := newRecoveredEvent("evt_B", goldenCollector(), &incident{
		ID: "inc_test2", Status: incidentOpen, Target: targetDefOf(nasTarget()), Down: down,
		DownSince: tr.At.Add(-90 * time.Second), Recovery: nil, Sinks: map[string]*sinkDelivery{},
	}, tr.At.Add(12*time.Minute), nil, "")
	if _, ok := decode(rec)["layer"]; ok {
		t.Fatal("recovered payload must omit layer")
	}
	if got := *rec.DurationSeconds; got != 12*60+90 {
		t.Fatalf("duration_seconds = %d", got)
	}
	if rec.DownEventID != "evt_A" || rec.Severity != down.Severity {
		t.Fatalf("recovered does not reference its down: %+v", rec)
	}
	started := newStartedEvent("evt_C", goldenCollector(), tr.At)
	m = decode(started)
	for _, k := range []string{"target", "layer", "dependents", "check"} {
		if _, ok := m[k]; ok {
			t.Fatalf("collector.started must omit %s", k)
		}
	}
	if m["version"].(float64) != 1 {
		t.Fatal("version must be 1")
	}
}

func TestStartedPayloadLocalOnlyIdentity(t *testing.T) {
	t.Parallel()
	ev := newStartedEvent(
		"evt_L",
		CollectorInfo{Name: "pg-ingress-0", TailscaleIP: ""},
		goldenTransition().At,
	)
	var m map[string]any
	if err := json.Unmarshal(marshalEvent(t, ev), &m); err != nil {
		t.Fatal(err)
	}
	col, ok := m["collector"].(map[string]any)
	if !ok {
		t.Fatalf("collector %v", m["collector"])
	}
	if col["name"] != "pg-ingress-0" {
		t.Fatalf("collector.name %v", col["name"])
	}
	if _, hasIP := col["tailscale_ip"]; hasIP {
		t.Fatal("local-only payload must omit collector.tailscale_ip")
	}
}

func TestMapTransition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		from State
		to   State
		flap bool
		open bool
		want mapKind
	}{
		{"UNKNOWN to DOWN pages", StateUnknown, StateDown, false, false, mapDown},
		{"UP to DOWN", StateUp, StateDown, false, false, mapDown},
		{"SUPPRESSED to DOWN", StateSuppressed, StateDown, false, false, mapDown},
		{"DOWN again with open incident dedupes", StateSuppressed, StateDown, false, true, mapNone},
		{"DOWN to UP with open incident", StateDown, StateUp, false, true, mapRecovered},
		{"SUPPRESSED to UP with open incident", StateSuppressed, StateUp, false, true, mapRecovered},
		{"UNKNOWN to UP is silent", StateUnknown, StateUp, false, false, mapNone},
		{"UP without incident is silent", StateDown, StateUp, false, false, mapNone},
		{"to SUPPRESSED is silent", StateUp, StateSuppressed, false, false, mapNone},
		{"to SUPPRESSED with open incident is silent", StateDown, StateSuppressed, false, true, mapNone},
		{"flapping warns", StateUp, StateDown, true, false, mapFlapping},
		{"flapping warns with open incident", StateDown, StateUp, true, true, mapFlapping},
	}
	for _, tt := range tests {
		got := mapTransition(Transition{
			Sequence: 0,
			Target: Target{
				Dynamic:          false,
				Name:             "",
				Addr:             "",
				Site:             "",
				Severity:         SeverityPage,
				Parent:           "",
				InsecureTLS:      false,
				Interval:         0,
				Timeout:          0,
				FailThreshold:    0,
				RecoverThreshold: 0,
				FlapLimit:        0,
				FlapWindow:       0,
				Network:          NetworkTailnet,
				Checks:           nil,
				Implicit:         false,
				StableID:         "",
				NodeID:           0,
			},
			From:       tt.from,
			To:         tt.to,
			Flapping:   tt.flap,
			Check:      "",
			Layer:      "",
			Err:        nil,
			Since:      time.Time{},
			At:         time.Time{},
			Failures:   0,
			Path:       pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
			Dependents: nil,
			DownSince:  time.Time{},
		}, tt.open)
		if got != tt.want {
			t.Errorf("%s: got %v want %v", tt.name, got, tt.want)
		}
	}
}

func TestDownLayers(t *testing.T) {
	t.Parallel()
	for _, l := range []layer{layerUnrouted, layerControl, layerTunnel, layerLAN, layerService} {
		tr := goldenTransition()
		tr.Layer = l
		if got := newDownEvent("evt_A", goldenCollector(), tr).Layer; got != string(l) {
			t.Fatalf("layer %q became %q", l, got)
		}
	}
}

func TestSeverityRules(t *testing.T) {
	t.Parallel()
	tr := goldenTransition()
	if newDownEvent("evt_A", goldenCollector(), tr).Severity != SeverityPage {
		t.Fatal("default severity is page")
	}
	tr.Target.Severity = SeverityWarn
	down := newDownEvent("evt_A", goldenCollector(), tr)
	if down.Severity != SeverityWarn {
		t.Fatal("warn target should produce warn down")
	}
	if newRecoveredEvent("evt_B", goldenCollector(), &incident{
		ID: "inc_test3", Status: incidentOpen, Target: targetDefOf(nasTarget()), Down: down,
		DownSince: tr.At.Add(-90 * time.Second), Recovery: nil, Sinks: map[string]*sinkDelivery{},
	}, tr.At, nil, "").Severity != SeverityWarn {
		t.Fatal("recovery takes the down's severity")
	}
	if newFlappingEvent("evt_C", goldenCollector(), tr).Severity != SeverityWarn {
		t.Fatal("flapping is warn")
	}
	if newStartedEvent("evt_D", goldenCollector(), tr.At).Severity != SeverityInfo ||
		newTestEvent("evt_E", goldenCollector(), tr.At).Severity != SeverityInfo {
		t.Fatal("started and test are info")
	}
}

func TestSinkAccepts(t *testing.T) {
	t.Parallel()
	tr := goldenTransition()
	down := newDownEvent("evt_A", goldenCollector(), tr)
	flap := newFlappingEvent("evt_B", goldenCollector(), tr)
	started := newStartedEvent("evt_C", goldenCollector(), tr.At)
	test := newTestEvent("evt_D", goldenCollector(), tr.At)
	base := testSink("s")
	with := func(mut func(*SinkConfig)) SinkConfig {
		s := base
		mut(&s)
		return s
	}
	tests := []struct {
		name string
		sink SinkConfig
		ev   Event
		tags []string
		want bool
	}{
		{"defaults accept all", base, down, nil, true},
		{"severity floor rejects warn", with(func(s *SinkConfig) { s.MinSeverity = SeverityPage }), flap, nil, false},
		{"severity floor accepts page", with(func(s *SinkConfig) { s.MinSeverity = SeverityPage }), down, nil, true},
		{"events list rejects", with(func(s *SinkConfig) { s.Events = []EventType{EventRecovered} }), down, nil, false},
		{"events list accepts", with(func(s *SinkConfig) { s.Events = []EventType{EventDown} }), down, nil, true},
		{"site rejects", with(func(s *SinkConfig) { s.Match.Sites = []string{"sfo", "lon"} }), down, nil, false},
		{"site accepts", with(func(s *SinkConfig) { s.Match.Sites = []string{"nyc", "sfo"} }), down, nil, true},
		{"target glob accepts", with(func(s *SinkConfig) { s.Match.Targets = []string{"nyc-*"} }), down, nil, true},
		{"target glob rejects", with(func(s *SinkConfig) { s.Match.Targets = []string{"sfo-*"} }), down, nil, false},
		{
			"tag accepts",
			with(func(s *SinkConfig) { s.Match.Tags = []string{"tag:nas"} }),
			down,
			[]string{"tag:nas"},
			true,
		},
		{
			"tag rejects",
			with(func(s *SinkConfig) { s.Match.Tags = []string{"tag:nas"} }),
			down,
			[]string{"tag:other"},
			false,
		},
		{
			"untagged never matches tags",
			with(func(s *SinkConfig) { s.Match.Tags = []string{"tag:nas"} }),
			down,
			nil,
			false,
		},
		{"every match key must hold", with(func(s *SinkConfig) {
			s.Match.Sites = []string{"nyc"}
			s.Match.Targets = []string{"sfo-*"}
		}), down, nil, false},
		{"started ignores match", with(func(s *SinkConfig) { s.Match.Sites = []string{"sfo"} }), started, nil, true},
		{
			"started still honours events",
			with(func(s *SinkConfig) { s.Events = []EventType{EventDown} }),
			started,
			nil,
			false,
		},
		{"test bypasses everything", with(func(s *SinkConfig) {
			s.MinSeverity = SeverityPage
			s.Events = []EventType{EventDown}
			s.Match.Sites = []string{"sfo"}
		}), test, nil, true},
	}
	for _, tt := range tests {
		if got := tt.sink.accepts(tt.ev, tt.tags); got != tt.want {
			t.Errorf("%s: got %v want %v", tt.name, got, tt.want)
		}
	}
}
