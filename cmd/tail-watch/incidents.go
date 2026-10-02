package main

import (
	"log/slog"
	"slices"
	"strings"
	"time"

	"tailscale.com/tailcfg"
)

// phase is where one event stands for one sink.
type phase string

const (
	phaseNone      phase = ""
	phasePending   phase = "pending"
	phaseAttempted phase = "attempted"
	phaseAcked     phase = "acknowledged"
	phaseDropped   phase = "dropped"
)

func (p phase) terminal() bool {
	return p == phaseNone || p == phaseAcked || p == phaseDropped
}

// sinkDelivery is one sink's delivery state for one incident. The *Tried flags
// remember that a request was made even after the phase became dropped: the
// receiver may have accepted a request whose response was lost.
type sinkDelivery struct {
	Down      phase `json:"down"`
	DownTried bool  `json:"down_tried,omitempty"`
	Rec       phase `json:"recovery,omitempty"`
	RecTried  bool  `json:"recovery_tried,omitempty"`
}

type incidentStatus string

const (
	incidentOpen    incidentStatus = "open"
	incidentClosing incidentStatus = "closing"
)

// targetDef is the persisted identity of an incident's target. It carries
// enough to recreate a learned router before checks are scheduled.
type targetDef struct {
	Name     string   `json:"name"`
	Addr     string   `json:"addr"`
	Site     string   `json:"site,omitempty"`
	Parent   string   `json:"parent,omitempty"`
	Severity Severity `json:"severity"`
	Implicit bool     `json:"implicit,omitempty"`
	StableID string   `json:"stable_id,omitempty"`
	NodeID   int64    `json:"node_id,omitempty"`
}

func targetDefOf(t Target) targetDef {
	return targetDef{
		Name: t.Name, Addr: t.Addr, Site: t.Site, Parent: t.Parent, Severity: t.Severity,
		Implicit: t.Implicit, StableID: string(t.StableID), NodeID: int64(t.NodeID),
	}
}

// implicitTarget rebuilds a learned reachability target from its record.
func (d targetDef) implicitTarget() *Target {
	base := builtinTiming()
	return &Target{
		Dynamic: false,
		Name:    d.Name, Addr: d.Addr, Site: d.Site, Parent: d.Parent, Severity: d.Severity,
		InsecureTLS: false, Interval: base.Interval, Timeout: base.Timeout,
		FailThreshold: base.FailThreshold, RecoverThreshold: base.RecoverThreshold,
		FlapLimit: base.FlapLimit, FlapWindow: base.FlapWindow, Network: NetworkTailnet,
		Checks:   []check{tspingCheck{resolver: nil}},
		Implicit: true, StableID: tailcfg.StableNodeID(d.StableID), NodeID: tailcfg.NodeID(d.NodeID), Route: "",
	}
}

// incident is one outage with its delivery bookkeeping. It lives until every
// sink's intents are terminal.
type incident struct {
	ID        string                   `json:"id"`
	Status    incidentStatus           `json:"status"`
	Target    targetDef                `json:"target"`
	Down      Event                    `json:"down"`
	DownSince time.Time                `json:"down_since"`
	Recovery  *Event                   `json:"recovery,omitempty"`
	Sinks     map[string]*sinkDelivery `json:"sinks"`
}

// queued is one delivery intent for one sink.
type queued struct {
	Seq        uint64 `json:"seq"`
	IncidentID string `json:"incident_id,omitempty"`
	Event      Event  `json:"event"`
	Attempted  bool   `json:"attempted,omitempty"`
	UnitID     string `json:"unit_id,omitempty"`
	// Mention is the route mention for a page-severity down. It is not part of
	// the event payload. An empty value falls back to the sink's mention.
	Mention string `json:"mention,omitempty"`
}

// unit is one request: a single event, a webhook batch or a Slack group.
// Body is fixed at the first attempt and reused byte for byte.
type unit struct {
	ID      string   `json:"id"`
	Sink    string   `json:"sink"`
	Members []uint64 `json:"members"`
	Body    []byte   `json:"body"`
}

// unitKey identifies a unit. A request id is only unique within one sink.
func unitKey(sink, id string) string { return sink + "/" + id }

func (u *unit) key() string { return unitKey(u.Sink, u.ID) }

// book is the notifier's authoritative in-memory state. It is guarded by the
// notifier mutex; a snapshot of it is what queue.go persists.
type book struct {
	log       *slog.Logger
	m         *notifyMetrics
	sinks     map[string]SinkConfig
	incidents map[string]*incident
	open      map[string]string
	queues    map[string]*memQueue
	units     map[string]*unit
	nextSeq   uint64
}

func newBook(log *slog.Logger, m *notifyMetrics, sinks []SinkConfig) *book {
	b := &book{
		log: log, m: m,
		sinks:     map[string]SinkConfig{},
		incidents: map[string]*incident{},
		open:      map[string]string{},
		queues:    map[string]*memQueue{},
		units:     map[string]*unit{},
		nextSeq:   1,
	}
	for _, s := range sinks {
		b.sinks[s.Name] = s
		b.queues[s.Name] = newMemQueue(s.QueueSize)
	}
	return b
}

func (b *book) seq() uint64 {
	s := b.nextSeq
	b.nextSeq++
	return s
}

func (b *book) openIncidentFor(target string) *incident {
	if id, ok := b.open[target]; ok {
		return b.incidents[id]
	}
	return nil
}

func (b *book) delivery(it *queued, sink string) (*incident, *sinkDelivery) {
	if it.IncidentID == "" {
		return nil, nil
	}
	inc := b.incidents[it.IncidentID]
	if inc == nil {
		return nil, nil
	}
	return inc, inc.Sinks[sink]
}

func (b *book) refreshQueueLen(sink string) {
	if q := b.queues[sink]; q != nil {
		b.m.queueLen(sink, q.Len())
	}
}

// push queues it for sink, dropping the oldest unattempted event when the
// queue is full. An attempted event is in flight and is never a victim.
func (b *book) push(sink string, it *queued) {
	q := b.queues[sink]
	if q == nil {
		return
	}
	for q.Len() >= q.limit {
		victim := q.oldestPending()
		if victim == nil {
			b.dropItem(sink, it, reasonQueueFull)
			return
		}
		q.Remove(victim.Seq)
		b.dropItem(sink, victim, reasonQueueFull)
	}
	if it.Event.Type == EventRecovered {
		if _, d := b.delivery(it, sink); d == nil || d.Rec != phasePending {
			return
		}
	}
	q.push(it)
	b.refreshQueueLen(sink)
}

// openIncident records a new outage and queues its down event for sinks.
func (b *book) openIncident(ev Event, def targetDef, downSince time.Time, sinks []string, mention string) *incident {
	inc := &incident{
		ID: ev.ID, Status: incidentOpen, Target: def, Down: ev, DownSince: downSince,
		Recovery: nil, Sinks: map[string]*sinkDelivery{},
	}
	b.incidents[inc.ID] = inc
	b.open[def.Name] = inc.ID
	for _, name := range sinks {
		inc.Sinks[name] = &sinkDelivery{Down: phasePending, DownTried: false, Rec: phaseNone, RecTried: false}
	}
	for _, name := range sinks {
		b.push(name, &queued{
			Seq: b.seq(), IncidentID: inc.ID, Event: ev, Attempted: false, UnitID: "", Mention: mention,
		})
	}
	return inc
}

// closeIncident moves inc to closing and queues rec for every sink whose down
// state still makes a recovery meaningful.
func (b *book) closeIncident(inc *incident, rec Event) {
	inc.Status = incidentClosing
	inc.Recovery = &rec
	delete(b.open, inc.Target.Name)
	for _, name := range slices.Sorted(mapsKeys(inc.Sinks)) {
		d := inc.Sinks[name]
		if _, ok := b.sinks[name]; !ok {
			b.log.Warn("sink removed; skipping recovery", "sink", name, "incident", inc.ID)
			d.Rec = phaseDropped
			continue
		}
		if d.Down == phaseDropped && !d.DownTried {
			d.Rec = phaseDropped
			continue
		}
		d.Rec = phasePending
		b.push(name, &queued{
			Seq: b.seq(), IncidentID: inc.ID, Event: rec, Attempted: false, UnitID: "", Mention: "",
		})
	}
	b.gc(inc)
}

func mapsKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// gc forgets a closing incident once every intent is terminal.
func (b *book) gc(inc *incident) {
	if inc == nil || inc.Status != incidentClosing {
		return
	}
	for _, d := range inc.Sinks {
		if !d.Down.terminal() || !d.Rec.terminal() {
			return
		}
	}
	delete(b.incidents, inc.ID)
}

// beginUnit marks items attempted, together, and records the first-attempt body.
func (b *book) beginUnit(sink, id string, items []*queued, body []byte) *unit {
	u := &unit{ID: id, Sink: sink, Members: make([]uint64, 0, len(items)), Body: body}
	for _, it := range items {
		it.Attempted = true
		it.UnitID = id
		u.Members = append(u.Members, it.Seq)
		_, d := b.delivery(it, sink)
		if d == nil {
			continue
		}
		switch it.Event.Type {
		case EventFlapping, EventDegraded, EventStarted, EventTest:
			// carries no incident delivery state
		case EventDown:
			d.Down, d.DownTried = phaseAttempted, true
		case EventRecovered:
			d.Rec, d.RecTried = phaseAttempted, true
		}
	}
	b.units[u.key()] = u
	return u
}

func (b *book) unitItems(u *unit) []*queued {
	q := b.queues[u.Sink]
	if q == nil {
		return nil
	}
	items := make([]*queued, 0, len(u.Members))
	for _, seq := range u.Members {
		if it := q.Get(seq); it != nil {
			items = append(items, it)
		}
	}
	return items
}

// ackUnit records a 2xx for every member of u.
func (b *book) ackUnit(u *unit, now time.Time) {
	items := b.unitItems(u)
	q := b.queues[u.Sink]
	for _, it := range items {
		q.Remove(it.Seq)
		b.m.sent(u.Sink, it.Event.Type, now.Sub(it.Event.Time.Time()))
		inc, d := b.delivery(it, u.Sink)
		if d == nil {
			continue
		}
		switch it.Event.Type {
		case EventFlapping, EventDegraded, EventStarted, EventTest:
			// carries no incident delivery state
		case EventDown:
			d.Down = phaseAcked
		case EventRecovered:
			d.Rec = phaseAcked
		}
		b.gc(inc)
	}
	delete(b.units, u.key())
	b.refreshQueueLen(u.Sink)
}

// dropUnit drops every member of u, counting each event.
func (b *book) dropUnit(u *unit, reason string) {
	items := b.unitItems(u)
	q := b.queues[u.Sink]
	for _, it := range items {
		q.Remove(it.Seq)
	}
	for _, it := range items {
		b.dropItem(u.Sink, it, reason)
	}
	delete(b.units, u.key())
	b.refreshQueueLen(u.Sink)
}

// dropItem counts a lost event and updates the incident's delivery state. A
// never-attempted down takes its unsent recovery with it; an attempted one
// keeps the recovery eligible.
func (b *book) dropItem(sink string, it *queued, reason string) {
	b.m.dropped(sink, reason)
	b.log.Warn(
		"notification dropped",
		"sink",
		sink,
		"event",
		it.Event.ID,
		"type",
		string(it.Event.Type),
		"reason",
		reason,
	)
	inc, d := b.delivery(it, sink)
	if d == nil {
		return
	}
	switch it.Event.Type {
	case EventFlapping, EventDegraded, EventStarted, EventTest:
		// carries no incident delivery state
	case EventDown:
		d.Down = phaseDropped
		if !d.DownTried && d.Rec == phasePending {
			if q := b.queues[sink]; q != nil {
				if rec := q.findRecovery(inc.ID); rec != nil {
					q.Remove(rec.Seq)
				}
			}
			d.Rec = phaseDropped
		}
	case EventRecovered:
		d.Rec = phaseDropped
	}
	b.gc(inc)
}

// expireStale drops unattempted events older than the sink's max_age.
func (b *book) expireStale(sink string, now time.Time) bool {
	q := b.queues[sink]
	cfg, ok := b.sinks[sink]
	if q == nil || !ok {
		return false
	}
	changed := false
	for _, it := range slices.Clone(q.items) {
		if it.Attempted || now.Sub(it.Event.Time.Time()) <= cfg.MaxAge {
			continue
		}
		q.Remove(it.Seq)
		b.dropItem(sink, it, reasonExpired)
		changed = true
	}
	b.refreshQueueLen(sink)
	return changed
}

func (b *book) pendingItems(sink string) []*queued {
	q := b.queues[sink]
	if q == nil {
		return nil
	}
	var out []*queued
	for _, it := range q.items {
		if !it.Attempted {
			out = append(out, it)
		}
	}
	return out
}

// sinkUnits returns sink's in-flight units, oldest first.
func (b *book) sinkUnits(sink string) []*unit {
	var out []*unit
	for _, u := range b.units {
		if u.Sink == sink {
			out = append(out, u)
		}
	}
	slices.SortFunc(out, func(x, y *unit) int {
		switch {
		case len(x.Members) == 0 || len(y.Members) == 0:
			return 0
		case x.Members[0] < y.Members[0]:
			return -1
		case x.Members[0] > y.Members[0]:
			return 1
		default:
			return 0
		}
	})
	return out
}

// unitExpired reports whether any member of u is older than the sink's max_age.
func (b *book) unitExpired(u *unit, now time.Time) bool {
	cfg, ok := b.sinks[u.Sink]
	if !ok {
		return false
	}
	return slices.ContainsFunc(b.unitItems(u), func(it *queued) bool {
		return now.Sub(it.Event.Time.Time()) > cfg.MaxAge
	})
}

func (b *book) openCount() int {
	return len(b.open)
}

// incidentRestorer seeds a target DOWN without a second page.
type incidentRestorer interface {
	Restore(name string, since time.Time) error
}

// implicitRestorer recreates a learned reachability target by node identity.
type implicitRestorer interface {
	RestoreImplicit(t *Target)
}

// restoreOpen re-establishes every open incident before any check is scheduled:
// configured targets are seeded DOWN, learned routers are recreated from their
// recorded identity and seeded, and a configured target that no longer exists
// closes as removed. A restore refusal is logged and the incident retained; a
// learned router that cannot be rediscovered stays open until authoritative
// topology reconciliation removes it.
func (n *notifier) restoreOpen(configured map[string]bool, alerts incidentRestorer, implicit implicitRestorer) {
	open := n.openIncidents()
	slices.SortFunc(open, func(a, b incident) int { return strings.Compare(a.Target.Name, b.Target.Name) })
	for _, inc := range open {
		name := inc.Target.Name
		switch {
		case inc.Target.Implicit:
			if implicit == nil {
				continue
			}
			if err := alerts.Restore(name, inc.DownSince); err != nil {
				n.log.Error("cannot restore open incident; keeping it", "target", name, "incident", inc.ID, "err", err)
				continue
			}
			implicit.RestoreImplicit(inc.Target.implicitTarget())
		case configured[name]:
			if err := alerts.Restore(name, inc.DownSince); err != nil {
				n.log.Error("cannot restore open incident; keeping it", "target", name, "incident", inc.ID, "err", err)
			}
		default:
			n.log.Info("configured target removed while an incident was open", "target", name, "incident", inc.ID)
			n.closeRemoved(name)
		}
	}
}
