package main

import (
	"sync"
	"sync/atomic"
	"testing"
)

func dbRoute(sev Severity, indexes []int, mention string, missing []string) Route {
	return Route{
		Name: "db-team", Severity: sev, Names: []string{"db-hook"}, Indexes: indexes,
		Missing: missing, Mention: mention, Source: sourceConfig,
	}
}

func TestRouteTableLookupAndSnapshot(t *testing.T) {
	t.Parallel()
	src := []Route{dbRoute(SeverityWarn, []int{0, 1}, "<!db>", []string{"gone"})}
	tab := newRouteTable(src)
	src[0].Indexes[0] = 9
	src[0].Missing[0] = "changed"

	r, ok := tab.get("db-team")
	if !ok || r.Severity != SeverityWarn || r.Mention != "<!db>" {
		t.Fatalf("lookup %+v %v", r, ok)
	}
	if len(r.Indexes) != 2 || r.Indexes[0] != 0 || r.Indexes[1] != 1 {
		t.Fatalf("indexes %v", r.Indexes)
	}
	if len(r.Missing) != 1 || r.Missing[0] != "gone" {
		t.Fatalf("missing %v", r.Missing)
	}
	if _, found := tab.get("other"); found {
		t.Fatal("unknown route looked up")
	}
	if _, found := (*routeTable)(nil).get("db-team"); found {
		t.Fatal("nil table looked up")
	}
}

func TestRouteCandidates(t *testing.T) {
	t.Parallel()
	sinks := []SinkConfig{testSink("a"), testSink("b")}
	tab := newRouteTable([]Route{dbRoute(SeverityPage, []int{1}, "<!db>", nil)})
	down := newDownEvent("evt_D", goldenCollector(), goldenTransition())
	down.Severity = SeverityPage

	idx, mention := routeCandidates(sinks, tab, down, "")
	if len(idx) != 2 || mention != "" {
		t.Fatalf("unrouted idx %v mention %q", idx, mention)
	}
	idx, mention = routeCandidates(sinks, tab, down, "missing")
	if idx != nil || mention != "" {
		t.Fatalf("missing route idx %v mention %q", idx, mention)
	}
	idx, mention = routeCandidates(sinks, tab, down, "db-team")
	if len(idx) != 1 || idx[0] != 1 || mention != "<!db>" {
		t.Fatalf("routed idx %v mention %q", idx, mention)
	}
	down.Severity = SeverityWarn
	idx, mention = routeCandidates(sinks, tab, down, "db-team")
	if len(idx) != 1 || mention != "" {
		t.Fatalf("warn mention %q idx %v", mention, idx)
	}
	started := newStartedEvent("evt_S", goldenCollector(), down.Time.Time())
	idx, mention = routeCandidates(sinks, tab, started, "db-team")
	if len(idx) != 2 || mention != "" {
		t.Fatalf("started idx %v mention %q", idx, mention)
	}
}

func TestRouteTableReadDuringPublish(t *testing.T) {
	t.Parallel()
	var p atomic.Pointer[routeTable]
	p.Store(newRouteTable([]Route{dbRoute(SeverityPage, []int{0}, "", nil)}))
	var bad atomic.Bool
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			r, ok := p.Load().get("db-team")
			if !ok || (r.Severity != SeverityPage && r.Severity != SeverityWarn) || len(r.Indexes) != 1 {
				bad.Store(true)
				return
			}
		}
	})
	for i := range 200 {
		sev := SeverityPage
		if i%2 == 1 {
			sev = SeverityWarn
		}
		p.Store(newRouteTable([]Route{dbRoute(sev, []int{0}, "", nil)}))
	}
	close(stop)
	wg.Wait()
	if bad.Load() {
		t.Fatal("reader saw a broken route snapshot")
	}
}
