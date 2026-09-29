package main

import (
	"fmt"
	"slices"
	"time"
)

// AddTarget registers a target. A pending restore starts it DOWN. A down
// parent starts it SUPPRESSED. Any other target starts UNKNOWN.
func (a *alerter) AddTarget(t *Target) error {
	if a == nil {
		return nil
	}
	if t == nil || t.Name == "" {
		return fmt.Errorf("target name is required")
	}
	a.mu.Lock()
	err := a.addLocked(t)
	var released []string
	if err == nil {
		_, released = a.syncTopology()
	}
	a.mu.Unlock()
	a.fireRechecks(released)
	return err
}

func (a *alerter) addLocked(t *Target) error {
	if _, ok := a.targets[t.Name]; ok {
		return fmt.Errorf("target %s is already registered", t.Name)
	}
	ts := &targetState{
		target:           copyTarget(t),
		state:            StateUnknown,
		emitted:          StateUnknown,
		observed:         false,
		flapping:         false,
		downSince:        time.Time{},
		emittedDownSince: time.Time{},
		trip:             emptyTrip(),
		checks:           map[string]*checkState{},
		changes:          nil,
		hold:             holdState{on: false, gen: 0, check: "", timer: nil},
		flapGen:          0,
		generation:       a.newGeneration(),
		releasing:        false,
		flapTimer:        nil,
		changeHead:       0,
	}
	for _, chk := range ts.target.Checks {
		ts.checks[chk.String()] = newCheckState()
	}
	if since, ok := a.pending[t.Name]; ok {
		delete(a.pending, t.Name)
		a.applyRestore(ts, since)
	} else if a.ancestorDown(t.Name) {
		ts.state = StateSuppressed
	}
	a.targets[t.Name] = ts
	a.paintSnapshot(ts)
	a.writeGauge(ts)
	return nil
}

// RemoveTarget drops a target whose last emitted state is not DOWN. An implicit
// target may be removed while DOWN: the learner asks only after authoritative
// reconciliation, and the removed snapshot lets the notifier close its incident.
// Suppressed dependents are released the way a parent recovery releases them.
func (a *alerter) RemoveTarget(name string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	released, err := a.removeLocked(name)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	a.fireRechecks(released)
	return nil
}

func (a *alerter) removeLocked(name string) ([]string, error) {
	ts := a.targets[name]
	if ts == nil {
		return nil, nil
	}
	if ts.incidentOpen() && !ts.target.Implicit {
		return nil, fmt.Errorf("target %s is down", name)
	}
	a.disarmHold(ts)
	a.stopFlap(ts)
	delete(a.parents, name)
	delete(a.targets, name)
	if a.gauge != nil {
		_ = a.gauge.DeleteLabelValues(name)
	}
	a.markRemoved(ts)
	_, released := a.syncTopology()
	a.finishFalls()
	released = append(released, a.pendingOut...)
	a.pendingOut = nil
	return released, nil
}

// IsDown reports whether name is down, suppressed, or still emitted as down.
func (a *alerter) IsDown(name string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ts := a.targets[name]
	if ts == nil {
		return false
	}
	return ts.state == StateDown || ts.state == StateSuppressed || ts.incidentOpen()
}

// Restore seeds a registered but unobserved target as DOWN since the given time.
// An unknown name is recorded until AddTarget. A target that already observed
// a result returns an error.
func (a *alerter) Restore(name string, since time.Time) error {
	if a == nil {
		return nil
	}
	if name == "" {
		return fmt.Errorf("target name is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ts := a.targets[name]
	if ts == nil {
		a.pending[name] = since
		return nil
	}
	if ts.observed {
		return fmt.Errorf("target %s has already observed a result", name)
	}
	a.applyRestore(ts, since)
	a.paintSnapshot(ts)
	_, _ = a.syncTopology()
	a.writeGauge(ts)
	return nil
}

func (a *alerter) applyRestore(ts *targetState, since time.Time) {
	ts.state = StateDown
	ts.emitted = StateDown
	ts.downSince = since
	ts.emittedDownSince = since
	ts.releasing = false
	for _, cs := range ts.checks {
		cs.failedSinceDown = true
	}
}

// PendingRestores returns pending restore names without removing them.
func (a *alerter) PendingRestores() []string {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.pending))
	for name := range a.pending {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// DiscardPendingRestore forgets one pending restore.
func (a *alerter) DiscardPendingRestore(name string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	delete(a.pending, name)
	a.mu.Unlock()
}

// FirstRoundDone fires once every target/check pair present at construction
// has produced a result. It is already closed when that set is empty.
func (a *alerter) FirstRoundDone() <-chan struct{} {
	if a == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return a.first
}

func (a *alerter) noteFirst(target, check string) {
	key := pairKey(target, check)
	if _, want := a.required[key]; !want {
		return
	}
	a.seen[key] = struct{}{}
	if len(a.seen) >= len(a.required) {
		a.signalFirst()
	}
}

func (a *alerter) signalFirst() {
	a.firstOnce.Do(func() { close(a.first) })
}

// newGeneration gives each registration and release a distinct result epoch.
// Runs from a removed target cannot affect a later target using the same name.
func (a *alerter) newGeneration() uint64 {
	a.nextGeneration++
	return a.nextGeneration
}

func (ts *targetState) incidentOpen() bool {
	return ts.emitted == StateDown
}
