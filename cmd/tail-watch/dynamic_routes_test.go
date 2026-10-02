package main

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func routeFileConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := parseConfig([]byte(`
sinks:
  - {name: hook, type: webhook, url_env: U}
routes:
  - {name: yaml-team, sinks: [hook]}
targets:
  - {name: nyc, addr: 10.0.0.1, checks: [icmp]}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRoutesFileRoundTripAndMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := []dynamicRoute{
		{Name: "db-team", Severity: "", Sinks: []string{"hook"}, Mention: ""},
		{Name: "cams", Severity: "warn", Sinks: []string{"hook"}, Mention: "<!subteam^S0>"},
	}
	if err := saveRoutes(dir, in); err != nil {
		t.Fatal(err)
	}
	got, err := loadRoutes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "db-team" || got[0].Severity != "" || got[1].Mention != "<!subteam^S0>" {
		t.Fatalf("%+v", got)
	}
	info, err := os.Stat(filepath.Join(dir, routesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != fs.FileMode(0o600) {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temp file left behind: %v %v", entries, err)
	}
	if _, loadErr := loadRoutes(t.TempDir()); loadErr != nil {
		t.Fatal(loadErr)
	}
}

func TestRoutesLenientUnknownField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := []byte("[{\"name\":\"db-team\",\"sinks\":[\"hook\"],\"extra\":1}]\n")
	if err := os.WriteFile(filepath.Join(dir, routesFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadRoutes(dir)
	if err != nil || len(got) != 1 || got[0].Name != "db-team" {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestMergeRoutesSkipsClashAndInvalid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := []byte(`[
  {"name":"bad name","sinks":["hook"]},
  {"name":"ok","sinks":["hook"]},
  {"name":"yaml-team","sinks":["hook"]},
  {"name":"empty","sinks":[]}
]
`)
	path := filepath.Join(dir, routesFileName)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	entries, err := loadRoutes(dir)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cfg := routeFileConfig(t)
	cfg.mergeRoutes(entries, slog.New(slog.NewJSONHandler(&logs, nil)))
	out := logs.String()
	for _, want := range []string{"bad name", "yaml-team", "empty"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %s:\n%s", want, out)
		}
	}
	if len(cfg.Routes) != 2 || cfg.Routes[0].Name != "yaml-team" || cfg.Routes[0].Source != sourceConfig {
		t.Fatalf("routes %+v", cfg.Routes)
	}
	ok := cfg.Routes[1]
	if ok.Name != "ok" || ok.Source != sourceUI || ok.Severity != SeverityPage {
		t.Fatalf("ui route %+v", ok)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("routes.json was rewritten")
	}
}

func TestMergeRoutesKeepsMissingSink(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	cfg := routeFileConfig(t)
	cfg.mergeRoutes([]dynamicRoute{{
		Name: "cams", Severity: "warn", Sinks: []string{"gone", "hook"}, Mention: "",
	}}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if len(cfg.Routes) != 2 {
		t.Fatalf("routes %+v", cfg.Routes)
	}
	got := cfg.Routes[1]
	if got.Source != sourceUI || len(got.Indexes) != 1 || cfg.Sinks[got.Indexes[0]].Name != "hook" {
		t.Fatalf("%+v", got)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "gone" || !strings.Contains(logs.String(), "gone") {
		t.Fatalf("missing %+v log %s", got.Missing, logs.String())
	}
}

func TestCorruptRoutesStopStartupWhenUIOn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, routesFileName), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := appWithState(dir, slog.New(slog.DiscardHandler), true)
	err := a.loadRouteFile(false)
	if err == nil || !strings.Contains(err.Error(), routesFileName) {
		t.Fatalf("err %v", err)
	}
}

func TestUIOffLeavesRoutesFileAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, routesFileName)
	body := []byte("nope")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	stat, _ := os.Stat(path)
	var logs bytes.Buffer
	a := appWithState(dir, slog.New(slog.NewJSONHandler(&logs, nil)), false)
	if err := a.loadRouteFile(false); err != nil {
		t.Fatal(err)
	}
	if a.uiRoutes != nil {
		t.Fatal("routes loaded with the UI off")
	}
	if !strings.Contains(logs.String(), "UI is off") {
		t.Fatalf("log %s", logs.String())
	}
	after, _ := os.ReadFile(path)
	stat2, _ := os.Stat(path)
	if !bytes.Equal(before, after) || !stat.ModTime().Equal(stat2.ModTime()) {
		t.Fatal("routes.json was modified")
	}
}
