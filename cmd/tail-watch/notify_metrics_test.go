package main

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNotifyMetricsAfterScriptedRun(t *testing.T) {
	t.Parallel()
	plain := testSink("plain")
	batch := testSink("batch")
	batch.Batch = true
	h := newHarness(t, harnessOpts{
		sinks:    []SinkConfig{plain, batch},
		stateDir: filepath.Join(t.TempDir(), "state"),
		tags:     nil,
		intake:   0,
		scripts:  map[string][]deliveryResult{"plain": {statusRes(503)}},
		clock:    nil,
	})
	var failed atomic.Bool
	h.n.store.setInject(func() error {
		if failed.CompareAndSwap(false, true) {
			return errors.New("disk full")
		}
		return nil
	})
	h.start(t)
	before := h.clock.timers.Load()
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool { return h.sinks["plain"].callCount() == 1 })
	h.clock.waitTimers(t, before)
	h.clock.Advance(batch.GroupWait)
	eventually(t, func() bool {
		return h.sinks["plain"].callCount() == 2 && h.sinks["batch"].callCount() == 1
	})
	eventually(t, func() bool {
		return testutil.ToFloat64(h.n.m.sentC.WithLabelValues("plain", "target.down")) == 1
	})

	if n := testutil.ToFloat64(h.n.m.failedC.WithLabelValues("plain", failHTTP5xx)); n != 1 {
		t.Fatalf("failed_total = %v", n)
	}
	if n := testutil.ToFloat64(h.n.m.persistErr.WithLabelValues(sinkState)); n != 1 {
		t.Fatalf("persist_errors_total = %v", n)
	}
	if n := testutil.ToFloat64(h.n.m.queueLenG.WithLabelValues("plain")); n != 0 {
		t.Fatalf("queue_len = %v", n)
	}
	if n := testutil.ToFloat64(h.n.m.openG); n != 1 {
		t.Fatalf("open_incidents = %v", n)
	}
	if n := testutil.CollectAndCount(h.n.m.latencyH); n != 2 {
		t.Fatalf("latency series = %d", n)
	}
	if n := testutil.CollectAndCount(h.n.m.batchSizeH); n != 1 {
		t.Fatalf("batch_size series = %d (batched sinks only)", n)
	}
	fams, err := h.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, f := range fams {
		have[f.GetName()] = true
	}
	for _, name := range []string{
		"tailwatch_notify_sent_total", "tailwatch_notify_failed_total", "tailwatch_notify_queue_len",
		"tailwatch_notify_persist_errors_total", "tailwatch_notify_batch_size",
		"tailwatch_notify_latency_seconds", "tailwatch_open_incidents",
	} {
		if !have[name] {
			t.Errorf("metric %s not registered", name)
		}
	}
}

func TestQueueOverflowCountsQueueFull(t *testing.T) {
	t.Parallel()
	sc := testSink("a")
	sc.QueueSize = 2
	h := newHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.n.mu.Lock()
	for i := range 5 {
		h.n.enqueueLocked(newFlappingEvent("evt_"+string(rune('A'+i)), goldenCollector(), goldenTransition()))
	}
	h.n.mu.Unlock()
	if n := testutil.ToFloat64(h.n.m.droppedC.WithLabelValues("a", reasonQueueFull)); n != 3 {
		t.Fatalf("dropped queue_full = %v", n)
	}
	if n := testutil.ToFloat64(h.n.m.queueLenG.WithLabelValues("a")); n != 2 {
		t.Fatalf("queue_len = %v", n)
	}
}

func TestSlowCommitDelaysDurableEnqueuesButNotIntake(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOpts{
		sinks:    []SinkConfig{testSink("a")},
		stateDir: filepath.Join(t.TempDir(), "state"),
		tags:     nil,
		intake:   0,
		scripts:  nil,
		clock:    nil,
	})
	gate := make(chan struct{})
	h.n.store.setInject(func() error { <-gate; return nil })
	h.start(t)
	done := make(chan struct{})
	go func() {
		for i := range 20 {
			h.n.ObserveTransition(downTransition(uint64(i+1), h.clock.Now()))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("intake blocked behind a slow commit")
	}
	time.Sleep(30 * time.Millisecond)
	if h.sinks["a"].callCount() != 0 {
		t.Fatal("nothing may be sent while the commit is pending")
	}
	h.n.store.setInject(nil)
	close(gate)
	eventually(t, func() bool { return h.sinks["a"].callCount() == 1 })
}
