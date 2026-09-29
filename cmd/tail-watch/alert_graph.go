package main

import "slices"

// UpdateParents replaces the dependency graph when it has no cycle.
// Reachability changes suppress or release targets before this returns.
func (a *alerter) UpdateParents(parents map[string]string) error {
	if a == nil {
		return nil
	}
	next := cloneParents(parents)
	if path, cyc := graphCycle(next); cyc {
		return errCycle(path)
	}
	a.mu.Lock()
	a.parents = next
	_, released := a.syncTopology()
	a.finishFalls()
	extra := append([]string{}, a.pendingOut...)
	a.pendingOut = nil
	a.mu.Unlock()
	a.fireRechecks(append(released, extra...))
	return nil
}

func errCycle(path string) error {
	return &cycleError{path: path}
}

type cycleError struct {
	path string
}

func (e *cycleError) Error() string {
	return "dependency cycle: " + e.path
}

func (a *alerter) suppressor(name string) bool {
	ts := a.targets[name]
	if ts == nil {
		return false
	}
	return reachabilityOnly(ts.target)
}

func (a *alerter) ancestorDown(name string) bool {
	seen := map[string]bool{}
	for {
		parent := a.parents[name]
		if parent == "" || seen[parent] || !a.suppressor(parent) {
			return false
		}
		seen[parent] = true
		ps := a.targets[parent]
		if ps == nil {
			return false
		}
		if ps.state == StateDown || (ps.state == StateSuppressed && !ps.releasing) {
			return true
		}
		name = parent
	}
}

func canPull(ts *targetState) bool {
	if ts == nil || ts.state == StateDown {
		return false
	}
	if ts.hold.on || ts.releasing {
		return true
	}
	return ts.state == StateUnknown || ts.state == StateUp
}

func (a *alerter) forceSuppress(ts *targetState) {
	if ts.releasing {
		ts.generation = a.newGeneration()
	}
	a.disarmHold(ts)
	ts.releasing = false
	if ts.flapping && ts.state != StateSuppressed {
		a.armFlap(ts)
	}
	ts.state = StateSuppressed
	a.paintSnapshot(ts)
	a.writeGauge(ts)
}

func (a *alerter) beginRelease(ts *targetState) {
	for name := range ts.checks {
		ts.checks[name] = newCheckState()
	}
	ts.releasing = true
	ts.generation = a.newGeneration()
	a.disarmHold(ts)
	a.writeGauge(ts)
}

func (a *alerter) syncTopology() ([]string, []string) {
	if a.syncing {
		return nil, nil
	}
	a.syncing = true
	defer func() { a.syncing = false }()
	var suppressed, released []string
	for range len(a.targets) + 1 {
		if !a.syncPass(&suppressed, &released) {
			break
		}
	}
	slices.Sort(suppressed)
	slices.Sort(released)
	return suppressed, released
}

func (a *alerter) syncPass(suppressed, released *[]string) bool {
	progress := false
	for _, name := range a.sortedNames() {
		ts := a.targets[name]
		if ts == nil {
			continue
		}
		if a.ancestorDown(name) {
			if canPull(ts) {
				a.forceSuppress(ts)
				*suppressed = append(*suppressed, name)
				progress = true
			}
			continue
		}
		if ts.hold.on && !a.suppressor(a.parents[name]) {
			a.queueFall(ts)
			progress = true
			continue
		}
		if ts.state == StateSuppressed && !ts.releasing {
			a.beginRelease(ts)
			*released = append(*released, name)
			progress = true
		}
	}
	return progress
}

func (a *alerter) queueFall(ts *targetState) {
	a.falls = append(a.falls, fallOpen{name: ts.target.Name, check: ts.hold.check})
	a.disarmHold(ts)
}

func (a *alerter) finishFalls() {
	falls := a.falls
	a.falls = nil
	for _, fall := range falls {
		ts := a.targets[fall.name]
		if ts == nil || ts.state == StateDown || ts.state == StateSuppressed {
			continue
		}
		cs := ts.checks[fall.check]
		if cs == nil || !cs.lastFail || cs.consecFail < failThreshold(ts.target.FailThreshold) {
			continue
		}
		a.pendingOut = append(a.pendingOut, a.enterDown(ts, cs)...)
	}
}

func (a *alerter) sortedNames() []string {
	names := make([]string, 0, len(a.targets))
	for name := range a.targets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
