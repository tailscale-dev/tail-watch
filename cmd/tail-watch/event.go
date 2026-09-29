package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"time"
)

const (
	eventVersion  = 1
	idAlphabet    = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	idChars       = 26
	idPadBits     = 2
	byteMask      = 0xff
	bitsPerByte   = 8
	idTimeBytes   = 6
	prefixEvent   = "evt_"
	prefixBatch   = "bat_"
	reasonRemoved = "removed"
)

// newID returns prefix plus a 26 character Crockford base32 identifier: 48 bits
// of Unix milliseconds followed by 80 random bits, so ids sort by time across
// milliseconds.
func newID(prefix string, now time.Time, src io.Reader) (string, error) {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := range idTimeBytes {
		b[i] = byte((ms >> (bitsPerByte * (idTimeBytes - 1 - i))) & byteMask)
	}
	if src == nil {
		src = rand.Reader
	}
	if _, err := io.ReadFull(src, b[idTimeBytes:]); err != nil {
		return "", fmt.Errorf("event id: %w", err)
	}
	out := make([]byte, idChars)
	for i := range out {
		v := 0
		for j := range 5 {
			v <<= 1
			if p := i*5 + j - idPadBits; p >= 0 && b[p/bitsPerByte]>>(bitsPerByte-1-p%bitsPerByte)&1 == 1 {
				v |= 1
			}
		}
		out[i] = idAlphabet[v]
	}
	return prefix + string(out), nil
}

// stamp marshals as RFC 3339 UTC with second precision.
type stamp time.Time

func (s stamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(s).UTC().Format(time.RFC3339))
}

func (s *stamp) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		return err
	}
	*s = stamp(t)
	return nil
}

// Time returns s as a [time.Time].
func (s stamp) Time() time.Time { return time.Time(s) }

// CollectorInfo identifies the collector in every payload.
type CollectorInfo struct {
	Name        string `json:"name"`
	TailscaleIP string `json:"tailscale_ip,omitempty"`
}

// EventTarget is the target object of a payload.
type EventTarget struct {
	Name     string `json:"name"`
	Addr     string `json:"addr"`
	Site     string `json:"site,omitempty"`
	Parent   string `json:"parent,omitempty"`
	Implicit bool   `json:"implicit,omitempty"`
}

// EventPath is how the latest probe reached the target.
type EventPath struct {
	Kind       string `json:"kind,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	DERPRegion string `json:"derp_region,omitempty"`
	LatencyMS  *int64 `json:"latency_ms,omitempty"`
}

// Event is the version 1 notification payload. Changes within version 1 only
// add fields.
type Event struct {
	Version         int           `json:"version"`
	ID              string        `json:"id"`
	Type            EventType     `json:"type"`
	Severity        Severity      `json:"severity"`
	Time            stamp         `json:"time"`
	Late            bool          `json:"late,omitempty"`
	Collector       CollectorInfo `json:"collector"`
	Target          *EventTarget  `json:"target,omitempty"`
	Check           string        `json:"check,omitempty"`
	Layer           string        `json:"layer,omitempty"`
	Error           string        `json:"error,omitempty"`
	Since           *stamp        `json:"since,omitempty"`
	Failures        int           `json:"failures,omitempty"`
	Path            *EventPath    `json:"path,omitempty"`
	Dependents      *[]string     `json:"dependents,omitempty"`
	DurationSeconds *int64        `json:"duration_seconds,omitempty"`
	DownEventID     string        `json:"down_event_id,omitempty"`
	Reason          string        `json:"reason,omitempty"`
}

func depsPtr(d []string) *[]string {
	out := slices.Clone(d)
	if out == nil {
		out = []string{}
	}
	return &out
}

func eventTargetOf(t Target) *EventTarget {
	return &EventTarget{Name: t.Name, Addr: t.Addr, Site: t.Site, Parent: t.Parent, Implicit: t.Implicit}
}

func eventPathOf(p pathInfo) *EventPath {
	if p == (pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0}) {
		return nil
	}
	out := &EventPath{Kind: p.Kind, Endpoint: p.Endpoint, DERPRegion: p.DERP, LatencyMS: nil}
	if p.Latency > 0 {
		ms := p.Latency.Milliseconds()
		out.LatencyMS = &ms
	}
	return out
}

func stampPtr(t time.Time) *stamp {
	if t.IsZero() {
		return nil
	}
	s := stamp(t)
	return &s
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// baseEvent fills the fields every payload carries.
func baseEvent(id string, typ EventType, sev Severity, at time.Time, col CollectorInfo) Event {
	return Event{
		Version: eventVersion, ID: id, Type: typ, Severity: sev, Time: stamp(at), Late: false,
		Collector: col, Target: nil, Check: "", Layer: "", Error: "", Since: nil, Failures: 0,
		Path: nil, Dependents: nil, DurationSeconds: nil, DownEventID: "", Reason: "",
	}
}

func newDownEvent(id string, col CollectorInfo, tr Transition) Event {
	sev := tr.Target.Severity
	if sev == "" {
		sev = SeverityPage
	}
	ev := baseEvent(id, EventDown, sev, tr.At, col)
	ev.Target = eventTargetOf(tr.Target)
	ev.Check = tr.Check
	ev.Layer = string(tr.Layer)
	ev.Error = errString(tr.Err)
	ev.Since = stampPtr(downSinceOf(tr))
	ev.Failures = tr.Failures
	ev.Path = eventPathOf(tr.Path)
	ev.Dependents = depsPtr(tr.Dependents)
	return ev
}

func downSinceOf(tr Transition) time.Time {
	if !tr.Since.IsZero() {
		return tr.Since
	}
	return tr.DownSince
}

func newFlappingEvent(id string, col CollectorInfo, tr Transition) Event {
	ev := baseEvent(id, EventFlapping, SeverityWarn, tr.At, col)
	ev.Target = eventTargetOf(tr.Target)
	ev.Check = tr.Check
	ev.Layer = string(tr.Layer)
	ev.Error = errString(tr.Err)
	ev.Since = stampPtr(downSinceOf(tr))
	ev.Failures = tr.Failures
	ev.Path = eventPathOf(tr.Path)
	return ev
}

// newRecoveredEvent resolves the down event held by inc. Reason is "removed"
// when the target disappeared rather than recovered.
func newRecoveredEvent(id string, col CollectorInfo, inc *incident, at time.Time, deps []string, reason string) Event {
	down := inc.Down
	ev := baseEvent(id, EventRecovered, down.Severity, at, col)
	ev.Target = down.Target
	ev.Check = down.Check
	ev.Error = down.Error
	ev.Since = down.Since
	ev.Failures = down.Failures
	ev.Path = down.Path
	ev.Dependents = depsPtr(deps)
	ev.DownEventID = down.ID
	ev.Reason = reason
	begin := down.Time.Time()
	if down.Since != nil {
		begin = down.Since.Time()
	}
	secs := max(int64(at.Sub(begin).Seconds()), 0)
	ev.DurationSeconds = &secs
	return ev
}

func newStartedEvent(id string, col CollectorInfo, at time.Time) Event {
	return baseEvent(id, EventStarted, SeverityInfo, at, col)
}

func newTestEvent(id string, col CollectorInfo, at time.Time) Event {
	return baseEvent(id, EventTest, SeverityInfo, at, col)
}

// mapKind is what a transition means for the incident book.
type mapKind int

const (
	mapNone mapKind = iota
	mapDown
	mapRecovered
	mapFlapping
)

// mapTransition applies the transition-to-event table. open reports whether
// the target already has an open incident.
func mapTransition(tr Transition, open bool) mapKind {
	switch {
	case tr.Flapping:
		return mapFlapping
	case tr.To == StateDown && !open:
		return mapDown
	case tr.To == StateUp && open:
		return mapRecovered
	default:
		return mapNone
	}
}

// accepts reports whether the sink's routing filters pass ev. tags are the
// tailnet tags of the node the event's target resolves to.
func (s SinkConfig) accepts(ev Event, tags []string) bool {
	if ev.Type == EventTest {
		return true
	}
	if len(s.Events) > 0 && !slices.Contains(s.Events, ev.Type) {
		return false
	}
	if ev.Severity.rank() < s.MinSeverity.rank() {
		return false
	}
	if ev.Target == nil {
		return true
	}
	m := s.Match
	if len(m.Sites) > 0 && !slices.Contains(m.Sites, ev.Target.Site) {
		return false
	}
	if len(m.Targets) > 0 && !slices.ContainsFunc(m.Targets, func(g string) bool {
		ok, err := path.Match(g, ev.Target.Name)
		return err == nil && ok
	}) {
		return false
	}
	if len(m.Tags) > 0 && !slices.ContainsFunc(m.Tags, func(t string) bool { return slices.Contains(tags, t) }) {
		return false
	}
	return true
}
