package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/tstime"
)

const (
	defaultIntakeCap  = 4096
	reconcileInterval = time.Second
)

// alertBridge is the part of the alert state machine the notifier uses.
type alertBridge interface {
	Snapshots() []alertSnapshot
	Acknowledge(name string, seq uint64)
	MarkDirty(name string)
	FirstRoundDone() <-chan struct{}
}

type notifierConfig struct {
	Clock     tstime.Clock
	Log       *slog.Logger
	Sinks     []SinkConfig
	Collector CollectorInfo
	StateDir  string
	Alerts    alertBridge
	// Tags reports the tailnet tags of the node a target resolves to.
	Tags     func(target string) []string
	Registry prometheus.Registerer
	NewSink  func(SinkConfig) (sink, error)
	// Env carries sink connections. Nil uses the default dialer.
	Env    netEnv
	Jitter func() float64
	// IntakeCap overrides the 4096-slot intake for tests.
	IntakeCap int
}

// notifier turns alert transitions into incidents and sink deliveries. One
// mutex guards the book; every mutation and its durable commit happen under it,
// so workers only see new intents after they are committed. Checks never touch
// that mutex: ObserveTransition only writes to the intake channel.
type notifier struct {
	cfg    notifierConfig
	log    *slog.Logger
	clock  tstime.Clock
	m      *notifyMetrics
	intake chan Transition

	mu         sync.Mutex
	b          *book
	store      *stateStore
	sinks      map[string]sink
	reconciled map[string]uint64
	wake       map[string]chan struct{}
	persistBad bool

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// routes is the latest published route snapshot. A nil load means no routes.
	routes atomic.Pointer[routeTable]
}

func newNotifier(cfg notifierConfig) (*notifier, error) {
	if cfg.Clock == nil {
		cfg.Clock = tstime.StdClock{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Jitter == nil {
		cfg.Jitter = rand.Float64
	}
	if cfg.IntakeCap <= 0 {
		cfg.IntakeCap = defaultIntakeCap
	}
	if cfg.NewSink == nil {
		cfg.NewSink = func(sc SinkConfig) (sink, error) { return newSinkWithEnv(sc, cfg.Collector, cfg.Env) }
	}
	m := newNotifyMetrics(cfg.Registry, cfg.StateDir != "")
	n := &notifier{
		cfg: cfg, log: cfg.Log, clock: cfg.Clock, m: m,
		intake:     make(chan Transition, cfg.IntakeCap),
		mu:         sync.Mutex{},
		b:          newBook(cfg.Log, m, cfg.Sinks),
		store:      nil,
		sinks:      map[string]sink{},
		reconciled: map[string]uint64{},
		wake:       map[string]chan struct{}{},
		persistBad: false,
		cancel:     nil,
		wg:         sync.WaitGroup{},
		routes:     atomic.Pointer[routeTable]{},
	}
	for _, sc := range cfg.Sinks {
		s, err := cfg.NewSink(sc)
		if err != nil {
			return nil, err
		}
		n.sinks[sc.Name] = s
		n.wake[sc.Name] = make(chan struct{}, 1)
		m.queueLen(sc.Name, 0)
	}
	if cfg.StateDir != "" {
		store, err := newStateStore(cfg.StateDir, cfg.Log)
		if err != nil {
			return nil, err
		}
		st, err := store.load()
		if err != nil {
			return nil, err
		}
		n.store = store
		if st != nil {
			n.b.restore(st, cfg.Clock.Now())
		}
		n.commitLocked()
	}
	return n, nil
}

// attach connects the notifier to the alert state machine and presence tags
// before Start.
func (n *notifier) attach(al alertBridge, tags func(string) []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cfg.Alerts = al
	n.cfg.Tags = tags
}

// ObserveTransition queues tr without I/O or waiting. A full intake drops the
// transition and marks its target dirty for reconciliation.
func (n *notifier) ObserveTransition(tr Transition) {
	select {
	case n.intake <- tr:
	default:
		n.m.dropped(sinkIntake, reasonQueueFull)
		n.log.Warn("notification intake full; transition dropped", "target", tr.Target.Name, "seq", tr.Sequence)
		if n.cfg.Alerts != nil {
			n.cfg.Alerts.MarkDirty(tr.Target.Name)
		}
	}
}

// Start runs the coordinator and one worker per sink until ctx ends or Stop.
func (n *notifier) Start(ctx context.Context) {
	ctx, n.cancel = context.WithCancel(ctx)
	n.wg.Add(1)
	go n.loop(ctx)
	for _, sc := range n.cfg.Sinks {
		w := &worker{
			n: n, cfg: sc, s: n.sinks[sc.Name], wake: n.wake[sc.Name],
			loop:   retryLoop{clock: n.clock, jitter: n.cfg.Jitter, sink: n.sinks[sc.Name], last: time.Time{}},
			resume: nil, windowStart: time.Time{},
		}
		n.wg.Add(1)
		go w.run(ctx)
	}
	if n.cfg.Alerts != nil {
		n.wg.Add(1)
		go n.watchFirstRound(ctx)
	}
}

// Stop ends the workers and waits for them. Attempted deliveries stay attempted.
func (n *notifier) Stop() {
	if n.cancel != nil {
		n.cancel()
	}
	n.wg.Wait()
}

func (n *notifier) loop(ctx context.Context) {
	defer n.wg.Done()
	tk, tick := n.clock.NewTicker(reconcileInterval)
	defer tk.Stop()
	for {
		select {
		case tr := <-n.intake:
			n.handle(tr)
		case <-tick:
			n.reconcile()
			n.retryPersist()
		case <-ctx.Done():
			return
		}
	}
}

func (n *notifier) handle(tr Transition) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.applyLocked(tr, false) {
		n.commitLocked()
		n.wakeLocked()
	}
}

// reconcile applies every dirty alert snapshot, so a dropped transition or a
// removed target still gets its event without another state change.
func (n *notifier) reconcile() {
	if n.cfg.Alerts == nil {
		return
	}
	snaps := n.cfg.Alerts.Snapshots()
	n.mu.Lock()
	defer n.mu.Unlock()
	changed := false
	for _, s := range snaps {
		if !s.Dirty {
			continue
		}
		if n.applyLocked(transitionOfSnapshot(s), s.Removed) {
			changed = true
		}
	}
	if changed {
		n.commitLocked()
		n.wakeLocked()
	}
}

func transitionOfSnapshot(s alertSnapshot) Transition {
	return Transition{
		Sequence: s.Sequence, Target: s.Target, From: s.State, To: s.State, Flapping: false,
		Check: s.Check, Layer: s.Layer, Err: s.Err, Since: s.Since, At: s.At, Failures: s.Failures,
		Path: s.Path, Dependents: nil, DownSince: s.DownSince,
	}
}

// applyLocked maps one transition (or reconciled snapshot) onto the incident
// book and reports whether anything changed. Sequences at or below the
// target's reconciled sequence are stale and ignored.
func (n *notifier) applyLocked(tr Transition, removed bool) bool {
	name := tr.Target.Name
	if tr.Sequence != 0 && tr.Sequence <= n.reconciled[name] {
		return false
	}
	n.reconciled[name] = max(n.reconciled[name], tr.Sequence)
	if n.cfg.Alerts != nil {
		defer n.cfg.Alerts.Acknowledge(name, tr.Sequence)
	}
	inc := n.b.openIncidentFor(name)
	if removed {
		if inc == nil {
			return false
		}
		return n.closeLocked(inc, n.clock.Now(), nil, reasonRemoved)
	}
	switch mapTransition(tr, inc != nil) {
	case mapDown:
		id, ok := n.newIDLocked(prefixEvent)
		if !ok {
			return false
		}
		ev := newDownEventIn(id, n.cfg.Collector, tr, n.routes.Load())
		sinks, mention := n.routeLocked(ev, tr.Target.Route)
		n.b.openIncident(ev, targetDefOf(tr.Target), downSinceOf(tr), sinks, mention)
		n.log.Info("incident opened", "target", name, "event", id, "check", tr.Check, "layer", string(tr.Layer))
		return true
	case mapRecovered:
		return n.closeLocked(inc, tr.At, tr.Dependents, "")
	case mapFlapping:
		id, ok := n.newIDLocked(prefixEvent)
		if !ok {
			return false
		}
		n.enqueueOn(newFlappingEvent(id, n.cfg.Collector, tr), tr.Target.Route)
		return true
	case mapNone:
		return false
	}
	return false
}

func (n *notifier) closeLocked(inc *incident, at time.Time, deps []string, reason string) bool {
	id, ok := n.newIDLocked(prefixEvent)
	if !ok {
		return false
	}
	n.b.closeIncident(inc, newRecoveredEvent(id, n.cfg.Collector, inc, at, deps, reason))
	n.log.Info("incident closed", "target", inc.Target.Name, "event", id, "reason", reason)
	return true
}

func (n *notifier) newIDLocked(prefix string) (string, bool) {
	id, err := newID(prefix, n.clock.Now(), nil)
	if err != nil {
		n.log.Error("cannot allocate event id", "err", err)
		return "", false
	}
	return id, true
}

// routeLocked lists the sinks whose filters accept ev, in config order, and
// the mention to store on a page-severity down. route narrows the candidates
// before the sink filters run.
func (n *notifier) routeLocked(ev Event, route string) ([]string, string) {
	indexes, mention := routeCandidates(n.cfg.Sinks, n.routes.Load(), ev, route)
	var tags []string
	if ev.Target != nil && n.cfg.Tags != nil {
		tags = n.cfg.Tags(ev.Target.Name)
	}
	var names []string
	for _, idx := range indexes {
		if idx < 0 || idx >= len(n.cfg.Sinks) {
			continue
		}
		sc := n.cfg.Sinks[idx]
		if ok, _ := sc.explain(ev, tags); ok {
			names = append(names, sc.Name)
		}
	}
	return names, mention
}

// enqueueLocked queues an event that opens no incident and is not routed.
func (n *notifier) enqueueLocked(ev Event) {
	n.enqueueOn(ev, "")
}

// enqueueOn queues ev for the sinks that accept it on route.
func (n *notifier) enqueueOn(ev Event, route string) {
	names, _ := n.routeLocked(ev, route)
	for _, name := range names {
		n.b.push(name, &queued{
			Seq: n.b.seq(), IncidentID: "", Event: ev, Attempted: false, UnitID: "", Mention: "",
		})
	}
}

func (n *notifier) watchFirstRound(ctx context.Context) {
	defer n.wg.Done()
	select {
	case <-n.cfg.Alerts.FirstRoundDone():
	case <-ctx.Done():
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	id, ok := n.newIDLocked(prefixEvent)
	if !ok {
		return
	}
	n.enqueueLocked(newStartedEvent(id, n.cfg.Collector, n.clock.Now()))
	n.commitLocked()
	n.wakeLocked()
}

// commitLocked persists the book. On failure the in-memory state stays
// authoritative, delivery continues, and the full state is retried later.
func (n *notifier) commitLocked() {
	n.m.openIncidents(n.b.openCount())
	if n.store == nil {
		return
	}
	if err := n.store.commit(n.b.snapshot()); err != nil {
		n.m.persistFailed()
		if !n.persistBad {
			n.log.Error("notification state not persisted; durability degraded", "err", err)
		}
		n.persistBad = true
		return
	}
	if n.persistBad {
		n.log.Info("notification state persistence restored")
	}
	n.persistBad = false
}

func (n *notifier) retryPersist() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.persistBad {
		n.commitLocked()
	}
}

func (n *notifier) wakeLocked() {
	for _, ch := range n.wake {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// openIncidents returns copies of the open incident records.
func (n *notifier) openIncidents() []incident {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []incident
	for _, id := range n.b.open {
		if inc := n.b.incidents[id]; inc != nil {
			out = append(out, *inc)
		}
	}
	return out
}

// closeRemoved closes name's open incident with reason removed, as when a
// configured target was deleted from the config while tail-watch was stopped.
func (n *notifier) closeRemoved(name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if inc := n.b.openIncidentFor(name); inc != nil && n.closeLocked(inc, n.clock.Now(), nil, reasonRemoved) {
		n.commitLocked()
		n.wakeLocked()
	}
}

// worker delivers one sink's queue in order.
type worker struct {
	n           *notifier
	cfg         SinkConfig
	s           sink
	wake        chan struct{}
	loop        retryLoop
	resume      []*unit
	windowStart time.Time
}

func (w *worker) grouped() bool {
	return w.cfg.groupsMessages()
}

func (w *worker) groupCap() int {
	switch {
	case w.cfg.messageSink():
		return w.cfg.QueueSize
	case w.cfg.Batch:
		return w.cfg.MaxBatch
	default:
		return 1
	}
}

func (w *worker) run(ctx context.Context) {
	defer w.n.wg.Done()
	w.n.mu.Lock()
	w.resume = w.n.b.sinkUnits(w.cfg.Name)
	w.n.mu.Unlock()
	for ctx.Err() == nil {
		u := w.next(ctx)
		if u == nil {
			return
		}
		w.deliver(ctx, u)
	}
}

// next returns the next request to send, resuming attempted units before
// forming a new one. It blocks until there is work or ctx ends.
func (w *worker) next(ctx context.Context) *unit {
	for {
		if len(w.resume) > 0 {
			u := w.resume[0]
			w.resume = w.resume[1:]
			return u
		}
		if ctx.Err() != nil {
			return nil
		}
		u, delay := w.poll()
		if u != nil {
			return u
		}
		if delay >= 0 && !w.wait(ctx, delay) {
			return nil
		}
	}
}

// poll decides, under the notifier lock, whether a request is ready. It returns
// a formed unit, or how long to wait (zero: until woken; negative: poll again).
func (w *worker) poll() (*unit, time.Duration) {
	n := w.n
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.clock.Now()
	changed := n.b.expireStale(w.cfg.Name, now)
	pending := n.b.pendingItems(w.cfg.Name)
	if len(pending) == 0 {
		if changed {
			n.commitLocked()
		}
		w.windowStart = time.Time{}
		return nil, 0
	}
	if remaining := w.groupRemaining(now, len(pending)); remaining > 0 {
		if changed {
			n.commitLocked()
		}
		return nil, remaining
	}
	u := w.formLocked(pending[:min(len(pending), w.groupCap())], now)
	n.commitLocked()
	w.windowStart = time.Time{}
	if u == nil {
		return nil, -1
	}
	return u, 0
}

// groupRemaining is how much of the group window is left. Zero means send now.
func (w *worker) groupRemaining(now time.Time, pending int) time.Duration {
	if !w.grouped() {
		return 0
	}
	if w.windowStart.IsZero() {
		w.windowStart = now
	}
	if pending >= w.groupCap() {
		return 0
	}
	return max(w.cfg.GroupWait-now.Sub(w.windowStart), 0)
}

// formLocked fixes the first-attempt body of a request and marks its members
// attempted. The commit that follows makes that durable before any send.
func (w *worker) formLocked(items []*queued, now time.Time) *unit {
	n := w.n
	evs := make([]Event, len(items))
	mentions := make([]string, len(items))
	for i, it := range items {
		it.Event.Late = now.Sub(it.Event.Time.Time()) > lateAfter
		evs[i] = it.Event
		mentions[i] = it.Mention
	}
	id := items[0].Event.ID
	if w.grouped() {
		bid, ok := n.newIDLocked(prefixBatch)
		if !ok {
			return nil
		}
		id = bid
	}
	body, err := renderBody(w.s, evs, id, mentions)
	u := n.b.beginUnit(w.cfg.Name, id, items, body)
	if err != nil {
		n.log.Error("cannot render notification", "sink", w.cfg.Name, "err", err)
		n.b.dropUnit(u, reasonPermanent)
		return nil
	}
	if w.cfg.Type == sinkWebhook && w.cfg.Batch {
		n.m.batch(w.cfg.Name, len(items))
	}
	return u
}

// wait blocks until a wake-up, d elapsing (when d > 0) or ctx ending.
func (w *worker) wait(ctx context.Context, d time.Duration) bool {
	var timeout <-chan time.Time
	if d > 0 {
		t, ch := w.n.clock.NewTimer(d)
		defer t.Stop()
		timeout = ch
	}
	select {
	case <-w.wake:
		return true
	case <-timeout:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *worker) deliver(ctx context.Context, u *unit) {
	n := w.n
	expired := func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.b.unitExpired(u, n.clock.Now())
	}
	onFail := func(res deliveryResult) {
		n.m.failed(w.cfg.Name, res.Reason)
		n.log.WarnContext(ctx, "notification attempt failed", "sink", w.cfg.Name, "id", u.ID, "reason", res.Reason,
			"status", res.Status, "err", res.Err)
	}
	out := w.loop.run(ctx, u.Body, u.ID, expired, onFail)
	if out.Kind == outcomeCancelled {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch out.Kind {
	case outcomeDelivered:
		n.b.ackUnit(u, n.clock.Now())
	case outcomePermanent:
		n.log.ErrorContext(
			ctx,
			"notification rejected permanently",
			"sink",
			w.cfg.Name,
			"id",
			u.ID,
			"status",
			out.Result.Status,
		)
		n.b.dropUnit(u, reasonPermanent)
	case outcomeExpired:
		n.b.dropUnit(u, reasonExpired)
	case outcomeCancelled:
		return
	}
	n.commitLocked()
	n.wakeLocked()
}
