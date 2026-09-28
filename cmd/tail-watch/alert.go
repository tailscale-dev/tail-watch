package main

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/tstime"
)

const (
	defaultTransitionQueue = 256
	gaugeFlapping          = 4
)

// State is the alert state of one target.
type State int

const (
	StateUnknown State = iota
	StateUp
	StateDown
	StateSuppressed
)

// String returns the spec name of s.
func (s State) String() string {
	switch s {
	case StateUnknown:
		return "UNKNOWN"
	case StateUp:
		return "UP"
	case StateDown:
		return "DOWN"
	case StateSuppressed:
		return "SUPPRESSED"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Transition is one immutable alert transition, in process-local sequence order.
type Transition struct {
	Sequence   uint64
	Target     Target
	From       State
	To         State
	Flapping   bool
	Check      string
	Layer      layer
	Err        error
	Since      time.Time
	At         time.Time
	Failures   int
	Path       pathInfo
	Dependents []string
	DownSince  time.Time
}

type transitionConsumer interface {
	ObserveTransition(Transition)
}

// alertSnapshot is the latest notification-visible view of one target.
type alertSnapshot struct {
	Sequence  uint64
	Name      string
	State     State
	Flapping  bool
	Check     string
	Layer     layer
	Err       error
	Since     time.Time
	At        time.Time
	Failures  int
	Path      pathInfo
	DownSince time.Time
	Removed   bool
	Dirty     bool
	Target    Target
}

type alertConfig struct {
	Clock      tstime.Clock
	Targets    []*Target
	Parents    map[string]string
	Recheck    func(string)
	Registerer prometheus.Registerer
	// QueueCap is the transition FIFO limit. Zero selects the default.
	// Negative keeps no queue slots, so every transition is a dirty snapshot.
	QueueCap int
}

type tripInfo struct {
	Check    string
	Layer    layer
	Err      error
	Since    time.Time
	Failures int
	Path     pathInfo
}

type checkState struct {
	consecFail      int
	consecOK        int
	runStart        time.Time
	failedSinceDown bool
	sawSuccess      bool
	lastFail        bool
	lastErr         error
	lastLayer       layer
	lastPath        pathInfo
	opened          bool
	openLayer       layer
	openErr         error
	openPath        pathInfo
	openSince       time.Time
	openCount       int
}

type holdState struct {
	on    bool
	gen   uint64
	check string
	timer tstime.TimerController
}

type targetState struct {
	target           Target
	state            State
	emitted          State
	observed         bool
	flapping         bool
	downSince        time.Time
	emittedDownSince time.Time
	trip             tripInfo
	checks           map[string]*checkState
	changes          []time.Time
	hold             holdState
	flapGen          uint64
	generation       uint64
	releasing        bool
	flapTimer        tstime.TimerController
	changeHead       int
}

type fallOpen struct {
	name  string
	check string
}

// alerter is the per-target alert state machine.
type alerter struct {
	mu             sync.Mutex
	clock          tstime.Clock
	recheck        func(string)
	gauge          *prometheus.GaugeVec
	parents        map[string]string
	targets        map[string]*targetState
	pending        map[string]time.Time
	required       map[string]struct{}
	seen           map[string]struct{}
	first          chan struct{}
	firstOnce      sync.Once
	out            []transitionConsumer
	queue          []Transition
	queueCap       int
	cond           *sync.Cond
	stop           bool
	drained        sync.WaitGroup
	seq            uint64
	snapshots      map[string]*alertSnapshot
	timerCallbacks sync.WaitGroup
	nextGeneration uint64
	syncing        bool
	pendingOut     []string
	falls          []fallOpen
}

func newAlerter(cfg alertConfig) (*alerter, error) {
	clock := cfg.Clock
	if clock == nil {
		clock = tstime.StdClock{}
	}
	parents := cloneParents(cfg.Parents)
	var graphErr error
	if path, cyc := graphCycle(parents); cyc {
		graphErr = fmt.Errorf("dependency cycle: %s", path)
		parents = map[string]string{}
	}
	a := &alerter{
		mu:             sync.Mutex{},
		clock:          clock,
		recheck:        cfg.Recheck,
		gauge:          newStateGauge(),
		parents:        parents,
		targets:        map[string]*targetState{},
		pending:        map[string]time.Time{},
		required:       map[string]struct{}{},
		seen:           map[string]struct{}{},
		first:          make(chan struct{}),
		firstOnce:      sync.Once{},
		out:            nil,
		queue:          nil,
		queueCap:       queueCapacity(cfg.QueueCap),
		cond:           nil,
		stop:           false,
		drained:        sync.WaitGroup{},
		seq:            0,
		snapshots:      map[string]*alertSnapshot{},
		timerCallbacks: sync.WaitGroup{},
		nextGeneration: 0,
		syncing:        false,
		pendingOut:     nil,
		falls:          nil,
	}
	a.cond = sync.NewCond(&a.mu)
	if cfg.Registerer != nil {
		cfg.Registerer.MustRegister(a.gauge)
	}
	for _, t := range cfg.Targets {
		if t == nil || t.Name == "" {
			continue
		}
		if err := a.addLocked(t); err != nil {
			continue
		}
		a.noteRequired(t)
	}
	if len(a.required) == 0 {
		a.signalFirst()
	}
	a.drained.Add(1)
	go a.drain()
	return a, graphErr
}

func queueCapacity(n int) int {
	switch {
	case n < 0:
		return 0
	case n == 0:
		return defaultTransitionQueue
	default:
		return n
	}
}

func newStateGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      "target_state",
		Help:      "Alert state of a target: 0 unknown, 1 up, 2 down, 3 suppressed, 4 flapping.",
	}, []string{labelTarget})
}

func (a *alerter) noteRequired(t *Target) {
	for _, chk := range t.Checks {
		a.required[pairKey(t.Name, chk.String())] = struct{}{}
	}
}

func pairKey(target, check string) string {
	return target + "\x00" + check
}

func (a *alerter) stopDrain() {
	a.mu.Lock()
	a.stop = true
	for _, ts := range a.targets {
		a.disarmHold(ts)
		a.stopFlap(ts)
	}
	a.cond.Broadcast()
	a.mu.Unlock()
	a.drained.Wait()
	a.timerCallbacks.Wait()
}

func cloneParents(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for child, parent := range in {
		if child == "" || parent == "" {
			continue
		}
		out[child] = parent
	}
	return out
}

func copyTarget(t *Target) Target {
	if t == nil {
		return Target{}
	}
	out := *t
	out.Checks = slices.Clone(t.Checks)
	return out
}

func configuredParents(targets []*Target) map[string]string {
	out := make(map[string]string)
	for _, t := range targets {
		if t != nil && t.Name != "" && t.Parent != "" {
			out[t.Name] = t.Parent
		}
	}
	return out
}

// ObserveClassified applies one classified result to the state machine.
func (a *alerter) ObserveClassified(cl Classified) {
	if a == nil {
		return
	}
	a.mu.Lock()
	names := a.observeLocked(cl)
	a.mu.Unlock()
	a.fireRechecks(names)
}

func (a *alerter) observeLocked(cl Classified) []string {
	if a.stop || cl.Target == nil || cl.Check == "" {
		return nil
	}
	ts := a.targets[cl.Target.Name]
	if ts == nil {
		return nil
	}
	a.noteFirst(ts.target.Name, cl.Check)
	ts.observed = true
	if cl.Generation != 0 && cl.Generation != ts.generation {
		return nil
	}
	cs := a.ensureCheck(ts, cl.Check)
	var names []string
	if cl.Err == nil {
		names = a.observeSuccess(ts, cs)
	} else {
		names = a.observeFailure(ts, cs, cl)
	}
	names = append(names, a.pendingOut...)
	a.pendingOut = nil
	return names
}

func (a *alerter) ensureCheck(ts *targetState, name string) *checkState {
	cs := ts.checks[name]
	if cs == nil {
		cs = newCheckState()
		ts.checks[name] = cs
	}
	return cs
}

func newCheckState() *checkState {
	return &checkState{
		consecFail:      0,
		consecOK:        0,
		runStart:        time.Time{},
		failedSinceDown: false,
		sawSuccess:      false,
		lastFail:        false,
		lastErr:         nil,
		lastLayer:       layerNone,
		lastPath:        pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
		opened:          false,
		openLayer:       layerNone,
		openErr:         nil,
		openPath:        pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
		openSince:       time.Time{},
		openCount:       0,
	}
}

func (a *alerter) observeSuccess(ts *targetState, cs *checkState) []string {
	cs.consecFail = 0
	cs.runStart = time.Time{}
	cs.consecOK++
	cs.lastFail = false
	cs.lastErr = nil
	cs.sawSuccess = true
	cs.opened = false
	if ts.hold.on && ts.hold.check == checkName(ts, cs) {
		a.disarmHold(ts)
	}
	switch {
	case ts.releasing:
		if a.eachSawSuccess(ts) {
			return a.becomeUp(ts, true)
		}
	case ts.state == StateSuppressed:
		return nil
	case ts.state == StateUnknown:
		if a.eachSawSuccess(ts) {
			return a.becomeUp(ts, true)
		}
	case ts.state == StateDown:
		if a.recovered(ts) {
			return a.becomeUp(ts, false)
		}
	default:
	}
	return nil
}

func checkName(ts *targetState, want *checkState) string {
	for name, cs := range ts.checks {
		if cs == want {
			return name
		}
	}
	return ""
}

func (a *alerter) observeFailure(ts *targetState, cs *checkState, cl Classified) []string {
	a.recordFailure(cs, cl)
	if ts.state == StateDown {
		cs.failedSinceDown = true
	}
	if ts.state == StateSuppressed && !ts.releasing {
		return nil
	}
	if cs.consecFail < failThreshold(ts.target.FailThreshold) {
		return nil
	}
	a.freezeOpen(cs)
	if ts.state == StateDown {
		return nil
	}
	if ts.hold.on && holdable(cl.Layer) {
		return nil
	}
	if a.ancestorDown(ts.target.Name) {
		a.forceSuppress(ts)
		return nil
	}
	if holdable(cl.Layer) && a.suppressor(a.parents[ts.target.Name]) {
		a.startHold(ts, cl.Check)
		return nil
	}
	if ts.hold.on {
		a.disarmHold(ts)
	}
	return a.enterDown(ts, cs)
}

func (a *alerter) recordFailure(cs *checkState, cl Classified) {
	cs.consecOK = 0
	cs.lastFail = true
	cs.lastErr = cl.Err
	cs.lastLayer = cl.Layer
	cs.lastPath = cl.Path
	if cs.consecFail == 0 {
		cs.runStart = resultTime(cl, a.now())
	}
	cs.consecFail++
}

func resultTime(cl Classified, fallback time.Time) time.Time {
	if !cl.Start.IsZero() {
		return cl.Start
	}
	return fallback
}

func failThreshold(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func (a *alerter) freezeOpen(cs *checkState) {
	if cs.opened {
		return
	}
	cs.opened = true
	cs.openLayer = cs.lastLayer
	cs.openErr = cs.lastErr
	cs.openPath = cs.lastPath
	cs.openSince = cs.runStart
	cs.openCount = cs.consecFail
}

func holdable(l layer) bool {
	return l == layerControl || l == layerTunnel
}

func (a *alerter) startHold(ts *targetState, check string) {
	parent := a.targets[a.parents[ts.target.Name]]
	if parent == nil {
		return
	}
	window := parent.target.Interval*time.Duration(failThreshold(parent.target.FailThreshold)) + parent.target.Timeout
	ts.hold.gen++
	gen := ts.hold.gen
	ts.hold.on = true
	ts.hold.check = check
	name := ts.target.Name
	ts.hold.timer = a.afterFunc(window, func() { a.finishHold(name, ts, gen) })
}

// afterFunc defers state work until after the clock callback returns. In
// particular, tstest.Clock invokes callbacks while holding its own mutex.
func (a *alerter) afterFunc(d time.Duration, f func()) tstime.TimerController {
	return a.clock.AfterFunc(d, func() {
		a.timerCallbacks.Go(f)
	})
}

func (a *alerter) finishHold(name string, expected *targetState, gen uint64) {
	a.mu.Lock()
	if a.stop || a.targets[name] != expected {
		a.mu.Unlock()
		return
	}
	names := a.finishHoldLocked(name, gen)
	pending := a.pendingOut
	a.pendingOut = nil
	a.mu.Unlock()
	a.fireRechecks(append(names, pending...))
}

func (a *alerter) finishHoldLocked(name string, gen uint64) []string {
	ts := a.targets[name]
	if ts == nil || !ts.hold.on || ts.hold.gen != gen {
		return nil
	}
	check := ts.hold.check
	ts.hold.on = false
	cs := ts.checks[check]
	if cs == nil || !cs.lastFail || cs.consecFail < failThreshold(ts.target.FailThreshold) {
		return nil
	}
	if a.ancestorDown(name) {
		a.forceSuppress(ts)
		return nil
	}
	return a.enterDown(ts, cs)
}

func (a *alerter) disarmHold(ts *targetState) {
	if ts.hold.timer != nil {
		ts.hold.timer.Stop()
		ts.hold.timer = nil
	}
	ts.hold.on = false
	ts.hold.gen++
	ts.hold.check = ""
}

func (a *alerter) enterDown(ts *targetState, cs *checkState) []string {
	a.applyTrip(ts, cs)
	cs.failedSinceDown = true
	ts.releasing = false
	if ts.state == StateUp {
		return a.applyCounted(ts, StateDown)
	}
	from := ts.state
	if from == StateSuppressed {
		return a.emitFreshDown(ts, from)
	}
	ts.state = StateDown
	sup, rel := a.syncTopology()
	a.finishFalls()
	if ts.flapping {
		a.armFlap(ts)
		a.writeGauge(ts)
		return rel
	}
	ts.emitted = StateDown
	ts.emittedDownSince = ts.downSince
	a.writeGauge(ts)
	a.emit(ts, from, StateDown, false, sup)
	return rel
}

func (a *alerter) emitFreshDown(ts *targetState, from State) []string {
	ts.state = StateDown
	sup, rel := a.syncTopology()
	a.finishFalls()
	if ts.flapping {
		a.armFlap(ts)
		a.writeGauge(ts)
		return rel
	}
	ts.emitted = StateDown
	ts.emittedDownSince = ts.downSince
	a.writeGauge(ts)
	a.emit(ts, from, StateDown, false, sup)
	return rel
}

func (a *alerter) applyTrip(ts *targetState, cs *checkState) {
	trip := tripFrom(cs, checkName(ts, cs))
	if ts.flapping && ts.emitted == StateDown && !ts.downSince.IsZero() {
		return
	}
	ts.trip = trip
	ts.downSince = trip.Since
}

func emptyTrip() tripInfo {
	return tripInfo{
		Check: "", Layer: layerNone, Err: nil, Since: time.Time{}, Failures: 0,
		Path: pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
	}
}

func tripFrom(cs *checkState, check string) tripInfo {
	if cs.opened {
		since := cs.openSince
		return tripInfo{
			Check: check, Layer: cs.openLayer, Err: cs.openErr,
			Since: since, Failures: cs.openCount, Path: cs.openPath,
		}
	}
	return tripInfo{
		Check: check, Layer: cs.lastLayer, Err: cs.lastErr,
		Since: cs.runStart, Failures: cs.consecFail, Path: cs.lastPath,
	}
}

func (a *alerter) becomeUp(ts *targetState, silent bool) []string {
	for _, cs := range ts.checks {
		cs.failedSinceDown = false
		if silent {
			cs.consecFail = 0
		}
	}
	ts.releasing = false
	if silent {
		ts.state = StateUp
		ts.emitted = StateUp
		a.paintSnapshot(ts)
		a.writeGauge(ts)
		_, rel := a.syncTopology()
		a.finishFalls()
		return rel
	}
	return a.applyCounted(ts, StateUp)
}

func (a *alerter) applyCounted(ts *targetState, to State) []string {
	from := ts.state
	ts.state = to
	sup, rel := a.syncTopology()
	a.finishFalls()
	deps := rel
	if to == StateDown {
		deps = sup
	}
	n := a.noteFlap(ts)
	if ts.target.FlapLimit > 0 && !ts.flapping && n > ts.target.FlapLimit {
		ts.flapping = true
		a.armFlap(ts)
		a.writeGauge(ts)
		a.emit(ts, from, to, true, deps)
		return rel
	}
	if ts.flapping {
		a.armFlap(ts)
		a.writeGauge(ts)
		return rel
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
	return rel
}

func (a *alerter) eachSawSuccess(ts *targetState) bool {
	if len(ts.target.Checks) == 0 {
		return false
	}
	for _, chk := range ts.target.Checks {
		cs := ts.checks[chk.String()]
		if cs == nil || !cs.sawSuccess {
			return false
		}
	}
	return true
}

func (a *alerter) recovered(ts *targetState) bool {
	if len(ts.target.Checks) == 0 {
		return false
	}
	need := max(ts.target.RecoverThreshold, 1)
	for _, chk := range ts.target.Checks {
		cs := ts.checks[chk.String()]
		if cs == nil {
			return false
		}
		if cs.failedSinceDown {
			if cs.consecOK < need {
				return false
			}
			continue
		}
		if cs.lastFail || !cs.sawSuccess {
			return false
		}
	}
	return true
}

func (a *alerter) now() time.Time {
	return a.clock.Now()
}

func (a *alerter) writeGauge(ts *targetState) {
	if a.gauge == nil || ts == nil {
		return
	}
	value := float64(ts.state)
	if ts.flapping {
		value = gaugeFlapping
	}
	a.gauge.WithLabelValues(ts.target.Name).Set(value)
}

func (a *alerter) generation(name string) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	ts := a.targets[name]
	if ts == nil {
		return 0
	}
	return ts.generation
}
