package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	liveSetecWait    = 3 * time.Minute
	liveSetecURLName = "live/ops-url"
	liveSetecKeyName = "live/ops-hmac"
	liveSetecHBName  = "live/hb-url"
	liveSetecSlack   = "live/slack-url"
)

// TestLiveSetec runs the collector with every secret in a setec server, on a
// real tailnet. The server runs in --dev mode as its own tsnet node. The test
// seeds three secrets, checks that signed webhook posts and heartbeats arrive,
// then rotates the heartbeat URL in setec and waits for pings to reach a second
// receiver without restarting the collector. It also runs notify-test. It is
// off unless TAIL_WATCH_LIVE_TAILNET=1, and needs the tag:secrets node and the
// grants in testdata/snippet.hujson (see docs/contributing/live-notification-test.md).
func TestLiveSetec(t *testing.T) {
	t.Parallel()
	if os.Getenv("TAIL_WATCH_LIVE_TAILNET") == "" {
		t.Skip("set TAIL_WATCH_LIVE_TAILNET=1 to run the Docker tailnet test")
	}
	requireLiveTools(t)
	if len(liveTags()) == 0 {
		t.Fatal("TestLiveSetec needs the collector tagged tag:monitor: leave TAIL_WATCH_LIVE_TAG unset")
	}
	api := liveCreds(t)
	runID := liveHostPrefix + "-setec-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	t.Cleanup(func() { api.deleteRun(t, runID) })

	setecBin := buildLiveSetec(t)
	first, second := newLiveReceiver(t), newLiveReceiver(t)
	stack := newLiveStack(t, runID, api.authKey(t, liveTags()))
	stack.files = append(stack.files, filepath.Join(packageDir(t), "testdata", "compose.live-notify.yaml"))
	stack.services = append(stack.services, "setec")
	stack.env["TS_FX_PREFIX"] = runID
	stack.env["SETEC_BIN"] = setecBin
	stack.env["TS_LIVE_ACCEPT_DNS"] = "true"
	for _, key := range []string{"TS_KEY_ICMP", "TS_KEY_TSPING", "TS_KEY_TCP", "TS_KEY_HTTP", "TS_KEY_LAN", "TS_KEY_SETEC"} {
		stack.env[key] = "unused"
	}
	stack.env["TS_KEY_SETEC"] = api.authKey(t, []string{liveSetecTag()})

	stack.start(t)
	requireKernelMode(t, stack)
	slackURL := liveSlackURL(t)
	server := "https://" + liveSetecName(t, api, runID+"-setec")

	stack.env["SETEC_SERVER"] = server
	stack.setecPut(t, liveSetecURLName, first.hookURL, 1)
	stack.setecPut(t, liveSetecKeyName, first.secret, 1)
	stack.setecPut(t, liveSetecHBName, first.beatURL, 1)

	if slackURL != "" {
		stack.setecPut(t, liveSetecSlack, slackURL, 1)
	}

	stack.env["TAIL_WATCH_CONFIG_FILE"] = writeSetecConfig(t, server, slackURL != "")
	stack.output(t, "up", "--detach", "--no-deps", "tail-watch")

	first.waitFor(t, "collector.started", func(e Event) bool { return e.Type == EventStarted })
	first.waitBeats(t, 2)

	out := stack.output(t, "run", "--rm", "--no-deps", "--no-TTY", "tail-watch", "notify-test")
	t.Logf("notify-test:\n%s", strings.TrimSpace(redactSecrets(string(out))))
	if !strings.Contains(string(out), "ops-webhook webhook 200") {
		t.Fatalf("notify-test did not report the webhook sink as 200:\n%s", redactSecrets(string(out)))
	}

	if slackURL != "" && !strings.Contains(string(out), "ops-slack slack 200") {
		t.Fatalf("notify-test did not report the Slack sink as 200:\n%s", redactSecrets(string(out)))
	}

	stack.setecPut(t, liveSetecHBName, second.beatURL, 2)
	if !pollUntil(liveSetecWait, func() bool { return second.beatCount() > 0 }) {
		logs, _ := stack.command(t.Context(), "logs", "--no-color", "tail-watch").CombinedOutput()
		t.Fatalf("no heartbeat reached the rotated URL within %s; the first receiver saw %d\n%s",
			liveSetecWait, first.beatCount(), redactSecrets(string(logs)))
	}
	if bad := first.badSignatures(); bad != 0 {
		t.Fatalf("%d webhook requests failed signature verification", bad)
	}
	t.Logf("setec: %d signed events, %d heartbeats before rotation, %d after",
		first.eventCount(), first.beatCount(), second.beatCount())
}

func liveSetecTag() string {
	if tag := os.Getenv("TAIL_WATCH_LIVE_SETEC_TAG"); tag != "" {
		return tag
	}
	return "tag:secrets"
}

func buildLiveSetec(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.CommandContext(t.Context(), filepath.Join(repoRoot(t), "scripts", "build-setec.sh"), dir).Output()
	if err != nil {
		t.Fatalf("build setec: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// liveSetecName waits for the setec node to register and returns its MagicDNS
// name without the trailing dot.
func liveSetecName(t *testing.T, api *liveAPI, hostname string) string {
	t.Helper()
	var name string
	if !pollUntil(2*time.Minute, func() bool {
		devices, err := api.devices(t.Context())
		if err != nil {
			return false
		}
		for _, d := range devices {
			if strings.EqualFold(d.Hostname, hostname) && d.Name != "" {
				name = strings.TrimSuffix(d.Name, ".")
				return true
			}
		}
		return false
	}) {
		t.Fatalf("setec node %s never registered", hostname)
	}
	return name
}

// setecPut stores value under name from the collector's network namespace and
// activates it when version is not 1. The first request can take over thirty
// seconds while the server obtains a certificate, so it retries. The value and
// server reach the container through the environment, not the command line.
func (s *liveStack) setecPut(t *testing.T, name, value string, version int) {
	t.Helper()
	s.env["SETEC_NAME"] = name
	s.env["SETEC_VALUE"] = value
	s.env["SETEC_VERSION"] = strconv.Itoa(version)
	const script = `printf %s "$SETEC_VALUE" | /setec -s "$SETEC_SERVER" put "$SETEC_NAME" && ` +
		`{ [ "$SETEC_VERSION" = 1 ] || /setec -s "$SETEC_SERVER" activate "$SETEC_NAME" "$SETEC_VERSION"; }`
	var last string
	if !pollUntil(3*time.Minute, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		out, err := s.command(ctx, "run", "--rm", "--no-deps", "--no-TTY",
			"-e", "SETEC_NAME", "-e", "SETEC_VALUE", "-e", "SETEC_VERSION", "-e", "SETEC_SERVER",
			"setec-cli", "-c", script).CombinedOutput()
		last = redactSecrets(string(out))
		return err == nil
	}) {
		logs, _ := s.command(t.Context(), "logs", "--no-color", "setec").CombinedOutput()
		t.Fatalf("setec put %s: %s\nsetec logs:\n%s", name, last, redactSecrets(string(logs)))
	}
}

func writeSetecConfig(t *testing.T, server string, withSlack bool) string {
	t.Helper()
	slackSink := ""
	if withSlack {
		slackSink = "  - name: ops-slack\n    type: slack\n    url_setec: " + liveSetecSlack + "\n    group_wait: 2s\n"
	}
	body := fmt.Sprintf(`collector:
  listen: 127.0.0.1:9109
  state_dir: /var/lib/tail-watch
  setec:
    server: %s
    poll_interval: 1m
defaults:
  interval: 5s
  timeout: 2s
targets:
  - {name: self, addr: 127.0.0.1, network: local, checks: ["tcp:9109"]}
sinks:
  - name: ops-webhook
    type: webhook
    url_setec: %s
    secret_setec: %s
    queue: disk
    group_wait: 2s
%sheartbeat:
  url_setec: %s
  interval: 5s
`, server, liveSetecURLName, liveSetecKeyName, slackSink, liveSetecHBName)
	path := filepath.Join(t.TempDir(), "tail-watch.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
