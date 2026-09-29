package main

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"tailscale.com/ipn"
	"tailscale.com/tstest"
	"tailscale.com/tstime"
)

func TestSnapshotCopiesDoNotAlias(t *testing.T) {
	t.Parallel()
	parent := alertTarget("router", "tsping")
	child := alertTarget("nas", "icmp")
	al, _, tr := testAlerter(t, []*Target{parent, child}, map[string]string{"nas": "router"})
	pass(al, child)
	failN(al, parent, "tsping", layerTunnel, 3)
	got := tr.wait(t, 1)
	if len(got[0].Dependents) != 1 || got[0].Dependents[0] != "nas" {
		t.Fatalf("dependents %v", got[0].Dependents)
	}
	got[0].Dependents[0] = "mutated"
	got[0].Target.Checks = append(got[0].Target.Checks, icmpCheck{})
	parent.FailThreshold = 99
	parent.Checks = append(parent.Checks, icmpCheck{})
	al.mu.Lock()
	internal := len(al.targets["router"].target.Checks)
	threshold := al.targets["router"].target.FailThreshold
	al.mu.Unlock()
	if internal != 1 || threshold != 3 {
		t.Fatalf("internal checks %d threshold %d", internal, threshold)
	}
	recoverTarget(al, parent)
	pass(al, child)
	failN(al, parent, "tsping", layerTunnel, 3)
	again := tr.wait(t, 3)
	if len(again[2].Dependents) != 1 || again[2].Dependents[0] != "nas" {
		t.Fatalf("dependents %v", again[2].Dependents)
	}
}

func TestUnknownTransitions(t *testing.T) {
	t.Parallel()
	t.Run("silent up", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{target}, nil)
		passCheck(al, target, "icmp")
		if gotState(al, "nas") != StateUnknown {
			t.Fatal("up before every check")
		}
		passCheck(al, target, "tcp:445")
		if gotState(al, "nas") != StateUp {
			t.Fatalf("state %s", gotState(al, "nas"))
		}
		tr.expectNone(t)
	})
	t.Run("down at start", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "tcp:445")
		al, clk, tr := testAlerter(t, []*Target{target}, nil)
		failAt(al, target, "tcp:445", layerService, clkTimes(clk, 3)...)
		got := tr.wait(t, 1)
		if got[0].From != StateUnknown || got[0].To != StateDown || got[0].Check != "tcp:445" {
			t.Fatalf("%+v", got[0])
		}
	})
}

func TestGoingDown(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC)
	t.Run("threshold", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nyc-nas", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{target}, nil)
		pass(al, target)
		failAt(al, target, "tcp:445", layerService, start, start.Add(30*time.Second), start.Add(time.Minute))
		got := tr.wait(t, 1)
		if got[0].From != StateUp || got[0].To != StateDown || got[0].Check != "tcp:445" ||
			got[0].Failures != 3 || !got[0].Since.Equal(start) {
			t.Fatalf("%+v", got[0])
		}
	})
	t.Run("intermittent", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{target}, nil)
		pass(al, target)
		failN(al, target, "tcp:445", layerService, 2)
		passCheck(al, target, "tcp:445")
		failN(al, target, "tcp:445", layerService, 2)
		if gotState(al, "nas") != StateUp {
			t.Fatalf("state %s", gotState(al, "nas"))
		}
		tr.expectNone(t)
	})
	t.Run("first check to trip", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{target}, nil)
		pass(al, target)
		failN(al, target, "icmp", layerLAN, 3)
		failN(al, target, "tcp:445", layerService, 1)
		got := tr.wait(t, 1)
		if got[0].Check != "icmp" || got[0].Layer != layerLAN {
			t.Fatalf("trip %+v", got[0])
		}
	})
}

func TestRecovery(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC)
	t.Run("full", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{target}, nil)
		pass(al, target)
		failAt(al, target, "tcp:445", layerService, start, start.Add(time.Second), start.Add(2*time.Second))
		passCheck(al, target, "tcp:445")
		passCheck(al, target, "tcp:445")
		got := tr.wait(t, 2)
		up := got[1]
		if up.From != StateDown || up.To != StateUp || !up.DownSince.Equal(start) || up.Check != "tcp:445" ||
			up.Layer != layerService {
			t.Fatalf("%+v", up)
		}
	})
	t.Run("partial and second check", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{target}, nil)
		pass(al, target)
		failN(al, target, "tcp:445", layerService, 3)
		failN(al, target, "icmp", layerLAN, 1)
		passCheck(al, target, "tcp:445")
		passCheck(al, target, "tcp:445")
		passCheck(al, target, "icmp")
		if gotState(al, "nas") != StateDown {
			t.Fatalf("state %s", gotState(al, "nas"))
		}
		passCheck(al, target, "icmp")
		got := tr.wait(t, 2)
		if got[1].To != StateUp {
			t.Fatalf("%+v", got[1])
		}
	})
}

func TestOrderedDelivery(t *testing.T) {
	t.Parallel()
	target := alertTarget("nas", "icmp")
	clk := newAlertClock()
	reg := prometheus.NewRegistry()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var got []Transition
	al, err := newAlerter(alertConfig{
		Clock: clk, Targets: []*Target{target}, Parents: nil, Recheck: nil, Registerer: reg, QueueCap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(al.stopDrain)
	al.addConsumer(consumerFunc(func(tr Transition) {
		once.Do(func() {
			if !al.IsDown(tr.Target.Name) {
				t.Errorf("down query during callback")
			}
			_ = al.Snapshots()
			close(entered)
			<-release
		})
		mu.Lock()
		got = append(got, tr)
		mu.Unlock()
	}))
	pass(al, target)
	failN(al, target, "icmp", layerLAN, 3)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not run")
	}
	passCheck(al, target, "icmp")
	passCheck(al, target, "icmp")
	close(release)
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("got %d", n)
		case <-time.After(time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got[0].To != StateDown || got[1].To != StateUp || got[0].Sequence >= got[1].Sequence {
		t.Fatalf("%+v then %+v", got[0], got[1])
	}
}

//nolint:gocognit // each subtest is one flap scenario
func TestFlapping(t *testing.T) {
	t.Parallel()
	t.Run("fifth change", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp")
		al, _, tr := testAlerter(t, []*Target{target}, nil)
		pass(al, target)
		for range 2 {
			failN(al, target, "icmp", layerLAN, 3)
			recoverTarget(al, target)
		}
		failN(al, target, "icmp", layerLAN, 3)
		got := tr.wait(t, 5)
		var flaps int
		for _, trn := range got {
			if trn.Flapping {
				flaps++
			}
		}
		if flaps != 1 || !got[4].Flapping {
			t.Fatalf("flaps %d last %+v", flaps, got[4])
		}
		if gaugeOf(t, al) != gaugeFlapping {
			t.Fatalf("gauge %v", gaugeOf(t, al))
		}
		recoverTarget(al, target)
		failN(al, target, "icmp", layerLAN, 3)
		tr.expectQuiet(t, 5)
	})
	t.Run("settle unresolved down", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp")
		al, clk, tr := testAlerter(t, []*Target{target}, nil)
		since := time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC)
		if err := al.Restore("nas", since); err != nil {
			t.Fatal(err)
		}
		recoverTarget(al, target)
		failN(al, target, "icmp", layerLAN, 3)
		recoverTarget(al, target)
		downSince := time.Date(2026, 4, 2, 14, 3, 0, 0, time.UTC)
		failAt(al, target, "icmp", layerTunnel, downSince, downSince.Add(time.Second), downSince.Add(2*time.Second))
		recoverTarget(al, target)
		got := tr.wait(t, 5)
		if !got[4].Flapping || got[4].To != StateUp {
			t.Fatalf("flap %+v", got[4])
		}
		advanceAlertClock(al, clk, 15*time.Minute)
		got = tr.wait(t, 6)
		last := got[5]
		if last.Flapping || last.From != StateDown || last.To != StateUp || !last.DownSince.Equal(downSince) {
			t.Fatalf("settle %+v", last)
		}
		if gaugeOf(t, al) != float64(StateUp) {
			t.Fatalf("gauge %v", gaugeOf(t, al))
		}
	})
	t.Run("settle reported state", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp")
		al, clk, tr := testAlerter(t, []*Target{target}, nil)
		pass(al, target)
		for range 2 {
			failN(al, target, "icmp", layerLAN, 3)
			recoverTarget(al, target)
		}
		failN(al, target, "icmp", layerLAN, 3)
		tr.wait(t, 5)
		recoverTarget(al, target)
		advanceAlertClock(al, clk, 15*time.Minute)
		tr.expectQuiet(t, 5)
		if gotState(al, "nas") != StateUp || gaugeOf(t, al) != float64(StateUp) {
			t.Fatalf("state %s gauge %v", gotState(al, "nas"), gaugeOf(t, al))
		}
	})
}

func TestParentGraph(t *testing.T) {
	t.Parallel()
	router := alertTarget("nyc-subnet-router", "tsping")
	other := alertTarget("nyc-router", "tsping")
	sw := alertTarget("nyc-core-switch", "icmp")
	nas := alertTarget("nyc-nas", "tcp:445")
	al, _, _ := testAlerter(t, []*Target{router, other, sw, nas}, map[string]string{"nyc-nas": "nyc-subnet-router"})
	if !al.suppressor("nyc-subnet-router") || al.ancestorDown("nyc-nas") {
		t.Fatal("override parent")
	}
	if err := al.UpdateParents(map[string]string{"nyc-nas": "nyc-router"}); err != nil {
		t.Fatal(err)
	}
	al.mu.Lock()
	got := al.parents["nyc-nas"]
	al.mu.Unlock()
	if got != "nyc-router" {
		t.Fatalf("learned %s", got)
	}
	if err := al.UpdateParents(map[string]string{"nyc-nas": "nyc-router", "nyc-core-switch": "nyc-nas"}); err != nil {
		t.Fatal(err)
	}
	if al.suppressor("nyc-nas") {
		t.Fatal("service target is a parent")
	}
	if err := al.UpdateParents(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	failN(al, nas, "tcp:445", layerUnrouted, 3)
	if gotState(al, "nyc-nas") != StateDown {
		t.Fatal("no-parent target did not go down")
	}
	cycle := map[string]string{"nyc-subnet-router": "nyc-router", "nyc-router": "nyc-subnet-router"}
	if err := al.UpdateParents(cycle); err == nil {
		t.Fatal("accepted cycle")
	}
	al.mu.Lock()
	defer al.mu.Unlock()
	if len(al.parents) != 0 {
		t.Fatalf("cycle replaced graph %v", al.parents)
	}
}

func TestSuppression(t *testing.T) {
	t.Parallel()
	t.Run("router dies", func(t *testing.T) {
		t.Parallel()
		router := alertTarget("nyc-subnet-router", "tsping")
		var targets []*Target
		targets = append(targets, router)
		parents := map[string]string{}
		for i := range 14 {
			name := "dev-" + string(rune('a'+i))
			targets = append(targets, alertTarget(name, "icmp"))
			parents[name] = router.Name
		}
		al, _, tr := testAlerter(t, targets, parents)
		for _, target := range targets[1:] {
			pass(al, target)
		}
		failN(al, router, "tsping", layerTunnel, 3)
		got := tr.wait(t, 1)
		if got[0].Target.Name != router.Name || len(got[0].Dependents) != 14 {
			t.Fatalf("dependents %d", len(got[0].Dependents))
		}
		if !slices.IsSorted(got[0].Dependents) {
			t.Fatal("unsorted")
		}
		for _, target := range targets[1:] {
			if gotState(al, target.Name) != StateSuppressed {
				t.Fatalf("%s %s", target.Name, gotState(al, target.Name))
			}
		}
		tr.expectQuiet(t, 1)
	})
	t.Run("already down", func(t *testing.T) {
		t.Parallel()
		router := alertTarget("router", "tsping")
		printer := alertTarget("nyc-printer", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{router, printer}, map[string]string{"nyc-printer": "router"})
		pass(al, printer)
		failN(al, printer, "tcp:445", layerService, 3)
		failN(al, router, "tsping", layerTunnel, 3)
		got := tr.wait(t, 2)
		if len(got[1].Dependents) != 0 || gotState(al, "nyc-printer") != StateDown {
			t.Fatalf("deps %v state %s", got[1].Dependents, gotState(al, "nyc-printer"))
		}
	})
	t.Run("transitive", func(t *testing.T) {
		t.Parallel()
		router := alertTarget("router", "tsping")
		sw := alertTarget("switch", "icmp")
		cam := alertTarget("cam", "tcp:80")
		al, _, tr := testAlerter(t, []*Target{router, sw, cam}, map[string]string{"switch": "router", "cam": "switch"})
		pass(al, sw)
		pass(al, cam)
		failN(al, router, "tsping", layerControl, 3)
		got := tr.wait(t, 1)
		if len(got[0].Dependents) != 2 || gotState(al, "cam") != StateSuppressed {
			t.Fatalf("deps %v cam %s", got[0].Dependents, gotState(al, "cam"))
		}
	})
}

func TestGraceHold(t *testing.T) {
	t.Parallel()
	router := func() *Target {
		t := alertTarget("nyc-subnet-router", "tsping")
		t.Interval = 15 * time.Second
		t.FailThreshold = 3
		t.Timeout = 5 * time.Second
		return t
	}
	t.Run("device first", func(t *testing.T) {
		t.Parallel()
		parent := router()
		sw := alertTarget("nyc-core-switch", "icmp")
		al, clk, tr := testAlerter(t, []*Target{parent, sw}, map[string]string{"nyc-core-switch": parent.Name})
		pass(al, sw)
		failN(al, sw, "icmp", layerControl, 3)
		tr.expectNone(t)
		failN(al, parent, "tsping", layerTunnel, 3)
		got := tr.wait(t, 1)
		if got[0].Target.Name != parent.Name || len(got[0].Dependents) != 1 || got[0].Dependents[0] != sw.Name {
			t.Fatalf("%+v", got[0])
		}
		advanceAlertClock(al, clk, 50*time.Second)
		tr.expectQuiet(t, 1)
		if gotState(al, sw.Name) != StateSuppressed {
			t.Fatalf("state %s", gotState(al, sw.Name))
		}
	})
	t.Run("parent stays up", func(t *testing.T) {
		t.Parallel()
		parent := router()
		sw := alertTarget("sw", "icmp")
		al, clk, tr := testAlerter(t, []*Target{parent, sw}, map[string]string{"sw": parent.Name})
		pass(al, sw)
		failN(al, sw, "icmp", layerControl, 3)
		advanceAlertClock(al, clk, 50*time.Second)
		got := tr.wait(t, 1)
		if got[0].To != StateDown || got[0].Layer != layerControl || got[0].Target.Name != "sw" {
			t.Fatalf("%+v", got[0])
		}
	})
	t.Run("recovered during hold", func(t *testing.T) {
		t.Parallel()
		parent := router()
		sw := alertTarget("sw", "icmp")
		al, clk, tr := testAlerter(t, []*Target{parent, sw}, map[string]string{"sw": parent.Name})
		pass(al, sw)
		failN(al, sw, "icmp", layerTunnel, 3)
		passCheck(al, sw, "icmp")
		advanceAlertClock(al, clk, 50*time.Second)
		tr.expectNone(t)
		if gotState(al, "sw") != StateUp {
			t.Fatalf("state %s", gotState(al, "sw"))
		}
	})
	for _, l := range []layer{layerUnrouted, layerLAN, layerService} {
		t.Run(string(l), func(t *testing.T) {
			t.Parallel()
			parent := router()
			sw := alertTarget("sw-"+string(l), "tcp:445")
			al, _, tr := testAlerter(t, []*Target{parent, sw}, map[string]string{sw.Name: parent.Name})
			pass(al, sw)
			failN(al, sw, "tcp:445", l, 3)
			got := tr.wait(t, 1)
			if got[0].To != StateDown || got[0].Layer != l {
				t.Fatalf("%s %+v", l, got[0])
			}
		})
	}
}

func TestRelease(t *testing.T) {
	t.Parallel()
	t.Run("router returns", func(t *testing.T) {
		t.Parallel()
		router := alertTarget("router", "tsping")
		nas := alertTarget("nas", "tcp:445")
		var rec []string
		var mu sync.Mutex
		al, _, tr := testAlerterOpt(
			t,
			[]*Target{router, nas},
			map[string]string{"nas": "router"},
			0,
			func(name string) {
				mu.Lock()
				rec = append(rec, name)
				mu.Unlock()
			},
		)
		pass(al, nas)
		failN(al, router, "tsping", layerTunnel, 3)
		recoverTarget(al, router)
		got := tr.wait(t, 2)
		if len(got[1].Dependents) != 1 || got[1].Dependents[0] != "nas" {
			t.Fatalf("deps %v", got[1].Dependents)
		}
		mu.Lock()
		gotRec := append([]string{}, rec...)
		mu.Unlock()
		if len(gotRec) != 1 || gotRec[0] != "nas" {
			t.Fatalf("recheck %v", gotRec)
		}
		pass(al, nas)
		if gotState(al, "nas") != StateUp {
			t.Fatalf("state %s", gotState(al, "nas"))
		}
		tr.expectQuiet(t, 2)
	})
	t.Run("device still dead", func(t *testing.T) {
		t.Parallel()
		router := alertTarget("router", "tsping")
		nas := alertTarget("nyc-nas", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{router, nas}, map[string]string{"nyc-nas": "router"})
		pass(al, nas)
		failN(al, router, "tsping", layerTunnel, 3)
		recoverTarget(al, router)
		failN(al, nas, "tcp:445", layerService, 3)
		got := tr.wait(t, 3)
		last := got[2]
		if last.Target.Name != "nyc-nas" || last.From != StateSuppressed || last.To != StateDown {
			t.Fatalf("%+v", last)
		}
	})
	t.Run("failover", func(t *testing.T) {
		t.Parallel()
		a := alertTarget("router-a", "tsping")
		b := alertTarget("router-b", "tsping")
		nas := alertTarget("nyc-nas", "tcp:445")
		al, _, tr := testAlerter(t, []*Target{a, b, nas}, map[string]string{"nyc-nas": "router-a"})
		pass(al, nas)
		failN(al, a, "tsping", layerTunnel, 3)
		if err := al.UpdateParents(map[string]string{"nyc-nas": "router-b"}); err != nil {
			t.Fatal(err)
		}
		if gotState(al, "nyc-nas") != StateSuppressed {
			t.Fatalf("state %s", gotState(al, "nyc-nas"))
		}
		failN(al, nas, "tcp:445", layerService, 3)
		got := tr.wait(t, 2)
		if got[1].From != StateSuppressed || got[1].To != StateDown || got[1].Target.Name != "nyc-nas" {
			t.Fatalf("%+v", got[1])
		}
		if gotState(al, "router-a") != StateDown {
			t.Fatal("router A recovered")
		}
	})
}

//nolint:gocognit // each subtest is one restore or first-round scenario
func TestRestoreAndFirstRound(t *testing.T) {
	t.Parallel()
	t.Run("restart during outage", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nyc-nas", "icmp")
		al, _, tr := testAlerter(t, []*Target{target}, nil)
		since := time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC)
		if err := al.Restore("nyc-nas", since); err != nil {
			t.Fatal(err)
		}
		recoverTarget(al, target)
		got := tr.wait(t, 1)
		if got[0].From != StateDown || got[0].To != StateUp || !got[0].DownSince.Equal(since) {
			t.Fatalf("%+v", got[0])
		}
	})
	t.Run("already observed", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp")
		al, _, _ := testAlerter(t, []*Target{target}, nil)
		pass(al, target)
		before := gotState(al, "nas")
		if err := al.Restore("nas", time.Now()); err == nil {
			t.Fatal("restore succeeded")
		}
		if gotState(al, "nas") != before {
			t.Fatal("state changed")
		}
	})
	t.Run("pending and discard", func(t *testing.T) {
		t.Parallel()
		al, _, _ := testAlerter(t, nil, nil)
		since := time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC)
		if err := al.Restore("old-router", since); err != nil {
			t.Fatal(err)
		}
		if err := al.Restore("gone-nas", since.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-al.FirstRoundDone():
		default:
			t.Fatal("zero targets did not signal")
		}
		if len(al.PendingRestores()) != 2 {
			t.Fatalf("pending %v", al.PendingRestores())
		}
		al.DiscardPendingRestore("gone-nas")
		if pend := al.PendingRestores(); len(pend) != 1 || pend[0] != "old-router" {
			t.Fatalf("pending %v", pend)
		}
		target := alertTarget("old-router", "tsping")
		if err := al.AddTarget(target); err != nil {
			t.Fatal(err)
		}
		if gotState(al, "old-router") != StateDown {
			t.Fatalf("state %s", gotState(al, "old-router"))
		}
		recoverTarget(al, target)
		// no consumer on this alerter's extra target path: state is enough
		if gotState(al, "old-router") != StateUp {
			t.Fatal("did not recover from the restored down")
		}
	})
	t.Run("slow fifth check", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp", "tcp:1", "tcp:2", "tcp:3", "tcp:4")
		al, _, _ := testAlerter(t, []*Target{target}, nil)
		for _, check := range []string{"icmp", "tcp:1", "tcp:2", "tcp:3"} {
			passCheck(al, target, check)
		}
		select {
		case <-al.FirstRoundDone():
			t.Fatal("signaled early")
		default:
		}
		failN(al, target, "tcp:4", layerService, 1)
		select {
		case <-al.FirstRoundDone():
		default:
			t.Fatal("still open")
		}
	})
	t.Run("implicit does not delay", func(t *testing.T) {
		t.Parallel()
		target := alertTarget("nas", "icmp")
		al, _, _ := testAlerter(t, []*Target{target}, nil)
		extra := alertTarget("nyc-router", "tsping")
		extra.Implicit = true
		if err := al.AddTarget(extra); err != nil {
			t.Fatal(err)
		}
		pass(al, target)
		select {
		case <-al.FirstRoundDone():
		default:
			t.Fatal("waited for the implicit target")
		}
	})
}

//nolint:gocognit // each subtest is one registration scenario
func TestRuntimeRegistration(t *testing.T) {
	t.Parallel()
	t.Run("implicit parent", func(t *testing.T) {
		t.Parallel()
		child := alertTarget("nas", "icmp")
		al, _, tr := testAlerter(t, []*Target{child}, nil)
		parent := alertTarget("nyc-router", "tsping")
		parent.Implicit = true
		if err := al.AddTarget(parent); err != nil {
			t.Fatal(err)
		}
		if err := al.UpdateParents(map[string]string{"nas": "nyc-router"}); err != nil {
			t.Fatal(err)
		}
		pass(al, child)
		failN(al, parent, "tsping", layerTunnel, 3)
		got := tr.wait(t, 1)
		if got[0].From != StateUnknown || got[0].To != StateDown || got[0].Target.Name != "nyc-router" ||
			len(got[0].Dependents) != 1 {
			t.Fatalf("%+v", got[0])
		}
	})
	t.Run("add paths", func(t *testing.T) {
		t.Parallel()
		al, _, tr := testAlerter(t, nil, nil)
		since := time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC)
		if err := al.Restore("nyc-router", since); err != nil {
			t.Fatal(err)
		}
		restored := alertTarget("nyc-router", "tsping")
		if err := al.AddTarget(restored); err != nil {
			t.Fatal(err)
		}
		if gotState(al, "nyc-router") != StateDown {
			t.Fatal(gotState(al, "nyc-router"))
		}
		tr.expectNone(t)
		recoverTarget(al, restored)
		plain := alertTarget("plain", "icmp")
		if err := al.AddTarget(plain); err != nil {
			t.Fatal(err)
		}
		if gotState(al, "plain") != StateUnknown {
			t.Fatal(gotState(al, "plain"))
		}
		if err := al.AddTarget(plain); err == nil {
			t.Fatal("duplicate")
		}
	})
	t.Run("under down parent", func(t *testing.T) {
		t.Parallel()
		parent := alertTarget("router", "tsping")
		al, _, _ := testAlerter(t, []*Target{parent}, nil)
		failN(al, parent, "tsping", layerTunnel, 3)
		child := alertTarget("nas", "icmp")
		if err := al.UpdateParents(map[string]string{"nas": "router"}); err != nil {
			t.Fatal(err)
		}
		if err := al.AddTarget(child); err != nil {
			t.Fatal(err)
		}
		if gotState(al, "nas") != StateSuppressed {
			t.Fatalf("state %s", gotState(al, "nas"))
		}
	})
}

func TestIsDownAndRemove(t *testing.T) {
	t.Parallel()
	target := alertTarget("nas", "icmp")
	al, clk, _ := testAlerter(t, []*Target{target}, nil)
	if al.IsDown("missing") || al.IsDown("nas") {
		t.Fatal("unknown or fresh reported down")
	}
	pass(al, target)
	if al.IsDown("nas") {
		t.Fatal("up")
	}
	if err := al.RemoveTarget("nas"); err != nil {
		t.Fatal(err)
	}
	if seriesHas(t, al, "nas") {
		t.Fatal("series remains")
	}
	if err := al.AddTarget(target); err != nil {
		t.Fatal(err)
	}
	pass(al, target)
	parent := alertTarget("router", "tsping")
	if err := al.AddTarget(parent); err != nil {
		t.Fatal(err)
	}
	if err := al.UpdateParents(map[string]string{"nas": "router"}); err != nil {
		t.Fatal(err)
	}
	failN(al, parent, "tsping", layerTunnel, 3)
	if !al.IsDown("nas") || gotState(al, "nas") != StateSuppressed {
		t.Fatal("suppressed query")
	}
	if err := al.RemoveTarget("nas"); err != nil {
		t.Fatal(err)
	}
	// Flapping after an unresolved down.
	leaf := alertTarget("leaf", "icmp")
	if err := al.AddTarget(leaf); err != nil {
		t.Fatal(err)
	}
	if err := al.Restore("leaf", time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	recoverTarget(al, leaf)
	failN(al, leaf, "icmp", layerLAN, 3)
	recoverTarget(al, leaf)
	failN(al, leaf, "icmp", layerLAN, 3)
	recoverTarget(al, leaf)
	if !al.IsDown("leaf") || gotState(al, "leaf") != StateUp {
		t.Fatalf("flapping query state %s down %v", gotState(al, "leaf"), al.IsDown("leaf"))
	}
	if err := al.RemoveTarget("leaf"); err == nil {
		t.Fatal("removed emitted down")
	}
	advanceAlertClock(al, clk, 15*time.Minute)
	_ = clk
}

//nolint:gocognit // the loop is the property sequence
func TestRemoveProperty(t *testing.T) {
	t.Parallel()
	parent := alertTarget("router", "tsping")
	child := alertTarget("nas", "icmp")
	al, _, _ := testAlerter(t, []*Target{parent, child}, map[string]string{"nas": "router"})
	rng := rand.New(rand.NewPCG(1, 2))
	targets := []*Target{parent, child}
	for range 40 {
		target := targets[rng.IntN(len(targets))]
		check := target.Checks[0].String()
		if rng.IntN(2) == 0 {
			passCheck(al, target, check)
		} else {
			failN(al, target, check, layerLAN, 1)
		}
		for _, name := range []string{"router", "nas", "missing"} {
			if !al.IsDown(name) {
				if err := al.RemoveTarget(name); err != nil {
					t.Fatalf("remove %s: %v", name, err)
				}
			}
		}
		if gotState(al, "router") < 0 {
			if err := al.AddTarget(parent); err != nil {
				t.Fatal(err)
			}
		}
		if gotState(al, "nas") < 0 {
			if err := al.AddTarget(child); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTargetStateGauge(t *testing.T) {
	t.Parallel()
	target := alertTarget("nas", "icmp")
	al, _, _ := testAlerter(t, []*Target{target}, nil)
	pass(al, target)
	if gaugeOf(t, al) != float64(StateUp) {
		t.Fatalf("up %v", gaugeOf(t, al))
	}
	parent := alertTarget("router", "tsping")
	if err := al.AddTarget(parent); err != nil {
		t.Fatal(err)
	}
	if err := al.UpdateParents(map[string]string{"nas": "router"}); err != nil {
		t.Fatal(err)
	}
	failN(al, parent, "tsping", layerTunnel, 3)
	if gaugeOf(t, al) != float64(StateSuppressed) {
		t.Fatalf("suppressed %v", gaugeOf(t, al))
	}
}

func TestDirtySnapshots(t *testing.T) {
	t.Parallel()
	target := alertTarget("nas", "icmp")
	al, _, _ := testAlerterOpt(t, []*Target{target}, nil, -1, nil)
	pass(al, target)
	failN(al, target, "icmp", layerLAN, 3)
	recoverTarget(al, target)
	snaps := al.Snapshots()
	if len(snaps) != 1 || !snaps[0].Dirty || snaps[0].State != StateUp || snaps[0].Sequence < 2 {
		t.Fatalf("%+v", snaps)
	}
	seq := snaps[0].Sequence
	al.Acknowledge("nas", seq-1)
	if !al.Snapshots()[0].Dirty {
		t.Fatal("older ack cleared the newer generation")
	}
	al.Acknowledge("nas", seq)
	if al.Snapshots()[0].Dirty {
		t.Fatal("still dirty")
	}
	snaps[0].Target.Checks = append(snaps[0].Target.Checks, icmpCheck{})
	al.mu.Lock()
	n := len(al.snapshots["nas"].Target.Checks)
	al.mu.Unlock()
	if n != 1 {
		t.Fatal("snapshot aliased checks")
	}
	if err := al.RemoveTarget("nas"); err != nil {
		t.Fatal(err)
	}
	snaps = al.Snapshots()
	if len(snaps) != 1 || !snaps[0].Removed || !snaps[0].Dirty {
		t.Fatalf("removed %+v", snaps)
	}
	old := snaps[0].Sequence
	passTarget := alertTarget("other", "icmp")
	if err := al.AddTarget(passTarget); err != nil {
		t.Fatal(err)
	}
	al.MarkDirty("nas")
	al.Acknowledge("nas", old-1)
	if len(al.Snapshots()) == 0 || !al.Snapshots()[0].Dirty {
		t.Fatal("removed snapshot dropped early")
	}
	al.Acknowledge("nas", old)
	for _, snap := range al.Snapshots() {
		if snap.Name == "nas" {
			t.Fatal("acked removal remained")
		}
	}
}

func TestStaleGeneration(t *testing.T) {
	t.Parallel()
	target := alertTarget("nas", "tcp:445")
	parent := alertTarget("router", "tsping")
	al, _, tr := testAlerter(t, []*Target{target, parent}, map[string]string{"nas": "router"})
	pass(al, target)
	failN(al, parent, "tsping", layerTunnel, 3)
	recoverTarget(al, parent)
	stale := failClass(target, "tcp:445", layerService, time.Time{})
	stale.Generation = 1
	al.ObserveClassified(stale)
	al.ObserveClassified(stale)
	al.ObserveClassified(stale)
	if gotState(al, "nas") == StateDown {
		t.Fatal("stale timeout counted")
	}
	failN(al, target, "tcp:445", layerService, 3)
	got := tr.wait(t, 3)
	last := got[len(got)-1]
	if last.From != StateSuppressed || last.To != StateDown || last.Target.Name != "nas" {
		t.Fatalf("%+v", last)
	}
}

func TestTopologyWithoutCheck(t *testing.T) {
	t.Parallel()
	router := alertTarget("router", "tsping")
	nas := alertTarget("nas", "icmp")
	var mu sync.Mutex
	var rec []string
	al, _, tr := testAlerterOpt(t, []*Target{router, nas}, map[string]string{"nas": "router"}, 0, func(name string) {
		mu.Lock()
		rec = append(rec, name)
		mu.Unlock()
	})
	pass(al, nas)
	failN(al, router, "tsping", layerTunnel, 3)
	if err := al.UpdateParents(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string{}, rec...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "nas" {
		t.Fatalf("recheck %v", got)
	}
	pass(al, nas)
	if gotState(al, "nas") != StateUp {
		t.Fatalf("state %s", gotState(al, "nas"))
	}
	if err := al.UpdateParents(map[string]string{"nas": "router"}); err != nil {
		t.Fatal(err)
	}
	if gotState(al, "nas") != StateSuppressed {
		t.Fatalf("not suppressed: %s", gotState(al, "nas"))
	}
	tr.expectQuiet(t, 1)
}

func TestAlerterWiring(t *testing.T) {
	t.Parallel()
	target := alertTarget("nas", "icmp")
	target.Addr = "100.64.0.8"
	target.Network = NetworkLocal
	var cfg Config
	cfg.Targets = []Target{*target}
	ctx, cancel := context.WithCancel(context.Background())
	reg := prometheus.NewRegistry()
	app := &app{
		cfg:      &cfg,
		env:      new(fakeEnv),
		log:      slog.New(slog.DiscardHandler),
		clock:    tstime.StdClock{},
		pub:      newPublisher(),
		metrics:  reg,
		stdout:   nil,
		socket:   "",
		listen:   "",
		ui:       false,
		dynamic:  nil,
		registry: nil,
		resolver: nil,
		presence: nil,
		class:    nil,
		learner:  nil,
		alerts:   nil,
		probeMu:  sync.Mutex{},
		probes:   nil,
		watch: func(context.Context, ipn.NotifyWatchOpt) (ipnWatch, error) {
			return nil, errors.New("stopped")
		}, notify: nil, collector: CollectorInfo{Name: "", TailscaleIP: ""},
	}
	runtime := app.startCollectors(ctx, nil)
	t.Cleanup(func() {
		cancel()
		runtime.pool.shutdown()
	})
	for range 3 {
		result := failClass(&cfg.Targets[0], "icmp", layerLAN, time.Time{}).Result
		result.Kind = checkICMP
		app.pub.publish(result)
	}
	deadline := time.After(2 * time.Second)
	for gotState(app.alerts, "nas") != StateDown {
		select {
		case <-deadline:
			t.Fatalf("state %s", gotState(app.alerts, "nas"))
		case <-time.After(time.Millisecond):
		}
	}
	child := alertTarget("cam", "tcp:80")
	if err := app.alerts.AddTarget(child); err != nil {
		t.Fatal(err)
	}
	if err := app.alerts.UpdateParents(map[string]string{"cam": "nas"}); err != nil {
		t.Fatal(err)
	}
	if gotState(app.alerts, "cam") != StateSuppressed {
		t.Fatalf("cam %s", gotState(app.alerts, "cam"))
	}
	if err := app.alerts.UpdateParents(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	pass(app.alerts, child)
	if gotState(app.alerts, "cam") != StateUp {
		t.Fatalf("released cam %s", gotState(app.alerts, "cam"))
	}
}

type consumerFunc func(Transition)

func (f consumerFunc) ObserveTransition(tr Transition) { f(tr) }

type trap struct {
	mu       sync.Mutex
	got      []Transition
	ch       chan Transition
	rechecks []string
}

func testAlerter(t *testing.T, targets []*Target, parents map[string]string) (*alerter, *tstest.Clock, *trap) {
	t.Helper()
	return testAlerterOpt(t, targets, parents, 0, nil)
}

func testAlerterOpt(
	t *testing.T,
	targets []*Target,
	parents map[string]string,
	queue int,
	recheck func(string),
) (*alerter, *tstest.Clock, *trap) {
	t.Helper()
	clk := newAlertClock()
	tr := &trap{mu: sync.Mutex{}, got: nil, ch: make(chan Transition, 64), rechecks: nil}
	prev := recheck
	cfg := alertConfig{
		Clock:      clk,
		Targets:    targets,
		Parents:    parents,
		Recheck:    nil,
		Registerer: prometheus.NewRegistry(),
		QueueCap:   queue,
	}
	cfg.Recheck = func(name string) {
		tr.mu.Lock()
		tr.rechecks = append(tr.rechecks, name)
		tr.mu.Unlock()
		if prev != nil {
			prev(name)
		}
	}
	al, err := newAlerter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	al.addConsumer(tr)
	t.Cleanup(al.stopDrain)
	return al, clk, tr
}

func newAlertClock() *tstest.Clock {
	return tstest.NewClock(
		tstest.ClockOpts{
			Start:            time.Date(2026, 4, 2, 14, 2, 11, 0, time.UTC),
			Step:             0,
			TimerChannelSize: 0,
			FollowRealTime:   false,
		},
	)
}

func (tr *trap) ObserveTransition(one Transition) {
	tr.mu.Lock()
	tr.got = append(tr.got, one)
	tr.mu.Unlock()
	tr.ch <- one
}

func (tr *trap) wait(t *testing.T, n int) []Transition {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		tr.mu.Lock()
		if len(tr.got) >= n {
			out := append([]Transition{}, tr.got...)
			tr.mu.Unlock()
			return out
		}
		tr.mu.Unlock()
		select {
		case <-tr.ch:
		case <-deadline:
			tr.mu.Lock()
			got := len(tr.got)
			tr.mu.Unlock()
			t.Fatalf("got %d transitions, want %d", got, n)
		}
	}
}

func (tr *trap) expectNone(t *testing.T) {
	t.Helper()
	tr.expectQuiet(t, 0)
}

func (tr *trap) expectQuiet(t *testing.T, have int) {
	t.Helper()
	select {
	case <-tr.ch:
	case <-time.After(30 * time.Millisecond):
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.got) != have {
		t.Fatalf("got %d transitions, want %d", len(tr.got), have)
	}
}

func alertTarget(name string, checks ...string) *Target {
	t := &Target{
		Dynamic:          false,
		Name:             name,
		Addr:             "10.20.0.5",
		Interval:         15 * time.Second,
		Timeout:          5 * time.Second,
		FailThreshold:    3,
		RecoverThreshold: 2,
		FlapLimit:        4,
		FlapWindow:       15 * time.Minute,
		Network:          NetworkTailnet,
		Severity:         SeverityPage,
		Site:             "", Parent: "", InsecureTLS: false, Checks: nil, Implicit: false, StableID: "", NodeID: 0,
	}
	for _, raw := range checks {
		chk, err := parseCheck(raw)
		if err != nil {
			panic(err)
		}
		t.Checks = append(t.Checks, chk)
	}
	return t
}

func pass(al *alerter, target *Target) {
	for _, chk := range target.Checks {
		passCheck(al, target, chk.String())
	}
}

func passCheck(al *alerter, target *Target, check string) {
	var cl Classified
	cl.Target, cl.Check = target, check
	cl.Start = time.Date(2026, 4, 2, 14, 0, 0, 0, time.UTC)
	al.ObserveClassified(cl)
}

func recoverTarget(al *alerter, target *Target) {
	for range target.RecoverThreshold {
		pass(al, target)
	}
}

func failN(al *alerter, target *Target, check string, l layer, n int) {
	for range n {
		al.ObserveClassified(failClass(target, check, l, time.Time{}))
	}
}

func failAt(al *alerter, target *Target, check string, l layer, at ...time.Time) {
	for _, one := range at {
		al.ObserveClassified(failClass(target, check, l, one))
	}
}

func failClass(target *Target, check string, l layer, at time.Time) Classified {
	var cl Classified
	cl.Target = target
	cl.Check = check
	cl.Start = at
	cl.Err = errors.New("timeout: down")
	cl.Layer = l
	return cl
}

func clkTimes(clk *tstest.Clock, n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = clk.Now().Add(time.Duration(i) * time.Second)
	}
	return out
}

func gotState(al *alerter, name string) State {
	al.mu.Lock()
	defer al.mu.Unlock()
	ts := al.targets[name]
	if ts == nil {
		return -1
	}
	return ts.state
}

func gaugeOf(t *testing.T, al *alerter) float64 {
	t.Helper()
	return testutil.ToFloat64(al.gauge.WithLabelValues("nas"))
}

func seriesHas(t *testing.T, al *alerter, name string) bool {
	t.Helper()
	ch := make(chan prometheus.Metric, 16)
	al.gauge.Collect(ch)
	close(ch)
	for metric := range ch {
		var pb dto.Metric
		if err := metric.Write(&pb); err != nil {
			t.Fatal(err)
		}
		for _, lp := range pb.GetLabel() {
			if lp.GetName() == labelTarget && lp.GetValue() == name {
				return true
			}
		}
	}
	return false
}

func advanceAlertClock(al *alerter, clk *tstest.Clock, d time.Duration) {
	clk.Advance(d)
	al.timerCallbacks.Wait()
}

func TestRemoveDownImplicitTarget(t *testing.T) {
	t.Parallel()
	implicit := alertTarget("nyc-router", "tsping")
	implicit.Implicit = true
	al, _, _ := testAlerter(t, []*Target{implicit}, nil)
	failN(al, implicit, "tsping", layerService, 3)
	if !al.IsDown("nyc-router") {
		t.Fatal("setup: implicit target should be down")
	}
	if err := al.RemoveTarget("nyc-router"); err != nil {
		t.Fatalf("a down implicit target must be removable: %v", err)
	}
	snaps := al.Snapshots()
	if len(snaps) != 1 || !snaps[0].Removed || !snaps[0].Dirty || snaps[0].State != StateDown {
		t.Fatalf("removal must leave a removed dirty snapshot of the open incident: %+v", snaps)
	}
}

func TestRemoveDownDynamicTarget(t *testing.T) {
	t.Parallel()
	dyn := alertTarget("lab-nas", "icmp")
	dyn.Dynamic = true
	al, _, tr := testAlerter(t, []*Target{dyn}, nil)
	failN(al, dyn, "icmp", layerService, 3)
	tr.wait(t, 1)
	if !al.IsDown("lab-nas") {
		t.Fatal("setup: dynamic target should be down")
	}
	if err := al.RemoveTarget("lab-nas"); err != nil {
		t.Fatalf("a down dynamic target must be removable: %v", err)
	}
	tr.expectQuiet(t, 1)
	snaps := al.Snapshots()
	if len(snaps) != 1 || !snaps[0].Removed || !snaps[0].Dirty || snaps[0].State != StateDown {
		t.Fatalf("removal must leave a removed dirty snapshot of the open incident: %+v", snaps)
	}
	if seriesHas(t, al, "lab-nas") {
		t.Fatal("series remains")
	}
}
