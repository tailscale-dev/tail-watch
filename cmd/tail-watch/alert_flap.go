package main

import "time"

func (a *alerter) noteFlap(ts *targetState) int {
	now := a.now()
	limit := max(ts.target.FlapLimit+1, 1)
	if len(ts.changes) < limit {
		ts.changes = append(ts.changes, now)
	} else {
		ts.changes[ts.changeHead] = now
		ts.changeHead = (ts.changeHead + 1) % limit
	}
	cutoff := now.Add(-flapWindow(ts))
	count := 0
	for _, at := range ts.changes {
		if !at.Before(cutoff) {
			count++
		}
	}
	return count
}

func flapWindow(ts *targetState) time.Duration {
	if ts.target.FlapWindow > 0 {
		return ts.target.FlapWindow
	}
	return defaultFlapWindow
}

func (a *alerter) stopFlap(ts *targetState) {
	ts.flapGen++
	if ts.flapTimer != nil {
		ts.flapTimer.Stop()
		ts.flapTimer = nil
	}
}

func (a *alerter) armFlap(ts *targetState) {
	a.stopFlap(ts)
	gen := ts.flapGen
	ts.flapTimer = a.afterFunc(flapWindow(ts), func() { a.finishFlap(ts, gen) })
}

func (a *alerter) finishFlap(expected *targetState, gen uint64) {
	a.mu.Lock()
	if a.stop || a.targets[expected.target.Name] != expected {
		a.mu.Unlock()
		return
	}
	a.finishFlapLocked(expected.target.Name, gen)
	pending := a.pendingOut
	a.pendingOut = nil
	a.mu.Unlock()
	a.fireRechecks(pending)
}

func (a *alerter) finishFlapLocked(name string, gen uint64) {
	ts := a.targets[name]
	if ts == nil || ts.flapGen != gen || !ts.flapping {
		return
	}
	ts.flapping = false
	// Suppression is silent even when a flap settles. Keep the last emitted
	// state so an outstanding DOWN can recover after the target is released.
	if ts.state == StateSuppressed || ts.state == ts.emitted {
		a.writeGauge(ts)
		a.paintSnapshot(ts)
		return
	}
	from := ts.emitted
	to := ts.state
	sup, rel := a.syncTopology()
	a.finishFalls()
	deps := rel
	if to == StateDown {
		deps = sup
	}
	ts.emitted = to
	if to == StateDown {
		ts.emittedDownSince = ts.downSince
	}
	a.writeGauge(ts)
	a.emit(ts, from, to, false, deps)
	if to == StateUp {
		ts.downSince = time.Time{}
		ts.trip = emptyTrip()
		ts.emittedDownSince = time.Time{}
	}
	a.pendingOut = append(a.pendingOut, rel...)
}
