package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

func testBook(sinks ...SinkConfig) (*book, *notifyMetrics) {
	m := newNotifyMetrics(prometheus.NewRegistry(), true)
	return newBook(discardLog(), m, sinks), m
}

func plainEvent(id string, at time.Time) Event {
	return baseEvent(id, EventFlapping, SeverityPage, at, goldenCollector())
}

func TestMemQueueOrderAndOverflow(t *testing.T) {
	t.Parallel()
	sc := testSink("a")
	sc.QueueSize = 3
	b, m := testBook(sc)
	for i := range 5 {
		b.push(
			"a",
			&queued{
				Seq:        b.seq(),
				Event:      plainEvent("evt_"+string(rune('A'+i)), testNow()),
				IncidentID: "",
				Attempted:  false,
				UnitID:     "",
			},
		)
	}
	q := b.queues["a"]
	if q.Len() != 3 {
		t.Fatalf("len %d", q.Len())
	}
	head := q.Peek(10)
	if head[0].Event.ID != "evt_C" || head[2].Event.ID != "evt_E" {
		t.Fatalf("drop-oldest broke ordering: %s..%s", head[0].Event.ID, head[2].Event.ID)
	}
	if n := testutil.ToFloat64(m.droppedC.WithLabelValues("a", reasonQueueFull)); n != 2 {
		t.Fatalf("queue_full = %v", n)
	}
	q.Ack(head[0].Seq)
	if q.Len() != 2 || q.Peek(1)[0].Event.ID != "evt_D" {
		t.Fatal("ack did not remove the head")
	}
}

func TestOverflowSparesInFlight(t *testing.T) {
	t.Parallel()
	sc := testSink("a")
	sc.QueueSize = 2
	b, _ := testBook(sc)
	first := &queued{
		Seq:        b.seq(),
		Event:      plainEvent("evt_A", testNow()),
		IncidentID: "",
		Attempted:  false,
		UnitID:     "",
	}
	second := &queued{
		Seq:        b.seq(),
		Event:      plainEvent("evt_B", testNow()),
		IncidentID: "",
		Attempted:  false,
		UnitID:     "",
	}
	b.push("a", first)
	b.push("a", second)
	b.beginUnit("a", "evt_A", []*queued{first}, []byte("x"))
	b.push(
		"a",
		&queued{
			Seq:        b.seq(),
			Event:      plainEvent("evt_C", testNow()),
			IncidentID: "",
			Attempted:  false,
			UnitID:     "",
		},
	)
	ids := []string{}
	for _, it := range b.queues["a"].items {
		ids = append(ids, it.Event.ID)
	}
	if strings.Join(ids, ",") != "evt_A,evt_C" {
		t.Fatalf("in-flight event must survive overflow, got %v", ids)
	}
}

func downFor(id string) Event {
	tr := goldenTransition()
	tr.Target.Name = "nas"
	return newDownEvent(id, goldenCollector(), tr)
}

func recFor(b *book) Event {
	const incID = "evt_D1"
	return newRecoveredEvent("evt_R"+incID, goldenCollector(), b.incidents[incID], testNow().Add(time.Minute), nil, "")
}

func TestNeverAttemptedDownDropTakesRecovery(t *testing.T) {
	t.Parallel()
	b, m := testBook(testSink("a"))
	inc := b.openIncident(
		downFor("evt_D1"),
		targetDef{
			Name:     "nas",
			Addr:     "",
			Site:     "",
			Parent:   "",
			Severity: SeverityPage,
			Implicit: false,
			StableID: "",
			NodeID:   0,
		},
		testNow(),
		[]string{"a"},
	)
	b.closeIncident(inc, recFor(b))
	q := b.queues["a"]
	if q.Len() != 2 {
		t.Fatalf("down and recovery should be queued, got %d", q.Len())
	}
	down := q.Peek(1)[0]
	q.Remove(down.Seq)
	b.dropItem("a", down, reasonQueueFull)
	if q.Len() != 0 {
		t.Fatal("recovery of a never-attempted dropped down must be discarded")
	}
	if _, ok := b.incidents["evt_D1"]; ok {
		t.Fatal("incident should be garbage collected once every intent is terminal")
	}
	if n := testutil.ToFloat64(m.droppedC.WithLabelValues("a", reasonQueueFull)); n != 1 {
		t.Fatalf("only the down drop is counted, got %v", n)
	}
}

func TestAttemptedDownKeepsRecoveryEligible(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{reasonExpired, reasonPermanent} {
		b, _ := testBook(testSink("a"))
		inc := b.openIncident(
			downFor("evt_D1"),
			targetDef{
				Name:     "nas",
				Addr:     "",
				Site:     "",
				Parent:   "",
				Severity: SeverityPage,
				Implicit: false,
				StableID: "",
				NodeID:   0,
			},
			testNow(),
			[]string{"a"},
		)
		down := b.queues["a"].Peek(1)[0]
		u := b.beginUnit("a", down.Event.ID, []*queued{down}, []byte("body"))
		b.closeIncident(inc, recFor(b))
		b.dropUnit(u, reason)
		d := b.incidents["evt_D1"].Sinks["a"]
		if d.Down != phaseDropped || !d.DownTried || d.Rec != phasePending {
			t.Fatalf("%s: down=%s tried=%v rec=%s", reason, d.Down, d.DownTried, d.Rec)
		}
		if q := b.queues["a"]; q.Len() != 1 || q.Peek(1)[0].Event.Type != EventRecovered {
			t.Fatalf("%s: recovery must remain queued", reason)
		}
	}
}

func TestRecoveryAfterDropStillEligibleWhenAttempted(t *testing.T) {
	t.Parallel()
	b, _ := testBook(testSink("a"))
	inc := b.openIncident(
		downFor("evt_D1"),
		targetDef{
			Name:     "nas",
			Addr:     "",
			Site:     "",
			Parent:   "",
			Severity: SeverityPage,
			Implicit: false,
			StableID: "",
			NodeID:   0,
		},
		testNow(),
		[]string{"a"},
	)
	down := b.queues["a"].Peek(1)[0]
	u := b.beginUnit("a", down.Event.ID, []*queued{down}, []byte("body"))
	b.dropUnit(u, reasonExpired)
	b.closeIncident(inc, recFor(b))
	if d := b.incidents["evt_D1"].Sinks["a"]; d.Rec != phasePending {
		t.Fatalf("recovery after an attempted-then-dropped down must be pending, got %s", d.Rec)
	}
}

func TestRecoveryIneligibleAfterUnattemptedDrop(t *testing.T) {
	t.Parallel()
	b, _ := testBook(testSink("a"))
	inc := b.openIncident(
		downFor("evt_D1"),
		targetDef{
			Name:     "nas",
			Addr:     "",
			Site:     "",
			Parent:   "",
			Severity: SeverityPage,
			Implicit: false,
			StableID: "",
			NodeID:   0,
		},
		testNow(),
		[]string{"a"},
	)
	down := b.queues["a"].Peek(1)[0]
	b.queues["a"].Remove(down.Seq)
	b.dropItem("a", down, reasonExpired)
	b.closeIncident(inc, recFor(b))
	if b.queues["a"].Len() != 0 {
		t.Fatal("no orphan recovery may be queued")
	}
}

func TestFilteredSinkNeverGetsRecovery(t *testing.T) {
	t.Parallel()
	b, _ := testBook(testSink("a"), testSink("b"))
	inc := b.openIncident(
		downFor("evt_D1"),
		targetDef{
			Name:     "nas",
			Addr:     "",
			Site:     "",
			Parent:   "",
			Severity: SeverityPage,
			Implicit: false,
			StableID: "",
			NodeID:   0,
		},
		testNow(),
		[]string{"a"},
	)
	b.closeIncident(inc, recFor(b))
	if b.queues["b"].Len() != 0 {
		t.Fatal("sink filtered out of the down must not see the recovery")
	}
	if b.queues["a"].Len() != 2 {
		t.Fatal("selected sink must hold down and recovery")
	}
}

func TestRecoveryBypassesFiltersAndSkipsRemovedSink(t *testing.T) {
	t.Parallel()
	b, _ := testBook(testSink("a"))
	inc := b.openIncident(
		downFor("evt_D1"),
		targetDef{
			Name:     "nas",
			Addr:     "",
			Site:     "",
			Parent:   "",
			Severity: SeverityPage,
			Implicit: false,
			StableID: "",
			NodeID:   0,
		},
		testNow(),
		[]string{"a", "gone"},
	)
	b.closeIncident(inc, recFor(b))
	if got := b.incidents["evt_D1"].Sinks["gone"].Rec; got != phaseDropped {
		t.Fatalf("removed sink recovery = %q, want dropped", got)
	}
	if b.queues["a"].Len() != 2 {
		t.Fatal("configured sink keeps its recovery")
	}
}

func TestAckReleasesClosingIncident(t *testing.T) {
	t.Parallel()
	b, m := testBook(testSink("a"))
	inc := b.openIncident(
		downFor("evt_D1"),
		targetDef{
			Name:     "nas",
			Addr:     "",
			Site:     "",
			Parent:   "",
			Severity: SeverityPage,
			Implicit: false,
			StableID: "",
			NodeID:   0,
		},
		testNow(),
		[]string{"a"},
	)
	b.closeIncident(inc, recFor(b))
	items := b.queues["a"].Peek(2)
	u := b.beginUnit("a", "bat_1", items, []byte("b"))
	d := b.incidents["evt_D1"].Sinks["a"]
	if d.Down != phaseAttempted || d.Rec != phaseAttempted || !d.DownTried || !d.RecTried {
		t.Fatalf("unattempted down and recovery must be marked together: %+v", d)
	}
	b.ackUnit(u, testNow().Add(2*time.Second))
	if len(b.incidents) != 0 {
		t.Fatal("closed incident with every intent acknowledged must be forgotten")
	}
	if n := testutil.ToFloat64(m.sentC.WithLabelValues("a", "target.recovered")); n != 1 {
		t.Fatalf("sent recovered = %v", n)
	}
}

func TestNewOutageOpensWhileRecoveryPending(t *testing.T) {
	t.Parallel()
	b, _ := testBook(testSink("a"))
	first := b.openIncident(
		downFor("evt_D1"),
		targetDef{
			Name:     "nas",
			Addr:     "",
			Site:     "",
			Parent:   "",
			Severity: SeverityPage,
			Implicit: false,
			StableID: "",
			NodeID:   0,
		},
		testNow(),
		[]string{"a"},
	)
	b.closeIncident(first, recFor(b))
	second := b.openIncident(
		downFor("evt_D2"),
		targetDef{
			Name:     "nas",
			Addr:     "",
			Site:     "",
			Parent:   "",
			Severity: SeverityPage,
			Implicit: false,
			StableID: "",
			NodeID:   0,
		},
		testNow(),
		[]string{"a"},
	)
	if second.ID == first.ID || b.openIncidentFor("nas") != second {
		t.Fatal("a fresh outage needs its own incident")
	}
	var order []string
	for _, it := range b.queues["a"].items {
		order = append(order, it.Event.ID)
	}
	if strings.Join(order, ",") != "evt_D1,evt_Revt_D1,evt_D2" {
		t.Fatalf("new down must queue behind the old recovery: %v", order)
	}
}

func newDiskNotifier(t *testing.T, dir string, sinks ...SinkConfig) *notifyHarness {
	t.Helper()
	for i := range sinks {
		sinks[i].Queue = queueDisk
	}
	return newHarness(t, harnessOpts{sinks: sinks, stateDir: dir, tags: nil, intake: 0, scripts: nil, clock: nil})
}

func readState(t *testing.T, dir string) notifyState {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	var st notifyState
	if unmarshalErr := json.Unmarshal(raw, &st); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	return st
}

func TestCommitHidesIntentsFromWorkersAndCommitsSinksTogether(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newDiskNotifier(t, dir, testSink("a"), testSink("b"))
	gate := make(chan struct{})
	h.n.store.setInject(func() error { <-gate; return nil })
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	time.Sleep(50 * time.Millisecond)
	if h.sinks["a"].callCount()+h.sinks["b"].callCount() != 0 {
		t.Fatal("workers sent a disk intent before its commit")
	}
	if _, err := os.Stat(filepath.Join(dir, stateFileName)); err == nil {
		st := readState(t, dir)
		if len(st.Incidents) != 0 {
			t.Fatal("snapshot changed before commit")
		}
	}
	h.n.store.setInject(nil)
	close(gate)
	eventually(t, func() bool { return h.sinks["a"].callCount() == 1 && h.sinks["b"].callCount() == 1 })
	eventually(t, func() bool {
		st := readState(t, dir)
		return len(st.Incidents) == 1 && len(st.Queues["a"]) == 0 && len(st.Queues["b"]) == 0 &&
			st.Incidents[0].Sinks["a"].Down == phaseAcked
	})
}

func TestSnapshotHoldsIncidentAndIntentsTogether(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	h := newDiskNotifier(t, dir, testSink("a"), testSink("b"))
	h.n.mu.Lock()
	h.n.applyLocked(downTransition(1, h.clock.Now()), false)
	h.n.commitLocked()
	h.n.mu.Unlock()
	st := readState(t, dir)
	if len(st.Incidents) != 1 || st.Incidents[0].Status != incidentOpen || len(st.Queues["a"]) != 1 ||
		len(st.Queues["b"]) != 1 {
		t.Fatalf("snapshot %+v", st)
	}
	fi, err := os.Stat(filepath.Join(dir, stateFileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %v, %v", fi.Mode(), err)
	}
	di, err := os.Stat(dir)
	if err != nil || di.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state dir mode %v, %v", di.Mode(), err)
	}
}

func TestReplayCrashAfterCommitBeforeDistribution(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := newDiskNotifier(t, dir, testSink("a"))
	first.n.mu.Lock()
	first.n.applyLocked(downTransition(1, first.clock.Now()), false)
	first.n.applyLocked(upTransition(2, first.clock.Now()), false)
	first.n.commitLocked()
	first.n.mu.Unlock()
	var downID string
	for _, inc := range readState(t, dir).Incidents {
		downID = inc.ID
		if inc.Status != incidentClosing || inc.Recovery == nil || inc.Recovery.DownEventID != inc.ID {
			t.Fatalf("closing incident lost its recovery: %+v", inc)
		}
	}
	second := newDiskNotifier(t, dir, testSink("a"))
	second.start(t)
	f := second.sinks["a"]
	eventually(t, func() bool { return f.callCount() == 2 })
	got := idsOf(t, f)
	if got[0] != downID {
		t.Fatalf("replay must keep the original down id %s, got %v", downID, got)
	}
	var body struct {
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(f.snapshot()[1].Body, &body); err != nil || body.Events[0].DownEventID != downID {
		t.Fatalf("recovery must reference the original down: %v %+v", err, body)
	}
}

func TestReplayCrashBeforeRenameKeepsOldSnapshot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := newDiskNotifier(t, dir, testSink("a"))
	first.n.mu.Lock()
	first.n.applyLocked(downTransition(1, first.clock.Now()), false)
	first.n.commitLocked()
	first.n.mu.Unlock()
	stale := filepath.Join(dir, stateFileName+".12345.tmp")
	if err := os.WriteFile(stale, []byte(`{"version":1,"incidents":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	second := newDiskNotifier(t, dir, testSink("a"))
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("abandoned temporary file must be removed")
	}
	if len(second.n.openIncidents()) != 1 {
		t.Fatal("old snapshot's open incident must survive")
	}
}

func TestReplayCrashAfterSendBeforeAckResendsSameBody(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := newDiskNotifier(t, dir, testSink("a"))
	var commits atomic.Int32
	first.n.store.setInject(func() error {
		if commits.Add(1) > 2 {
			return errors.New("crash before acknowledgment commit")
		}
		return nil
	})
	first.start(t)
	first.n.ObserveTransition(downTransition(1, first.clock.Now()))
	eventually(t, func() bool { return first.sinks["a"].callCount() == 1 })
	eventually(t, func() bool { return testutil.ToFloat64(first.n.m.persistErr.WithLabelValues(sinkState)) >= 1 })
	sent := first.sinks["a"].snapshot()[0]
	first.n.Stop()

	second := newDiskNotifier(t, dir, testSink("a"))
	second.start(t)
	eventually(t, func() bool { return second.sinks["a"].callCount() == 1 })
	again := second.sinks["a"].snapshot()[0]
	if again.ID != sent.ID || !bytes.Equal(again.Body, sent.Body) {
		t.Fatal("replay after an unacknowledged send must reuse the id and body bytes")
	}
}

func TestCorruptSnapshotStopsStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, stateFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sc := testSink("a")
	sc.Queue = queueDisk
	_, err := newNotifier(notifierConfig{
		Sinks: []SinkConfig{sc}, StateDir: dir, Registry: prometheus.NewRegistry(),
		NewSink: func(SinkConfig) (sink, error) {
			return &fakeSink{name: "a", interval: 0, mu: sync.Mutex{}, script: nil, calls: nil, block: nil}, nil
		}, Clock: nil, Log: nil, Collector: CollectorInfo{Name: "", TailscaleIP: ""}, Alerts: nil, Tags: nil, Env: nil, Jitter: nil, IntakeCap: 0,
	})
	if err == nil || !strings.Contains(err.Error(), "corrupt") || !strings.Contains(err.Error(), path) {
		t.Fatalf("want an actionable corruption error, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Fatal("corrupt file must be preserved for repair")
	}
}

func TestPersistFailureFallsBackToMemoryAndRecovers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newDiskNotifier(t, dir, testSink("a"))
	h.n.store.setInject(func() error { return errors.New("read-only file system") })
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool { return h.sinks["a"].callCount() == 1 })
	if n := testutil.ToFloat64(h.n.m.persistErr.WithLabelValues(sinkState)); n < 1 {
		t.Fatalf("persist_errors_total = %v", n)
	}
	h.n.store.setInject(nil)
	h.clock.Advance(reconcileInterval)
	eventually(t, func() bool {
		raw, err := os.ReadFile(filepath.Join(dir, stateFileName))
		return err == nil && bytes.Contains(raw, []byte(`"incidents"`)) && bytes.Contains(raw, []byte("nyc-nas"))
	})
}

func TestWarnsWhenStateDirReadableByOthers(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := newStateStore(dir, slog.New(slog.NewTextHandler(&buf, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "readable by group or others") {
		t.Fatalf("no warning logged: %q", buf.String())
	}
	var quiet bytes.Buffer
	private := filepath.Join(t.TempDir(), "private")
	if _, err := newStateStore(private, slog.New(slog.NewTextHandler(&quiet, nil))); err != nil {
		t.Fatal(err)
	}
	if quiet.Len() != 0 {
		t.Fatalf("private dir should not warn: %q", quiet.String())
	}
}

func TestExpiredMemberDropsWholeAttemptedBatch(t *testing.T) {
	t.Parallel()
	sc := testSink("a")
	sc.Batch = true
	sc.MaxAge = time.Hour
	b, m := testBook(sc)
	old := testNow().Add(-2 * time.Hour)
	for i, at := range []time.Time{testNow(), old, testNow()} {
		b.push("a", &queued{
			Seq: b.seq(), IncidentID: "", Attempted: false, UnitID: "",
			Event: plainEvent("evt_"+string(rune('A'+i)), at),
		})
	}
	u := b.beginUnit("a", "bat_1", b.queues["a"].Peek(3), []byte("body"))
	if !b.unitExpired(u, testNow()) {
		t.Fatal("a unit holding an old event must count as expired")
	}
	b.dropUnit(u, reasonExpired)
	if n := testutil.ToFloat64(m.droppedC.WithLabelValues("a", reasonExpired)); n != 3 {
		t.Fatalf("every event in the batch must be counted, got %v", n)
	}
	if b.queues["a"].Len() != 0 || len(b.units) != 0 {
		t.Fatal("the batch and its members must be gone")
	}
}
