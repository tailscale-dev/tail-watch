package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dynamicYAMLConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := parseConfig([]byte(`
collector: {state_dir: /tmp/x, ui: true}
defaults: {interval: 20s, timeout: 4s}
targets:
  - {name: nyc-router, addr: 10.0.0.1, checks: [icmp]}
  - {name: nyc-nas, addr: 10.0.0.2, checks: [icmp, 'tcp:445']}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDynamicStoreRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	threshold := 5
	in := []dynamicTarget{
		dynWith(dynT("lab-nas", "10.9.0.1", "icmp", "tcp:445"), func(d *dynamicTarget) {
			d.Interval, d.FailThreshold = "15s", &threshold
		}),
		dynWith(dynT("lab-cam", "10.9.0.2", "icmp"), func(d *dynamicTarget) { d.Site, d.Severity = "lab", "warn" }),
	}
	if err := saveDynamic(dir, in); err != nil {
		t.Fatal(err)
	}
	got, err := loadDynamic(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(in)
	b, _ := json.Marshal(got)
	if !bytes.Equal(a, b) {
		t.Fatalf("round trip\nwant %s\n got %s", a, b)
	}
	info, err := os.Stat(filepath.Join(dir, dynamicFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != fs.FileMode(0o600) {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temp file left behind: %v %v", entries, err)
	}
}

func TestDynamicStoreMissingAndCorrupt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := loadDynamic(dir)
	if err != nil || len(got) != 0 {
		t.Fatalf("missing file: %v %v", got, err)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, dynamicFileName), []byte("{not json"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, loadErr := loadDynamic(dir); loadErr == nil || !strings.Contains(loadErr.Error(), dynamicFileName) {
		t.Fatalf("corrupt file error %v", loadErr)
	}
}

func TestMergeDynamicSkipsClashAndInvalid(t *testing.T) {
	t.Parallel()
	cfg := dynamicYAMLConfig(t)
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	entries := []dynamicTarget{
		dynWith(dynT("lab-nas", "10.9.0.1", "icmp"), func(d *dynamicTarget) { d.Parent = "nyc-router" }),
		dynT("nyc-nas", "10.9.0.9", "icmp"),
		dynT("lab-bad", "10.9.0.3", "tcp:70000"),
	}
	added := cfg.mergeDynamic(entries, log)
	if len(added) != 1 || added[0].Name != "lab-nas" {
		t.Fatalf("added %+v", added)
	}
	if len(cfg.Targets) != 3 {
		t.Fatalf("targets %d", len(cfg.Targets))
	}
	merged := cfg.Targets[2]
	if !merged.Dynamic || merged.Interval.String() != "20s" || merged.Parent != "nyc-router" {
		t.Fatalf("merged %+v", merged)
	}
	out := logs.String()
	for _, want := range []string{`"target":"nyc-nas"`, `"target":"lab-bad"`, "tcp:70000"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %s:\n%s", want, out)
		}
	}
	if len(cfg.RawTargets) != 2 {
		t.Fatalf("YAML raw targets changed: %d", len(cfg.RawTargets))
	}
}

func TestDynamicNamesAreConfiguredForRestore(t *testing.T) {
	t.Parallel()
	cfg := dynamicYAMLConfig(t)
	cfg.mergeDynamic([]dynamicTarget{dynT("lab-nas", "10.9.0.1", "icmp")}, nil)
	ptrs := make([]*Target, len(cfg.Targets))
	for i := range cfg.Targets {
		ptrs[i] = &cfg.Targets[i]
	}
	names := targetNames(ptrs)
	if !names["lab-nas"] || !names["nyc-nas"] {
		t.Fatalf("names %v", names)
	}
}

func TestUIOffWarnsAndLeavesStoreAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := saveDynamic(dir, []dynamicTarget{
		dynT("a", "10.0.0.1", "icmp"),
		dynT("b", "10.0.0.2", "icmp"),
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, dynamicFileName)
	before, _ := os.ReadFile(path)
	stat, _ := os.Stat(path)
	var logs bytes.Buffer
	a := appWithState(dir, slog.New(slog.NewJSONHandler(&logs, nil)), false)
	if err := a.loadDynamicTargets(false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"count":2`) || !strings.Contains(logs.String(), "UI is off") {
		t.Fatalf("log %s", logs.String())
	}
	if len(a.dynamic) != 0 {
		t.Fatal("dynamic monitors loaded with the UI off")
	}
	after, _ := os.ReadFile(path)
	stat2, _ := os.Stat(path)
	if !bytes.Equal(before, after) || !stat.ModTime().Equal(stat2.ModTime()) {
		t.Fatal("targets.json was modified")
	}
}

func TestCorruptStoreStopsStartupWhenUIOn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, dynamicFileName), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := appWithState(dir, slog.New(slog.DiscardHandler), true)
	if err := a.loadDynamicTargets(false); err == nil || !strings.Contains(err.Error(), dynamicFileName) {
		t.Fatalf("err %v", err)
	}
}
