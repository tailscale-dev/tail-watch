package main

import (
	"testing"
	"time"
)

func routeHarness(t *testing.T, sinks ...SinkConfig) *notifyHarness {
	t.Helper()
	return newHarness(t, harnessOpts{
		sinks: sinks, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil,
	})
}

func publishRoutes(n *notifier, routes ...Route) {
	n.routes.Store(newRouteTable(routes))
}

func applyNow(t *testing.T, h *notifyHarness, tr Transition) {
	t.Helper()
	h.n.mu.Lock()
	defer h.n.mu.Unlock()
	if !h.n.applyLocked(tr, false) {
		t.Fatal("transition was not applied")
	}
}

func queuedOf(n *notifier, sink string) []queued {
	q := n.b.queues[sink]
	if q == nil {
		return nil
	}
	out := make([]queued, len(q.items))
	for i, it := range q.items {
		out[i] = *it
	}
	return out
}

func routedDown(at time.Time, route, site string, sev Severity) Transition {
	tr := downTransition(1, at)
	tr.Target.Route = route
	tr.Target.Site = site
	tr.Target.Severity = sev
	return tr
}

func TestRouteNarrowsSinks(t *testing.T) {
	t.Parallel()
	h := routeHarness(t, testSink("pagerduty"), testSink("ops-slack"), testSink("db-hook"))
	publishRoutes(h.n, dbRoute(SeverityPage, []int{2}, "", nil))
	applyNow(t, h, routedDown(h.clock.Now(), "db-team", "nyc", SeverityPage))
	if len(queuedOf(h.n, "pagerduty")) != 0 || len(queuedOf(h.n, "ops-slack")) != 0 {
		t.Fatal("down reached a sink outside the route")
	}
	got := queuedOf(h.n, "db-hook")
	if len(got) != 1 || got[0].Event.Type != EventDown || got[0].Event.Severity != SeverityPage {
		t.Fatalf("db-hook %+v", got)
	}
}

func TestRouteFilterStillDrops(t *testing.T) {
	t.Parallel()
	paged := testSink("pagerduty")
	paged.Match.Sites = []string{"nyc"}
	h := routeHarness(t, paged, testSink("ops-slack"))
	publishRoutes(h.n, dbRoute(SeverityPage, []int{0, 1}, "", nil))
	applyNow(t, h, routedDown(h.clock.Now(), "db-team", "lab", SeverityPage))
	if len(queuedOf(h.n, "pagerduty")) != 0 {
		t.Fatal("site filter should drop pagerduty")
	}
	if len(queuedOf(h.n, "ops-slack")) != 1 {
		t.Fatal("ops-slack should still receive the down")
	}
}

func TestRouteFlappingIsWarn(t *testing.T) {
	t.Parallel()
	h := routeHarness(t, testSink("db-hook"), testSink("ops-slack"))
	publishRoutes(h.n, dbRoute(SeverityPage, []int{0}, "<!db>", nil))
	tr := routedDown(h.clock.Now(), "db-team", "nyc", SeverityPage)
	tr.Flapping = true
	applyNow(t, h, tr)
	if len(queuedOf(h.n, "ops-slack")) != 0 {
		t.Fatal("flapping reached a sink outside the route")
	}
	got := queuedOf(h.n, "db-hook")
	if len(got) != 1 || got[0].Event.Type != EventFlapping || got[0].Event.Severity != SeverityWarn ||
		got[0].Mention != "" {
		t.Fatalf("flap %+v", got)
	}
}

func TestUnroutedIgnoresRouteTable(t *testing.T) {
	t.Parallel()
	h := routeHarness(t, testSink("a"), testSink("b"))
	publishRoutes(h.n, dbRoute(SeverityWarn, []int{0}, "<!db>", nil))
	applyNow(t, h, downTransition(1, h.clock.Now()))
	for _, name := range []string{"a", "b"} {
		got := queuedOf(h.n, name)
		if len(got) != 1 || got[0].Event.Severity != SeverityPage || got[0].Mention != "" {
			t.Fatalf("%s %+v", name, got)
		}
	}
}

func TestRouteEditKeepsRecoverySinks(t *testing.T) {
	t.Parallel()
	h := routeHarness(t, testSink("db-hook"), testSink("ops-slack"))
	publishRoutes(h.n, dbRoute(SeverityPage, []int{0}, "", nil))
	down := routedDown(h.clock.Now(), "db-team", "nyc", SeverityPage)
	applyNow(t, h, down)
	publishRoutes(h.n, dbRoute(SeverityWarn, []int{1}, "<!ops>", nil))
	up := upTransition(2, h.clock.Now())
	up.Target.Route = "db-team"
	applyNow(t, h, up)
	hook := queuedOf(h.n, "db-hook")
	if len(hook) != 2 || hook[0].Event.Type != EventDown || hook[1].Event.Type != EventRecovered {
		t.Fatalf("recovery sinks %+v", hook)
	}
	if len(queuedOf(h.n, "ops-slack")) != 0 {
		t.Fatal("recovery followed the edited route")
	}
	next := routedDown(h.clock.Now(), "db-team", "nyc", SeverityPage)
	next.Sequence = 3
	applyNow(t, h, next)
	if len(queuedOf(h.n, "db-hook")) != 2 {
		t.Fatal("next down stayed on the old sink")
	}
	slack := queuedOf(h.n, "ops-slack")
	if len(slack) != 1 || slack[0].Event.Type != EventDown || slack[0].Event.Severity != SeverityWarn {
		t.Fatalf("next down %+v", slack)
	}
}

func TestStartedIgnoresRoutes(t *testing.T) {
	t.Parallel()
	h := routeHarness(t, testSink("a"), testSink("b"))
	publishRoutes(h.n, dbRoute(SeverityPage, []int{0}, "<!db>", nil))
	h.n.mu.Lock()
	h.n.enqueueLocked(newStartedEvent("evt_S", h.n.cfg.Collector, h.clock.Now()))
	h.n.mu.Unlock()
	for _, name := range []string{"a", "b"} {
		got := queuedOf(h.n, name)
		if len(got) != 1 || got[0].Event.Type != EventStarted || got[0].Mention != "" {
			t.Fatalf("%s %+v", name, got)
		}
	}
}

func TestRouteMentionStoredOnPageDown(t *testing.T) {
	t.Parallel()
	h := routeHarness(t, testSink("db-hook"))
	publishRoutes(h.n, dbRoute(SeverityPage, []int{0}, "<!subteam^S0DB>", nil))
	applyNow(t, h, routedDown(h.clock.Now(), "db-team", "nyc", SeverityWarn))
	got := queuedOf(h.n, "db-hook")
	if len(got) != 1 || got[0].Event.Severity != SeverityPage || got[0].Mention != "<!subteam^S0DB>" {
		t.Fatalf("%+v", got)
	}
}

func TestMissingRouteNotifiesNoOne(t *testing.T) {
	t.Parallel()
	h := routeHarness(t, testSink("a"), testSink("b"))
	publishRoutes(h.n, dbRoute(SeverityPage, []int{0}, "", nil))
	applyNow(t, h, routedDown(h.clock.Now(), "gone", "nyc", SeverityPage))
	if len(queuedOf(h.n, "a")) != 0 || len(queuedOf(h.n, "b")) != 0 {
		t.Fatal("a missing route should notify no one")
	}
}

func TestDownSeverityFollowsLiveRoute(t *testing.T) {
	t.Parallel()
	tr := goldenTransition()
	tr.Target.Route = "db-team"
	tr.Target.Severity = SeverityPage
	tab := newRouteTable([]Route{dbRoute(SeverityWarn, []int{0}, "", nil)})
	ev := newDownEventIn("evt_D", goldenCollector(), tr, tab)
	if ev.Severity != SeverityWarn {
		t.Fatalf("severity %s", ev.Severity)
	}
	ev = newDownEventIn("evt_D", goldenCollector(), tr, nil)
	if ev.Severity != SeverityPage {
		t.Fatalf("nil table severity %s", ev.Severity)
	}
}
