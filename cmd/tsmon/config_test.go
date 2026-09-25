package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseDesignExample(t *testing.T) {
	const src = `
collector:
  listen: 127.0.0.1:9109
  state_dir: /var/lib/tsmon
defaults:
  interval: 30s
  timeout: 5s
  fail_threshold: 3
  recover_threshold: 2
  flap_limit: 4
  flap_window: 15m
targets:
  - name: nyc-core-switch
    addr: 10.20.0.1
    network: tailnet
    checks: [icmp]
  - name: nyc-nas
    addr: 10.20.0.50
    checks: [icmp, tcp:445, "http:https://10.20.0.50:5001/|DSM"]
    site: nyc
  - name: nyc-printer
    addr: 10.20.0.80
    checks: [icmp]
    severity: warn
  - name: nyc-subnet-router
    addr: nyc-router
    checks: [tsping]
    interval: 15s
`
	cfg, err := parseConfig([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collector.Listen != "127.0.0.1:9109" || cfg.Collector.StateDir != "/var/lib/tsmon" {
		t.Fatalf("collector: %+v", cfg.Collector)
	}
	if got := cfg.ListenAddr(""); got != "127.0.0.1:9109" {
		t.Fatalf("listen %s", got)
	}
	nas := cfg.Targets[1]
	if nas.Site != "nyc" || nas.Interval != 30*time.Second || nas.Timeout != 5*time.Second {
		t.Fatalf("nas timings: %+v", nas)
	}
	if nas.FailThreshold != 3 || nas.RecoverThreshold != 2 || nas.FlapLimit != 4 || nas.FlapWindow != 15*time.Minute {
		t.Fatalf("nas defaults: %+v", nas)
	}
	if formatChecks(nas.Checks) != "icmp,tcp:445,http:https://10.20.0.50:5001/|DSM" {
		t.Fatalf("checks %s", formatChecks(nas.Checks))
	}
	httpChk := nas.Checks[2].(httpCheck)
	if httpChk.url != "https://10.20.0.50:5001/" || httpChk.text != "DSM" || !httpChk.match {
		t.Fatalf("http check %+v", httpChk)
	}
	if cfg.Targets[2].Severity != SeverityWarn {
		t.Fatalf("severity %s", cfg.Targets[2].Severity)
	}
	if cfg.Targets[3].Interval != 15*time.Second || cfg.Targets[0].Interval != 30*time.Second {
		t.Fatal("per-target interval was not applied")
	}
	if cfg.Targets[0].Network != NetworkTailnet || cfg.Targets[1].Network != NetworkTailnet {
		t.Fatal("network default")
	}
}

func TestDefaultsResolution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		src      string
		interval time.Duration
		timeout  time.Duration
		fail     int
		window   time.Duration
	}{
		{
			name:     "no defaults",
			src:      "targets: [{name: a, addr: 10.0.0.1, checks: [icmp]}]",
			interval: 30 * time.Second, timeout: 5 * time.Second, fail: 3, window: 15 * time.Minute,
		},
		{
			name:     "partial defaults",
			src:      "defaults: {interval: 1m}\ntargets: [{name: a, addr: 10.0.0.1, checks: [icmp]}]",
			interval: time.Minute, timeout: 5 * time.Second, fail: 3, window: 15 * time.Minute,
		},
		{
			name:     "per-target override",
			src:      "defaults: {interval: 30s}\ntargets: [{name: nyc-subnet-router, addr: nyc-router, checks: [tsping], interval: 15s}]",
			interval: 15 * time.Second, timeout: 5 * time.Second, fail: 3, window: 15 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseConfig([]byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.Targets[0]
			if got.Interval != tt.interval || got.Timeout != tt.timeout || got.FailThreshold != tt.fail || got.FlapWindow != tt.window {
				t.Fatalf("got interval=%s timeout=%s fail=%d window=%s", got.Interval, got.Timeout, got.FailThreshold, got.FlapWindow)
			}
			if got.RecoverThreshold != 2 || got.FlapLimit != 4 || got.Severity != SeverityPage {
				t.Fatalf("defaults: %+v", got)
			}
		})
	}
}

func TestCheckParsing(t *testing.T) {
	t.Parallel()
	ok := []string{"icmp", "tcp:1", "tcp:65535", "tsping", "http:https://10.0.0.1/|DSM", "http:http://host.example/health"}
	for _, s := range ok {
		if _, err := parseCheck(s); err != nil {
			t.Errorf("parse %q: %v", s, err)
		}
	}
	bad := []struct {
		src  string
		want string
	}{
		{"tcp:70000", "invalid TCP port"},
		{"tcp:0", "invalid TCP port"},
		{"tcp:abc", "invalid TCP port"},
		{"http:not a url", "invalid HTTP URL"},
		{"tls:10.20.0.50:443", "tls checks are not supported in this version"},
		{"snmp", "unknown check type"},
	}
	for _, tt := range bad {
		_, err := parseCheck(tt.src)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("parse %q err=%v want %q", tt.src, err, tt.want)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		src   string
		wants []string
	}{
		{
			name: "duplicate and unknown key",
			src: `
colector: {listen: 127.0.0.1:1}
targets:
  - {name: a, addr: 10.0.0.1, checks: [icmp]}
  - {name: a, addr: 10.0.0.2, checks: [icmp]}
`,
			wants: []string{"colector", "duplicate target name \"a\""},
		},
		{
			name:  "missing addr",
			src:   "targets: [{name: nyc-nas, checks: [icmp]}]",
			wants: []string{"nyc-nas", "addr is required"},
		},
		{
			name:  "bad name",
			src:   "targets: [{name: 'has space', addr: 10.0.0.1, checks: [icmp]}]",
			wants: []string{"has space", "[A-Za-z0-9._-]"},
		},
		{
			name:  "bad port names target",
			src:   "targets: [{name: nyc-nas, addr: 10.0.0.1, checks: ['tcp:70000']}]",
			wants: []string{"targets[0].checks[0]", "nyc-nas", "tcp:70000"},
		},
		{
			name:  "tls phase 2",
			src:   "targets: [{name: nas, addr: 10.20.0.50, checks: ['tls:10.20.0.50:443']}]",
			wants: []string{"tls checks are not supported in this version"},
		},
		{
			name:  "timeout not below interval",
			src:   "targets: [{name: slow, addr: 10.0.0.1, interval: 5s, timeout: 5s, checks: [icmp]}]",
			wants: []string{"targets[0].timeout", "slow"},
		},
		{
			name:  "threshold",
			src:   "targets: [{name: a, addr: 10.0.0.1, fail_threshold: 0, checks: [icmp]}]",
			wants: []string{"fail_threshold", "a"},
		},
		{
			name: "parent cycle",
			src: `
targets:
  - {name: a, addr: 10.0.0.1, parent: b, checks: [icmp]}
  - {name: b, addr: 10.0.0.2, parent: a, checks: [tsping]}
`,
			wants: []string{"parent cycle", "a -> b -> a"},
		},
		{
			name:  "missing parent",
			src:   "targets: [{name: a, addr: 10.0.0.1, parent: missing, checks: [icmp]}]",
			wants: []string{"parent \"missing\"", "does not name a target"},
		},
		{
			name: "parent has a service check",
			src: `
targets:
  - {name: kid, addr: 10.0.0.2, parent: dad, checks: [icmp]}
  - {name: dad, addr: 10.0.0.1, checks: [icmp, tcp:22]}
`,
			wants: []string{"parent \"dad\"", "exactly one icmp or tsping"},
		},
		{
			name:  "local tsping",
			src:   "targets: [{name: lan, addr: 192.168.1.1, network: local, checks: [tsping]}]",
			wants: []string{"targets[0].checks[0]", "lan", "tsping is not valid for network local"},
		},
		{
			name:  "bad network",
			src:   "targets: [{name: a, addr: 10.0.0.1, network: wan, checks: [icmp]}]",
			wants: []string{"network \"wan\"", "tailnet or local"},
		},
		{
			name:  "http host mismatch",
			src:   "targets: [{name: nas, addr: 10.20.0.50, checks: ['http:https://10.20.0.51/']}]",
			wants: []string{"nas", "10.20.0.51", "10.20.0.50"},
		},
		{
			name:  "bad severity",
			src:   "targets: [{name: a, addr: 10.0.0.1, severity: critical, checks: [icmp]}]",
			wants: []string{"severity \"critical\""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfig([]byte(tt.src))
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range tt.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

func TestHTTPHostEquivalence(t *testing.T) {
	t.Parallel()
	src := "targets: [{name: nas, addr: NAS.Example, checks: ['http:https://nas.example./']}]\n"
	if _, err := parseConfig([]byte(src)); err != nil {
		t.Fatal(err)
	}
}

func TestConfigPathAndMissingFile(t *testing.T) {
	t.Setenv("TSMON_CONFIG", "/a.yaml")
	if got := resolveConfigPath("/b.yaml", os.Getenv("TSMON_CONFIG")); got != "/b.yaml" {
		t.Fatalf("flag got %s", got)
	}
	if got := resolveConfigPath("", os.Getenv("TSMON_CONFIG")); got != "/a.yaml" {
		t.Fatalf("env got %s", got)
	}
	t.Setenv("TSMON_CONFIG", "")
	if got := resolveConfigPath("", os.Getenv("TSMON_CONFIG")); got != defaultConfigPath {
		t.Fatalf("default got %s", got)
	}
	path := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err %v", err)
	}
}

func TestExampleConfig(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "examples", "tsmon.yaml")
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	nets := map[Network]bool{}
	for _, target := range cfg.Targets {
		nets[target.Network] = true
		for _, chk := range target.Checks {
			kinds[chk.Kind()] = true
		}
	}
	for _, kind := range []string{"icmp", "tcp", "http", "tsping"} {
		if !kinds[kind] {
			t.Errorf("example missing %s", kind)
		}
	}
	if !nets[NetworkTailnet] || !nets[NetworkLocal] {
		t.Fatalf("networks %v", nets)
	}
}

func TestListenFlagOverride(t *testing.T) {
	cfg := &Config{Collector: CollectorConfig{Listen: "127.0.0.1:1"}}
	if got := cfg.ListenAddr("127.0.0.1:2"); got != "127.0.0.1:2" {
		t.Fatal(got)
	}
	if got := (&Config{}).ListenAddr(""); got != defaultListen {
		t.Fatal(got)
	}
}
