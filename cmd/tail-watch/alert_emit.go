package main

import "slices"

func (a *alerter) addConsumer(c transitionConsumer) {
	if a == nil || c == nil {
		return
	}
	a.mu.Lock()
	a.out = append(a.out, c)
	a.mu.Unlock()
}

func (a *alerter) emit(ts *targetState, from, to State, flapping bool, deps []string) {
	a.seq++
	since := ts.downSince
	if to == StateUp && !ts.emittedDownSince.IsZero() {
		since = ts.emittedDownSince
	}
	tr := Transition{
		Sequence:   a.seq,
		Target:     copyTarget(&ts.target),
		From:       from,
		To:         to,
		Flapping:   flapping,
		Check:      ts.trip.Check,
		Layer:      ts.trip.Layer,
		Err:        ts.trip.Err,
		Since:      ts.trip.Since,
		At:         a.now(),
		Failures:   ts.trip.Failures,
		Path:       ts.trip.Path,
		Dependents: append([]string{}, deps...),
		DownSince:  since,
	}
	dirty := false
	if len(a.queue) >= a.queueCap {
		dirty = true
	} else {
		a.queue = append(a.queue, tr)
		a.cond.Signal()
	}
	a.putSnapshot(ts, tr, dirty)
}

func (a *alerter) putSnapshot(ts *targetState, tr Transition, dirty bool) {
	if prev := a.snapshots[ts.target.Name]; prev != nil && prev.Dirty {
		dirty = true
	}
	a.snapshots[ts.target.Name] = a.viewOf(ts, tr.Sequence, dirty, false)
}

func (a *alerter) viewOf(ts *targetState, seq uint64, dirty, removed bool) *alertSnapshot {
	view := ts.state
	if ts.flapping {
		view = ts.emitted
	}
	down := ts.downSince
	if !ts.emittedDownSince.IsZero() {
		down = ts.emittedDownSince
	}
	return &alertSnapshot{
		Sequence:  seq,
		Name:      ts.target.Name,
		State:     view,
		Flapping:  ts.flapping,
		Check:     ts.trip.Check,
		Layer:     ts.trip.Layer,
		Err:       ts.trip.Err,
		Since:     ts.trip.Since,
		At:        a.now(),
		Failures:  ts.trip.Failures,
		Path:      ts.trip.Path,
		DownSince: down,
		Removed:   removed,
		Dirty:     dirty,
		Target:    copyTarget(&ts.target),
	}
}

func (a *alerter) paintSnapshot(ts *targetState) {
	prev := a.snapshots[ts.target.Name]
	dirty := prev != nil && prev.Dirty
	// Clearing a flap without a transition still changes the reconciled view.
	if prev != nil && prev.Flapping != ts.flapping {
		dirty = true
	}
	a.seq++
	a.snapshots[ts.target.Name] = a.viewOf(ts, a.seq, dirty, false)
}

// Snapshots returns immutable copies of the latest notification-visible state.
func (a *alerter) Snapshots() []alertSnapshot {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]alertSnapshot, 0, len(a.snapshots))
	for _, snap := range a.snapshots {
		out = append(out, copySnapshot(snap))
	}
	slices.SortFunc(out, func(x, y alertSnapshot) int {
		switch {
		case x.Name < y.Name:
			return -1
		case x.Name > y.Name:
			return 1
		default:
			return 0
		}
	})
	return out
}

func copySnapshot(snap *alertSnapshot) alertSnapshot {
	if snap == nil {
		return alertSnapshot{}
	}
	out := *snap
	out.Target = copyTarget(&snap.Target)
	return out
}

// Acknowledge clears a dirty mark once the consumer has reconciled through seq.
// An older sequence leaves a newer dirty generation in place.
func (a *alerter) Acknowledge(name string, seq uint64) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	snap := a.snapshots[name]
	if snap == nil || seq < snap.Sequence {
		return
	}
	snap.Dirty = false
	if snap.Removed {
		delete(a.snapshots, name)
	}
}

// MarkDirty records that a notifier intake dropped an update for name.
func (a *alerter) MarkDirty(name string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if snap := a.snapshots[name]; snap != nil {
		snap.Dirty = true
	}
}

func (a *alerter) markRemoved(ts *targetState) {
	a.seq++
	snap := a.viewOf(ts, a.seq, true, true)
	if prev := a.snapshots[ts.target.Name]; prev != nil && prev.Dirty {
		snap.Dirty = true
	}
	a.snapshots[ts.target.Name] = snap
}

func (a *alerter) drain() {
	defer a.drained.Done()
	for {
		tr, cons, ok := a.pop()
		if !ok {
			return
		}
		for _, c := range cons {
			c.ObserveTransition(copyTransition(tr))
		}
	}
}

func (a *alerter) pop() (Transition, []transitionConsumer, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(a.queue) == 0 && !a.stop {
		a.cond.Wait()
	}
	if len(a.queue) == 0 {
		return Transition{}, nil, false
	}
	tr := a.queue[0]
	a.queue = a.queue[1:]
	return tr, slices.Clone(a.out), true
}

func (a *alerter) fireRechecks(names []string) {
	if a == nil || a.recheck == nil {
		return
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		a.recheck(name)
	}
}

func copyTransition(tr Transition) Transition {
	tr.Target = copyTarget(&tr.Target)
	tr.Dependents = slices.Clone(tr.Dependents)
	return tr
}
