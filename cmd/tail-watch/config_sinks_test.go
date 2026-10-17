package main

import (
	"context"
	"errors"
	"log/slog"
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
		wh.Headers["Authorization"].Env != "OPS_WEBHOOK_AUTH" || len(wh.Events) != 2 {
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

func TestTeamsSinkValid(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]byte(sinkBase + `
sinks:
  - name: ops-teams
    type: teams
    url_env: TEAMS_WEBHOOK_URL
    status_url: https://monitor-nyc.example.ts.net:9109
    group_wait: 30s
    min_severity: warn
    queue_size: 50
`))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Sinks[0]
	if s.Type != sinkTeams || s.URL.Env != "TEAMS_WEBHOOK_URL" ||
		s.StatusURL != "https://monitor-nyc.example.ts.net:9109" ||
		s.GroupWait != 30*time.Second || s.MinSeverity != SeverityWarn || s.QueueSize != 50 {
		t.Fatalf("teams: %+v", s)
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
		{"unknown type", "sinks:\n  - {name: a, type: pagerduty, url_env: U}", "must be webhook, slack or teams"},
		{"no url", "sinks:\n  - {name: a, type: slack}", "exactly one of url_env, url_file or url_setec"},
		{
			"two urls",
			"sinks:\n  - {name: a, type: slack, url_env: U, url_file: /f}",
			"exactly one of url_env, url_file or url_setec",
		},
		{
			"two urls name the keys",
			"sinks:\n  - {name: a, type: slack, url_env: U, url_setec: s}",
			"url_env and url_setec are set",
		},
		{
			"teams two urls",
			"sinks:\n  - {name: a, type: teams, url_env: U, url_file: /f}",
			"exactly one of url_env, url_file or url_setec",
		},
		{
			"slack batch",
			"sinks:\n  - {name: a, type: slack, url_env: U, batch: true}",
			"batch is not valid for a slack sink",
		},
		{
			"teams batch",
			"sinks:\n  - {name: a, type: teams, url_env: U, batch: true}",
			"batch is not valid for a teams sink",
		},
		{
			"teams max_batch",
			"sinks:\n  - {name: a, type: teams, url_env: U, max_batch: 5}",
			"max_batch is not valid for a teams sink",
		},
		{
			"teams secret",
			"sinks:\n  - {name: a, type: teams, url_env: U, secret_file: /f}",
			"secret_file is not valid for a teams sink",
		},
		{
			"teams headers",
			"sinks:\n  - {name: a, type: teams, url_env: U, headers_env: {Authorization: A}}",
			"headers_env is not valid for a teams sink",
		},
		{
			"teams mention",
			"sinks:\n  - {name: a, type: teams, url_env: U, mention_on_page: '<at>Jane</at>'}",
			"mention_on_page is not valid for a teams sink",
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
		{"heartbeat no url", "heartbeat:\n  interval: 1m", "exactly one of url_env, url_file or url_setec"},
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
	for _, want := range []string{"reserved", "must be webhook, slack or teams", "queue_size must be positive"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}

const routeSinks = `
sinks:
  - {name: pagerduty, type: webhook, url_env: U}
  - {name: ops-slack, type: slack, url_env: S}
`

func TestRouteValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			"unknown sink",
			"routes:\n  - {name: db-team, sinks: [pagerdutty]}",
			[]string{"routes[0].sinks[0]", "pagerdutty"},
		},
		{
			"empty sinks",
			"routes:\n  - {name: db-team, sinks: []}",
			[]string{"routes[0].sinks", "sinks must not be empty"},
		},
		{
			"sink listed twice",
			"routes:\n  - {name: db-team, sinks: [pagerduty, pagerduty]}",
			[]string{"routes[0].sinks[1]", "listed twice"},
		},
		{
			"duplicate name",
			"routes:\n  - {name: db-team, sinks: [pagerduty]}\n  - {name: db-team, sinks: [ops-slack]}",
			[]string{"routes[1].name", "duplicate route name"},
		},
		{
			"bad name",
			"routes:\n  - {name: 'db team', sinks: [pagerduty]}",
			[]string{"routes[0].name", "must match"},
		},
		{
			"unknown key",
			"routes:\n  - {name: db-team, sinks: [pagerduty], bogus: 1}",
			[]string{"routes[0].bogus", "unknown key"},
		},
		{
			"bad severity",
			"routes:\n  - {name: db-team, severity: loud, sinks: [pagerduty]}",
			[]string{"routes[0].severity", "must be page or warn"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseConfig([]byte(sinkBase + routeSinks + tt.yaml))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %v", want, err)
				}
			}
		})
	}
}

func TestRouteDefaultsAndValid(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]byte(sinkBase + routeSinks + `
routes:
  - {name: db-team, sinks: [pagerduty, ops-slack]}
  - {name: cameras, severity: warn, sinks: [ops-slack], mention: "<!subteam^S0CAM>"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("routes %d", len(cfg.Routes))
	}
	db := cfg.Routes[0]
	if db.Severity != SeverityPage || db.Mention != "" || len(db.Indexes) != 2 || db.Names[0] != "pagerduty" {
		t.Fatalf("default route %+v", db)
	}
	cam := cfg.Routes[1]
	if cam.Severity != SeverityWarn || cam.Mention != "<!subteam^S0CAM>" || len(cam.Indexes) != 1 {
		t.Fatalf("cameras %+v", cam)
	}
}

func TestRouteValidationCollectsAllErrors(t *testing.T) {
	t.Parallel()
	_, err := parseConfig([]byte(sinkBase + routeSinks + `
routes:
  - {name: "bad name", severity: loud, sinks: [nope, nope], extra: 1}
  - {name: db-team, sinks: []}
`))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{
		"routes[0].name",
		"must match",
		"routes[0].severity",
		"must be page or warn",
		"routes[0].sinks[0]",
		"unknown sink",
		"routes[0].sinks[1]",
		"listed twice",
		"routes[0].extra",
		"unknown key",
		"routes[1].sinks",
		"sinks must not be empty",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
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
		{"env", secretRef{Env: "TW_SECRET", File: "", Setec: "", src: nil}, "s3cret", ""},
		{"file trimmed", secretRef{Env: "", File: file, Setec: "", src: nil}, "from-file", ""},
		{
			"unset env",
			secretRef{Env: "TW_UNSET_NOPE", File: "", Setec: "", src: nil},
			"",
			"TW_UNSET_NOPE is unset or empty",
		},
		{"empty env", secretRef{Env: "TW_EMPTY", File: "", Setec: "", src: nil}, "", "TW_EMPTY is unset or empty"},
		{
			"unreadable file",
			secretRef{Env: "", File: filepath.Join(dir, "missing"), Setec: "", src: nil},
			"",
			"secret file",
		},
		{"empty file", secretRef{Env: "", File: empty, Setec: "", src: nil}, "", "is empty"},
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

func TestRedactSecrets(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"key tskey-abc123_x rest":                            "key redacted rest",
		"hook https://hooks.slack.com/services/T0/B0/x done": "hook redacted done",
		"flow https://contoso.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?api-version=2016&sig=p%2Fsecret done":                                               "flow redacted done",
		"flow https://default0a1b.07.environment.api.powerplatform.com:443/powerautomate/automations/direct/workflows/abc/triggers/manual/paths/invoke?api-version=1&sig=x done": "flow redacted done",
		"query sig=sekret&sp=sv done": "query redacted&sp=sv done",
		"nothing to hide":             "nothing to hide",
	}
	for in, want := range tests {
		if got := redactSecrets(in); got != want {
			t.Errorf("redactSecrets(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckConfigPrintsSinksWithoutSecrets(t *testing.T) {
	t.Setenv("TW_HOOK_URL", "https://hooks.example.com/SECRETPATH")
	t.Setenv("TW_HOOK_SECRET", "hmac-value-xyz")
	t.Setenv("TW_HOOK_AUTH", "Bearer token-value")
	t.Setenv("TW_TEAMS_URL", "https://contoso.logic.azure.com/workflows/abc?sig=teams-secret-value")
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
  - name: ops-teams
    type: teams
    url_env: TW_TEAMS_URL
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
	if !strings.Contains(out, "sink ops-teams type=teams") {
		t.Fatalf("missing teams sink description:\n%s", out)
	}
	for _, secret := range []string{
		"SECRETPATH", "hmac-value-xyz", "token-value", "TW_HOOK_SECRET",
		"teams-secret-value", "TW_TEAMS_URL",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("output leaks %q:\n%s", secret, out)
		}
	}
}

//nolint:gocognit // Each subtest is one source-validation case.
func TestSetecSinkSources(t *testing.T) {
	t.Parallel()
	const head = "collector:\n  setec:\n    server: https://setec.example.ts.net\n" +
		"targets:\n  - {name: nas, addr: 10.0.0.1, checks: [icmp]}\n"
	t.Run("each source alone", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig([]byte(head + `
sinks:
  - {name: from-setec, type: slack, url_setec: tail-watch/slack-url}
  - name: from-env
    type: webhook
    url_env: OPS_URL
    secret_setec: hook/hmac
    headers_setec: {Authorization: hook/auth}
heartbeat:
  url_setec: hb/url
`))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Sinks[0].URL.Setec != "tail-watch/slack-url" || cfg.Sinks[0].URL.Env != "" {
			t.Fatalf("slack url %+v", cfg.Sinks[0].URL)
		}
		if cfg.Sinks[1].URL.Env != "OPS_URL" || cfg.Sinks[1].Secret.Setec != "hook/hmac" {
			t.Fatalf("webhook %+v secret %+v", cfg.Sinks[1].URL, cfg.Sinks[1].Secret)
		}
		if cfg.Sinks[1].Headers["Authorization"].Setec != "hook/auth" {
			t.Fatalf("header %+v", cfg.Sinks[1].Headers)
		}
		if cfg.Heartbeat == nil || cfg.Heartbeat.URL.Setec != "hb/url" {
			t.Fatalf("heartbeat %+v", cfg.Heartbeat)
		}
	})
	t.Run("two url sources", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte(head +
			"sinks:\n  - {name: ops, type: webhook, url_env: OPS_URL, url_setec: ops-url}\n"))
		if err == nil || !strings.Contains(err.Error(), "sink ops") ||
			!strings.Contains(err.Error(), "url_env") || !strings.Contains(err.Error(), "url_setec") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("two secret sources", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte(head +
			"sinks:\n  - {name: ops, type: webhook, url_env: U, secret_env: S, secret_setec: hook/hmac}\n"))
		if err == nil || !strings.Contains(err.Error(), "secret_env") ||
			!strings.Contains(err.Error(), "secret_setec") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("secret_setec on slack", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte(head + "sinks:\n  - {name: ops, type: slack, url_setec: u, secret_setec: s}\n"))
		if err == nil || !strings.Contains(err.Error(), "secret_setec is not valid") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("headers_setec on teams", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte(head +
			"sinks:\n  - {name: ops, type: teams, url_env: U, headers_setec: {Authorization: s}}\n"))
		if err == nil || !strings.Contains(err.Error(), "headers_setec is not valid") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("header in both maps", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig([]byte(head + `
sinks:
  - name: ops
    type: webhook
    url_env: U
    headers_env: {Authorization: TOK}
    headers_setec: {Authorization: hook/auth}
`))
		if err == nil || !strings.Contains(err.Error(), "sink ops") ||
			!strings.Contains(err.Error(), "Authorization") ||
			!strings.Contains(err.Error(), "headers_env") || !strings.Contains(err.Error(), "headers_setec") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSecretRefSetec(t *testing.T) {
	t.Parallel()
	src := mapSource{values: map[string]string{
		"hook/url":    "  https://example.test/hook  ",
		"hook/empty":  "  ",
		"hook/secret": "top-secret-value",
	}}
	tests := []struct {
		name string
		ref  secretRef
		want string
		err  string
	}{
		{"value", secretRef{Env: "", File: "", Setec: "hook/url", src: src}, "https://example.test/hook", ""},
		{"unbound", secretRef{Env: "", File: "", Setec: "hook/url", src: nil}, "", "setec is not configured"},
		{"missing", secretRef{Env: "", File: "", Setec: "missing", src: src}, "", "setec: secret not found: missing"},
		{"empty", secretRef{Env: "", File: "", Setec: "hook/empty", src: src}, "", "is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.ref.resolve()
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) ||
					strings.Contains(err.Error(), "top-secret-value") {
					t.Fatalf("got %q %v", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q %v", got, err)
			}
		})
	}
}

func TestBindSecretsReachesEveryRef(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]byte(`
collector:
  setec:
    server: https://setec.example.ts.net
targets:
  - {name: nas, addr: 10.0.0.1, checks: [icmp]}
sinks:
  - name: hook
    type: webhook
    url_setec: hook/url
    secret_setec: hook/secret
    headers_setec: {Authorization: hook/auth}
heartbeat:
  url_setec: hb/url
`))
	if err != nil {
		t.Fatal(err)
	}
	_, unbound := cfg.Sinks[0].URL.resolve()
	if unbound == nil || !strings.Contains(unbound.Error(), "setec is not configured") {
		t.Fatalf("unbound %v", unbound)
	}
	cfg.bindSecrets(mapSource{values: map[string]string{
		"hook/url":    "https://example.test/hook",
		"hook/secret": "hmac",
		"hook/auth":   "Bearer t",
		"hb/url":      "https://example.test/hb",
	}})
	got := map[string]string{}
	for _, pair := range []struct {
		name string
		ref  secretRef
	}{
		{"url", cfg.Sinks[0].URL},
		{"secret", cfg.Sinks[0].Secret},
		{"header", cfg.Sinks[0].Headers["Authorization"]},
		{"heartbeat", cfg.Heartbeat.URL},
	} {
		v, resolveErr := pair.ref.resolve()
		if resolveErr != nil {
			t.Fatalf("%s: %v", pair.name, resolveErr)
		}
		got[pair.name] = v
	}
	if got["url"] != "https://example.test/hook" || got["secret"] != "hmac" ||
		got["header"] != "Bearer t" || got["heartbeat"] != "https://example.test/hb" {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckSecretsSetec(t *testing.T) {
	t.Parallel()
	load := func(t *testing.T) *Config {
		t.Helper()
		cfg, err := parseConfig([]byte(`
collector:
  setec:
    server: https://setec.example.ts.net
targets:
  - {name: nas, addr: 10.0.0.1, checks: [icmp]}
sinks:
  - name: hook
    type: webhook
    url_setec: hook/url
    secret_setec: hook/secret
    headers_setec: {Authorization: hook/auth}
heartbeat:
  url_setec: hb/url
`))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		cfg := load(t)
		cfg.bindSecrets(mapSource{values: map[string]string{}})
		err := cfg.resolveSecrets()
		for _, want := range []string{
			"sink hook: url:", "hook/url", "sink hook: secret:", "hook/secret",
			"header Authorization", "hook/auth", "heartbeat:", "hb/url",
		} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("missing %q in %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "top-secret-value") {
			t.Fatalf("value leaked: %v", err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		cfg := load(t)
		cfg.bindSecrets(mapSource{values: map[string]string{
			"hook/url": "  ", "hook/secret": "hmac", "hook/auth": "Bearer t", "hb/url": "https://example.test/hb",
		}})
		err := cfg.Sinks[0].checkSecrets()
		if err == nil || !strings.Contains(err.Error(), "sink hook: url:") ||
			!strings.Contains(err.Error(), "hook/url is empty") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		cfg := load(t)
		cfg.bindSecrets(mapSource{values: map[string]string{
			"hook/url": "https://example.test/hook", "hook/secret": "top-secret-value",
			"hook/auth": "Bearer t", "hb/url": "https://example.test/hb",
		}})
		if err := cfg.resolveSecrets(); err != nil {
			t.Fatal(err)
		}
	})
}

//nolint:gocognit,paralleltest // Stubs the process-wide constructor; each subtest is one check-config case.
func TestCheckConfigSetec(t *testing.T) {
	called := false
	stubOpenSetec(t, func(
		context.Context, netEnv, setecSettings, []string, *slog.Logger,
	) (*setecStore, error) {
		called = true
		return nil, errors.New("setec store must not be built")
	})
	const target = "targets:\n  - {name: nas, addr: 10.0.0.1, checks: [icmp]}\n"
	t.Run("prints the name offline", func(t *testing.T) {
		path := writeCfg(t, "collector:\n  setec:\n    server: https://setec.example.ts.net\n"+target+
			"sinks:\n  - {name: ops, type: slack, url_setec: tail-watch/slack-url}\n")
		var stdout, stderr strings.Builder
		if code := run([]string{"check-config", "--config", path}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "url=setec:tail-watch/slack-url") {
			t.Fatalf("stdout %s", stdout.String())
		}
		if strings.Contains(stdout.String(), "https://hooks.example") {
			t.Fatalf("printed a value:\n%s", stdout.String())
		}
	})
	t.Run("local only", func(t *testing.T) {
		path := writeCfg(t, `
collector: {local_only: true, setec: {server: https://setec.example.ts.net}}
targets:
  - {name: web, addr: web.apps.svc, checks: ['tcp:443']}
sinks:
  - {name: ops, type: webhook, url_setec: hook/url}
`)
		var stdout, stderr strings.Builder
		if code := run([]string{"check-config", "--config", path}, &stdout, &stderr); code != 1 {
			t.Fatalf("exit %d\n%s", code, stderr.String())
		}
		for _, want := range []string{
			"collector.setec", "sinks[0].url_setec", "sink ops", "unavailable in local-only mode",
		} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("missing %q in %s", want, stderr.String())
			}
		}
	})
	t.Run("missing server", func(t *testing.T) {
		path := writeCfg(t, target+"sinks:\n  - {name: ops, type: slack, url_setec: tail-watch/slack-url}\n")
		var stdout, stderr strings.Builder
		if code := run([]string{"check-config", "--config", path}, &stdout, &stderr); code != 1 {
			t.Fatalf("exit %d\n%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "url_setec") ||
			!strings.Contains(stderr.String(), "collector.setec.server") {
			t.Fatalf("stderr %s", stderr.String())
		}
	})
	t.Run("unused section", func(t *testing.T) {
		path := writeCfg(t, "collector:\n  setec:\n    server: https://setec.example.ts.net\n"+target)
		var stdout, stderr strings.Builder
		if code := run([]string{"check-config", "--config", path}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
	})
	if called {
		t.Fatal("check-config built a setec store")
	}
}
