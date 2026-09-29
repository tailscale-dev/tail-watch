package main

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
)

func TestSnapshotsIncludeSilentStates(t *testing.T) {
	t.Parallel()
	parent := alertTarget("router", "tsping")
	child := alertTarget("nas", "icmp")
	al, _, _ := testAlerter(t, []*Target{parent, child}, map[string]string{"nas": "router"})
	if got := al.Snapshots(); len(got) != 2 || got[0].State != StateUnknown {
		t.Fatalf("initial snapshots: %+v", got)
	}
	pass(al, child)
	if got := al.Snapshots()[0]; got.State != StateUp {
		t.Fatalf("silent up: %+v", got)
	}
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := al.Restore("router", since); err != nil {
		t.Fatal(err)
	}
	got := al.Snapshots()
	if got[0].State != StateSuppressed || got[1].State != StateDown || got[1].DownSince != since {
		t.Fatalf("restored parent: %+v", got)
	}
}

func TestRestoredRegistrationSuppressesExistingChildren(t *testing.T) {
	t.Parallel()
	child := alertTarget("nas", "icmp")
	al, clk, tr := testAlerter(t, []*Target{child}, map[string]string{"nas": "router"})
	pass(al, child)
	if err := al.Restore("router", clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := al.AddTarget(alertTarget("router", "tsping")); err != nil {
		t.Fatal(err)
	}
	if gotState(al, "nas") != StateSuppressed {
		t.Fatal("restored router did not suppress existing child")
	}
	tr.expectNone(t)
}

func TestTransitionConsumerIsolation(t *testing.T) {
	t.Parallel()
	parent := alertTarget("router", "tsping")
	child := alertTarget("nas", "icmp")
	al, _, _ := testAlerter(t, []*Target{parent, child}, map[string]string{"nas": "router"})
	al.addConsumer(consumerFunc(func(tr Transition) {
		tr.Target.Checks[0] = icmpCheck{}
		tr.Dependents[0] = "changed"
	}))
	observed := make(chan Transition, 1)
	al.addConsumer(consumerFunc(func(tr Transition) { observed <- tr }))
	failN(al, parent, "tsping", layerTunnel, 3)
	select {
	case tr := <-observed:
		if tr.Target.Checks[0].String() != "tsping" || !slices.Equal(tr.Dependents, []string{"nas"}) {
			t.Fatalf("consumer mutated another consumer's transition: %+v", tr)
		}
	case <-time.After(time.Second):
		t.Fatal("transition not delivered")
	}
}

func TestFlapTimerArmedByGraceExpiry(t *testing.T) {
	t.Parallel()
	parent := alertTarget("router", "tsping")
	child := alertTarget("nas", "icmp")
	child.FlapLimit = 2
	al, clk, tr := testAlerter(t, []*Target{parent, child}, map[string]string{"nas": "router"})
	pass(al, child)
	failN(al, child, "icmp", layerLAN, 3)
	recoverTarget(al, child)
	failN(al, child, "icmp", layerControl, 3)
	advanceAlertClock(al, clk, 50*time.Second)
	if got := tr.wait(t, 3); !got[2].Flapping {
		t.Fatalf("grace expiry: %+v", got)
	}
	advanceAlertClock(al, clk, 15*time.Minute)
	if got := tr.wait(t, 4); got[3].Flapping || got[3].To != StateDown {
		t.Fatalf("settle: %+v", got)
	}
}

func TestRemovedTargetTimersAndResultsStayRemoved(t *testing.T) {
	t.Parallel()
	parent := alertTarget("router", "tsping")
	child := alertTarget("nas", "icmp")
	al, clk, tr := testAlerter(t, []*Target{parent, child}, map[string]string{"nas": "router"})
	pass(al, child)
	failN(al, child, "icmp", layerControl, 3)
	oldGeneration := al.generation("nas")
	if err := al.RemoveTarget("nas"); err != nil {
		t.Fatal(err)
	}
	advanceAlertClock(al, clk, 25*time.Second)
	if err := al.AddTarget(child); err != nil {
		t.Fatal(err)
	}
	if err := al.UpdateParents(map[string]string{"nas": "router"}); err != nil {
		t.Fatal(err)
	}
	pass(al, child)
	stale := failClass(child, "icmp", layerLAN, clk.Now())
	stale.Generation = oldGeneration
	for range 3 {
		al.ObserveClassified(stale)
	}
	if gotState(al, "nas") != StateUp {
		t.Fatal("old registration's results counted")
	}
	failN(al, child, "icmp", layerControl, 3)
	advanceAlertClock(al, clk, 25*time.Second)
	if gotState(al, "nas") != StateUp {
		t.Fatal("removed target's timer ended the new hold early")
	}
	advanceAlertClock(al, clk, 25*time.Second)
	tr.wait(t, 1)
}

func TestFlapClearAdvancesSnapshot(t *testing.T) {
	t.Parallel()
	target := alertTarget("nas", "icmp")
	al, clk, tr := testAlerterOpt(t, []*Target{target}, nil, -1, nil)
	pass(al, target)
	for range 3 {
		failN(al, target, "icmp", layerLAN, 3)
		recoverTarget(al, target)
	}
	before := al.Snapshots()[0]
	advanceAlertClock(al, clk, target.FlapWindow)
	al.Acknowledge("nas", before.Sequence)
	after := al.Snapshots()[0]
	if after.Flapping || !after.Dirty || after.Sequence <= before.Sequence {
		t.Fatalf("stale acknowledgment cleared flap update: before=%+v after=%+v", before, after)
	}
	tr.expectNone(t)
}

func TestFailedLearningRetainsPendingRestores(t *testing.T) {
	t.Parallel()
	nas := alertTarget("nas", "icmp")
	al, clk, _ := testAlerter(t, []*Target{nas}, nil)
	if err := al.Restore("old-router", clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := al.Restore("gone-nas", clk.Now()); err != nil {
		t.Fatal(err)
	}
	var env fakeEnv
	classifier, _ := newTestClassifier(t, &env, nas)
	runtime := newRuntimeRegistry(t.Context(), nil, nil)
	runtime.alert = al
	t.Cleanup(runtime.pool.shutdown)
	classifier.registry = runtime
	classifier.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return nil, errors.New("discovery failed")
	}
	learner := newLearner(classifier)
	learner.pass(t.Context())
	learner.finish()
	<-learner.FirstPassDone()
	if got := al.PendingRestores(); !slices.Equal(got, []string{"gone-nas", "old-router"}) {
		t.Fatalf("failed discovery discarded pending incidents: %v", got)
	}
	if err := runtime.RemoveTarget("gone-nas"); err != nil {
		t.Fatal(err)
	}
	if got := al.PendingRestores(); !slices.Equal(got, []string{"old-router"}) {
		t.Fatalf("targeted removal lost another restore: %v", got)
	}
}

func TestSilentPromotionPreservesFailureRun(t *testing.T) {
	t.Parallel()
	for _, afterRelease := range []bool{false, true} {
		name := "startup"
		if afterRelease {
			name = "release"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parent := alertTarget("router", "tsping")
			child := alertTarget("nas", "icmp", "tcp:445")
			al, clk, tr := testAlerter(t, []*Target{parent, child}, map[string]string{"nas": "router"})
			wantTransitions := 1
			if afterRelease {
				pass(al, child)
				failN(al, parent, "tsping", layerTunnel, 3)
				recoverTarget(al, parent)
				wantTransitions += 2
			}
			passCheck(al, child, "tcp:445")
			since := clk.Now()
			failAt(al, child, "tcp:445", layerService, since, since.Add(time.Second))
			passCheck(al, child, "icmp")
			if gotState(al, child.Name) != StateUp {
				t.Fatal("target did not become UP after each check succeeded")
			}
			failAt(al, child, "tcp:445", layerService, since.Add(2*time.Second))
			if gotState(al, child.Name) != StateDown {
				t.Fatal("silent promotion lost the consecutive TCP failures")
			}
			got := tr.wait(t, wantTransitions)
			down := got[len(got)-1]
			if down.From != StateUp || down.To != StateDown || down.Check != "tcp:445" ||
				down.Failures != 3 || !down.Since.Equal(since) {
				t.Fatalf("failure run changed during silent promotion: %+v", down)
			}
		})
	}
}
