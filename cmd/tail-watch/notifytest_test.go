package main

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func notifyTestConfig(t *testing.T, sinks string) string {
	t.Helper()
	return writeCfg(t, "targets:\n  - name: nas\n    addr: 10.0.0.1\n    checks: [icmp]\n"+sinks)
}

func staticIdentity(context.Context) (CollectorInfo, error) {
	return CollectorInfo{Name: "monitor-nyc", TailscaleIP: "100.101.102.103"}, nil
}

func TestNotifyTestAllSinksSucceed(t *testing.T) {
	a := newReceiver(t)
	b := newReceiver(t)
	t.Setenv("TW_NT_A", a.srv.URL)
	t.Setenv("TW_NT_B", b.srv.URL)
	path := notifyTestConfig(t, `
sinks:
  - {name: hook, type: webhook, url_env: TW_NT_A, min_severity: page, events: [target.down]}
  - {name: chat, type: slack, url_env: TW_NT_B, match: {sites: [nowhere]}}
`)
	var out, errOut strings.Builder
	code := runNotifyTestWith(
		[]string{"--config", path},
		&out,
		&errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil},
	)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{"hook webhook 200", "chat slack 200"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in\n%s", want, out.String())
		}
	}
	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("filters must be bypassed: %d %d", a.count(), b.count())
	}
	if !strings.Contains(string(a.request(0).Body), `"type":"test"`) {
		t.Fatalf("body %s", a.request(0).Body)
	}
}

func TestNotifyTestReportsFailingSink(t *testing.T) {
	good := newReceiver(t)
	bad := newReceiver(t, http.StatusUnauthorized)
	t.Setenv("TW_NT_A", good.srv.URL)
	t.Setenv("TW_NT_B", bad.srv.URL)
	path := notifyTestConfig(t, `
sinks:
  - {name: hook, type: webhook, url_env: TW_NT_A}
  - {name: strict, type: webhook, url_env: TW_NT_B}
`)
	var out, errOut strings.Builder
	code := runNotifyTestWith(
		[]string{"--config", path},
		&out,
		&errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil},
	)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "strict webhook 401") || !strings.Contains(out.String(), "hook webhook 200") {
		t.Fatalf("output\n%s", out.String())
	}
}

func TestNotifyTestSelectsOneSink(t *testing.T) {
	a := newReceiver(t)
	b := newReceiver(t)
	t.Setenv("TW_NT_A", a.srv.URL)
	t.Setenv("TW_NT_B", b.srv.URL)
	path := notifyTestConfig(t, `
sinks:
  - {name: one, type: webhook, url_env: TW_NT_A}
  - {name: two, type: webhook, url_env: TW_NT_B}
`)
	var out, errOut strings.Builder
	if code := runNotifyTestWith([]string{"--config", path, "--sink", "two"}, &out, &errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil}); code != 0 {
		t.Fatalf("exit %d %s", code, errOut.String())
	}
	if a.count() != 0 || b.count() != 1 {
		t.Fatalf("counts %d %d", a.count(), b.count())
	}
}

func TestNotifyTestUnknownSinkListsNames(t *testing.T) {
	t.Setenv("TW_NT_A", "http://127.0.0.1:1")
	path := notifyTestConfig(t, `
sinks:
  - {name: one, type: webhook, url_env: TW_NT_A}
  - {name: two, type: webhook, url_env: TW_NT_A}
`)
	var out, errOut strings.Builder
	code := runNotifyTestWith([]string{"--config", path, "--sink", "nope"}, &out, &errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil})
	if code == 0 {
		t.Fatal("unknown sink must fail")
	}
	if !strings.Contains(errOut.String(), "one") || !strings.Contains(errOut.String(), "two") {
		t.Fatalf("stderr %q must list the configured sinks", errOut.String())
	}
}

func TestNotifyTestWorksWithoutTailscaled(t *testing.T) {
	rc := newReceiver(t)
	t.Setenv("TW_NT_A", rc.srv.URL)
	path := notifyTestConfig(t, "sinks:\n  - {name: hook, type: webhook, url_env: TW_NT_A}\n")
	var out, errOut strings.Builder
	down := func(context.Context) (CollectorInfo, error) { return CollectorInfo{}, errors.New("no socket") }
	if code := runNotifyTestWith(
		[]string{"--config", path},
		&out,
		&errOut,
		notifyTestDeps{identity: down, env: nil, clock: nil},
	); code != 0 {
		t.Fatalf("exit %d %s", code, errOut.String())
	}
	body := string(rc.request(0).Body)
	if !strings.Contains(body, `"collector":{"name":"`) || strings.Contains(body, "tailscale_ip") {
		t.Fatalf("hostname fallback expected, body %s", body)
	}
}

func TestNotifyTestLocalOnlySkipsLocalAPI(t *testing.T) {
	rc := newReceiver(t)
	t.Setenv("TW_NT_A", rc.srv.URL)
	path := writeCfg(
		t,
		"collector:\n  local_only: true\ntargets:\n  - name: nas\n    addr: 10.0.0.1\n    checks: [icmp]\n"+
			"sinks:\n  - {name: hook, type: webhook, url_env: TW_NT_A}\n",
	)
	var calls atomic.Int32
	env := countingLocalEnv{netEnv: newKernelEnv(filepath.Join(t.TempDir(), "missing.sock")), calls: &calls}
	var out, errOut strings.Builder
	if code := runNotifyTestWith(
		[]string{"--config", path},
		&out,
		&errOut,
		notifyTestDeps{identity: nil, env: env, clock: nil},
	); code != 0 {
		t.Fatalf("exit %d %s", code, errOut.String())
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("local-only notify-test reached the LocalAPI %d times", n)
	}
	body := string(rc.request(0).Body)
	if !strings.Contains(body, `"collector":{"name":"`) || strings.Contains(body, "tailscale_ip") {
		t.Fatalf("hostname identity expected, body %s", body)
	}
}

func TestNotifyTestUnsetSecretIsReported(t *testing.T) {
	t.Parallel()
	path := notifyTestConfig(t, "sinks:\n  - {name: hook, type: webhook, url_env: TW_NT_NEVER_SET}\n")
	var out, errOut strings.Builder
	code := runNotifyTestWith(
		[]string{"--config", path},
		&out,
		&errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil},
	)
	if code != 1 || !strings.Contains(out.String(), "TW_NT_NEVER_SET") {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
}

func TestNotifyTestTimeoutStopsRetrying(t *testing.T) {
	rc := newReceiver(t, 503, 503, 503, 503, 503, 503)
	t.Setenv("TW_NT_A", rc.srv.URL)
	path := notifyTestConfig(t, "sinks:\n  - {name: flaky, type: webhook, url_env: TW_NT_A}\n")
	var out, errOut strings.Builder
	start := time.Now()
	code := runNotifyTestWith([]string{"--config", path, "--timeout", "300ms"}, &out, &errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil})
	if code != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("exit %d after %s\n%s", code, time.Since(start), out.String())
	}
	if !strings.Contains(out.String(), "flaky webhook") {
		t.Fatalf("output %q", out.String())
	}
}

func TestNotifyTestHelpPrintsFlags(t *testing.T) {
	t.Parallel()
	var out, errOut strings.Builder
	if code := run([]string{"notify-test", "--help"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, flagName := range []string{"-sink", "-config", "-timeout"} {
		if !strings.Contains(errOut.String(), flagName) {
			t.Fatalf("help lacks %s:\n%s", flagName, errOut.String())
		}
	}
}

// A grouped sink sends notify-test as a one-event group with a bat_ id. Teams
// never prints the id, so a batched webhook, which sends it as a header, is
// where the id is observable.
func TestNotifyTestBatchedWebhookUsesBatchID(t *testing.T) {
	rc := newReceiver(t)
	t.Setenv("TW_NT_A", rc.srv.URL)
	path := notifyTestConfig(t, "\nsinks:\n  - {name: hook, type: webhook, url_env: TW_NT_A, batch: true}\n")
	var out, errOut strings.Builder
	if code := runNotifyTestWith([]string{"--config", path}, &out, &errOut,
		notifyTestDeps{identity: staticIdentity, env: nil, clock: nil}); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if rc.count() != 1 {
		t.Fatalf("requests %d", rc.count())
	}
	if id := rc.request(0).Header.Get("Tail-Watch-Batch-Id"); !strings.HasPrefix(id, "bat_") {
		t.Fatalf("Tail-Watch-Batch-Id %q, want a bat_ id", id)
	}
}
