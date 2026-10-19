package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
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
	return newAPIEnvFrom(t, `
collector: {state_dir: /unused, ui: true}
targets:
  - {name: nyc-router, addr: 10.0.0.1, checks: [icmp]}
  - {name: nyc-nas, addr: 10.0.0.2, checks: [icmp, 'tcp:445']}
`)
}

func newAPIEnvFrom(t *testing.T, src string) *apiEnv {
	t.Helper()
	cfg, err := parseConfig([]byte(src))
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

	e.mgr.authorize = func(*http.Request, role) error { return errors.New("read-only viewer") }
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/v1/targets"}, {"PUT", "/api/v1/targets/lab-nas"}, {"DELETE", "/api/v1/targets/lab-nas"},
	} {
		if s, b := e.do(t, r.method, r.path, labNAS); s != 403 || !strings.Contains(b, "read-only viewer") {
			t.Errorf("%s denied write: %d %s", r.method, s, b)
		}
	}
	e.mgr.mu.Lock()
	kept := false
	for _, ent := range e.mgr.entries {
		if ent.Name == "lab-nas" {
			kept = true
		}
	}
	e.mgr.mu.Unlock()
	if !kept {
		t.Fatal("a denied delete removed the monitor")
	}
	s, b := e.do(t, http.MethodPost, "/api/v1/validate", labNAS)
	if s != 403 || !strings.Contains(b, "read-only viewer") {
		t.Fatalf("validate must be authorized: %d %s", s, b)
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

// openTCPPort returns a loopback port with a listener accepting and closing
// connections until the test ends.
func openTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// closedTCPPort returns a loopback port nothing listens on.
func closedTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// wantProbeResult is the expected outcome of one probed check.
type wantProbeResult struct {
	check string
	ok    bool
	err   string
}

// probeCase is one POST /api/v1/probe call and its expected answer. results
// is nil when the call must not run a check.
type probeCase struct {
	name     string
	body     string
	env      func(dialed *atomic.Bool) netEnv
	want     int
	wantBody string
	results  []wantProbeResult
}

// noDialEnv returns an env whose dial records the attempt and fails.
func noDialEnv(dialed *atomic.Bool) netEnv {
	env := new(fakeEnv)
	env.dial = func(context.Context, string, string) (net.Conn, error) {
		dialed.Store(true)
		return nil, errors.New("must not dial")
	}
	return env
}

// blockDialEnv returns an env whose dial records the attempt and blocks until
// the context is done.
func blockDialEnv(dialed *atomic.Bool) netEnv {
	env := new(fakeEnv)
	env.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialed.Store(true)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return env
}

func TestMonitorProbe(t *testing.T) {
	t.Parallel()
	http404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(http404.Close)
	httpPort := http404.Listener.Addr().(*net.TCPAddr).Port
	up1, up2 := openTCPPort(t), openTCPPort(t)
	dead := closedTCPPort(t)

	tests := []probeCase{
		{
			name: "all pass",
			body: fmt.Sprintf(
				`{"name":"lab-probe","addr":"127.0.0.1","checks":["tcp:%d","tcp:%d"]}`, up1, up2,
			),
			env:      nil,
			want:     http.StatusOK,
			wantBody: "",
			results: []wantProbeResult{
				{check: fmt.Sprintf("tcp:%d", up1), ok: true, err: ""},
				{check: fmt.Sprintf("tcp:%d", up2), ok: true, err: ""},
			},
		},
		{
			name:     "connection refused",
			body:     fmt.Sprintf(`{"name":"lab-probe","addr":"127.0.0.1","checks":["tcp:%d"]}`, dead),
			env:      nil,
			want:     http.StatusOK,
			wantBody: "",
			results: []wantProbeResult{
				{check: fmt.Sprintf("tcp:%d", dead), ok: false, err: "connection refused"},
			},
		},
		{
			name: "non-200 HTTP status",
			body: fmt.Sprintf(
				`{"name":"lab-http","addr":"127.0.0.1","checks":["http:http://127.0.0.1:%d/"]}`, httpPort,
			),
			env:      nil,
			want:     http.StatusOK,
			wantBody: "",
			results: []wantProbeResult{
				{check: fmt.Sprintf("http:http://127.0.0.1:%d/", httpPort), ok: false, err: "HTTP status 404"},
			},
		},
		{
			name:     "invalid definition runs no check",
			body:     `{"name":"lab-bad","addr":"10.9.0.2","checks":["tcp:70000"]}`,
			env:      noDialEnv,
			want:     http.StatusUnprocessableEntity,
			wantBody: "checks[0]",
			results:  nil,
		},
		{
			name:     "yaml name runs no check",
			body:     `{"name":"nyc-nas","addr":"10.9.0.9","checks":["tcp:65000"]}`,
			env:      noDialEnv,
			want:     http.StatusConflict,
			wantBody: "config file",
			results:  nil,
		},
		{
			name:     "slow check is bounded",
			body:     `{"name":"lab-slow","addr":"10.9.0.3","checks":["tcp:65000"],"interval":"15s","timeout":"200ms"}`,
			env:      blockDialEnv,
			want:     http.StatusOK,
			wantBody: "",
			results: []wantProbeResult{
				{check: "tcp:65000", ok: false, err: "timeout: "},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runProbeCase(t, tt)
		})
	}
}

func runProbeCase(t *testing.T, tt probeCase) {
	t.Helper()
	e := newAPIEnv(t)
	var dialed atomic.Bool
	if tt.env != nil {
		e.mgr.env = tt.env(&dialed)
	}
	status, body := e.do(t, http.MethodPost, "/api/v1/probe", tt.body)
	if status != tt.want || !strings.Contains(body, tt.wantBody) {
		t.Fatalf("got %d %s, want %d containing %q", status, body, tt.want, tt.wantBody)
	}
	if tt.results == nil {
		if dialed.Load() {
			t.Fatal("a check ran")
		}
	} else {
		checkProbeResults(t, body, tt.results)
	}
	if got := e.list(t); len(got) != 2 {
		t.Fatalf("probe changed the list: %v", got)
	}
}

func checkProbeResults(t *testing.T, body string, want []wantProbeResult) {
	t.Helper()
	var resp probeView
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != len(want) {
		t.Fatalf("results %+v, want %d", resp.Results, len(want))
	}
	for i, w := range want {
		got := resp.Results[i]
		if got.Check != w.check || got.OK != w.ok {
			t.Errorf("results[%d] = %+v, want check %q ok %v", i, got, w.check, w.ok)
		}
		if w.err == "" && got.Error != "" {
			t.Errorf("results[%d] error %q, want none", i, got.Error)
		}
		if w.err != "" && !strings.Contains(got.Error, w.err) {
			t.Errorf("results[%d] error %q, want %q", i, got.Error, w.err)
		}
	}
}

func TestMonitorProbeGuard(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	var dialed atomic.Bool
	e.mgr.env = noDialEnv(&dialed)
	body := `{"name":"lab-probe","addr":"10.9.0.4","checks":["tcp:65000"]}`
	if s, b := e.do(
		t, http.MethodPost, "/api/v1/probe", body, withHeader("Content-Type", "text/plain"),
	); s != http.StatusUnsupportedMediaType {
		t.Errorf("non-JSON content type: %d %s", s, b)
	}
	if s, b := e.do(
		t, http.MethodPost, "/api/v1/probe", body, withHeader("Origin", "https://example.com"),
	); s != http.StatusForbidden {
		t.Errorf("foreign origin: %d %s", s, b)
	}
	e.mgr.authorize = func(*http.Request, role) error { return errors.New("read-only viewer") }
	if s, b := e.do(t, http.MethodPost, "/api/v1/probe", body); s != http.StatusForbidden ||
		!strings.Contains(b, "read-only viewer") {
		t.Errorf("denied probe: %d %s", s, b)
	}
	if dialed.Load() {
		t.Error("a refused probe ran a check")
	}
	e.mgr.authorize = func(*http.Request, role) error { return nil }
	if s, b := e.do(t, http.MethodPost, "/api/v1/probe", body); s != http.StatusOK {
		t.Fatalf("probe: %d %s", s, b)
	}
	if got := e.list(t); len(got) != 2 {
		t.Fatalf("probe changed the list: %v", got)
	}
	out := e.logs.String()
	for _, want := range []string{
		`"op":"probe"`, `"target":"lab-probe"`, `"outcome":"accepted"`, `"outcome":"refused"`, `"remote":"127.0.0.1:`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log missing %s:\n%s", want, out)
		}
	}
}

// TestMonitorProbeDoesNotHoldLock proves the probe runs its checks after the
// manager's mutex is released: a probe whose check never answers must not
// stall a concurrent create.
func TestMonitorProbeDoesNotHoldLock(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	env := new(fakeEnv)
	env.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil, errors.New("released")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e.mgr.env = env
	type answer struct {
		status int
		err    error
	}
	probeDone := make(chan answer, 1)
	go func() {
		s, err := postStatus(t, e.srv.URL+"/api/v1/probe",
			`{"name":"lab-slow","addr":"10.9.0.3","checks":["tcp:65000"],"interval":"30s","timeout":"25s"}`)
		probeDone <- answer{status: s, err: err}
	}()
	<-started
	createDone := make(chan answer, 1)
	go func() {
		s, err := postStatus(t, e.srv.URL+"/api/v1/targets", labNAS)
		createDone <- answer{status: s, err: err}
	}()
	select {
	case a := <-createDone:
		if a.err != nil || a.status != http.StatusCreated {
			t.Fatalf("create during probe: %d %v", a.status, a.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("create blocked behind a running probe")
	}
	close(release)
	if a := <-probeDone; a.err != nil || a.status != http.StatusOK {
		t.Fatalf("probe: %d %v", a.status, a.err)
	}
}

// postStatus POSTs body as JSON and reports the status. Unlike apiEnv.do it
// returns errors, so it is safe to call from a helper goroutine.
func postStatus(t *testing.T, url, body string) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
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
			{"POST", "/api/v1/probe"}, {"GET", "/api/v1/peers"}, {"GET", "/ui/app.js"},
			{"GET", "/api/v1/routes"}, {"POST", "/api/v1/routes"},
			{"PUT", "/api/v1/routes/db-team"}, {"DELETE", "/api/v1/routes/db-team"},
			{"GET", "/api/v1/sinks"}, {"POST", "/api/v1/route-preview"},
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
	a.env, a.clock = e.mgr.env, tstime.StdClock{}
	return a
}

// A probed tsping check must resolve through the app's shared resolver and
// report a timeout with the prober's "timeout: " prefix, which only runCheck
// adds for tsping.
func TestMonitorProbeTSPingMatchesProber(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	a := uiApp(e, "127.0.0.1:9109", slog.New(slog.DiscardHandler))
	var fetched atomic.Bool
	a.resolver = newTSResolver(nil, nil, func(ctx context.Context) (*ipnstate.Status, error) {
		fetched.Store(true)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	mux := http.NewServeMux()
	a.mountMonitorUI(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body := `{"name":"lab-ts","addr":"lab-ts.invalid","checks":["tsping"],"interval":"15s","timeout":"200ms"}`
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, srv.URL+"/api/v1/probe", strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe: %d %s", resp.StatusCode, b)
	}
	checkProbeResults(t, string(b), []wantProbeResult{{check: "tsping", ok: false, err: "timeout: "}})
	if !fetched.Load() {
		t.Error("the probe did not use the app's resolver")
	}
}

func (e *apiEnv) useIdentity(who *apitype.WhoIsResponse) {
	e.mgr.authz = roleResolver(ident(who, nil), "", true)
	e.mgr.authorize = e.mgr.authorizeRequest
}

//nolint:gocognit // Each subtest is one role against the route table.
func TestAuthorizationByRole(t *testing.T) {
	t.Parallel()
	reads := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/targets"},
		{http.MethodGet, "/api/v1/peers"},
		{http.MethodGet, "/api/v1/routes"},
		{http.MethodGet, "/api/v1/sinks"},
	}
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/targets", labNAS},
		{http.MethodPut, "/api/v1/targets/lab-nas", labNAS},
		{http.MethodDelete, "/api/v1/targets/lab-nas", ""},
		{http.MethodPost, "/api/v1/validate", labNAS},
		{http.MethodPost, "/api/v1/probe", labNAS},
		{http.MethodPost, "/api/v1/routes", `{"name":"db-team","sinks":["hook"]}`},
		{http.MethodPut, "/api/v1/routes/db-team", `{"name":"db-team","sinks":["hook"]}`},
		{http.MethodDelete, "/api/v1/routes/db-team", ""},
		{http.MethodPost, "/api/v1/route-preview", `{"name":"x","addr":"10.0.0.1","checks":["icmp"]}`},
	}
	as := func(t *testing.T, e *apiEnv, method, path, body string, opts ...reqOpt) (int, string) {
		t.Helper()
		opts = append(opts, withHeader("X-Forwarded-For", "100.64.0.7"))
		return e.do(t, method, path, body, opts...)
	}
	t.Run("viewer", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		e.useIdentity(whoisCaller("alice@example.com", "phone", nil, `{"role":"viewer"}`))
		for _, r := range reads {
			if s, b := as(t, e, r.method, r.path, ""); s != http.StatusOK {
				t.Errorf("%s %s: %d %s", r.method, r.path, s, b)
			}
		}
		for _, r := range writes {
			s, b := as(t, e, r.method, r.path, r.body)
			if s != http.StatusForbidden || !strings.Contains(b, "role editor is required") {
				t.Errorf("%s %s: %d %s", r.method, r.path, s, b)
			}
		}
		e.mgr.mu.Lock()
		created := len(e.mgr.entries)
		e.mgr.mu.Unlock()
		if created != 0 {
			t.Fatal("a viewer created a monitor")
		}
		s, b := as(t, e, http.MethodPost, "/api/v1/targets", labNAS, withHeader("Content-Type", "text/plain"))
		if s != http.StatusUnsupportedMediaType {
			t.Fatalf("content type before the role check: %d %s", s, b)
		}
		s, b = as(t, e, http.MethodPost, "/api/v1/targets", labNAS, withHeader("Origin", "https://example.com"))
		if s != http.StatusForbidden || !strings.Contains(b, "cross-origin") || strings.Contains(b, "role editor") {
			t.Fatalf("origin before the role check: %d %s", s, b)
		}
	})
	t.Run("editor", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		e.useIdentity(whoisCaller("alice@example.com", "phone", nil, `{"role":"editor"}`))
		for _, r := range reads {
			if s, b := as(t, e, r.method, r.path, ""); s != http.StatusOK {
				t.Errorf("%s %s: %d %s", r.method, r.path, s, b)
			}
		}
		for _, r := range writes {
			if s, b := as(t, e, r.method, r.path, r.body); s == http.StatusForbidden {
				t.Errorf("%s %s forbidden: %s", r.method, r.path, b)
			}
		}
	})
	t.Run("no role", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		e.useIdentity(whoisCaller("alice@example.com", "phone", nil, `{"role":"owner"}`))
		for _, r := range append(reads, []struct{ method, path string }{
			{http.MethodPost, "/api/v1/targets"},
		}...) {
			body := ""
			if r.method != http.MethodGet {
				body = labNAS
			}
			s, b := as(t, e, r.method, r.path, body)
			if s != http.StatusForbidden || !strings.Contains(b, "no role was granted") {
				t.Errorf("%s %s: %d %s", r.method, r.path, s, b)
			}
		}
		for _, r := range writes {
			s, b := as(t, e, r.method, r.path, r.body)
			if s != http.StatusForbidden || !strings.Contains(b, "no role was granted") {
				t.Errorf("%s %s: %d %s", r.method, r.path, s, b)
			}
		}
	})
	t.Run("off", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		for _, r := range reads {
			if s, b := e.do(t, r.method, r.path, ""); s != http.StatusOK {
				t.Errorf("%s %s: %d %s", r.method, r.path, s, b)
			}
		}
		for _, r := range writes {
			if s, b := e.do(t, r.method, r.path, r.body); s == http.StatusForbidden {
				t.Errorf("%s %s forbidden while authorization is off: %s", r.method, r.path, b)
			}
		}
	})
}

//nolint:gocognit // Each subtest is one caller of GET /api/v1/me.
func TestMe(t *testing.T) {
	t.Parallel()
	t.Run("editor", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		e.useIdentity(whoisCaller("alice@example.com", "phone", nil, `{"role":"editor"}`))
		status, body := e.do(t, http.MethodGet, "/api/v1/me", "",
			withHeader("X-Forwarded-For", "100.64.0.7"),
			withHeader("Tailscale-User-Login", "forged@example.com"),
			withHeader("Tailscale-App-Capabilities", `{"example.com/cap/tail-watch":[{"role":"viewer"}]}`),
		)
		if status != http.StatusOK {
			t.Fatalf("me %d %s", status, body)
		}
		var got meView
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Authorization || got.Role != "editor" || got.User != "alice@example.com" || got.Node != "phone" ||
			!got.Identified {
			t.Fatalf("me %+v", got)
		}
		if strings.Contains(body, "forged@example.com") || strings.Contains(body, "Tailscale-App-Capabilities") {
			t.Fatalf("response includes a header value: %s", body)
		}
	})
	t.Run("authorization off", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		status, body := e.do(t, http.MethodGet, "/api/v1/me", "",
			withHeader("Tailscale-User-Login", "forged@example.com"),
		)
		if status != http.StatusOK || strings.Contains(body, "forged@example.com") {
			t.Fatalf("me %d %s", status, body)
		}
		var got meView
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if got.Authorization || got.Role != "editor" || got.User != "" {
			t.Fatalf("me %+v", got)
		}
	})
	t.Run("no role", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		e.useIdentity(whoisCaller("alice@example.com", "phone", nil, `{"role":"owner"}`))
		status, body := e.do(t, http.MethodGet, "/api/v1/me", "", withHeader("X-Forwarded-For", "100.64.0.7"))
		if status != http.StatusOK {
			t.Fatalf("me %d %s", status, body)
		}
		var got meView
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Authorization || got.Role != "" || got.User != "alice@example.com" || !got.Identified {
			t.Fatalf("me %+v", got)
		}
	})
	t.Run("unidentified", func(t *testing.T) {
		t.Parallel()
		e := newAPIEnv(t)
		e.useIdentity(whoisCaller("alice@example.com", "phone", nil, `{"role":"editor"}`))
		status, body := e.do(t, http.MethodGet, "/api/v1/me", "")
		if status != http.StatusOK {
			t.Fatalf("me %d %s", status, body)
		}
		var got meView
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Authorization || got.Role != "" || got.User != "" || got.Node != "" || got.Identified {
			t.Fatalf("me %+v", got)
		}
		if !strings.Contains(e.logs.String(), "ui caller unresolved") {
			t.Fatalf("unresolved caller was not logged: %s", e.logs.String())
		}
	})
}

func TestViewerWriteIsAudited(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	e.useIdentity(whoisCaller("alice@example.com", "phone", nil, `{"role":"viewer"}`))
	status, body := e.do(t, http.MethodPost, "/api/v1/targets", labNAS, withHeader("X-Forwarded-For", "100.64.0.7"))
	if status != http.StatusForbidden {
		t.Fatalf("write %d %s", status, body)
	}
	out := e.logs.String()
	for _, want := range []string{
		`"msg":"monitor write"`, `"outcome":"refused"`, `"user":"alice@example.com"`,
		`"node":"phone"`, `"role":"viewer"`, `"remote":"127.0.0.1:`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit line missing %s:\n%s", want, out)
		}
	}
}
