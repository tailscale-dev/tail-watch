package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tailscale.com/tstime"
)

const (
	maxMonitorBody  = 64 << 10
	contentTypeJSON = "application/json"
)

const (
	sourceConfig = "config"
	sourceUI     = "ui"
)

type snapshotSource interface {
	Snapshots() []alertSnapshot
}

// peerSource is the part of the presence view the peer list reads. A nil
// source behaves as unseeded.
type peerSource interface {
	Peers() []peerInfo
	seededNow() bool
}

// monitorManager owns the dynamic monitors: their file, their validation and
// their live registration. One mutex serializes every change, so a change is
// persisted and then applied before the next one starts.
type monitorManager struct {
	cfg   *Config
	dir   string
	reg   targetRegistry
	env   netEnv
	clock tstime.Clock
	// bindChecks gives a probed target's checks the app's shared resolver,
	// as the prober's targets have.
	bindChecks func(*Target)
	alerts     snapshotSource
	peers      peerSource
	log        *slog.Logger
	// authz resolves the caller. It is set whenever tailscaled can be asked,
	// including when authorization is off, so the audit log stays verified.
	authz *resolver
	// authorize is the single decision. With authorization off it permits
	// every request. need is the role the operation requires.
	authorize  func(*http.Request, role) error
	save       func(dir string, targets []dynamicTarget) error
	saveRoutes func(dir string, routes []dynamicRoute) error
	// closeIncident closes name's open incident with reason removed. Re-adding
	// an edited monitor overwrites the removed snapshot the notifier would
	// otherwise close it from, so the manager closes it directly.
	closeIncident func(name string)
	// published is the route snapshot readers load. When a notifier exists it
	// is the notifier's pointer, so a route write is what the next down reads.
	published *atomic.Pointer[routeTable]

	mu           sync.Mutex
	yaml         []Target
	entries      []dynamicTarget
	live         map[string]Target
	yamlRoutes   []Route
	routeEntries []dynamicRoute
}

// newMonitorManager takes the YAML targets and the dynamic entries the
// startup merge kept from cfg.Targets. Entries in the file that were skipped
// stay in it untouched and are not listed.
func newMonitorManager(cfg *Config, entries []dynamicTarget, reg targetRegistry, alerts snapshotSource,
	log *slog.Logger,
) *monitorManager {
	m := &monitorManager{
		cfg:   cfg,
		dir:   cfg.Collector.StateDir,
		reg:   reg,
		env:   nil,
		clock: tstime.StdClock{},

		bindChecks: func(*Target) {},
		alerts:     alerts,
		peers:      nil,
		log:        log,
		authz:      nil,
		authorize:  func(*http.Request, role) error { return nil },
		save:       saveDynamic,
		saveRoutes: saveRoutes,

		closeIncident: func(string) {},
		published:     new(atomic.Pointer[routeTable]),
		mu:            sync.Mutex{},
		yaml:          nil,
		entries:       slices.Clone(entries),
		live:          map[string]Target{},
		yamlRoutes:    yamlRoutesOf(cfg),
		routeEntries:  nil,
	}
	for _, t := range cfg.Targets {
		if t.Dynamic {
			m.live[t.Name] = copyTarget(&t)
		} else if !t.Implicit {
			m.yaml = append(m.yaml, copyTarget(&t))
		}
	}
	m.published.Store(newRouteTable(cfg.Routes))
	return m
}

// monitorView is one row of the list and the body of a create or replace.
type monitorView struct {
	Name             string   `json:"name"`
	Addr             string   `json:"addr"`
	Site             string   `json:"site"`
	Severity         string   `json:"severity"`
	Route            string   `json:"route,omitempty"`
	Parent           string   `json:"parent"`
	InsecureTLS      bool     `json:"insecure_tls"`
	Interval         string   `json:"interval"`
	Timeout          string   `json:"timeout"`
	FailThreshold    int      `json:"fail_threshold"`
	RecoverThreshold int      `json:"recover_threshold"`
	FlapLimit        int      `json:"flap_limit"`
	FlapWindow       string   `json:"flap_window"`
	Network          string   `json:"network"`
	Checks           []string `json:"checks"`
	Source           string   `json:"source"`
	Readonly         bool     `json:"readonly"`
	State            string   `json:"state,omitempty"`
	Dependents       []string `json:"dependents,omitempty"`
	// Definition is the stored entry of a UI monitor, so an edit starts from
	// what the operator typed rather than from resolved defaults.
	Definition *dynamicTarget `json:"definition,omitempty"`
}

func viewOf(t Target) monitorView {
	source := sourceUI
	if !t.Dynamic {
		source = sourceConfig
	}
	checks := make([]string, len(t.Checks))
	for i, c := range t.Checks {
		checks[i] = c.String()
	}
	return monitorView{
		Name: t.Name, Addr: t.Addr, Site: t.Site, Severity: string(t.Severity), Route: t.Route, Parent: t.Parent,
		InsecureTLS: t.InsecureTLS, Interval: t.Interval.String(), Timeout: t.Timeout.String(),
		FailThreshold: t.FailThreshold, RecoverThreshold: t.RecoverThreshold, FlapLimit: t.FlapLimit,
		FlapWindow: t.FlapWindow.String(), Network: string(t.Network), Checks: checks,
		Source: source, Readonly: !t.Dynamic, State: "", Dependents: nil, Definition: nil,
	}
}

// targets returns every listed monitor: YAML first, then dynamic in file order.
func (m *monitorManager) targets() []Target {
	out := slices.Clone(m.yaml)
	for _, e := range m.entries {
		if t, ok := m.live[e.Name]; ok {
			out = append(out, t)
		}
	}
	return out
}

// routeSet is the route list a new monitor is validated against.
func (m *monitorManager) routeSet() []Route {
	if m == nil || m.cfg == nil {
		return nil
	}
	return m.cfg.Routes
}

func (m *monitorManager) isYAML(name string) bool {
	return slices.ContainsFunc(m.yaml, func(t Target) bool { return t.Name == name })
}

func (m *monitorManager) dependents(name string) []string {
	var out []string
	for _, t := range m.targets() {
		if t.Parent == name {
			out = append(out, t.Name)
		}
	}
	return out
}

func (m *monitorManager) list() []monitorView {
	states := map[string]string{}
	if m.alerts != nil {
		for _, s := range m.alerts.Snapshots() {
			if !s.Removed {
				states[s.Name] = s.State.String()
			}
		}
	}
	all := m.targets()
	table := (*routeTable)(nil)
	if m.published != nil {
		table = m.published.Load()
	}
	out := make([]monitorView, len(all))
	for i, t := range all {
		v := viewOf(t)
		if r, ok := table.get(t.Route); ok && r.Severity != "" {
			v.Severity = string(r.Severity)
		}
		v.State = states[t.Name]
		v.Dependents = m.dependents(t.Name)
		v.Definition = m.definitionOf(t)
		out[i] = v
	}
	return out
}

// definitionOf returns the stored entry of a running UI monitor.
func (m *monitorManager) definitionOf(t Target) *dynamicTarget {
	if !t.Dynamic {
		return nil
	}
	at := slices.IndexFunc(m.entries, func(e dynamicTarget) bool { return e.Name == t.Name })
	if at < 0 {
		return nil
	}
	def := m.entries[at]
	return &def
}

// existingRaw is every running target's raw form except the one named skip.
func (m *monitorManager) existingRaw(skip string) []rawTarget {
	out := slices.Clone(m.cfg.RawTargets)
	for _, e := range m.entries {
		if _, ok := m.live[e.Name]; ok && e.Name != skip {
			out = append(out, e.raw())
		}
	}
	return out
}

type apiError struct {
	status int
	errs   []string
	usedBy []string
}

func (e *apiError) Error() string { return fmt.Sprint(e.errs) }

func fail(status int, msgs ...string) *apiError {
	return &apiError{status: status, errs: msgs, usedBy: nil}
}

func failUsed(status int, usedBy []string, msgs ...string) *apiError {
	return &apiError{status: status, errs: msgs, usedBy: usedBy}
}

func (m *monitorManager) validate(def dynamicTarget, replacing string) (Target, *apiError) {
	t, errs := m.cfg.validateCandidate(m.existingRaw(replacing), def.raw(), m.routeSet())
	if len(errs) == 0 {
		return t, nil
	}
	msgs := make([]string, len(errs))
	for i, err := range errs {
		msgs[i] = err.Error()
	}
	return t, fail(http.StatusUnprocessableEntity, msgs...)
}

func (m *monitorManager) persist(entries []dynamicTarget) *apiError {
	if err := m.save(m.dir, entries); err != nil {
		m.log.Error("persist monitors", "err", err)
		return fail(http.StatusInternalServerError, "cannot save "+dynamicFileName+": "+err.Error())
	}
	return nil
}

// create adds a monitor: persist first, then register it.
func (m *monitorManager) create(def dynamicTarget) (monitorView, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if def.Name == "" {
		return monitorView{}, fail(http.StatusUnprocessableEntity, "name: name is required")
	}
	if m.isYAML(def.Name) || slices.ContainsFunc(m.entries, func(e dynamicTarget) bool { return e.Name == def.Name }) {
		return monitorView{}, fail(http.StatusConflict, fmt.Sprintf("a monitor named %q already exists", def.Name))
	}
	t, aerr := m.validate(def, "")
	if aerr != nil {
		return monitorView{}, aerr
	}
	next := append(slices.Clone(m.entries), def)
	if aerr = m.persist(next); aerr != nil {
		return monitorView{}, aerr
	}
	live := copyTarget(&t)
	if err := m.reg.AddTarget(&live); err != nil {
		m.rollback()
		return monitorView{}, fail(http.StatusInternalServerError, "cannot start monitor: "+err.Error())
	}
	m.entries = next
	m.live[def.Name] = t
	return viewOf(t), nil
}

// replace is remove then add of the new definition, restoring the old one
// if the add fails.
func (m *monitorManager) replace(name string, def dynamicTarget) (monitorView, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if aerr := m.requireDynamic(name); aerr != nil {
		return monitorView{}, aerr
	}
	if def.Name == "" {
		def.Name = name
	}
	if def.Name != name {
		return monitorView{}, fail(
			http.StatusUnprocessableEntity,
			"name: the name of a monitor cannot change; delete it and add a new one",
		)
	}
	t, aerr := m.validate(def, name)
	if aerr != nil {
		return monitorView{}, aerr
	}
	old := m.live[name]
	next := slices.Clone(m.entries)
	next[slices.IndexFunc(next, func(e dynamicTarget) bool { return e.Name == name })] = def
	if aerr = m.persist(next); aerr != nil {
		return monitorView{}, aerr
	}
	if err := m.reg.RemoveTarget(name); err != nil {
		m.rollback()
		return monitorView{}, fail(http.StatusInternalServerError, "cannot stop monitor: "+err.Error())
	}
	m.closeIncident(name)
	live := copyTarget(&t)
	if err := m.reg.AddTarget(&live); err != nil {
		restore := copyTarget(&old)
		if restoreErr := m.reg.AddTarget(&restore); restoreErr != nil {
			m.log.Error("cannot restore monitor after failed edit", "target", name, "err", restoreErr)
		}
		m.rollback()
		return monitorView{}, fail(http.StatusInternalServerError, "cannot start monitor: "+err.Error())
	}
	m.entries = next
	m.live[name] = t
	return viewOf(t), nil
}

func (m *monitorManager) remove(name string) *apiError {
	m.mu.Lock()
	defer m.mu.Unlock()
	if aerr := m.requireDynamic(name); aerr != nil {
		return aerr
	}
	if deps := m.dependents(name); len(deps) > 0 {
		return fail(
			http.StatusConflict,
			fmt.Sprintf("%q is the parent of %v; delete or change those first", name, deps),
		)
	}
	next := slices.DeleteFunc(slices.Clone(m.entries), func(e dynamicTarget) bool { return e.Name == name })
	if aerr := m.persist(next); aerr != nil {
		return aerr
	}
	if err := m.reg.RemoveTarget(name); err != nil {
		m.rollback()
		return fail(http.StatusInternalServerError, "cannot stop monitor: "+err.Error())
	}
	m.entries = next
	delete(m.live, name)
	return nil
}

func (m *monitorManager) rollback() {
	if err := m.save(m.dir, m.entries); err != nil {
		m.log.Error("cannot roll back targets.json; it no longer matches the running monitors", "err", err)
	}
}

func (m *monitorManager) requireDynamic(name string) *apiError {
	if m.isYAML(name) {
		return fail(http.StatusConflict, fmt.Sprintf("%q comes from the config file and cannot be changed here", name))
	}
	if _, ok := m.live[name]; !ok {
		return fail(http.StatusNotFound, fmt.Sprintf("no monitor named %q", name))
	}
	return nil
}

// check validates a definition without applying it. A name that is already a
// dynamic monitor is validated as an edit of it.
func (m *monitorManager) check(def dynamicTarget) (monitorView, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.isYAML(def.Name) {
		return monitorView{}, fail(http.StatusConflict, fmt.Sprintf("%q comes from the config file", def.Name))
	}
	t, aerr := m.validate(def, def.Name)
	if aerr != nil {
		return monitorView{}, aerr
	}
	return viewOf(t), nil
}

// probeCallCap bounds the whole probe call, so one slow check cannot hold the
// response even when the definition's timeout is longer.
const probeCallCap = 15 * time.Second

// probeResult is what one run of a check reported, exactly as the prober
// would see it.
type probeResult struct {
	Check     string `json:"check"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// probeView is the body of POST /api/v1/probe.
type probeView struct {
	Results []probeResult `json:"results"`
}

// probe validates a definition like check does, then runs each of its checks
// once against the app's netEnv, concurrently, and reports one result per
// check in definition order. It persists and registers nothing. The mutex is
// held only for validation; the checks run after it is released, so a slow
// probe cannot stall a create or replace. A client disconnect cancels the
// runs through ctx.
func (m *monitorManager) probe(ctx context.Context, def dynamicTarget) ([]probeResult, *apiError) {
	m.mu.Lock()
	if m.isYAML(def.Name) {
		m.mu.Unlock()
		return nil, fail(http.StatusConflict, fmt.Sprintf("%q comes from the config file", def.Name))
	}
	t, aerr := m.validate(def, def.Name)
	m.mu.Unlock()
	if aerr != nil {
		return nil, aerr
	}
	m.bindChecks(&t)
	ctx, cancel := context.WithTimeout(ctx, probeCallCap)
	defer cancel()
	results := make([]probeResult, len(t.Checks))
	var wg sync.WaitGroup
	for i, c := range t.Checks {
		wg.Go(func() {
			runCtx, stop := context.WithTimeout(ctx, t.Timeout)
			defer stop()
			res := runCheckWith(runCtx, m.env, m.clock, &t, c)
			latency := res.Latency
			if latency <= 0 {
				latency = m.clock.Since(res.Start)
			}
			r := probeResult{Check: res.Check, OK: res.Err == nil, LatencyMS: latency.Milliseconds(), Error: ""}
			if res.Err != nil {
				r.Error = res.Err.Error()
			}
			results[i] = r
		})
	}
	wg.Wait()
	return results, nil
}

func decodeMonitor(w http.ResponseWriter, r *http.Request) (dynamicTarget, *apiError) {
	var def dynamicTarget
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMonitorBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&def); err != nil {
		return def, fail(http.StatusBadRequest, "invalid JSON body: "+err.Error())
	}
	return def, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeAPIError(w http.ResponseWriter, e *apiError) {
	body := map[string]any{"errors": e.errs}
	if len(e.usedBy) > 0 {
		body["used_by"] = e.usedBy
	}
	writeJSON(w, e.status, body)
}

// meView is the body of GET /api/v1/me.
type meView struct {
	Authorization bool     `json:"authorization"`
	User          string   `json:"user"`
	Node          string   `json:"node"`
	Tags          []string `json:"tags"`
	Role          string   `json:"role"`
	Identified    bool     `json:"identified"`
}

func (m *monitorManager) handleMe(w http.ResponseWriter, r *http.Request) {
	if m.authz == nil || !m.authz.enabled {
		writeJSON(w, http.StatusOK, meView{
			Authorization: false, User: "", Node: "", Tags: []string{}, Role: string(roleEditor), Identified: false,
		})
		return
	}
	c, err := m.authz.resolve(r)
	if err != nil {
		m.warnUnresolved(r, err)
		writeJSON(w, http.StatusOK, meView{
			Authorization: true, User: "", Node: "", Tags: []string{}, Role: "", Identified: false,
		})
		return
	}
	tags := c.Tags
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, meView{
		Authorization: true, User: c.User, Node: c.Node, Tags: tags, Role: string(c.Role), Identified: true,
	})
}

// authorizeRequest is the role check installed when collector.authorization is
// set. An unresolved caller is denied and logged at warn.
func (m *monitorManager) authorizeRequest(r *http.Request, need role) error {
	if m.authz == nil || !m.authz.enabled {
		return nil
	}
	c, err := m.authz.resolve(r)
	if err != nil {
		m.warnUnresolved(r, err)
		return errNoIdentity
	}
	return decide(need, c)
}

func (m *monitorManager) warnUnresolved(r *http.Request, err error) {
	if m.log == nil || r == nil {
		return
	}
	m.log.WarnContext(r.Context(), "ui caller unresolved", "remote", r.RemoteAddr, "err", err.Error())
}

func (m *monitorManager) handleList(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	views := m.list()
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, views)
}

func (m *monitorManager) handleCreate(w http.ResponseWriter, r *http.Request) {
	def, aerr := decodeMonitor(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	setAuditName(w, def.Name)
	view, aerr := m.create(def)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (m *monitorManager) handleReplace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	setAuditName(w, name)
	def, aerr := decodeMonitor(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	view, aerr := m.replace(name, def)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (m *monitorManager) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	setAuditName(w, name)
	if aerr := m.remove(name); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *monitorManager) handleValidate(w http.ResponseWriter, r *http.Request) {
	def, aerr := decodeMonitor(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	view, aerr := m.check(def)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (m *monitorManager) handleProbe(w http.ResponseWriter, r *http.Request) {
	def, aerr := decodeMonitor(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	setAuditName(w, def.Name)
	results, aerr := m.probe(r.Context(), def)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, probeView{Results: results})
}

type peerView struct {
	Name     string   `json:"name"`
	DNSName  string   `json:"dns_name"`
	HostName string   `json:"host_name"`
	IPs      []string `json:"ips"`
	Tags     []string `json:"tags"`
	Online   *bool    `json:"online"`
	Routes   []string `json:"routes"`
}

// peerListView is the body of GET /api/v1/peers. Peers is always a list,
// never null.
type peerListView struct {
	Seeded bool       `json:"seeded"`
	Peers  []peerView `json:"peers"`
}

func peerViewOf(p peerInfo) peerView {
	v := peerView{
		Name:     p.Name,
		DNSName:  p.DNSName,
		HostName: p.HostName,
		IPs:      make([]string, 0, len(p.IPs)),
		Tags:     slices.Clone(p.Tags),
		Online:   nil,
		Routes:   make([]string, 0, len(p.Routes)),
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	for _, ip := range p.IPs {
		v.IPs = append(v.IPs, ip.String())
	}
	for _, r := range p.Routes {
		v.Routes = append(v.Routes, r.String())
	}
	if online, ok := p.Online.Get(); ok {
		v.Online = &online
	}
	return v
}

// handlePeers lists the peers the collector can currently see. An unseeded
// view answers with an empty list rather than its last-known entries: those
// are of unknown age, and the seeded flag says the view cannot be trusted.
func (m *monitorManager) handlePeers(w http.ResponseWriter, _ *http.Request) {
	if m.peers == nil || !m.peers.seededNow() {
		writeJSON(w, http.StatusOK, peerListView{Seeded: false, Peers: []peerView{}})
		return
	}
	peers := m.peers.Peers()
	views := make([]peerView, len(peers))
	for i, p := range peers {
		views[i] = peerViewOf(p)
	}
	slices.SortFunc(views, func(a, b peerView) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, peerListView{Seeded: true, Peers: views})
}
