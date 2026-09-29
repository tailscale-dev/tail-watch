package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sinkBase = `
collector:
  state_dir: /var/lib/tail-watch
targets:
  - name: nas
    addr: 10.0.0.1
    checks: [icmp]
`

const designSinks = sinkBase + `
sinks:
  - name: ops-webhook
    type: webhook
    url_env: OPS_WEBHOOK_URL
    secret_env: OPS_WEBHOOK_SECRET
    headers_env:
      Authorization: OPS_WEBHOOK_AUTH
    min_severity: page
    events: [target.down, target.recovered]
    max_age: 1h
    queue: disk
    queue_size: 1000
    batch: true
    group_wait: 10s
    max_batch: 100
  - name: ops-slack
    type: slack
    url_env: SLACK_WEBHOOK_URL
    min_severity: warn
    mention_on_page: "<!subteam^S0123ABC>"
    group_wait: 10s
    queue: memory
    status_url: https://monitor-nyc.example.ts.net:9109
    match:
      sites: [nyc, sfo]
heartbeat:
  url_env: HEARTBEAT_URL
  interval: 1m
`

func TestParseDesignSinks(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]byte(designSinks))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sinks) != 2 || cfg.Heartbeat == nil {
		t.Fatalf("sinks=%d heartbeat=%v", len(cfg.Sinks), cfg.Heartbeat)
	}
	wh := cfg.Sinks[0]
	if wh.Queue != queueDisk || !wh.Batch || wh.MinSeverity != SeverityPage || wh.URL.Env != "OPS_WEBHOOK_URL" ||
		wh.HeadersEnv["Authorization"] != "OPS_WEBHOOK_AUTH" || len(wh.Events) != 2 {
		t.Fatalf("webhook: %+v", wh)
	}
	sl := cfg.Sinks[1]
	if sl.MentionOnPage != "<!subteam^S0123ABC>" || len(sl.Match.Sites) != 2 || sl.Queue != queueMemory {
		t.Fatalf("slack: %+v", sl)
	}
}

func TestSinkDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]byte(sinkBase + `
sinks:
  - {name: a, type: webhook, url_env: U}
heartbeat:
  url_file: /run/hb
`))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Sinks[0]
	if s.MinSeverity != SeverityInfo || len(s.Events) != 0 || s.MaxAge != time.Hour || s.Queue != queueMemory ||
		s.QueueSize != 1000 || s.GroupWait != 10*time.Second || s.MaxBatch != 100 || s.Batch {
		t.Fatalf("defaults: %+v", s)
	}
	if cfg.Heartbeat.Interval != time.Minute {
		t.Fatalf("heartbeat interval %s", cfg.Heartbeat.Interval)
	}
}

func TestSinkValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			"duplicate name",
			"sinks:\n  - {name: a, type: slack, url_env: U}\n  - {name: a, type: slack, url_env: U}",
			"duplicate sink name",
		},
		{"invalid name", "sinks:\n  - {name: 'a b', type: slack, url_env: U}", "must match"},
		{"reserved incidents", "sinks:\n  - {name: incidents, type: slack, url_env: U}", "reserved"},
		{"reserved intake", "sinks:\n  - {name: intake, type: slack, url_env: U}", "reserved"},
		{"reserved state", "sinks:\n  - {name: state, type: slack, url_env: U}", "reserved"},
		{"unknown type", "sinks:\n  - {name: a, type: pagerduty, url_env: U}", "must be webhook or slack"},
		{"no url", "sinks:\n  - {name: a, type: slack}", "exactly one of url_env or url_file"},
		{
			"two urls",
			"sinks:\n  - {name: a, type: slack, url_env: U, url_file: /f}",
			"exactly one of url_env or url_file",
		},
		{
			"slack batch",
			"sinks:\n  - {name: a, type: slack, url_env: U, batch: true}",
			"batch is not valid for a slack sink",
		},
		{"slack secret", "sinks:\n  - {name: a, type: slack, url_env: U, secret_env: S}", "secret_env is not valid"},
		{
			"webhook mention",
			"sinks:\n  - {name: a, type: webhook, url_env: U, mention_on_page: x}",
			"only valid for a slack sink",
		},
		{
			"disk without state_dir",
			"sinks:\n  - {name: a, type: webhook, url_env: U, queue: disk}",
			"requires collector.state_dir",
		},
		{"bad queue", "sinks:\n  - {name: a, type: webhook, url_env: U, queue: tape}", "must be memory or disk"},
		{
			"queue_size",
			"sinks:\n  - {name: a, type: webhook, url_env: U, queue_size: 0}",
			"queue_size must be positive",
		},
		{"max_batch", "sinks:\n  - {name: a, type: webhook, url_env: U, max_batch: -1}", "max_batch must be positive"},
		{
			"group_wait",
			"sinks:\n  - {name: a, type: webhook, url_env: U, group_wait: 0s}",
			"group_wait must be positive",
		},
		{"max_age", "sinks:\n  - {name: a, type: webhook, url_env: U, max_age: -1s}", "max_age must be positive"},
		{
			"bad event",
			"sinks:\n  - {name: a, type: webhook, url_env: U, events: [target.exploded]}",
			"unknown event type",
		},
		{"bad glob", "sinks:\n  - {name: a, type: webhook, url_env: U, match: {targets: ['[']}}", "invalid glob"},
		{"bad severity", "sinks:\n  - {name: a, type: webhook, url_env: U, min_severity: loud}", "min_severity"},
		{"heartbeat no url", "heartbeat:\n  interval: 1m", "exactly one of url_env or url_file"},
		{"heartbeat interval", "heartbeat:\n  url_env: H\n  interval: 0s", "interval must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := sinkBase
			if tt.name == "disk without state_dir" {
				src = strings.Replace(src, "  state_dir: /var/lib/tail-watch\n", "  listen: 127.0.0.1:1\n", 1)
			}
			_, err := parseConfig([]byte(src + tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestSinkValidationCollectsAllErrors(t *testing.T) {
	t.Parallel()
	_, err := parseConfig([]byte(sinkBase + `
sinks:
  - {name: state, type: slack, url_env: U}
  - {name: b, type: nope}
  - {name: c, type: webhook, url_env: U, queue: disk, queue_size: 0}
`))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"reserved", "must be webhook or slack", "queue_size must be positive"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}

func TestSecretRefResolve(t *testing.T) {
	t.Setenv("TW_SECRET", "s3cret")
	t.Setenv("TW_EMPTY", "")
	dir := t.TempDir()
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		ref  secretRef
		want string
		err  string
	}{
		{"env", secretRef{Env: "TW_SECRET", File: ""}, "s3cret", ""},
		{"file trimmed", secretRef{Env: "", File: file}, "from-file", ""},
		{"unset env", secretRef{Env: "TW_UNSET_NOPE", File: ""}, "", "TW_UNSET_NOPE is unset or empty"},
		{"empty env", secretRef{Env: "TW_EMPTY", File: ""}, "", "TW_EMPTY is unset or empty"},
		{"unreadable file", secretRef{Env: "", File: filepath.Join(dir, "missing")}, "", "secret file"},
		{"empty file", secretRef{Env: "", File: empty}, "", "is empty"},
	}
	for _, tt := range tests {
		got, err := tt.ref.resolve()
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("%s: want %q, got %v", tt.name, tt.err, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("%s: got %q, %v", tt.name, got, err)
		}
	}
}

func TestResolveSecretsNamesSinkAndVariable(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]byte(sinkBase + "sinks:\n  - {name: ops, type: slack, url_env: TW_NOPE_URL}\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.resolveSecrets()
	if err == nil || !strings.Contains(err.Error(), "ops") || !strings.Contains(err.Error(), "TW_NOPE_URL") {
		t.Fatalf("got %v", err)
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"https://hooks.slack.com/services/T0/B0/x": "https://hooks.slack.com/…",
		"http://10.0.0.1:8080/a?k=v":               "http://10.0.0.1:8080/…",
		"not a url":                                "<redacted>",
	}
	for in, want := range tests {
		if got := redactURL(in); got != want {
			t.Fatalf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckConfigPrintsSinksWithoutSecrets(t *testing.T) {
	t.Setenv("TW_HOOK_URL", "https://hooks.example.com/SECRETPATH")
	t.Setenv("TW_HOOK_SECRET", "hmac-value-xyz")
	t.Setenv("TW_HOOK_AUTH", "Bearer token-value")
	path := filepath.Join(t.TempDir(), "c.yaml")
	src := sinkBase + `
sinks:
  - name: ops
    type: webhook
    url_env: TW_HOOK_URL
    secret_env: TW_HOOK_SECRET
    headers_env: {Authorization: TW_HOOK_AUTH}
    events: [target.down]
    match: {sites: [nyc]}
heartbeat:
  url_env: TW_HOOK_URL
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if code := run([]string{"check-config", "--config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "sink ops type=webhook") || !strings.Contains(out, "queue=memory") ||
		!strings.Contains(out, "target.down") || !strings.Contains(out, "sites:nyc") {
		t.Fatalf("missing sink description:\n%s", out)
	}
	for _, secret := range []string{"SECRETPATH", "hmac-value-xyz", "token-value", "TW_HOOK_SECRET"} {
		if strings.Contains(out, secret) {
			t.Fatalf("output leaks %q:\n%s", secret, out)
		}
	}
}
