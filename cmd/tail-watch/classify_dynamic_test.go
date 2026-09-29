package main

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"tailscale.com/tstime"
)

func lastGraph(t *testing.T, c *classifier) map[string]string {
	t.Helper()
	mem := c.registry.(*memRegistry)
	mem.mu.Lock()
	defer mem.mu.Unlock()
	if len(mem.graphs) == 0 {
		return nil
	}
	return mem.graphs[len(mem.graphs)-1]
}

func dynamicChild(name, parent string) *Target {
	t := alertTarget(name, "icmp")
	t.Dynamic = true
	t.Parent = parent
	return t
}

func TestAddConfiguredPublishesAndRemovesParentEdge(t *testing.T) {
	t.Parallel()
	router := alertTarget("router", "icmp")
	c, _ := newTestClassifier(t, new(fakeEnv), router)
	ctx := context.Background()

	child := dynamicChild("lab-nas", "router")
	if err := c.AddConfigured(ctx, child); err != nil {
		t.Fatal(err)
	}
	if got := lastGraph(t, c)["lab-nas"]; got != "router" {
		t.Fatalf("published graph %v", lastGraph(t, c))
	}
	if c.target("lab-nas") != child {
		t.Fatal("classifier does not know the target")
	}
	if n := testutil.ToFloat64(c.parents.WithLabelValues("lab-nas", "router")); n != 1 {
		t.Fatalf("parent metric %v", n)
	}
	if err := c.AddConfigured(ctx, dynamicChild("lab-nas", "")); err == nil {
		t.Fatal("duplicate add accepted")
	}

	c.RemoveConfigured(ctx, "lab-nas")
	if _, ok := lastGraph(t, c)["lab-nas"]; ok {
		t.Fatalf("edge survived removal: %v", lastGraph(t, c))
	}
	if c.target("lab-nas") != nil {
		t.Fatal("classifier still knows the target")
	}
	if testutil.CollectAndCount(c.parents) != 0 {
		t.Fatal("parent series remains")
	}
}

func TestAddConfiguredRollsBackOnCycle(t *testing.T) {
	t.Parallel()
	router := alertTarget("router", "icmp")
	c, _ := newTestClassifier(t, new(fakeEnv), router)
	if err := c.AddConfigured(context.Background(), dynamicChild("loop", "loop")); err == nil {
		t.Fatal("cycle accepted")
	}
	if c.target("loop") != nil || len(c.configured) != 1 {
		t.Fatal("failed add was not rolled back")
	}
}

func TestDynamicParentSurvivesLearningRound(t *testing.T) {
	t.Parallel()
	router := alertTarget("router", "icmp")
	c, _ := newTestClassifier(t, new(fakeEnv), router)
	ctx := context.Background()
	if err := c.AddConfigured(ctx, dynamicChild("lab-nas", "router")); err != nil {
		t.Fatal(err)
	}
	c.parentOf = map[string]string{}
	c.finishRound(ctx, nil, true)
	if got := lastGraph(t, c)["lab-nas"]; got != "router" {
		t.Fatalf("learning round dropped the dynamic edge: %v", lastGraph(t, c))
	}
}

func TestDynamicTargetSuppressedByDownParent(t *testing.T) {
	t.Parallel()
	router := alertTarget("router", "icmp")
	al, _, _ := testAlerter(t, []*Target{router}, nil)
	a := new(app)
	reg := newRuntimeRegistry(t.Context(), a, nil)
	reg.alert = al
	reg.seed([]*Target{router})
	a.class = newClassifier(
		newPresence(nil, tstime.StdClock{}, nil), new(fakeEnv), nil, tstime.StdClock{},
		prometheus.NewRegistry(), []*Target{router}, reg,
	)
	if err := reg.AddTarget(router); err != nil {
		t.Fatal(err)
	}
	failN(al, router, "icmp", layerLAN, 3)
	if !al.IsDown("router") {
		t.Fatal("setup: router should be down")
	}

	child := dynamicChild("lab-nas", "router")
	if err := reg.AddTarget(child); err != nil {
		t.Fatal(err)
	}
	if gotState(al, "lab-nas") != StateSuppressed {
		t.Fatalf("child state %s, want SUPPRESSED", gotState(al, "lab-nas"))
	}
	if err := reg.RemoveTarget("lab-nas"); err != nil {
		t.Fatal(err)
	}
	if a.class.target("lab-nas") != nil {
		t.Fatal("removal left the target in the classifier")
	}
}

func TestDynamicTargetFailureIsClassified(t *testing.T) {
	t.Parallel()
	yamlNAS := alertTarget("yaml-nas", "icmp")
	c, _ := newTestClassifier(t, new(fakeEnv), yamlNAS)
	dynNAS := dynamicChild("lab-nas", "")
	if err := c.AddConfigured(context.Background(), dynNAS); err != nil {
		t.Fatal(err)
	}
	fail := func(target *Target) Classified {
		return c.observeOne(Result{Target: target, Kind: checkICMP, Err: context.DeadlineExceeded,
			Check:      "",
			Start:      time.Time{},
			Generation: 0,
			Latency:    0,
			Ping:       nil,
		})
	}
	want, got := fail(yamlNAS), fail(dynNAS)
	if got.Layer == "" || got.Layer != want.Layer {
		t.Fatalf("dynamic layer %q, YAML layer %q", got.Layer, want.Layer)
	}
}
