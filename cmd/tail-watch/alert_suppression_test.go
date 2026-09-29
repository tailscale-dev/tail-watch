package main

import (
	"testing"
	"time"
)

func TestFlapSuppressionRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		settleFirst   bool
		failOnRelease bool
	}{
		{name: "release before settling", settleFirst: false, failOnRelease: false},
		{name: "settle before release", settleFirst: true, failOnRelease: false},
		{name: "failure before settling", settleFirst: false, failOnRelease: true},
		{name: "failure after settling", settleFirst: true, failOnRelease: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testFlapSuppressionRecovery(t, tc.settleFirst, tc.failOnRelease)
		})
	}
}

func testFlapSuppressionRecovery(t *testing.T, settleFirst, failOnRelease bool) {
	t.Helper()
	parent := alertTarget("router", "tsping")
	child := alertTarget("nas", "icmp")
	child.FlapLimit = 1
	al, clk, tr := testAlerter(t, []*Target{parent, child}, map[string]string{"nas": "router"})
	pass(al, parent)
	pass(al, child)
	since := clk.Now()
	failN(al, child, "icmp", layerLAN, 3)
	recoverTarget(al, child)
	advanceAlertClock(al, clk, time.Minute)
	failN(al, parent, "tsping", layerTunnel, 3)
	if gotState(al, child.Name) != StateSuppressed {
		t.Fatal("flapping child was not suppressed")
	}
	if settleFirst {
		advanceAlertClock(al, clk, child.FlapWindow+time.Second)
		if gaugeOf(t, al) != float64(StateSuppressed) {
			t.Fatal("flap mark did not clear while suppressed")
		}
	}
	assertOpenSuppressedIncident(t, al, since, !settleFirst)
	recoverTarget(al, parent)
	if failOnRelease {
		failN(al, child, "icmp", layerUnrouted, 3)
		if gotState(al, child.Name) != StateDown {
			t.Fatal("fresh failures did not move released child DOWN")
		}
		recoverTarget(al, child)
	} else {
		pass(al, child)
	}
	if !settleFirst {
		if !al.IsDown(child.Name) {
			t.Fatal("release erased the outstanding recovery before settling")
		}
		advanceAlertClock(al, clk, child.FlapWindow-time.Second)
		if !al.IsDown(child.Name) {
			t.Fatal("recovery emitted before the child settled UP")
		}
		advanceAlertClock(al, clk, time.Second)
	}
	al.stopDrain()
	assertChildRecovery(t, tr, since)
	if al.IsDown(child.Name) {
		t.Fatal("recovered child still has an outstanding DOWN")
	}
	if err := al.RemoveTarget(child.Name); err != nil {
		t.Fatalf("recovered child cannot be removed: %v", err)
	}
}

func assertOpenSuppressedIncident(t *testing.T, al *alerter, since time.Time, flapping bool) {
	t.Helper()
	snap := al.Snapshots()[0] // nas sorts before router.
	if snap.Name != "nas" || snap.State != StateDown || snap.Flapping != flapping ||
		!snap.DownSince.Equal(since) || snap.Check != "icmp" || snap.Layer != layerLAN {
		t.Fatalf("suppression lost the outstanding incident: %+v", snap)
	}
	if err := al.RemoveTarget("nas"); err == nil {
		t.Fatal("removed a child whose incident still needs recovery")
	}
}

func assertChildRecovery(t *testing.T, tr *trap, since time.Time) {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var childEvents []Transition
	for _, event := range tr.got {
		if event.Target.Name == "nas" {
			childEvents = append(childEvents, event)
		}
	}
	if len(childEvents) != 3 {
		t.Fatalf("want DOWN, flapping and one recovery, got %+v", childEvents)
	}
	if childEvents[0].To != StateDown || !childEvents[1].Flapping {
		t.Fatalf("unexpected initial transitions: %+v", childEvents)
	}
	up := childEvents[2]
	if up.From != StateDown || up.To != StateUp || up.Flapping || !up.DownSince.Equal(since) ||
		up.Check != "icmp" || up.Layer != layerLAN {
		t.Fatalf("recovery lost the original incident: %+v", up)
	}
}

func TestFlapSuppressionDirtySnapshot(t *testing.T) {
	t.Parallel()
	parent := alertTarget("router", "tsping")
	child := alertTarget("nas", "icmp")
	child.FlapLimit = 1
	al, clk, _ := testAlerterOpt(t, []*Target{parent, child}, map[string]string{"nas": "router"}, -1, nil)
	pass(al, parent)
	pass(al, child)
	since := clk.Now()
	failN(al, child, "icmp", layerLAN, 3)
	recoverTarget(al, child)
	failN(al, parent, "tsping", layerTunnel, 3)
	before := al.Snapshots()[0]
	advanceAlertClock(al, clk, child.FlapWindow)
	assertOpenSuppressedIncident(t, al, since, false)
	al.Acknowledge(child.Name, before.Sequence)
	settled := al.Snapshots()[0]
	if !settled.Dirty || settled.Sequence <= before.Sequence {
		t.Fatal("stale acknowledgment cleared the settled incident")
	}
	recoverTarget(al, parent)
	pass(al, child)
	al.Acknowledge(child.Name, settled.Sequence)
	recovered := al.Snapshots()[0]
	if recovered.State != StateUp || !recovered.Dirty || !recovered.DownSince.Equal(since) ||
		recovered.Sequence <= settled.Sequence {
		t.Fatalf("overflow lost the child's recovery: %+v", recovered)
	}
	al.Acknowledge(child.Name, recovered.Sequence)
	if al.Snapshots()[0].Dirty {
		t.Fatal("reconciled recovery is still dirty")
	}
}

func TestRecoveryLostToFullQueueStaysDirty(t *testing.T) {
	t.Parallel()
	nas := alertTarget("nas", "icmp")
	other := alertTarget("other", "icmp")
	al, _, tr := testAlerterOpt(t, []*Target{nas, other}, nil, 1, nil)
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	al.addConsumer(consumerFunc(func(Transition) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
	}))
	t.Cleanup(func() { close(gate) })
	pass(al, nas)
	pass(al, other)
	failN(al, nas, "icmp", layerLAN, 3)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer never received the first DOWN")
	}
	failN(al, other, "icmp", layerLAN, 3)
	recoverTarget(al, nas)
	snap := al.Snapshots()[0]
	if snap.Name != "nas" || snap.State != StateUp || !snap.Dirty || snap.DownSince.IsZero() {
		t.Fatalf("dropped recovery is not held in the dirty snapshot: %+v", snap)
	}
	gate <- struct{}{}
	gate <- struct{}{}
	for _, event := range tr.wait(t, 2) {
		if event.Target.Name == "nas" && event.To == StateUp {
			t.Fatalf("recovery was delivered despite a full queue: %+v", event)
		}
	}
	al.Acknowledge("nas", snap.Sequence)
	if al.Snapshots()[0].Dirty {
		t.Fatal("reconciled recovery is still dirty")
	}
}
