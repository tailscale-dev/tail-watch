package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseDesignExample(t *testing.T) {
	t.Parallel()
	const src = `
collector:
  listen: 127.0.0.1:9109
  state_dir: /var/lib/tail-watch
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
	if cfg.Collector.Listen != "127.0.0.1:9109" || cfg.Collector.StateDir != "/var/lib/tail-watch" {
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
			interval: 15 * time.Second,
			timeout:  5 * time.Second,
			fail:     3,
			window:   15 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseConfig([]byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.Targets[0]
			if got.Interval != tt.interval || got.Timeout != tt.timeout || got.FailThreshold != tt.fail ||
				got.FlapWindow != tt.window {
				t.Fatalf(
					"got interval=%s timeout=%s fail=%d window=%s",
					got.Interval,
					got.Timeout,
					got.FailThreshold,
					got.FlapWindow,
				)
			}
			if got.RecoverThreshold != 2 || got.FlapLimit != 4 || got.Severity != SeverityPage {
				t.Fatalf("defaults: %+v", got)
			}
		})
	}
}

func TestCheckParsing(t *testing.T) {
	t.Parallel()
	ok := []string{
		"icmp",
		"tcp:1",
		"tcp:65535",
		"tsping",
		"http:https://10.0.0.1/|DSM",
		"http:http://host.example/health",
	}
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
			t.Parallel()
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
	t.Setenv("TAIL_WATCH_CONFIG", "/a.yaml")
	if got := resolveConfigPath("/b.yaml", os.Getenv("TAIL_WATCH_CONFIG")); got != "/b.yaml" {
		t.Fatalf("flag got %s", got)
	}
	if got := resolveConfigPath("", os.Getenv("TAIL_WATCH_CONFIG")); got != "/a.yaml" {
		t.Fatalf("env got %s", got)
	}
	t.Setenv("TAIL_WATCH_CONFIG", "")
	if got := resolveConfigPath("", os.Getenv("TAIL_WATCH_CONFIG")); got != defaultConfigPath {
		t.Fatalf("default got %s", got)
	}
	path := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err %v", err)
	}
}

func TestExampleConfig(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "examples", "tail-watch.yaml")
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

//nolint:gocognit // Local-only acceptance and rejection cases keep their inputs next to the assertions.
func TestLocalOnlyConfig(t *testing.T) {
	t.Parallel()
	t.Run("targets default to local", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig([]byte(`
collector: {local_only: true}
targets:
  - {name: web, addr: web.apps.svc.cluster.local, checks: ['tcp:443']}
  - {name: api, addr: api.apps.svc.cluster.local, network: local, checks: ['tcp:80']}
`))
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Collector.LocalOnly {
			t.Fatal("local_only not resolved")
		}
		for _, target := range cfg.Targets {
			if target.Network != NetworkLocal {
				t.Errorf("target %s network %s, want local", target.Name, target.Network)
			}
		}
	})
	t.Run("rejections", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name  string
			src   string
			wants []string
		}{
			{
				name: "tailnet target",
				src: `
collector: {local_only: true}
targets:
  - {name: nas, addr: 100.64.0.1, network: tailnet, checks: [icmp]}
`,
				wants: []string{"targets[0].network", "nas", "tailnet", "local-only"},
			},
			{
				name: "tsping check",
				src: `
collector: {local_only: true}
targets:
  - {name: nas, addr: 100.64.0.1, checks: [tsping]}
`,
				wants: []string{"targets[0].checks[0]", "nas", "tsping is not valid in local-only mode"},
			},
			{
				name: "ui on",
				src: `
collector: {local_only: true, ui: true, state_dir: /var/lib/tail-watch}
targets:
  - {name: web, addr: 10.0.0.1, checks: ['tcp:443']}
`,
				wants: []string{"collector.ui", "unavailable in local-only mode"},
			},
			{
				name: "several problems together",
				src: `
collector: {local_only: true, ui: true, state_dir: /var/lib/tail-watch}
targets:
  - {name: nas, addr: 100.64.0.1, network: tailnet, checks: [tsping]}
  - {name: web, addr: 10.0.0.1, checks: ['tcp:443']}
`,
				wants: []string{
					"collector.ui",
					"targets[0].network",
					"targets[0].checks[0]",
					"tsping is not valid in local-only mode",
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
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
	})
	t.Run("local_only off keeps today", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig([]byte(`
collector: {local_only: false}
targets:
  - {name: nas, addr: 100.64.0.1, checks: [tsping]}
`))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Collector.LocalOnly {
			t.Fatal("local_only resolved on")
		}
		if cfg.Targets[0].Network != NetworkTailnet {
			t.Fatalf("network %s, want tailnet", cfg.Targets[0].Network)
		}
	})
}

func TestRoutedTarget(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]byte(`
sinks:
  - {name: db-hook, type: webhook, url_env: U}
routes:
  - {name: db-team, severity: warn, sinks: [db-hook]}
targets:
  - {name: pg-primary, addr: 10.0.0.1, checks: [icmp], route: db-team}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Targets[0]
	if got.Route != "db-team" || got.Severity != SeverityWarn {
		t.Fatalf("route %q severity %q", got.Route, got.Severity)
	}
}

func TestTargetRouteRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			"both keys",
			"targets:\n  - {name: a, addr: 10.0.0.1, checks: [icmp], route: db-team, severity: warn}\n" +
				"routes:\n  - {name: db-team, sinks: [db-hook]}",
			"mutually exclusive",
		},
		{
			"unknown route",
			"targets:\n  - {name: a, addr: 10.0.0.1, checks: [icmp], route: db-tem}",
			`unknown route "db-tem"`,
		},
	}
	const sinks = "sinks:\n  - {name: db-hook, type: webhook, url_env: U}\n"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseConfig([]byte(sinks + tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			if tt.name == "both keys" && !strings.Contains(err.Error(), "targets[0].route") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestListenFlagOverride(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Collector: CollectorConfig{
			Listen: "127.0.0.1:1", StateDir: "", UI: false, LocalOnly: false,
			Setec:         setecSettings{Server: "", PollInterval: 0, pollTicker: nil},
			Authorization: Authorization{Capability: "", Service: ""},
		},
		Targets:    nil,
		Sinks:      nil,
		Heartbeat:  nil,
		RawTargets: nil,
		Base:       builtinTiming(), Routes: nil,
	}
	if got := cfg.ListenAddr("127.0.0.1:2"); got != "127.0.0.1:2" {
		t.Fatal(got)
	}
	if got := (new(Config)).ListenAddr(""); got != defaultListen {
		t.Fatal(got)
	}
}

func TestValidateCandidate(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]byte(`
defaults: {interval: 30s, timeout: 5s}
targets:
  - {name: router, addr: 10.0.0.1, checks: [icmp]}
  - {name: nas, addr: 10.0.0.2, checks: [icmp, 'tcp:445']}
  - {name: cam, addr: 10.0.0.3, checks: [icmp], parent: router}
`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		candidate rawTarget
		wants     []string
	}{
		{
			name:      "valid",
			candidate: rawT("lab", "10.9.0.1", "icmp"),
			wants:     nil,
		},
		{
			name:      "bad check port",
			candidate: rawT("lab", "10.9.0.1", "icmp", "tcp:70000"),
			wants:     []string{"checks[1]: "},
		},
		{
			name: "timeout not below interval",
			candidate: rawWith(rawT("lab", "10.9.0.1", "icmp"), func(r *rawTarget) {
				r.Interval, r.Timeout = dur(10*time.Second), dur(10*time.Second)
			}),
			wants: []string{"timeout: "},
		},
		{
			name:      "duplicate name",
			candidate: rawT("nas", "10.9.0.1", "icmp"),
			wants:     []string{"name: duplicate target name"},
		},
		{
			name:      "unknown parent",
			candidate: rawWith(rawT("lab", "10.9.0.1", "icmp"), func(r *rawTarget) { r.Parent = "ghost" }),
			wants:     []string{"parent: "},
		},
		{
			name:      "non-reachability parent",
			candidate: rawWith(rawT("lab", "10.9.0.1", "icmp"), func(r *rawTarget) { r.Parent = "nas" }),
			wants:     []string{"parent: ", "exactly one icmp or tsping"},
		},
		{
			name:      "self parent cycle",
			candidate: rawWith(rawT("lab", "10.9.0.1", "icmp"), func(r *rawTarget) { r.Parent = "lab" }),
			wants:     []string{"parent: parent cycle: lab -> lab"},
		},
		{
			name:      "several errors at once",
			candidate: rawT("lab", "", "tcp:70000"),
			wants:     []string{"addr: ", "checks[0]: "},
		},
		{
			name:      "breaks a dependent's parent",
			candidate: rawT("router", "10.0.0.1", "tcp:22"),
			wants:     []string{"cam", "exactly one icmp or tsping"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			existing := cfg.RawTargets
			if tt.name == "breaks a dependent's parent" {
				existing = cfg.RawTargets[1:]
			}
			got, errs := cfg.validateCandidate(existing, tt.candidate, cfg.Routes)
			if len(tt.wants) == 0 {
				requireValidCandidate(t, got, errs)
				return
			}
			requireCandidateErrors(t, errs, tt.wants)
		})
	}
}

func requireValidCandidate(t *testing.T, got Target, errs []error) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("errors %v", errs)
	}
	if !got.Dynamic || got.Interval != 30*time.Second {
		t.Fatalf("resolved %+v", got)
	}
}

func requireCandidateErrors(t *testing.T, errs []error, wants []string) {
	t.Helper()
	joined := errors.Join(errs...).Error()
	for _, want := range wants {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "targets[") {
		t.Errorf("error still carries a target index:\n%s", joined)
	}
}

//nolint:gocognit,cyclop,gocyclo // Each subtest is one collector.setec validation case.
func TestSetecConfig(t *testing.T) {
	t.Parallel()
	const target = "targets:\n  - {name: nas, addr: 10.0.0.1, checks: [icmp]}\n"
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig([]byte(`
collector:
  setec:
    server: https://setec.example.ts.net
    poll_interval: 5m
` + target + "sinks:\n  - {name: ops, type: slack, url_setec: tail-watch/slack-url}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Collector.Setec.Server != "https://setec.example.ts.net" ||
			cfg.Collector.Setec.PollInterval != 5*time.Minute {
			t.Fatalf("setec %+v", cfg.Collector.Setec)
		}
	})
	t.Run("omitted poll", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig([]byte("collector:\n  setec:\n    server: https://setec.example.ts.net\n" + target))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Collector.Setec.PollInterval != 5*time.Minute {
			t.Fatalf("poll %s", cfg.Collector.Setec.PollInterval)
		}
	})
	t.Run("http server", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte("collector:\n  setec:\n    server: http://setec.example.ts.net\n" + target))
		if err == nil || !strings.Contains(err.Error(), "collector.setec.server") ||
			!strings.Contains(err.Error(), "https") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("missing host", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte("collector:\n  setec:\n    server: https://\n" + target))
		if err == nil || !strings.Contains(err.Error(), "host") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("malformed server is reported once", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte("collector:\n  setec:\n    server: http://setec.example.ts.net\n" + target +
			"sinks:\n  - {name: ops, type: slack, url_setec: s}\n"))
		if err == nil || strings.Contains(err.Error(), "requires collector.setec.server") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("poll interval 10s", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte("collector:\n  setec:\n    server: https://setec.example.ts.net\n" +
			"    poll_interval: 10s\n" + target))
		if err == nil || !strings.Contains(err.Error(), "at least 1m") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("poll interval 1m", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig([]byte("collector:\n  setec:\n    server: https://setec.example.ts.net\n" +
			"    poll_interval: 1m\n" + target))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Collector.Setec.PollInterval != time.Minute {
			t.Fatalf("poll %s", cfg.Collector.Setec.PollInterval)
		}
	})
	t.Run("local only section", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte(`
collector: {local_only: true, setec: {server: https://setec.example.ts.net}}
targets:
  - {name: web, addr: web.apps.svc, checks: ['tcp:443']}
`))
		if err == nil || !strings.Contains(err.Error(), "collector.setec") ||
			!strings.Contains(err.Error(), "local-only") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("local only keys", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte(`
collector: {local_only: true}
targets:
  - {name: web, addr: web.apps.svc, checks: ['tcp:443']}
sinks:
  - name: ops
    type: webhook
    url_setec: hook/url
    secret_setec: hook/secret
    headers_setec: {Authorization: hook/auth}
heartbeat:
  url_setec: hb/url
`))
		if err == nil {
			t.Fatal("expected errors")
		}
		for _, want := range []string{
			"sinks[0].url_setec", "sinks[0].secret_setec", "sinks[0].headers_setec",
			"heartbeat.url_setec", "sink ops", "unavailable in local-only mode",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("missing %q in %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "requires collector.setec.server") {
			t.Fatalf("local-only also reported a missing server: %v", err)
		}
	})
	t.Run("missing server", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte(target + `
sinks:
  - name: ops
    type: webhook
    url_setec: hook/url
    secret_setec: hook/secret
    headers_setec: {Authorization: hook/auth}
heartbeat:
  url_setec: hb/url
`))
		if err == nil {
			t.Fatal("expected errors")
		}
		for _, want := range []string{
			"sinks[0].url_setec", "sink ops", "secret_setec", "headers_setec",
			"heartbeat.url_setec", "requires collector.setec.server",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("missing %q in %v", want, err)
			}
		}
	})
	t.Run("unused section", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig([]byte("collector:\n  setec:\n    server: https://setec.example.ts.net\n" + target))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Collector.Setec.Server == "" || len(setecSecretNames(cfg)) != 0 {
			t.Fatalf("setec %+v names %v", cfg.Collector.Setec, setecSecretNames(cfg))
		}
	})
	t.Run("empty section", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig([]byte("collector:\n  setec: {}\n" + target))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Collector.Setec.Server != "" || cfg.Collector.Setec.PollInterval != 0 {
			t.Fatalf("empty section resolved %+v", cfg.Collector.Setec)
		}
	})
}

func TestAuthorizationUIFlagOverridesFile(t *testing.T) {
	t.Parallel()
	fileOff, err := parseConfig([]byte("collector:\n  authorization:\n    capability: example.com/cap/tail-watch\n"))
	if err != nil {
		t.Fatalf("ui unset in file: %v", err)
	}
	if uiErr := fileOff.requireAuthzUI(true); uiErr != nil {
		t.Fatalf("--ui with collector.ui unset: %v", uiErr)
	}
	if uiErr := fileOff.requireAuthzUI(false); uiErr == nil {
		t.Fatal("UI off with authorization set was accepted")
	}
}

func TestAuthorizationConfig(t *testing.T) {
	t.Parallel()
	const valid = `
collector:
  ui: true
  state_dir: /var/lib/tail-watch
  authorization:
    capability: example.com/cap/tail-watch
    service: svc:tail-watch
`
	cfg, err := parseConfig([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Collector.Authorization.On() || cfg.Collector.Authorization.Service != "svc:tail-watch" {
		t.Fatalf("authorization %+v", cfg.Collector.Authorization)
	}
	if uiErr := cfg.requireAuthzUI(false); uiErr == nil || !strings.Contains(uiErr.Error(), "collector.authorization") {
		t.Fatalf("--ui=false: %v", uiErr)
	}

	absent, err := parseConfig([]byte("collector: {ui: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if absent.Collector.Authorization.On() {
		t.Fatal("absent section turned authorization on")
	}

	tests := []struct {
		name  string
		src   string
		wants []string
	}{
		{
			name: "empty capability",
			src: `
collector:
  ui: true
  authorization:
    capability: ""
`,
			wants: []string{"collector.authorization.capability"},
		},
		{
			name: "no cap segment",
			src: `
collector:
  ui: true
  authorization:
    capability: example.com/tail-watch
`,
			wants: []string{"collector.authorization.capability", "no /cap/ segment"},
		},
		{
			name: "tailscale.com domain",
			src: `
collector:
  ui: true
  authorization:
    capability: tailscale.com/cap/x
`,
			wants: []string{"collector.authorization.capability", "tailscale.com"},
		},
		{
			name: "https tailscale.com name",
			src: `
collector:
  ui: true
  authorization:
    capability: https://tailscale.com/cap/tail-watch
`,
			wants: []string{"collector.authorization.capability", "tailscale.com"},
		},
		{
			name: "bad service",
			src: `
collector:
  ui: true
  authorization:
    capability: example.com/cap/tail-watch
    service: tail-watch
`,
			wants: []string{"collector.authorization.service"},
		},
		{
			name: "local only",
			src: `
collector:
  local_only: true
  ui: true
  authorization:
    capability: example.com/cap/tail-watch
`,
			wants: []string{"collector.authorization", "local-only", "collector.ui"},
		},
		{
			name: "every problem",
			src: `
collector:
  local_only: true
  authorization:
    capability: ""
    service: tail-watch
`,
			wants: []string{
				"collector.authorization.capability",
				"collector.authorization.service",
				"collector.authorization",
				"local-only",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, parseErr := parseConfig([]byte(tt.src))
			if parseErr == nil {
				t.Fatal("expected error")
			}
			for _, want := range tt.wants {
				if !strings.Contains(parseErr.Error(), want) {
					t.Errorf("error %q missing %q", parseErr, want)
				}
			}
		})
	}
}

func TestCheckConfigAuthorization(t *testing.T) {
	t.Parallel()
	ok := writeCfg(t, `
collector:
  ui: true
  state_dir: /var/lib/tail-watch
  authorization:
    capability: example.com/cap/tail-watch
`)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check-config", "--config", ok}, &stdout, &stderr); code != 0 {
		t.Fatalf("valid: code %d\n%s%s", code, stdout.String(), stderr.String())
	}
	bad := writeCfg(t, `
collector:
  ui: true
  authorization:
    capability: tailscale.com/cap/x
`)
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"check-config", "--config", bad}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "collector.authorization.capability") {
		t.Fatalf("invalid: code %d\n%s%s", code, stdout.String(), stderr.String())
	}
}
