package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/tstime"
	"tailscale.com/types/opt"
)

type apiEnv struct {
	mgr    *monitorManager
	srv    *httptest.Server
	al     *alerter
	reg    *runtimeRegistry
	app    *app
	dir    string
	logs   *bytes.Buffer
	closed *[]string
}

type flakyRegistry struct {
	targetRegistry

	mu      sync.Mutex
	failAdd map[string]int
}

func (f *flakyRegistry) AddTarget(t *Target) error {
	f.mu.Lock()
	if f.failAdd[t.Name] > 0 {
		f.failAdd[t.Name]--
		f.mu.Unlock()
		return errors.New("forced add failure")
	}
	f.mu.Unlock()
	return f.targetRegistry.AddTarget(t)
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	cfg, err := parseConfig([]byte(`
collector: {state_dir: /unused, ui: true}
targets:
  - {name: nyc-router, addr: 10.0.0.1, checks: [icmp]}
  - {name: nyc-nas, addr: 10.0.0.2, checks: [icmp, 'tcp:445']}
`))
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
	var closed []string
	mgr.closeIncident = func(name string) { closed = append(closed, name) }
	mux := http.NewServeMux()
	mgr.routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &apiEnv{mgr: mgr, srv: srv, al: al, reg: reg, app: a, dir: dir, logs: logs, closed: &closed}
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

func (e *apiEnv) do(t *testing.T, method, path, body string, opts ...reqOpt) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *apiEnv) list(t *testing.T) map[string]monitorView {
	t.Helper()
	status, body := e.do(t, http.MethodGet, "/api/v1/targets", "")
	if status != http.StatusOK {
		t.Fatalf("list %d %s", status, body)
	}
	var views []monitorView
	if err := json.Unmarshal([]byte(body), &views); err != nil {
		t.Fatal(err)
	}
	out := map[string]monitorView{}
	for _, v := range views {
		out[v.Name] = v
	}
	return out
}

const labNAS = `{"name":"lab-nas","addr":"10.9.0.1","checks":["icmp"],"interval":"15s","timeout":"3s"}`

func TestMonitorListJoinsSourceAndState(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	if status, body := e.do(t, http.MethodPost, "/api/v1/targets", labNAS); status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	failN(e.al, alertTarget("lab-nas", "icmp"), "icmp", layerLAN, 3)
	got := e.list(t)
	if len(got) != 3 {
		t.Fatalf("list %+v", got)
	}
	if v := got["nyc-nas"]; v.Source != "config" || !v.Readonly || v.State != "UNKNOWN" {
		t.Fatalf("yaml row %+v", v)
	}
	if v := got["lab-nas"]; v.Source != "ui" || v.Readonly || v.State != "DOWN" || v.Interval != "15s" {
		t.Fatalf("dynamic row %+v", v)
	}
}

func TestMonitorStatusCodes(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	tests := []struct {
		name         string
		method, path string
		body         string
		want         int
		wantBody     string
	}{
		{"create", "POST", "/api/v1/targets", labNAS, 201, `"source":"ui"`},
		{"create duplicate", "POST", "/api/v1/targets", labNAS, 409, "already exists"},
		{"create over yaml name", "POST", "/api/v1/targets",
			`{"name":"nyc-nas","addr":"10.9.0.1","checks":["icmp"]}`, 409, "already exists"},
		{
			"invalid definition",
			"POST",
			"/api/v1/targets",
			`{"name":"lab-bad","addr":"10.9.0.2","checks":["tcp:70000"],"interval":"10s","timeout":"10s"}`,
			422,
			"checks[0]",
		},
		{"malformed body", "POST", "/api/v1/targets", `{`, 400, "invalid JSON"},
		{"unknown field", "POST", "/api/v1/targets", `{"name":"x","bogus":1}`, 400, "bogus"},
		{"replace", "PUT", "/api/v1/targets/lab-nas",
			`{"name":"lab-nas","addr":"10.9.0.5","checks":["icmp"]}`, 200, "10.9.0.5"},
		{"replace name mismatch", "PUT", "/api/v1/targets/lab-nas",
			`{"name":"other","addr":"10.9.0.5","checks":["icmp"]}`, 422, "name"},
		{"replace missing", "PUT", "/api/v1/targets/ghost", `{"addr":"10.9.0.5","checks":["icmp"]}`, 404, "no monitor"},
		{"replace yaml", "PUT", "/api/v1/targets/nyc-nas", `{"addr":"10.9.0.5","checks":["icmp"]}`, 409, "config file"},
		{"delete yaml", "DELETE", "/api/v1/targets/nyc-nas", "", 409, "config file"},
		{"delete missing", "DELETE", "/api/v1/targets/ghost", "", 404, "no monitor"},
		{
			"validate ok",
			"POST",
			"/api/v1/validate",
			`{"name":"lab-new","addr":"10.9.0.6","checks":["icmp"]}`,
			200,
			`"lab-new"`,
		},
		{"validate bad", "POST", "/api/v1/validate", `{"name":"lab-new","addr":"","checks":["tcp:1"]}`, 422, "addr"},
		{"delete", "DELETE", "/api/v1/targets/lab-nas", "", 204, ""},
		{"unknown api path", "GET", "/api/v1/nope", "", 404, "not found"},
	}
	for _, tt := range tests {
		status, body := e.do(t, tt.method, tt.path, tt.body)
		if status != tt.want || !strings.Contains(body, tt.wantBody) {
			t.Errorf("%s: got %d %s, want %d containing %q", tt.name, status, body, tt.want, tt.wantBody)
		}
	}
	if _, ok := e.list(t)["lab-new"]; ok {
		t.Fatal("validate created a monitor")
	}
}

func TestMonitorInvalidReportsEveryProblem(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	status, body := e.do(t, http.MethodPost, "/api/v1/targets",
		`{"name":"lab-bad","addr":"10.9.0.2","checks":["tcp:70000"],"interval":"10s","timeout":"10s"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", status, body)
	}
	var resp struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	var fields []string
	for _, msg := range resp.Errors {
		field, _, _ := strings.Cut(msg, ": ")
		fields = append(fields, field)
	}
	slices.Sort(fields)
	if !slices.Equal(fields, []string{"checks[0]", "timeout"}) {
		t.Fatalf("fields %v from %v", fields, resp.Errors)
	}
	if _, ok := e.list(t)["lab-bad"]; ok {
		t.Fatal("invalid monitor was created")
	}
}

func TestMonitorParentRules(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	post := func(body string) (int, string) { return e.do(t, http.MethodPost, "/api/v1/targets", body) }
	if s, b := post(`{"name":"lab-x","addr":"10.9.0.9","checks":["icmp"],"parent":"nyc-nas"}`); s != 422 ||
		!strings.Contains(b, "exactly one icmp or tsping") {
		t.Fatalf("non-reachability parent: %d %s", s, b)
	}
	if s, b := post(`{"name":"lab-router","addr":"10.9.0.1","checks":["icmp"]}`); s != 201 {
		t.Fatalf("%d %s", s, b)
	}
	if s, b := post(`{"name":"lab-nas","addr":"10.9.0.2","checks":["icmp"],"parent":"lab-router"}`); s != 201 {
		t.Fatalf("dynamic parent: %d %s", s, b)
	}
	if got := e.list(t)["lab-router"].Dependents; !slices.Equal(got, []string{"lab-nas"}) {
		t.Fatalf("dependents %v", got)
	}
	if s, b := e.do(
		t,
		http.MethodDelete,
		"/api/v1/targets/lab-router",
		"",
	); s != 409 ||
		!strings.Contains(b, "lab-nas") {
		t.Fatalf("delete parent: %d %s", s, b)
	}
	if got := e.list(t); got["lab-router"].Name == "" {
		t.Fatal("parent was deleted")
	}
	if s, b := e.do(t, http.MethodPut, "/api/v1/targets/lab-router",
		`{"addr":"10.9.0.11","checks":["icmp"]}`); s != 200 {
		t.Fatalf("edit parent: %d %s", s, b)
	}
	if got := e.list(t)["lab-nas"].Parent; got != "lab-router" {
		t.Fatalf("child parent %q", got)
	}
	if s, b := e.do(t, http.MethodPut, "/api/v1/targets/lab-router",
		`{"addr":"10.9.0.11","checks":["tcp:22"]}`); s != 422 {
		t.Fatalf("breaking a dependent: %d %s", s, b)
	}
}

func TestMonitorEditAndDeleteDownMonitor(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	if s, b := e.do(t, http.MethodPost, "/api/v1/targets", labNAS); s != 201 {
		t.Fatalf("%d %s", s, b)
	}
	failN(e.al, alertTarget("lab-nas", "icmp"), "icmp", layerLAN, 3)
	if !e.al.IsDown("lab-nas") {
		t.Fatal("setup: should be down")
	}
	if s, b := e.do(t, http.MethodPut, "/api/v1/targets/lab-nas",
		`{"addr":"10.9.0.5","checks":["icmp"]}`); s != 200 {
		t.Fatalf("edit down monitor: %d %s", s, b)
	}
	if e.al.IsDown("lab-nas") || e.list(t)["lab-nas"].State != "UNKNOWN" {
		t.Fatalf("edited monitor should start over: %+v", e.list(t)["lab-nas"])
	}
	if !slices.Equal(*e.closed, []string{"lab-nas"}) {
		t.Fatalf("incident closes %v", *e.closed)
	}
	failN(e.al, alertTarget("lab-nas", "icmp"), "icmp", layerLAN, 3)
	if s, _ := e.do(t, http.MethodDelete, "/api/v1/targets/lab-nas", ""); s != 204 {
		t.Fatalf("delete down monitor: %d", s)
	}
	if !slices.Equal(*e.closed, []string{"lab-nas"}) {
		t.Fatalf("incident closes %v", *e.closed)
	}
	snaps := e.al.Snapshots()
	for _, s := range snaps {
		if s.Name == "lab-nas" && (!s.Removed || !s.Dirty) {
			t.Fatalf("snapshot %+v", s)
		}
	}
	if a := e.app.class.target("lab-nas"); a != nil {
		t.Fatal("classifier kept the deleted monitor")
	}
}

func TestMonitorEditRestoresOldOnAddFailure(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	if s, b := e.do(t, http.MethodPost, "/api/v1/targets", labNAS); s != 201 {
		t.Fatalf("%d %s", s, b)
	}
	flaky := &flakyRegistry{targetRegistry: e.reg, mu: sync.Mutex{}, failAdd: map[string]int{"lab-nas": 1}}
	e.mgr.reg = flaky
	s, b := e.do(t, http.MethodPut, "/api/v1/targets/lab-nas", `{"addr":"10.9.0.5","checks":["icmp"]}`)
	if s != 500 {
		t.Fatalf("%d %s", s, b)
	}
	if got := e.list(t)["lab-nas"]; got.Addr != "10.9.0.1" {
		t.Fatalf("old definition not kept: %+v", got)
	}
	if e.al.Snapshots() == nil || !slices.ContainsFunc(e.al.Snapshots(), func(s alertSnapshot) bool {
		return s.Name == "lab-nas" && !s.Removed
	}) {
		t.Fatal("old monitor is not running again")
	}
	saved, err := loadDynamic(e.dir)
	if err != nil || len(saved) != 1 || saved[0].Addr != "10.9.0.1" {
		t.Fatalf("file not restored: %v %v", saved, err)
	}
}

func TestMonitorPersistFailureAppliesNothing(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.mgr.dir = notADir
	s, b := e.do(t, http.MethodPost, "/api/v1/targets", labNAS)
	if s != http.StatusInternalServerError {
		t.Fatalf("%d %s", s, b)
	}
	if _, ok := e.list(t)["lab-nas"]; ok {
		t.Fatal("monitor listed after a failed save")
	}
	if slices.ContainsFunc(e.al.Snapshots(), func(s alertSnapshot) bool { return s.Name == "lab-nas" }) {
		t.Fatal("monitor is running after a failed save")
	}
}

func TestMonitorGuardRejectsBeforeChanging(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	tests := []struct {
		name string
		opts []reqOpt
		want int
	}{
		{"form post", []reqOpt{withHeader("Content-Type", "application/x-www-form-urlencoded")}, 415},
		{"no content type", []reqOpt{withHeader("Content-Type", "")}, 415},
		{"foreign origin", []reqOpt{withHeader("Origin", "https://example.com")}, 403},
		{"null origin", []reqOpt{withHeader("Origin", "null")}, 403},
	}
	for _, tt := range tests {
		s, b := e.do(t, http.MethodPost, "/api/v1/targets", labNAS, tt.opts...)
		if s != tt.want {
			t.Errorf("%s: %d %s, want %d", tt.name, s, b, tt.want)
		}
	}
	if _, ok := e.list(t)["lab-nas"]; ok {
		t.Fatal("a rejected request changed a monitor")
	}
	same := withHeader("Origin", e.srv.URL)
	if s, b := e.do(t, http.MethodPost, "/api/v1/targets", labNAS, same); s != 201 {
		t.Fatalf("same-origin request refused: %d %s", s, b)
	}
	if s, _ := e.do(
		t,
		http.MethodDelete,
		"/api/v1/targets/lab-nas",
		"",
		withHeader("Content-Type", "text/plain"),
	); s != 415 {
		t.Fatalf("delete without JSON type: %d", s)
	}

	e.mgr.authorize = func(*http.Request) error { return errors.New("read-only viewer") }
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/v1/targets"}, {"PUT", "/api/v1/targets/lab-nas"}, {"DELETE", "/api/v1/targets/lab-nas"},
	} {
		if s, b := e.do(t, r.method, r.path, labNAS); s != 403 || !strings.Contains(b, "read-only viewer") {
			t.Errorf("%s denied write: %d %s", r.method, s, b)
		}
	}
	if _, ok := e.list(t)["lab-nas"]; !ok {
		t.Fatal("a denied delete removed the monitor")
	}
	if s, _ := e.do(t, http.MethodPost, "/api/v1/validate", labNAS); s != 200 {
		t.Fatalf("validate must not need authorization: %d", s)
	}
}

func TestMonitorWriteAuditLog(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	e.do(t, http.MethodPost, "/api/v1/targets", labNAS)
	e.do(t, http.MethodPost, "/api/v1/targets", labNAS)
	e.do(t, http.MethodDelete, "/api/v1/targets/lab-nas", "")
	out := e.logs.String()
	for _, want := range []string{
		`"op":"create"`, `"target":"lab-nas"`, `"outcome":"accepted"`, `"outcome":"refused"`, `"op":"delete"`,
		`"remote":"127.0.0.1:`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log missing %s:\n%s", want, out)
		}
	}
}

type fakePeerSource struct {
	peers  []peerInfo
	seeded bool
}

func (f *fakePeerSource) Peers() []peerInfo { return f.peers }
func (f *fakePeerSource) seededNow() bool   { return f.seeded }

func peerListFixture() []peerInfo {
	return []peerInfo{
		{
			NodeID:     1,
			StableID:   "",
			Name:       "nyc-router",
			DNSName:    "nyc-router.tailnet.ts.net",
			HostName:   "nyc-router-host",
			IPs:        []netip.Addr{netip.MustParseAddr("100.64.0.1")},
			Routes:     []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
			AllowedIPs: nil,
			Tags:       []string{"tag:router"},
			Online:     opt.NewBool(true),
		},
		{
			NodeID:     2,
			StableID:   "",
			Name:       "lab-nas",
			DNSName:    "lab-nas.tailnet.ts.net",
			HostName:   "nas",
			IPs:        []netip.Addr{netip.MustParseAddr("100.64.0.2")},
			Routes:     nil,
			AllowedIPs: nil,
			Tags:       nil,
			Online:     opt.NewBool(false),
		},
	}
}

func getPeers(t *testing.T, e *apiEnv) (int, string) {
	t.Helper()
	// A plain GET with no Content-Type, as fetch sends it.
	return e.do(t, http.MethodGet, "/api/v1/peers", "", withHeader("Content-Type", ""))
}

func TestMonitorPeerList(t *testing.T) {
	t.Parallel()
	fixture := peerListFixture()
	tests := []struct {
		name   string
		source peerSource
		seeded bool
		peers  []peerView
	}{
		{
			name:   "seeded with peers",
			source: &fakePeerSource{peers: fixture, seeded: true},
			seeded: true,
			peers: []peerView{
				{
					Name:     "lab-nas",
					DNSName:  "lab-nas.tailnet.ts.net",
					HostName: "nas",
					IPs:      []string{"100.64.0.2"},
					Tags:     []string{},
					Online:   new(false),
					Routes:   []string{},
				},
				{
					Name:     "nyc-router",
					DNSName:  "nyc-router.tailnet.ts.net",
					HostName: "nyc-router-host",
					IPs:      []string{"100.64.0.1"},
					Tags:     []string{"tag:router"},
					Online:   new(true),
					Routes:   []string{"10.20.0.0/16"},
				},
			},
		},
		{
			name: "unknown online state",
			source: &fakePeerSource{seeded: true, peers: []peerInfo{{
				NodeID:     3,
				StableID:   "",
				Name:       "new-node",
				DNSName:    "",
				HostName:   "",
				IPs:        []netip.Addr{netip.MustParseAddr("100.64.0.3")},
				Routes:     nil,
				AllowedIPs: nil,
				Tags:       nil,
				Online:     opt.Bool(""),
			}}},
			seeded: true,
			peers: []peerView{{
				Name:     "new-node",
				DNSName:  "",
				HostName: "",
				IPs:      []string{"100.64.0.3"},
				Tags:     []string{},
				Online:   nil,
				Routes:   []string{},
			}},
		},
		{
			name: "no MagicDNS name",
			source: &fakePeerSource{seeded: true, peers: []peerInfo{{
				NodeID:     4,
				StableID:   "",
				Name:       "printer",
				DNSName:    "",
				HostName:   "printer",
				IPs:        []netip.Addr{netip.MustParseAddr("100.64.0.4")},
				Routes:     nil,
				AllowedIPs: nil,
				Tags:       nil,
				Online:     opt.NewBool(true),
			}}},
			seeded: true,
			peers: []peerView{{
				Name:     "printer",
				DNSName:  "",
				HostName: "printer",
				IPs:      []string{"100.64.0.4"},
				Tags:     []string{},
				Online:   new(true),
				Routes:   []string{},
			}},
		},
		{
			name:   "unseeded",
			source: &fakePeerSource{peers: nil, seeded: false},
			seeded: false,
			peers:  []peerView{},
		},
		{
			name:   "bus down after seeding keeps stale entries hidden",
			source: &fakePeerSource{peers: fixture, seeded: false},
			seeded: false,
			peers:  []peerView{},
		},
		{
			name:   "nil source",
			source: nil,
			seeded: false,
			peers:  []peerView{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newAPIEnv(t)
			e.mgr.peers = tt.source
			status, body := getPeers(t, e)
			if status != http.StatusOK {
				t.Fatalf("status %d %s", status, body)
			}
			var got peerListView
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}
			if got.Seeded != tt.seeded {
				t.Errorf("seeded %v, want %v", got.Seeded, tt.seeded)
			}
			wantJSON, _ := json.Marshal(tt.peers)
			gotJSON, _ := json.Marshal(got.Peers)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("peers %s, want %s", gotJSON, wantJSON)
			}
			if len(tt.peers) == 0 && !strings.Contains(body, `"peers":[]`) {
				t.Errorf("peers must be [] not null: %s", body)
			}
		})
	}
}

// TestMonitorPeerListHidesUnseenPeers stands in for a device the tailnet
// policy hides from the collector: the source simply never holds it, so the
// response cannot contain it.
func TestMonitorPeerListHidesUnseenPeers(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	e.mgr.peers = &fakePeerSource{peers: peerListFixture(), seeded: true}
	status, body := getPeers(t, e)
	if status != http.StatusOK {
		t.Fatalf("status %d %s", status, body)
	}
	var got peerListView
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range got.Peers {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"lab-nas", "nyc-router"}) {
		t.Fatalf("peers %v, want only the visible ones sorted by name", names)
	}
	if strings.Contains(body, "hidden") {
		t.Fatal("a device outside the collector's view appeared")
	}
}

func TestLoopbackListen(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"127.0.0.1:9109": true, "[::1]:9109": true, "localhost:9109": true,
		"0.0.0.0:9109": false, ":9109": false, "100.64.0.1:9109": false, "bad": false,
	} {
		if got := loopbackListen(addr); got != want {
			t.Errorf("%s: %v want %v", addr, got, want)
		}
	}
}

func TestMountMonitorUI(t *testing.T) {
	t.Parallel()
	status := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "status page") })
	get := func(t *testing.T, mux *http.ServeMux, method, path string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
		return rec.Code, rec.Body.String()
	}
	t.Run("off", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		a := new(app)
		a.listen, a.log = "127.0.0.1:9109", slog.New(slog.NewJSONHandler(&logs, nil))
		mux := http.NewServeMux()
		mux.Handle("/", status)
		a.mountMonitorUI(mux)
		for _, r := range []struct{ method, path string }{
			{"GET", "/ui/"}, {"GET", "/api/v1/targets"}, {"POST", "/api/v1/targets"},
			{"GET", "/api/v1/peers"}, {"GET", "/ui/app.js"},
		} {
			if code, _ := get(t, mux, r.method, r.path); code != http.StatusNotFound {
				t.Errorf("%s %s: %d", r.method, r.path, code)
			}
		}
		if code, body := get(t, mux, "GET", "/"); code != 200 || body != "status page" {
			t.Fatalf("status page: %d %s", code, body)
		}
		if strings.Contains(logs.String(), "write API") {
			t.Fatalf("logged a write API with the UI off: %s", logs.String())
		}
	})
	t.Run("on, non-loopback", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		var logs bytes.Buffer
		a := uiApp(e, "0.0.0.0:9109", slog.New(slog.NewJSONHandler(&logs, nil)))
		mux := http.NewServeMux()
		mux.Handle("/", status)
		a.mountMonitorUI(mux)
		if code, _ := get(t, mux, "GET", "/api/v1/targets"); code != 200 {
			t.Fatalf("list: %d", code)
		}
		if code, body := get(t, mux, "GET", "/"); code != 200 || body != "status page" {
			t.Fatalf("status page: %d %s", code, body)
		}
		out := logs.String()
		if !strings.Contains(out, "write API enabled") || !strings.Contains(out, "0.0.0.0:9109") ||
			!strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "controlled only by the network") {
			t.Fatalf("logs %s", out)
		}
	})
	t.Run("on, loopback", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		var logs bytes.Buffer
		a := uiApp(e, "127.0.0.1:9109", slog.New(slog.NewJSONHandler(&logs, nil)))
		a.mountMonitorUI(http.NewServeMux())
		if strings.Contains(logs.String(), "WARN") {
			t.Fatalf("warned on loopback: %s", logs.String())
		}
	})
}

func TestUIServedWithSameOriginPolicy(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	var logs bytes.Buffer
	a := uiApp(e, "127.0.0.1:9109", slog.New(slog.NewJSONHandler(&logs, nil)))
	mux := http.NewServeMux()
	mux.Handle("/", http.NotFoundHandler())
	a.mountMonitorUI(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fetch := func(path string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, html := fetch("/ui/")
	if resp.StatusCode != http.StatusOK || !strings.Contains(html, "<title>tail-watch monitors</title>") {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	for _, path := range []string{"/ui/", "/ui/app.js", "/ui/style.css"} {
		r, _ := fetch(path)
		csp := r.Header.Get("Content-Security-Policy")
		if r.StatusCode != http.StatusOK || !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: %d csp %q", path, r.StatusCode, csp)
		}
	}
	_, js := fetch("/ui/app.js")
	_, css := fetch("/ui/style.css")
	if strings.Contains(js, "http://") || strings.Contains(js, "https://") {
		t.Error("app.js names another origin")
	}
	if strings.Contains(css, "@import") || strings.Contains(css, "url(") {
		t.Error("style.css loads another resource")
	}
	for _, m := range regexp.MustCompile(`(?:src|href)="([^"]*)"`).FindAllStringSubmatch(html, -1) {
		if strings.Contains(m[1], "//") || strings.Contains(m[1], ":") {
			t.Errorf("index.html references %q", m[1])
		}
	}
}

func uiApp(e *apiEnv, listen string, log *slog.Logger) *app {
	a := new(app)
	a.ui, a.listen, a.cfg, a.registry, a.alerts, a.log = true, listen, e.mgr.cfg, e.reg, e.al, log
	return a
}
