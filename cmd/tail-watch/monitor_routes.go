package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

const recoveryNote = "A recovery goes to the sinks that received its down."

const (
	outcomeSent    = "sent"
	outcomeDropped = "dropped"
	outcomeUnknown = "unknown"
)

var routeFieldPrefix = regexp.MustCompile(`^routes\[\d+\]\.`)

// routeView is one row of GET /api/v1/routes and the body of a create or replace.
type routeView struct {
	Name         string   `json:"name"`
	Severity     string   `json:"severity"`
	Sinks        []string `json:"sinks"`
	Mention      string   `json:"mention,omitempty"`
	Source       string   `json:"source"`
	Readonly     bool     `json:"readonly"`
	UsedBy       []string `json:"used_by"`
	MissingSinks []string `json:"missing_sinks"`
}

// sinkMatchView is the non-secret filter set of one sink.
type sinkMatchView struct {
	Sites   []string `json:"sites"`
	Targets []string `json:"targets"`
	Tags    []string `json:"tags"`
}

// sinkView is one row of GET /api/v1/sinks. It never carries a URL, a secret,
// a header value, or the name of the variable or file that holds one.
type sinkView struct {
	Name          string        `json:"name"`
	Type          string        `json:"type"`
	Events        []string      `json:"events"`
	MinSeverity   string        `json:"min_severity"`
	Match         sinkMatchView `json:"match"`
	MentionOnPage string        `json:"mention_on_page,omitempty"`
}

// previewSink is one sink's fate for one event.
type previewSink struct {
	Sink    string `json:"sink"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// previewEvent is the routing of one event type.
type previewEvent struct {
	Type     string        `json:"type"`
	Severity string        `json:"severity"`
	Sinks    []previewSink `json:"sinks"`
}

// previewView is the body of POST /api/v1/route-preview. An empty Route means
// the monitor is unrouted and every sink is a candidate.
type previewView struct {
	Severity string       `json:"severity"`
	Route    string       `json:"route"`
	Recovery string       `json:"recovery"`
	Down     previewEvent `json:"down"`
	Flapping previewEvent `json:"flapping"`
}

// peerByAddr is the optional address lookup a peerSource may provide. The
// presence view implements it. A source that does not is scanned instead.
type peerByAddr interface {
	Lookup(addr string) (peerInfo, bool)
}

// yamlRoutesOf copies the routes that came from the config file. UI routes
// are rebuilt from routes.json, so they are not part of this set.
func yamlRoutesOf(cfg *Config) []Route {
	if cfg == nil {
		return nil
	}
	out := make([]Route, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		if r.Source == sourceUI {
			continue
		}
		r.Names = slices.Clone(r.Names)
		r.Indexes = slices.Clone(r.Indexes)
		r.Missing = slices.Clone(r.Missing)
		out = append(out, r)
	}
	return out
}

func stringList(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return slices.Clone(in)
}

func (m *monitorManager) routeView(r Route) routeView {
	source := r.Source
	if source == "" {
		source = sourceConfig
	}
	return routeView{
		Name: r.Name, Severity: string(r.Severity), Sinks: stringList(r.Names), Mention: r.Mention,
		Source: source, Readonly: source != sourceUI, UsedBy: m.usedBy(r.Name), MissingSinks: stringList(r.Missing),
	}
}

func (m *monitorManager) usedBy(name string) []string {
	out := []string{}
	for _, t := range m.targets() {
		if t.Route == name {
			out = append(out, t.Name)
		}
	}
	return out
}

func (m *monitorManager) listRoutes() []routeView {
	out := make([]routeView, 0, len(m.cfg.Routes))
	for _, r := range m.cfg.Routes {
		out = append(out, m.routeView(r))
	}
	return out
}

func (m *monitorManager) isYAMLRoute(name string) bool {
	return slices.ContainsFunc(m.yamlRoutes, func(r Route) bool { return r.Name == name })
}

func (m *monitorManager) hasActiveRoute(name string) bool {
	if name == "" || m.cfg == nil {
		return false
	}
	return slices.ContainsFunc(m.cfg.Routes, func(r Route) bool { return r.Name == name })
}

func (m *monitorManager) hasUIRoute(name string) bool {
	if m.cfg == nil {
		return false
	}
	for _, r := range m.cfg.Routes {
		if r.Name == name && r.Source == sourceUI {
			return true
		}
	}
	return false
}

func (m *monitorManager) otherRouteNames(skip string) map[string]int {
	seen := map[string]int{}
	if m.cfg == nil {
		return seen
	}
	for i, r := range m.cfg.Routes {
		if r.Name != skip {
			seen[r.Name] = i
		}
	}
	return seen
}

func routeErrTexts(errs []error) []string {
	out := make([]string, len(errs))
	for i, err := range errs {
		out[i] = routeFieldPrefix.ReplaceAllString(err.Error(), "")
	}
	return out
}

// checkRoute validates a UI route. replacing is the current name on a replace
// and empty on a create. A missing sink is kept and reported, matching the file.
func (m *monitorManager) checkRoute(def dynamicRoute, replacing string) *apiError {
	if replacing != "" && def.Name == "" {
		def.Name = replacing
	}
	if replacing != "" && def.Name != replacing {
		return fail(
			http.StatusUnprocessableEntity,
			"name: the name of a route cannot change; delete it and add a new one",
		)
	}
	if replacing == "" && m.hasActiveRoute(def.Name) {
		return fail(http.StatusConflict, fmt.Sprintf("a route named %q already exists", def.Name))
	}
	rr := rawRoute{
		Name: def.Name, Severity: def.Severity, Sinks: append([]string(nil), def.Sinks...), Mention: def.Mention,
		unknown: nil, err: nil,
	}
	var errs []error
	if _, ok := resolveOneRoute(&errs, 0, rr, m.cfg.Sinks, m.otherRouteNames(def.Name), false); !ok {
		return fail(http.StatusUnprocessableEntity, routeErrTexts(errs)...)
	}
	return nil
}

func (m *monitorManager) persistRoutes(entries []dynamicRoute) *apiError {
	if err := m.saveRoutes(m.dir, entries); err != nil {
		m.log.Error("persist routes", "err", err)
		return fail(http.StatusInternalServerError, "cannot save "+routesFileName+": "+err.Error())
	}
	return nil
}

// rebuildRoutes resolves the file against the YAML routes. Invalid file
// entries are skipped, as they are at startup, and stay in the file.
func (m *monitorManager) rebuildRoutes(entries []dynamicRoute) []Route {
	scratch := *m.cfg
	scratch.Routes = slices.Clone(m.yamlRoutes)
	scratch.mergeRoutes(entries, nil)
	return scratch.Routes
}

func (m *monitorManager) applyRoutes(entries []dynamicRoute) {
	next := m.rebuildRoutes(entries)
	m.cfg.Routes = next
	if m.published != nil {
		m.published.Store(newRouteTable(next))
	}
	m.routeEntries = slices.Clone(entries)
}

func (m *monitorManager) oneRoute(name string) routeView {
	for _, r := range m.cfg.Routes {
		if r.Name == name {
			return m.routeView(r)
		}
	}
	return routeView{}
}

func replaceRouteEntry(entries []dynamicRoute, name string, def dynamicRoute) []dynamicRoute {
	next := make([]dynamicRoute, 0, len(entries))
	placed := false
	for _, e := range entries {
		if e.Name != name {
			next = append(next, e)
			continue
		}
		if !placed {
			next = append(next, def)
			placed = true
		}
	}
	if !placed {
		next = append(next, def)
	}
	return next
}

// createRoute persists a UI route, then publishes the route table. checkRoute
// refuses a name that is loaded, so a file entry with the same name is one
// that was skipped at startup; the new route replaces it.
func (m *monitorManager) createRoute(def dynamicRoute) (routeView, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if aerr := m.checkRoute(def, ""); aerr != nil {
		return routeView{}, aerr
	}
	next := replaceRouteEntry(m.routeEntries, def.Name, def)
	if aerr := m.persistRoutes(next); aerr != nil {
		return routeView{}, aerr
	}
	m.applyRoutes(next)
	return m.oneRoute(def.Name), nil
}

// replaceRoute writes a new definition for a UI route. It does not re-register
// monitors or close incidents: a recovery still uses the sinks of its down.
func (m *monitorManager) replaceRoute(name string, def dynamicRoute) (routeView, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.isYAMLRoute(name) {
		return routeView{}, fail(
			http.StatusConflict, fmt.Sprintf("%q comes from the config file and cannot be changed here", name),
		)
	}
	if !m.hasUIRoute(name) {
		return routeView{}, fail(http.StatusNotFound, fmt.Sprintf("no route named %q", name))
	}
	if def.Name == "" {
		def.Name = name
	}
	if aerr := m.checkRoute(def, name); aerr != nil {
		return routeView{}, aerr
	}
	next := replaceRouteEntry(m.routeEntries, name, def)
	if aerr := m.persistRoutes(next); aerr != nil {
		return routeView{}, aerr
	}
	m.applyRoutes(next)
	return m.oneRoute(name), nil
}

func (m *monitorManager) deleteRoute(name string) *apiError {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.isYAMLRoute(name) {
		return fail(http.StatusConflict, fmt.Sprintf("%q comes from the config file and cannot be changed here", name))
	}
	if !m.hasUIRoute(name) {
		return fail(http.StatusNotFound, fmt.Sprintf("no route named %q", name))
	}
	if used := m.usedBy(name); len(used) > 0 {
		return failUsed(http.StatusConflict, used, fmt.Sprintf(
			"%q is used by %s; delete or change those monitors first", name, strings.Join(used, ", "),
		))
	}
	next := slices.DeleteFunc(slices.Clone(m.routeEntries), func(e dynamicRoute) bool { return e.Name == name })
	if aerr := m.persistRoutes(next); aerr != nil {
		return aerr
	}
	m.applyRoutes(next)
	return nil
}

func decodeRoute(w http.ResponseWriter, r *http.Request) (dynamicRoute, *apiError) {
	var def dynamicRoute
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMonitorBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&def); err != nil {
		return def, fail(http.StatusBadRequest, "invalid JSON body: "+err.Error())
	}
	return def, nil
}

func (m *monitorManager) handleListRoutes(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	views := m.listRoutes()
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, views)
}

func (m *monitorManager) handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	def, aerr := decodeRoute(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	setAuditName(w, def.Name)
	view, aerr := m.createRoute(def)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (m *monitorManager) handleReplaceRoute(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	setAuditName(w, name)
	def, aerr := decodeRoute(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	view, aerr := m.replaceRoute(name, def)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (m *monitorManager) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	setAuditName(w, name)
	if aerr := m.deleteRoute(name); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func sinkViewOf(s SinkConfig) sinkView {
	events := make([]string, 0, len(s.Events))
	for _, e := range s.Events {
		events = append(events, string(e))
	}
	mention := ""
	if s.Type == sinkSlack {
		mention = s.MentionOnPage
	}
	return sinkView{
		Name: s.Name, Type: s.Type, Events: events, MinSeverity: string(s.MinSeverity),
		Match: sinkMatchView{
			Sites: stringList(s.Match.Sites), Targets: stringList(s.Match.Targets), Tags: stringList(s.Match.Tags),
		},
		MentionOnPage: mention,
	}
}

func (m *monitorManager) handleListSinks(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	views := make([]sinkView, 0, len(m.cfg.Sinks))
	for _, s := range m.cfg.Sinks {
		views = append(views, sinkViewOf(s))
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, views)
}

func previewTransition(t Target) Transition {
	return Transition{
		Sequence: 0, Target: t, From: StateUnknown, To: StateDown, Flapping: false,
		Check: "", Layer: "", Err: nil, Since: time.Time{}, At: time.Time{}, Failures: 0,
		Path:       pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
		Dependents: nil, DownSince: time.Time{},
	}
}

func previewOutcome(sc SinkConfig, ev Event, inRoute, routed bool, tags []string, tagsKnown bool) (string, string) {
	if routed && !inRoute {
		return outcomeDropped, "not_in_route"
	}
	use := tags
	if !tagsKnown {
		use = nil
	}
	ok, reason := sc.explain(ev, use)
	if ok {
		return outcomeSent, ""
	}
	if reason == "match.tags" && !tagsKnown {
		return outcomeUnknown, reason
	}
	return outcomeDropped, reason
}

func (m *monitorManager) previewTags(addr string) ([]string, bool) {
	if m.peers == nil || !m.peers.seededNow() {
		return nil, false
	}
	var (
		peer peerInfo
		ok   bool
	)
	if by, is := m.peers.(peerByAddr); is {
		peer, ok = by.Lookup(addr)
	} else {
		peer, ok = findPeer(m.peers.Peers(), addr)
	}
	if !ok {
		return nil, false
	}
	if peer.Tags == nil {
		return []string{}, true
	}
	return slices.Clone(peer.Tags), true
}

func missingNames(table *routeTable, route string) []string {
	r, ok := table.get(route)
	if !ok || len(r.Missing) == 0 {
		return nil
	}
	return r.Missing
}

func (m *monitorManager) previewEvent(
	ev Event, route string, table *routeTable, tags []string, tagsKnown bool,
) previewEvent {
	indexes, _ := routeCandidates(m.cfg.Sinks, table, ev, route)
	inRoute := map[int]struct{}{}
	for _, idx := range indexes {
		inRoute[idx] = struct{}{}
	}
	routed := route != ""
	sinks := make([]previewSink, 0, len(m.cfg.Sinks))
	for i, sc := range m.cfg.Sinks {
		_, candidate := inRoute[i]
		outcome, reason := previewOutcome(sc, ev, candidate, routed, tags, tagsKnown)
		sinks = append(sinks, previewSink{Sink: sc.Name, Outcome: outcome, Reason: reason})
	}
	for _, name := range missingNames(table, route) {
		sinks = append(sinks, previewSink{Sink: name, Outcome: outcomeDropped, Reason: "missing_sink"})
	}
	return previewEvent{Type: string(ev.Type), Severity: string(ev.Severity), Sinks: sinks}
}

// previewOf builds the down and flapping events with the real constructors and
// explains every sink. It changes nothing.
func (m *monitorManager) previewOf(t Target) previewView {
	table := (*routeTable)(nil)
	if m.published != nil {
		table = m.published.Load()
	}
	tr := previewTransition(t)
	col := CollectorInfo{Name: "", TailscaleIP: ""}
	tags, known := m.previewTags(t.Addr)
	return previewView{
		Severity: string(t.Severity),
		Route:    t.Route,
		Recovery: recoveryNote,
		Down:     m.previewEvent(newDownEventIn("preview", col, tr, table), t.Route, table, tags, known),
		Flapping: m.previewEvent(newFlappingEvent("preview", col, tr), t.Route, table, tags, known),
	}
}

// preview validates like POST /api/v1/validate, then explains routing. The
// manager mutex is held for the whole call, same as a route write.
func (m *monitorManager) preview(def dynamicTarget) (previewView, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.isYAML(def.Name) {
		return previewView{}, fail(http.StatusConflict, fmt.Sprintf("%q comes from the config file", def.Name))
	}
	t, aerr := m.validate(def, def.Name)
	if aerr != nil {
		return previewView{}, aerr
	}
	return m.previewOf(t), nil
}

func (m *monitorManager) handleRoutePreview(w http.ResponseWriter, r *http.Request) {
	def, aerr := decodeMonitor(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	view, aerr := m.preview(def)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func peerNameRank(p peerInfo, want string) (int, bool) {
	switch {
	case p.DNSName != "" && strings.EqualFold(p.DNSName, want):
		return namePrioDNS, true
	case p.Name != "" && !strings.EqualFold(p.Name, p.DNSName) && strings.EqualFold(p.Name, want):
		return namePrioShort, true
	case p.HostName != "" && strings.EqualFold(p.HostName, want):
		return namePrioHost, true
	default:
		return namePrioNone, false
	}
}

// findPeer matches addr against a copied peer list: name, Tailscale IP, then
// the longest subnet route. presence.Lookup is the view used in production.
func findPeer(peers []peerInfo, addr string) (peerInfo, bool) {
	addr = strings.TrimSuffix(strings.TrimSpace(addr), ".")
	if addr == "" {
		return peerInfo{}, false
	}
	best, found := namePrioNone, false
	var chosen peerInfo
	for _, p := range peers {
		rank, ok := peerNameRank(p, addr)
		if ok && rank < best {
			best, chosen, found = rank, p, true
		}
	}
	if found {
		return chosen, true
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return peerInfo{}, false
	}
	ip = ip.Unmap()
	for _, p := range peers {
		if slices.ContainsFunc(p.IPs, func(c netip.Addr) bool { return c.Unmap() == ip }) {
			return p, true
		}
	}
	return peerByPrefix(peers, ip)
}

func peerByPrefix(peers []peerInfo, ip netip.Addr) (peerInfo, bool) {
	var best peerInfo
	bestBits, found := -1, false
	for _, peer := range peers {
		for _, pref := range peer.Routes {
			if !pref.Contains(ip) || pref.Bits() < bestBits {
				continue
			}
			if pref.Bits() == bestBits && peer.Name >= best.Name {
				continue
			}
			best, bestBits, found = peer, pref.Bits(), true
		}
	}
	if !found {
		return peerInfo{}, false
	}
	return best, true
}
