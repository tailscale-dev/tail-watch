package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/tstime"
	"tailscale.com/types/opt"
)

const routeAPIConfig = `
collector: {state_dir: /unused, ui: true}
sinks:
  - name: hook
    type: webhook
    url_env: CANARY_URL_ENV
    secret_file: CANARY_SECRET_FILE
    headers_env: {Authorization: CANARY_HEADER_ENV}
    events: [target.down]
  - name: ops-slack
    type: slack
    url_file: CANARY_URL_FILE
    mention_on_page: "<!subteam^SINK>"
    status_url: https://status.example/canary-status
    min_severity: warn
  - name: pager
    type: webhook
    url_env: PAGER_URL
    min_severity: page
    match: {sites: [nyc]}
  - name: tagged
    type: webhook
    url_env: TAGGED_URL
    match: {tags: ["tag:db"]}
routes:
  - {name: yaml-team, sinks: [hook]}
targets:
  - {name: nyc-nas, addr: 10.0.0.2, checks: [icmp], route: yaml-team}
`

func newRouteEnv(t *testing.T) *apiEnv {
	t.Helper()
	cfg, err := parseConfig([]byte(routeAPIConfig))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.Collector.StateDir = dir
	ptrs := make([]*Target, len(cfg.Targets))
	for i := range cfg.Targets {
		ptrs[i] = &cfg.Targets[i]
	}
	al, _, _ := testAlerter(t, ptrs, nil)
	a := new(app)
	reg := newRuntimeRegistry(t.Context(), a, nil)
	reg.alert = al
	reg.seed(ptrs)
	a.class = newClassifier(newPresence(nil, tstime.StdClock{}, nil), new(fakeEnv), nil, tstime.StdClock{},
		prometheus.NewRegistry(), ptrs, reg)
	for _, p := range ptrs {
		if addErr := reg.AddTarget(p); addErr != nil {
			t.Fatal(addErr)
		}
	}
	logs := &bytes.Buffer{}
	mgr := newMonitorManager(cfg, nil, reg, al, slog.New(slog.NewJSONHandler(logs, nil)))
	mgr.env = new(fakeEnv)
	var closed []string
	mgr.closeIncident = func(name string) { closed = append(closed, name) }
	mux := http.NewServeMux()
	mgr.routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &apiEnv{mgr: mgr, srv: srv, al: al, reg: reg, app: a, dir: dir, logs: logs, closed: &closed}
}

type countingRegistry struct {
	targetRegistry

	adds    int
	removes int
}

func (c *countingRegistry) AddTarget(t *Target) error {
	c.adds++
	return c.targetRegistry.AddTarget(t)
}

func (c *countingRegistry) RemoveTarget(name string) error {
	c.removes++
	return c.targetRegistry.RemoveTarget(name)
}

func (e *apiEnv) routeList(t *testing.T) []routeView {
	t.Helper()
	status, body := e.do(t, http.MethodGet, "/api/v1/routes", "")
	if status != http.StatusOK {
		t.Fatalf("list routes %d %s", status, body)
	}
	var views []routeView
	if err := json.Unmarshal([]byte(body), &views); err != nil {
		t.Fatal(err)
	}
	return views
}

func routeByName(views []routeView, name string) (routeView, bool) {
	for _, v := range views {
		if v.Name == name {
			return v, true
		}
	}
	return routeView{}, false
}

func TestRouteStatusCodes(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	const dbTeam = `{"name":"db-team","severity":"warn","sinks":["hook","ops-slack"],"mention":"<!subteam^S0DB>"}`
	tests := []struct {
		name         string
		method, path string
		body         string
		want         int
		wantBody     string
	}{
		{"list yaml", "GET", "/api/v1/routes", "", 200, `"name":"yaml-team"`},
		{"create", "POST", "/api/v1/routes", dbTeam, 201, `"source":"ui"`},
		{"create duplicate", "POST", "/api/v1/routes", dbTeam, 409, "already exists"},
		{"create over yaml name", "POST", "/api/v1/routes",
			`{"name":"yaml-team","sinks":["hook"]}`, 409, "already exists"},
		{"bad name", "POST", "/api/v1/routes", `{"name":"bad name","sinks":["hook"]}`, 422, "name"},
		{"empty sinks", "POST", "/api/v1/routes", `{"name":"empty","sinks":[]}`, 422, "sinks"},
		{"duplicate sink", "POST", "/api/v1/routes", `{"name":"dup","sinks":["hook","hook"]}`, 422, "sinks[1]"},
		{
			"bad severity",
			"POST",
			"/api/v1/routes",
			`{"name":"sev","severity":"loud","sinks":["hook"]}`,
			422,
			"severity",
		},
		{"unknown field", "POST", "/api/v1/routes", `{"name":"x","sinks":["hook"],"bogus":1}`, 400, "bogus"},
		{"malformed body", "POST", "/api/v1/routes", `{`, 400, "invalid JSON"},
		{"replace", "PUT", "/api/v1/routes/db-team",
			`{"name":"db-team","sinks":["ops-slack"],"severity":"page","mention":"<!subteam^S0DB>"}`, 200, "ops-slack"},
		{"replace name mismatch", "PUT", "/api/v1/routes/db-team",
			`{"name":"other","sinks":["hook"]}`, 422, "name"},
		{"replace missing", "PUT", "/api/v1/routes/ghost", `{"sinks":["hook"]}`, 404, "no route"},
		{"replace yaml", "PUT", "/api/v1/routes/yaml-team", `{"sinks":["hook"]}`, 409, "config file"},
		{"delete yaml", "DELETE", "/api/v1/routes/yaml-team", "", 409, "config file"},
		{"delete missing", "DELETE", "/api/v1/routes/ghost", "", 404, "no route"},
		{"delete", "DELETE", "/api/v1/routes/db-team", "", 204, ""},
	}
	for _, tt := range tests {
		status, body := e.do(t, tt.method, tt.path, tt.body)
		if status != tt.want || !strings.Contains(body, tt.wantBody) {
			t.Errorf("%s: got %d %s, want %d containing %q", tt.name, status, body, tt.want, tt.wantBody)
		}
	}
	oversized := `{"name":"big","sinks":["hook"],"mention":"` + strings.Repeat("a", maxMonitorBody) + `"}`
	if status, body := e.do(t, http.MethodPost, "/api/v1/routes", oversized); status != http.StatusBadRequest {
		t.Errorf("oversize body: %d %s", status, body)
	}
	views := e.routeList(t)
	if _, ok := routeByName(views, "db-team"); ok {
		t.Fatal("deleted route is still listed")
	}
	yaml, ok := routeByName(views, "yaml-team")
	if !ok || !yaml.Readonly || yaml.Source != sourceConfig || yaml.Mention != "" {
		t.Fatalf("yaml route %+v", yaml)
	}
	if len(yaml.UsedBy) != 1 || yaml.UsedBy[0] != "nyc-nas" || len(yaml.MissingSinks) != 0 {
		t.Fatalf("yaml used_by/missing %+v", yaml)
	}
	if !strings.Contains(mustRouteBody(t, e), `"missing_sinks":[]`) ||
		!strings.Contains(mustRouteBody(t, e), `"used_by":["nyc-nas"]`) {
		t.Fatal("used_by or missing_sinks was null")
	}
}

func mustRouteBody(t *testing.T, e *apiEnv) string {
	t.Helper()
	status, body := e.do(t, http.MethodGet, "/api/v1/routes", "")
	if status != http.StatusOK {
		t.Fatalf("list %d %s", status, body)
	}
	return body
}

func TestRouteMissingSinkAndSkippedEntry(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	e.mgr.routeEntries = []dynamicRoute{{
		Name: "bad name", Severity: "", Sinks: []string{"hook"}, Mention: "",
	}}
	status, body := e.do(t, http.MethodPost, "/api/v1/routes", `{"name":"cams","sinks":["gone","hook"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	var view routeView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if view.Severity != string(SeverityPage) || len(view.MissingSinks) != 1 || view.MissingSinks[0] != "gone" {
		t.Fatalf("view %+v", view)
	}
	if _, ok := routeByName(e.routeList(t), "bad name"); ok {
		t.Fatal("skipped file entry was listed")
	}
	b, err := os.ReadFile(filepath.Join(e.dir, routesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "bad name") || !strings.Contains(string(b), "cams") {
		t.Fatalf("file lost an entry: %s", b)
	}
	fi, err := os.Stat(filepath.Join(e.dir, routesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
}

func TestRouteReadsNeedNoContentType(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	for _, path := range []string{"/api/v1/routes", "/api/v1/sinks"} {
		if s, b := e.do(t, http.MethodGet, path, "", withHeader("Content-Type", "")); s != http.StatusOK {
			t.Errorf("GET %s without Content-Type: %d %s", path, s, b)
		}
	}
}

func TestRouteCreateReplacesSkippedEntryOfSameName(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	e.mgr.routeEntries = []dynamicRoute{{
		Name: "cams", Severity: "loud", Sinks: []string{"hook"}, Mention: "",
	}}
	status, body := e.do(t, http.MethodPost, "/api/v1/routes", `{"name":"cams","severity":"warn","sinks":["hook"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	entries, err := loadRoutes(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Severity != string(SeverityWarn) {
		t.Fatalf("file entries %+v, want only the new cams", entries)
	}
}

func TestRoutePersistFailureLeavesTable(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	before := e.mgr.published.Load()
	e.mgr.saveRoutes = func(string, []dynamicRoute) error { return errors.New("disk full") }
	status, body := e.do(t, http.MethodPost, "/api/v1/routes", `{"name":"db-team","sinks":["hook"]}`)
	if status != http.StatusInternalServerError || !strings.Contains(body, routesFileName) {
		t.Fatalf("%d %s", status, body)
	}
	if _, ok := routeByName(e.routeList(t), "db-team"); ok {
		t.Fatal("failed save published the route")
	}
	if e.mgr.published.Load() != before {
		t.Fatal("route table pointer changed after a failed save")
	}
	if _, err := os.Stat(filepath.Join(e.dir, routesFileName)); !os.IsNotExist(err) {
		t.Fatalf("routes.json exists after a failed save: %v", err)
	}
}

func TestRouteCreateThenDownUsesIt(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	shared := new(atomic.Pointer[routeTable])
	shared.Store(e.mgr.published.Load())
	e.mgr.published = shared
	status, body := e.do(t, http.MethodPost, "/api/v1/routes",
		`{"name":"db-team","severity":"page","sinks":["hook"],"mention":"<!subteam^S0DB>"}`)
	if status != http.StatusCreated {
		t.Fatalf("route %d %s", status, body)
	}
	const monitor = `{"name":"pg","addr":"10.9.0.8","checks":["icmp"],"route":"db-team"}`
	status, body = e.do(t, http.MethodPost, "/api/v1/targets", monitor)
	if status != http.StatusCreated || !strings.Contains(body, `"route":"db-team"`) {
		t.Fatalf("monitor %d %s", status, body)
	}
	assertDownUses(t, e, "pg", "hook", SeverityPage, "<!subteam^S0DB>")

	counts := &countingRegistry{targetRegistry: e.mgr.reg, adds: 0, removes: 0}
	e.mgr.reg = counts
	closed := len(*e.closed)
	status, body = e.do(t, http.MethodPut, "/api/v1/routes/db-team",
		`{"name":"db-team","severity":"warn","sinks":["ops-slack"]}`)
	if status != http.StatusOK {
		t.Fatalf("replace %d %s", status, body)
	}
	if counts.adds != 0 || counts.removes != 0 || len(*e.closed) != closed {
		t.Fatalf("route edit re-registered or closed an incident: adds %d removes %d closed %v",
			counts.adds, counts.removes, *e.closed)
	}
	assertDownUses(t, e, "pg", "ops-slack", SeverityWarn, "")

	status, body = e.do(t, http.MethodDelete, "/api/v1/routes/db-team", "")
	if status != http.StatusConflict || !strings.Contains(body, "pg") || !strings.Contains(body, `"used_by":["pg"]`) {
		t.Fatalf("delete in use: %d %s", status, body)
	}
	if _, ok := routeByName(e.routeList(t), "db-team"); !ok {
		t.Fatal("refused delete removed the route")
	}
}

func assertDownUses(t *testing.T, e *apiEnv, monitor, sink string, sev Severity, mention string) {
	t.Helper()
	e.mgr.mu.Lock()
	live, ok := e.mgr.live[monitor]
	table := e.mgr.published.Load()
	sinks := e.mgr.cfg.Sinks
	e.mgr.mu.Unlock()
	if !ok {
		t.Fatalf("monitor %s is not live", monitor)
	}
	ev := newDownEventIn("down", CollectorInfo{Name: "", TailscaleIP: ""}, previewTransition(live), table)
	if ev.Severity != sev {
		t.Fatalf("down severity %s, want %s", ev.Severity, sev)
	}
	indexes, gotMention := routeCandidates(sinks, table, ev, live.Route)
	if gotMention != mention {
		t.Fatalf("mention %q, want %q", gotMention, mention)
	}
	if len(indexes) != 1 || sinks[indexes[0]].Name != sink {
		t.Fatalf("down sinks %v, want %s", indexes, sink)
	}
}

func TestRouteSinkListHidesSecrets(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	status, body := e.do(t, http.MethodGet, "/api/v1/sinks", "")
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, body)
	}
	for _, secret := range []string{
		"CANARY_URL_ENV", "CANARY_SECRET_FILE", "CANARY_HEADER_ENV", "CANARY_URL_FILE",
		"https://status.example/canary-status", "PAGER_URL", "TAGGED_URL",
		"url_env", "url_file", "secret_file", "headers_env", "status_url",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("sink list contains %q\n%s", secret, body)
		}
	}
	var views []sinkView
	if err := json.Unmarshal([]byte(body), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 4 {
		t.Fatalf("sinks %+v", views)
	}
	hook, slack := views[0], views[1]
	if hook.Name != "hook" || hook.Type != sinkWebhook || hook.MentionOnPage != "" ||
		len(hook.Events) != 1 || hook.Events[0] != string(EventDown) {
		t.Fatalf("hook %+v", hook)
	}
	if slack.Type != sinkSlack || slack.MentionOnPage != "<!subteam^SINK>" ||
		slack.MinSeverity != string(SeverityWarn) {
		t.Fatalf("slack %+v", slack)
	}
	if len(views[2].Match.Sites) != 1 || views[2].Match.Sites[0] != "nyc" ||
		views[2].MinSeverity != string(SeverityPage) {
		t.Fatalf("pager %+v", views[2])
	}
}

func TestRoutePreview(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	status, body := e.do(t, http.MethodPost, "/api/v1/routes",
		`{"name":"db-team","sinks":["pager","ops-slack","gone"]}`)
	if status != http.StatusCreated {
		t.Fatalf("route %d %s", status, body)
	}
	beforeRoutes := mustRouteBody(t, e)
	beforeTargets := e.list(t)
	const lab = `{"name":"lab-db","addr":"10.9.0.4","site":"lab","checks":["icmp"],"route":"db-team"}`
	status, body = e.do(t, http.MethodPost, "/api/v1/route-preview", lab)
	if status != http.StatusOK {
		t.Fatalf("preview %d %s", status, body)
	}
	var view previewView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if view.Severity != string(SeverityPage) || view.Route != "db-team" || view.Recovery != recoveryNote {
		t.Fatalf("preview header %+v", view)
	}
	if view.Down.Type != string(EventDown) || view.Down.Severity != string(SeverityPage) {
		t.Fatalf("down %+v", view.Down)
	}
	if view.Flapping.Type != string(EventFlapping) || view.Flapping.Severity != string(SeverityWarn) {
		t.Fatalf("flapping %+v", view.Flapping)
	}
	wantDown := map[string]previewSink{
		"hook":      {Sink: "hook", Outcome: "dropped", Reason: "not_in_route"},
		"ops-slack": {Sink: "ops-slack", Outcome: "sent", Reason: ""},
		"pager":     {Sink: "pager", Outcome: "dropped", Reason: "match.sites"},
		"tagged":    {Sink: "tagged", Outcome: "dropped", Reason: "not_in_route"},
		"gone":      {Sink: "gone", Outcome: "dropped", Reason: "missing_sink"},
	}
	assertPreviewSinks(t, view.Down.Sinks, wantDown)
	wantFlap := map[string]previewSink{
		"hook":      {Sink: "hook", Outcome: "dropped", Reason: "not_in_route"},
		"ops-slack": {Sink: "ops-slack", Outcome: "sent", Reason: ""},
		"pager":     {Sink: "pager", Outcome: "dropped", Reason: "min_severity"},
		"tagged":    {Sink: "tagged", Outcome: "dropped", Reason: "not_in_route"},
		"gone":      {Sink: "gone", Outcome: "dropped", Reason: "missing_sink"},
	}
	assertPreviewSinks(t, view.Flapping.Sinks, wantFlap)

	const nyc = `{"name":"nyc-db","addr":"10.9.0.5","site":"nyc","checks":["icmp"],"route":"db-team"}`
	status, body = e.do(t, http.MethodPost, "/api/v1/route-preview", nyc)
	if status != http.StatusOK {
		t.Fatalf("nyc preview %d %s", status, body)
	}
	view = decodePreview(t, body)
	pager := previewSinkByName(view.Down.Sinks, "pager")
	if pager.Outcome != "sent" || pager.Reason != "" {
		t.Fatalf("nyc pager %+v", pager)
	}
	if mustRouteBody(t, e) != beforeRoutes {
		t.Fatal("preview changed routes")
	}
	if _, ok := e.list(t)["lab-db"]; ok || len(e.list(t)) != len(beforeTargets) {
		t.Fatal("preview created a monitor")
	}
	status, body = e.do(t, http.MethodPost, "/api/v1/route-preview",
		`{"name":"lab-db","addr":"","checks":["tcp:1"]}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "addr") {
		t.Fatalf("invalid preview %d %s", status, body)
	}
}

func assertPreviewSinks(t *testing.T, got []previewSink, want map[string]previewSink) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sinks %d %+v, want %d", len(got), got, len(want))
	}
	for _, row := range got {
		w, ok := want[row.Sink]
		if !ok || row != w {
			t.Errorf("sink %+v, want %+v", row, w)
		}
	}
}

func decodePreview(t *testing.T, body string) previewView {
	t.Helper()
	var view previewView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func previewSinkByName(rows []previewSink, name string) previewSink {
	for _, row := range rows {
		if row.Sink == name {
			return row
		}
	}
	return previewSink{}
}

func TestRoutePreviewTags(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	if status, body := e.do(t, http.MethodPost, "/api/v1/routes",
		`{"name":"tag-team","sinks":["tagged"]}`); status != http.StatusCreated {
		t.Fatalf("route %d %s", status, body)
	}
	const body = `{"name":"db","addr":"100.64.0.9","checks":["icmp"],"route":"tag-team"}`
	status, got := e.do(t, http.MethodPost, "/api/v1/route-preview", body)
	if status != http.StatusOK {
		t.Fatalf("unseeded %d %s", status, got)
	}
	var view previewView
	if err := json.Unmarshal([]byte(got), &view); err != nil {
		t.Fatal(err)
	}
	tagged := previewSinkByName(view.Down.Sinks, "tagged")
	if tagged.Outcome != "unknown" || tagged.Reason != "match.tags" {
		t.Fatalf("unseeded tagged %+v", tagged)
	}
	hook := previewSinkByName(view.Down.Sinks, "hook")
	if hook.Outcome != "dropped" || hook.Reason != "not_in_route" {
		t.Fatalf("unseeded hook %+v", hook)
	}

	e.mgr.peers = &fakePeerSource{peers: peerListFixture(), seeded: true}
	status, got = e.do(t, http.MethodPost, "/api/v1/route-preview", body)
	if status != http.StatusOK {
		t.Fatalf("unknown peer %d %s", status, got)
	}
	view = decodePreview(t, got)
	tagged = previewSinkByName(view.Down.Sinks, "tagged")
	if tagged.Outcome != "unknown" || tagged.Reason != "match.tags" {
		t.Fatalf("unknown peer tagged %+v", tagged)
	}

	e.mgr.peers = &fakePeerSource{peers: []peerInfo{{
		NodeID: 9, StableID: "", Name: "db", DNSName: "db.tailnet.ts.net", HostName: "db",
		IPs: []netip.Addr{netip.MustParseAddr("100.64.0.9")}, Routes: nil, AllowedIPs: nil,
		Tags: []string{"tag:other"}, Online: opt.NewBool(true),
	}}, seeded: true}
	status, got = e.do(t, http.MethodPost, "/api/v1/route-preview", body)
	if status != http.StatusOK {
		t.Fatalf("mismatch %d %s", status, got)
	}
	view = decodePreview(t, got)
	tagged = previewSinkByName(view.Down.Sinks, "tagged")
	if tagged.Outcome != "dropped" || tagged.Reason != "match.tags" {
		t.Fatalf("mismatched tags %+v", tagged)
	}

	e.mgr.peers = &fakePeerSource{peers: []peerInfo{{
		NodeID: 9, StableID: "", Name: "db", DNSName: "db.tailnet.ts.net", HostName: "db",
		IPs: []netip.Addr{netip.MustParseAddr("100.64.0.9")}, Routes: nil, AllowedIPs: nil,
		Tags: []string{"tag:db"}, Online: opt.NewBool(true),
	}}, seeded: true}
	status, got = e.do(t, http.MethodPost, "/api/v1/route-preview", body)
	if status != http.StatusOK {
		t.Fatalf("match %d %s", status, got)
	}
	view = decodePreview(t, got)
	tagged = previewSinkByName(view.Down.Sinks, "tagged")
	if tagged.Outcome != "sent" || tagged.Reason != "" {
		t.Fatalf("matching tags %+v", tagged)
	}
}

func TestRouteGuard(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	const body = `{"name":"db-team","sinks":["hook"]}`
	if s, b := e.do(t, http.MethodPost, "/api/v1/routes", body, withHeader("Content-Type", "text/plain")); s != 415 {
		t.Fatalf("content type %d %s", s, b)
	}
	if s, b := e.do(t, http.MethodPost, "/api/v1/routes", body, withHeader("Origin", "https://example.com")); s != 403 {
		t.Fatalf("origin %d %s", s, b)
	}
	if s, b := e.do(t, http.MethodPost, "/api/v1/route-preview", `{"name":"x","addr":"10.0.0.1","checks":["icmp"]}`,
		withHeader("Content-Type", "text/plain")); s != 415 {
		t.Fatalf("preview content type %d %s", s, b)
	}
	e.mgr.authorize = func(*http.Request, role) error { return errors.New("read-only viewer") }
	if s, b := e.do(t, http.MethodPost, "/api/v1/routes", body); s != 403 || !strings.Contains(b, "read-only viewer") {
		t.Fatalf("denied create %d %s", s, b)
	}
	if s, b := e.do(t, http.MethodPut, "/api/v1/routes/yaml-team", body); s != 403 {
		t.Fatalf("denied replace %d %s", s, b)
	}
	if s, b := e.do(t, http.MethodDelete, "/api/v1/routes/yaml-team", ""); s != 403 {
		t.Fatalf("denied delete %d %s", s, b)
	}
	e.mgr.mu.Lock()
	created := false
	for _, ent := range e.mgr.routeEntries {
		if ent.Name == "db-team" {
			created = true
		}
	}
	e.mgr.mu.Unlock()
	if created {
		t.Fatal("a denied write created a route")
	}
	if s, b := e.do(t, http.MethodPost, "/api/v1/route-preview",
		`{"name":"x","addr":"10.0.0.9","checks":["icmp"]}`); s != 403 || !strings.Contains(b, "read-only viewer") {
		t.Fatalf("preview must be authorized: %d %s", s, b)
	}
	if !strings.Contains(e.logs.String(), `"remote":"127.0.0.1:`) {
		t.Fatalf("denied write was not audited:\n%s", e.logs.String())
	}
	e.mgr.authorize = func(*http.Request, role) error { return nil }
	e.logs.Reset()
	e.do(t, http.MethodPost, "/api/v1/routes", body)
	e.do(t, http.MethodPost, "/api/v1/routes", body)
	e.do(t, http.MethodGet, "/api/v1/routes", "")
	out := e.logs.String()
	for _, want := range []string{
		"route write", `"op":"route.create"`, `"route":"db-team"`, `"outcome":"accepted"`,
		`"outcome":"refused"`, `"remote":"127.0.0.1:`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log missing %s:\n%s", want, out)
		}
	}
	if strings.Count(out, "route write") != 2 {
		t.Fatalf("route reads were audited:\n%s", out)
	}
}
