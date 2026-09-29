package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/tailcfg"
)

// seedOpenIncident runs a first notifier until its down is acknowledged, then
// stops it, leaving an open incident in dir.
func seedOpenIncident(t *testing.T, dir string, tr Transition) {
	t.Helper()
	first := newDiskNotifier(t, dir, testSink("a"))
	first.start(t)
	first.n.ObserveTransition(tr)
	eventually(t, func() bool { return first.sinks["a"].callCount() == 1 })
	eventually(t, func() bool {
		st := readState(t, dir)
		return len(st.Incidents) == 1 && st.Incidents[0].Sinks["a"].Down == phaseAcked
	})
	first.n.Stop()
}

type alertedNotifier struct {
	*notifyHarness

	al *alerter
}

// newAlertedNotifier wires a notifier to a real alerter, as main does.
func newAlertedNotifier(t *testing.T, dir string, targets []*Target) *alertedNotifier {
	t.Helper()
	return newAlertedNotifierParents(t, dir, targets, nil)
}

func newAlertedNotifierParents(
	t *testing.T,
	dir string,
	targets []*Target,
	parents map[string]string,
) *alertedNotifier {
	t.Helper()
	clk := newCountingClock()
	al, err := newAlerter(alertConfig{
		Clock: clk, Targets: targets, Parents: parents, Recheck: nil, Registerer: prometheus.NewRegistry(), QueueCap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(al.stopDrain)
	sc := testSink("a")
	sc.Queue = queueDisk
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: dir, clock: clk, tags: nil, intake: 0, scripts: nil},
	)
	h.n.cfg.Alerts = al
	al.addConsumer(h.n)
	return &alertedNotifier{notifyHarness: h, al: al}
}

type fakeImplicit struct {
	al       *alerter
	restored []*Target
}

func (f *fakeImplicit) RestoreImplicit(t *Target) {
	f.restored = append(f.restored, t)
	_ = f.al.AddTarget(t)
}

func downIDs(t *testing.T, f *fakeSink) ([]Event, []Event) {
	t.Helper()
	var downs, recoveries []Event
	for _, c := range f.snapshot() {
		var body struct {
			Events []Event `json:"events"`
		}
		if err := json.Unmarshal(c.Body, &body); err != nil {
			t.Fatal(err)
		}
		for _, e := range body.Events {
			switch e.Type {
			case EventDown:
				downs = append(downs, e)
			case EventRecovered:
				recoveries = append(recoveries, e)
			case EventFlapping, EventDegraded, EventStarted, EventTest:
			}
		}
	}
	return downs, recoveries
}

func TestRestartDuringOutageRecoversWithOriginalID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := newCountingClock()
	seedOpenIncident(t, dir, downTransition(1, clk.Now()))
	var origID string
	for _, inc := range readState(t, dir).Incidents {
		origID = inc.ID
	}

	nas := alertTarget("nyc-nas", "icmp")
	an := newAlertedNotifier(t, dir, []*Target{nas})
	an.n.restoreOpen(map[string]bool{"nyc-nas": true}, an.al, nil)
	if !an.al.IsDown("nyc-nas") {
		t.Fatal("open incident must restore the target as DOWN")
	}
	an.start(t)
	failN(an.al, nas, "icmp", layerService, 3)
	time.Sleep(50 * time.Millisecond)
	if downs, _ := downIDs(t, an.sinks["a"]); len(downs) != 0 {
		t.Fatal("a restored outage must not page again")
	}
	recoverTarget(an.al, nas)
	f := an.sinks["a"]
	eventually(t, func() bool { _, r := downIDs(t, f); return len(r) == 1 })
	downs, recs := downIDs(t, f)
	if len(downs) != 0 || len(recs) != 1 || recs[0].DownEventID != origID {
		t.Fatalf("downs=%d recoveries=%+v want recovery of %s", len(downs), recs, origID)
	}
}

func TestImplicitIncidentSurvivesFailedDiscovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := newCountingClock()
	tr := downTransition(1, clk.Now())
	tr.Target = Target{
		Name:     "nyc-router",
		Addr:     "100.64.0.9",
		Site:     "nyc",
		Severity: SeverityPage,
		Implicit: true,
		StableID: tailcfg.StableNodeID(
			"nSTABLE1",
		),
		NodeID:           42,
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
	}
	seedOpenIncident(t, dir, tr)

	an := newAlertedNotifier(t, dir, nil)
	fi := &fakeImplicit{al: an.al, restored: nil}
	an.n.restoreOpen(map[string]bool{}, an.al, fi)
	if len(fi.restored) != 1 {
		t.Fatalf("restored %d implicit targets", len(fi.restored))
	}
	got := fi.restored[0]
	if got.Name != "nyc-router" || !got.Implicit || got.StableID != "nSTABLE1" || got.NodeID != 42 ||
		got.Addr != "100.64.0.9" {
		t.Fatalf("implicit identity not preserved: %+v", got)
	}
	if !an.al.IsDown("nyc-router") {
		t.Fatal("recreated router must start DOWN")
	}
	an.start(t)
	an.clock.Advance(3 * reconcileInterval)
	time.Sleep(50 * time.Millisecond)
	if _, recs := downIDs(t, an.sinks["a"]); len(recs) != 0 || len(an.n.openIncidents()) != 1 {
		t.Fatal("failed discovery must keep the incident without a removed recovery")
	}
}

func TestRestoreRefusalKeepsIncidentAndDedupes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := newCountingClock()
	seedOpenIncident(t, dir, downTransition(1, clk.Now()))
	var origID string
	for _, inc := range readState(t, dir).Incidents {
		origID = inc.ID
	}

	nas := alertTarget("nyc-nas", "icmp")
	an := newAlertedNotifier(t, dir, []*Target{nas})
	pass(an.al, nas)
	an.n.restoreOpen(map[string]bool{"nyc-nas": true}, an.al, nil)
	if len(an.n.openIncidents()) != 1 {
		t.Fatal("a refused restore must keep the incident")
	}
	an.start(t)
	failN(an.al, nas, "icmp", layerService, 3)
	time.Sleep(50 * time.Millisecond)
	if downs, _ := downIDs(t, an.sinks["a"]); len(downs) != 0 {
		t.Fatal("another down while the incident is open must be deduplicated")
	}
	recoverTarget(an.al, nas)
	f := an.sinks["a"]
	eventually(t, func() bool { _, r := downIDs(t, f); return len(r) == 1 })
	_, recs := downIDs(t, f)
	if len(recs) != 1 || recs[0].DownEventID != origID {
		t.Fatalf("recovery must use the original metadata: %+v", recs)
	}
}

func TestConfiguredTargetRemovedWhileOpenClosesRemoved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := newCountingClock()
	seedOpenIncident(t, dir, downTransition(1, clk.Now()))
	an := newAlertedNotifier(t, dir, nil)
	an.n.restoreOpen(map[string]bool{}, an.al, nil)
	an.start(t)
	f := an.sinks["a"]
	eventually(t, func() bool { _, r := downIDs(t, f); return len(r) == 1 })
	_, recs := downIDs(t, f)
	if len(recs) != 1 || recs[0].Reason != reasonRemoved {
		t.Fatalf("got %+v", recs)
	}
	eventually(t, func() bool {
		return len(readState(t, dir).Incidents) == 0
	})
}

func TestClosingIncidentNotRestoredAsDown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := newDiskNotifier(t, dir, testSink("a"))
	first.n.mu.Lock()
	first.n.applyLocked(downTransition(1, first.clock.Now()), false)
	first.n.applyLocked(upTransition(2, first.clock.Now()), false)
	first.n.commitLocked()
	first.n.mu.Unlock()
	second := newDiskNotifier(t, dir, testSink("a"))
	if len(second.n.openIncidents()) != 0 {
		t.Fatal("a closing incident must not be restored as open")
	}
	second.n.Start(t.Context())
	defer second.n.Stop()
	eventually(t, func() bool { return second.sinks["a"].callCount() == 2 })
}

func TestRestoredImplicitIncidentClosesWhenRemoved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := newCountingClock()
	tr := downTransition(1, clk.Now())
	tr.Target = Target{
		Name: "nyc-router", Addr: "100.64.0.9", Site: "nyc", Severity: SeverityPage, Implicit: true,
		StableID: tailcfg.StableNodeID("nSTABLE1"), NodeID: 42, Parent: "", InsecureTLS: false, Interval: 0,
		Timeout: 0, FailThreshold: 0, RecoverThreshold: 0, FlapLimit: 0, FlapWindow: 0,
		Network: NetworkTailnet, Checks: nil,
	}
	seedOpenIncident(t, dir, tr)
	var origID string
	for _, inc := range readState(t, dir).Incidents {
		origID = inc.ID
	}

	an := newAlertedNotifier(t, dir, nil)
	fi := &fakeImplicit{al: an.al, restored: nil}
	an.n.restoreOpen(map[string]bool{}, an.al, fi)
	an.start(t)
	if err := an.al.RemoveTarget("nyc-router"); err != nil {
		t.Fatalf("authoritative removal of a down implicit target must succeed: %v", err)
	}
	an.clock.Advance(reconcileInterval)
	f := an.sinks["a"]
	eventually(t, func() bool { _, r := downIDs(t, f); return len(r) == 1 })
	_, recs := downIDs(t, f)
	if recs[0].Reason != reasonRemoved || recs[0].DownEventID != origID {
		t.Fatalf("got %+v, want a removed recovery of %s", recs[0], origID)
	}
	if len(an.n.openIncidents()) != 0 {
		t.Fatal("incident must be closed after removal")
	}
}

func TestPagedTargetKeepsIncidentWhenParentFails(t *testing.T) {
	t.Parallel()
	router := alertTarget("router", "tsping")
	nas := alertTarget("nas", "icmp")
	an := newAlertedNotifierParents(t, t.TempDir(), []*Target{router, nas}, map[string]string{"nas": "router"})
	an.start(t)
	pass(an.al, router)
	pass(an.al, nas)
	failN(an.al, nas, "icmp", layerService, 3)
	f := an.sinks["a"]
	eventually(t, func() bool { d, _ := downIDs(t, f); return len(d) == 1 })
	failN(an.al, router, "tsping", layerService, 3)
	eventually(t, func() bool { d, _ := downIDs(t, f); return len(d) == 2 })
	recoverTarget(an.al, nas)
	eventually(t, func() bool { _, r := downIDs(t, f); return len(r) == 1 })
	downs, recs := downIDs(t, f)
	if recs[0].Target.Name != "nas" || recs[0].DownEventID != downs[0].ID {
		t.Fatalf("nas must recover exactly once against its own down: %+v", recs)
	}
	nasDowns := 0
	for _, d := range downs {
		if d.Target.Name == "nas" {
			nasDowns++
		}
	}
	if nasDowns != 1 {
		t.Fatalf("nas paged %d times, want 1", nasDowns)
	}
}

func TestReplayExpiresStaleDownAndItsRecovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := newDiskNotifier(t, dir, testSink("a"))
	first.n.mu.Lock()
	first.n.applyLocked(downTransition(1, first.clock.Now()), false)
	first.clock.Advance(50 * time.Minute)
	first.n.applyLocked(upTransition(2, first.clock.Now()), false)
	first.n.commitLocked()
	first.n.mu.Unlock()

	late := newCountingClock()
	late.Advance(70 * time.Minute)
	sc := testSink("a")
	sc.Queue = queueDisk
	second := newHarness(t, harnessOpts{
		sinks: []SinkConfig{sc}, stateDir: dir, tags: nil, intake: 0, scripts: nil, clock: late,
	})
	if n := testutil.ToFloat64(second.n.m.droppedC.WithLabelValues("a", reasonExpired)); n != 1 {
		t.Fatalf("expired drops = %v, want only the stale down", n)
	}
	st := readState(t, dir)
	if len(st.Incidents) != 0 || len(st.Queues["a"]) != 0 {
		t.Fatalf("the down and its dependent recovery must both be gone in one commit: %+v", st)
	}
	second.start(t)
	time.Sleep(50 * time.Millisecond)
	if second.sinks["a"].callCount() != 0 {
		t.Fatal("an orphan recovery was sent for a down that never went out")
	}
}

func TestReplayBatchRetryKeepsIDAndBody(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sc := testSink("a")
	sc.Queue = queueDisk
	sc.Batch = true
	first := newHarness(t, harnessOpts{
		sinks: []SinkConfig{sc}, stateDir: dir, tags: nil, intake: 0, scripts: nil, clock: nil,
	})
	first.sinks["a"].block = make(chan struct{})
	first.start(t)
	first.n.ObserveTransition(downTransition(1, first.clock.Now()))
	first.clock.waitTimers(t, 0)
	first.clock.Advance(sc.GroupWait)
	eventually(t, func() bool { return len(readState(t, dir).Units) == 1 })
	unit := readState(t, dir).Units[0]
	first.n.Stop()

	second := newHarness(t, harnessOpts{
		sinks: []SinkConfig{sc}, stateDir: dir, tags: nil, intake: 0, scripts: nil, clock: nil,
	})
	second.start(t)
	f := second.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 1 })
	got := f.snapshot()[0]
	if got.ID != unit.ID || !bytes.Equal(got.Body, unit.Body) {
		t.Fatalf("replay changed the batch: id %s vs %s", got.ID, unit.ID)
	}
	if !strings.HasPrefix(got.ID, prefixBatch) {
		t.Fatalf("not a batch id: %s", got.ID)
	}
}
