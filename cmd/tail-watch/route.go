package main

import "slices"

// routeTable is an immutable snapshot of resolved routes. Writers publish a
// new table; readers load it and never mutate it.
type routeTable struct {
	byName map[string]Route
}

func newRouteTable(routes []Route) *routeTable {
	byName := make(map[string]Route, len(routes))
	for _, r := range routes {
		r.Names = slices.Clone(r.Names)
		r.Indexes = slices.Clone(r.Indexes)
		r.Missing = slices.Clone(r.Missing)
		byName[r.Name] = r
	}
	return &routeTable{byName: byName}
}

func (t *routeTable) get(name string) (Route, bool) {
	if t == nil || name == "" {
		return Route{}, false
	}
	r, ok := t.byName[name]
	return r, ok
}

// routeCandidates lists sink indexes that may receive ev. An empty route, a
// test or started event, or an event with no target uses every sink. A named
// route that is missing yields no indexes. mention is set only for a
// page-severity target.down, and only from the route.
func routeCandidates(sinks []SinkConfig, table *routeTable, ev Event, route string) ([]int, string) {
	all := make([]int, len(sinks))
	for i := range sinks {
		all[i] = i
	}
	if ev.Type == EventTest || ev.Type == EventStarted || ev.Target == nil || route == "" {
		return all, ""
	}
	r, ok := table.get(route)
	if !ok {
		return nil, ""
	}
	mention := ""
	if ev.Type == EventDown && ev.Severity == SeverityPage {
		mention = r.Mention
	}
	return slices.Clone(r.Indexes), mention
}
