package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	liveNotifyWait  = 2 * time.Minute
	liveLANPrefix   = "10.10.53.0/24"
	liveLANAddr     = "10.10.53.1"
	liveSlackEnvVar = "SLACK_WEBHOOK_URL"
)

// TestLiveNotifications runs the whole notification path on a real tailnet.
// It brings up the collector plus one fixture node per check (icmp, tsping,
// tcp, http, and a LAN target behind a subnet router), fails and heals each
// one, and asserts on what a signature-verifying webhook receiver in this
// process saw. It is off unless TAIL_WATCH_LIVE_TAILNET=1.
//
// Credentials and cleanup work as in TestLiveTailnetDocker. Slack is added
// when SLACK_WEBHOOK_URL is set, or when TAIL_WATCH_SLACK_PULUMI_ENV names a
// pulumi env with environmentVariables.SLACK_WEBHOOK_URL. Slack is one-way, so
// the test checks tail-watch's own sent and failed counters for that sink;
// look in the channel for the messages. TAIL_WATCH_LIVE_MENTION sets
// mention_on_page for that sink.
func TestLiveNotifications(t *testing.T) {
	t.Parallel()
	if os.Getenv("TAIL_WATCH_LIVE_TAILNET") == "" {
		t.Skip("set TAIL_WATCH_LIVE_TAILNET=1 to run the Docker tailnet test")
	}
	requireLiveTools(t)
	api := liveCreds(t)
	runID := liveHostPrefix + "-notify-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	t.Cleanup(func() { api.deleteRun(t, runID) })

	slack := liveSlackURL(t)
	rcv := newLiveReceiver(t)
	stack := newLiveStack(t, runID, api.authKey(t, liveTags()))
	stack.files = append(stack.files, filepath.Join(packageDir(t), "testdata", "compose.live-notify.yaml"))
	stack.env["TS_FX_PREFIX"] = runID
	fixtures := map[string]string{
		"fx-icmp": "TS_KEY_ICMP", "fx-tsping": "TS_KEY_TSPING", "fx-tcp": "TS_KEY_TCP",
		"fx-http": "TS_KEY_HTTP", "lan-router": "TS_KEY_LAN",
	}
	for service, keyVar := range fixtures {
		stack.env[keyVar] = api.authKey(t, nil)
		stack.services = append(stack.services, service)
	}
	stack.env["OPS_WEBHOOK_URL"] = rcv.hookURL
	stack.env["OPS_WEBHOOK_SECRET"] = rcv.secret
	stack.env["HEARTBEAT_URL"] = rcv.beatURL
	if slack != "" {
		stack.env[liveSlackEnvVar] = slack
	}

	stack.start(t)
	enableLiveRoute(t, api, runID+"-lan", liveLANPrefix)
	requireKernelMode(t, stack)
	waitLiveRoute(t, stack, liveLANAddr)
	ips := map[string]string{}
	for _, service := range []string{"fx-icmp", "fx-tsping", "fx-tcp", "fx-http"} {
		ips[service] = liveFixtureIP(t, stack, service)
	}
	stack.env["TAIL_WATCH_CONFIG_FILE"] = writeNotifyConfig(t, ips, slack != "")
	stack.output(t, "up", "--detach", "--no-deps", "tail-watch")

	rcv.waitFor(t, "collector.started", func(e Event) bool { return e.Type == EventStarted })
	rcv.waitBeats(t, 2)

	scenarios := []liveScenario{
		{
			target: "fx-icmp", check: "icmp",
			fail: func() {
				stack.output(
					t,
					"exec",
					"--no-TTY",
					"fx-icmp",
					"iptables",
					"-I",
					"INPUT",
					"1",
					"-p",
					"icmp",
					"-j",
					"DROP",
				)
			},
			heal: func() {
				stack.output(t, "exec", "--no-TTY", "fx-icmp", "iptables", "-D", "INPUT", "-p", "icmp", "-j", "DROP")
			},
		},
		{
			target: "fx-tsping", check: "tsping",
			fail: func() { stack.output(t, "pause", "fx-tsping") },
			heal: func() { stack.output(t, "unpause", "fx-tsping") },
		},
		{
			target: "fx-tcp",
			check:  "tcp:9000",
			fail:   func() { stack.output(t, "exec", "--no-TTY", "fx-tcp", "sh", "-c", "touch /tmp/down; pkill nc || true") },
			heal:   func() { stack.output(t, "exec", "--no-TTY", "fx-tcp", "rm", "-f", "/tmp/down") },
		},
		{
			target: "fx-http", check: "http:",
			fail: func() {
				stack.output(t, "exec", "--no-TTY", "fx-http", "sh", "-c", "touch /tmp/down; pkill nc || true")
			},
			heal: func() { stack.output(t, "exec", "--no-TTY", "fx-http", "rm", "-f", "/tmp/down") },
		},
		{
			target: "fx-lan", check: "icmp",
			fail: func() {
				stack.output(
					t,
					"exec",
					"--no-TTY",
					"lan-router",
					"iptables",
					"-I",
					"INPUT",
					"1",
					"-p",
					"icmp",
					"-j",
					"DROP",
				)
			},
			heal: func() {
				stack.output(t, "exec", "--no-TTY", "lan-router", "iptables", "-D", "INPUT", "-p", "icmp", "-j", "DROP")
			},
		},
	}
	for _, sc := range scenarios {
		down, rec := sc.run(t, rcv, runID)
		t.Logf("%s: down layer=%q check=%q error=%q; recovered after %ds",
			sc.target, down.Layer, down.Check, down.Error, *rec.DurationSeconds)
	}

	restartMidOutage(t, stack, rcv, scenarios[2])

	out := stack.output(t, "run", "--rm", "--no-deps", "--no-TTY", "tail-watch", "notify-test")
	t.Logf("notify-test:\n%s", strings.TrimSpace(redactSecrets(string(out))))
	if !strings.Contains(string(out), "ops-webhook webhook 200") {
		t.Fatalf("notify-test did not report the webhook sink as 200:\n%s", out)
	}
	if slack != "" && !strings.Contains(string(out), "ops-slack slack 200") {
		t.Fatalf("notify-test did not report the Slack sink as 200:\n%s", out)
	}

	metrics := string(stack.output(t, "exec", "--no-TTY", "tailscale", "wget", "-qO-", "http://127.0.0.1:9109/metrics"))
	assertSinkCounters(t, metrics, "ops-webhook")
	if slack != "" {
		assertSinkCounters(t, metrics, "ops-slack")
	}
	if bad := rcv.badSignatures(); bad != 0 {
		t.Fatalf("%d webhook requests failed signature verification", bad)
	}
	t.Logf("verified %d webhook events, %d heartbeats; slack=%v", rcv.eventCount(), rcv.beatCount(), slack != "")
}

// liveScenario fails one fixture, expects exactly one down for its target, heals
// it, and expects the matching recovery.
type liveScenario struct {
	target string
	check  string
	fail   func()
	heal   func()
}

func (sc liveScenario) run(t *testing.T, rcv *liveReceiver, runID string) (Event, Event) {
	t.Helper()
	sc.fail()
	down := rcv.waitFor(t, sc.target+" down", func(e Event) bool {
		return e.Type == EventDown && e.targetLabel() == sc.target && !rcv.hasRecovery(e.ID)
	})
	if !strings.HasPrefix(down.Check, sc.check) || down.Severity != SeverityPage || down.Dependents == nil ||
		down.Collector.Name != runID || down.Collector.TailscaleIP == "" {
		t.Fatalf("%s: unexpected down payload %+v", sc.target, down)
	}
	sc.heal()
	rec := rcv.waitFor(t, sc.target+" recovered", func(e Event) bool {
		return e.Type == EventRecovered && e.DownEventID == down.ID
	})
	if rec.DurationSeconds == nil || rec.Layer != "" {
		t.Fatalf("%s: unexpected recovery payload %+v", sc.target, rec)
	}
	if n := rcv.countDowns(sc.target); n != 1 {
		t.Fatalf("%s: %d down events, want 1", sc.target, n)
	}
	return down, rec
}

func (e Event) targetLabel() string {
	if e.Target == nil {
		return ""
	}
	return e.Target.Name
}

// restartMidOutage checks that a restart during an outage neither pages again
// nor loses the recovery: the disk queue and open incident survive.
func restartMidOutage(t *testing.T, stack *liveStack, rcv *liveReceiver, sc liveScenario) {
	t.Helper()
	before := rcv.countDowns(sc.target)
	sc.fail()
	down := rcv.waitFor(t, "restart scenario down", func(e Event) bool {
		return e.Type == EventDown && e.targetLabel() == sc.target && !rcv.hasRecovery(e.ID)
	})
	starts := rcv.countType(EventStarted)
	stack.output(t, "restart", "tail-watch")
	rcv.waitFor(t, "collector.started after restart", func(Event) bool { return rcv.countType(EventStarted) > starts })
	time.Sleep(20 * time.Second)
	if n := rcv.countDowns(sc.target); n != before+1 {
		t.Fatalf("restart paged again: %d downs for %s, want %d", n, sc.target, before+1)
	}
	sc.heal()
	rcv.waitFor(t, "recovery after restart", func(e Event) bool {
		return e.Type == EventRecovered && e.DownEventID == down.ID
	})
}

func assertSinkCounters(t *testing.T, metrics, sink string) {
	t.Helper()
	sent, unexpected := 0, 0
	for line := range strings.SplitSeq(metrics, "\n") {
		if !strings.Contains(line, `sink="`+sink+`"`) {
			continue
		}
		switch {
		case strings.HasPrefix(line, "tailwatch_notify_sent_total{"):
			sent += metricValue(line)
		case strings.HasPrefix(line, "tailwatch_notify_failed_total{"),
			strings.HasPrefix(line, "tailwatch_notify_dropped_total{"):
			t.Logf("sink %s: %s", sink, line)
			// A memory queue loses events still waiting when the collector restarts.
			if metricValue(line) > 0 && !strings.Contains(line, `reason="restart"`) {
				unexpected += metricValue(line)
			}
		}
	}
	t.Logf("sink %s: sent=%d unexpected failures or drops=%d", sink, sent, unexpected)
	if sent == 0 || unexpected != 0 {
		t.Fatalf("sink %s: sent=%d unexpected=%d", sink, sent, unexpected)
	}
}

func metricValue(line string) int {
	f := strings.Fields(line)
	v, _ := strconv.ParseFloat(f[len(f)-1], 64)
	return int(v)
}

func liveFixtureIP(t *testing.T, stack *liveStack, service string) string {
	t.Helper()
	var ip string
	ok := pollUntil(60*time.Second, func() bool {
		out, err := stack.command(t.Context(), "exec", "--no-TTY", service, "tailscale", "ip", "-4").Output()
		ip = strings.TrimSpace(string(out))
		return err == nil && ip != ""
	})
	if !ok {
		t.Fatalf("%s has no tailnet address", service)
	}
	return firstIPv4(strings.Fields(ip))
}

func writeNotifyConfig(t *testing.T, ips map[string]string, withSlack bool) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`collector:
  listen: 127.0.0.1:9109
  state_dir: /var/lib/tail-watch
defaults:
  interval: 5s
  timeout: 2s
  fail_threshold: 2
  recover_threshold: 2
targets:
`)
	fmt.Fprintf(&b, "  - {name: fx-icmp, addr: %s, checks: [icmp]}\n", ips["fx-icmp"])
	fmt.Fprintf(&b, "  - {name: fx-tsping, addr: %s, checks: [tsping]}\n", ips["fx-tsping"])
	fmt.Fprintf(&b, "  - {name: fx-tcp, addr: %s, checks: [\"tcp:9000\"]}\n", ips["fx-tcp"])
	fmt.Fprintf(&b, "  - {name: fx-http, addr: %s, checks: [\"http:http://%s:8080/|tail-watch-ok\"]}\n",
		ips["fx-http"], ips["fx-http"])
	fmt.Fprintf(&b, "  - {name: fx-lan, addr: %s, checks: [icmp]}\n", liveLANAddr)
	b.WriteString(`sinks:
  - name: ops-webhook
    type: webhook
    url_env: OPS_WEBHOOK_URL
    secret_env: OPS_WEBHOOK_SECRET
    queue: disk
    group_wait: 2s
`)
	if withSlack {
		b.WriteString("  - name: ops-slack\n    type: slack\n    url_env: SLACK_WEBHOOK_URL\n    group_wait: 2s\n")
		if mention := os.Getenv("TAIL_WATCH_LIVE_MENTION"); mention != "" {
			fmt.Fprintf(&b, "    mention_on_page: %q\n", mention)
		}
	}
	b.WriteString("heartbeat:\n  url_env: HEARTBEAT_URL\n  interval: 5s\n")
	path := filepath.Join(t.TempDir(), "tail-watch.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// liveSlackURL returns the Slack webhook URL from the environment or a pulumi
// env, or "" to run without Slack. It is never logged.
func liveSlackURL(t *testing.T) string {
	t.Helper()
	if v := os.Getenv(liveSlackEnvVar); v != "" {
		return v
	}
	name := os.Getenv("TAIL_WATCH_SLACK_PULUMI_ENV")
	if name == "" {
		t.Log("no Slack webhook configured; running the webhook sink only")
		return ""
	}
	out, err := exec.CommandContext(t.Context(), "pulumi", "env", "open", name, "--format", "json").Output()
	if err != nil {
		t.Fatalf("pulumi env open %s: %v", name, err)
	}
	var env struct {
		Vars map[string]string `json:"environmentVariables"`
	}
	if uerr := json.Unmarshal(out, &env); uerr != nil || env.Vars[liveSlackEnvVar] == "" {
		t.Fatalf("%s has no environmentVariables.%s", name, liveSlackEnvVar)
	}
	return env.Vars[liveSlackEnvVar]
}

// liveReceiver is the webhook and heartbeat receiver the collector posts to. It
// runs in the test process and verifies every signature.
type liveReceiver struct {
	hookURL, beatURL, secret string

	mu     sync.Mutex
	events []Event
	beats  int
	bad    int
}

func newLiveReceiver(t *testing.T) *liveReceiver {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	r := &liveReceiver{
		hookURL: "", beatURL: "", secret: hex.EncodeToString(raw),
		mu: sync.Mutex{}, events: nil, beats: 0, bad: 0,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/hook", r.handleHook)
	mux.HandleFunc("/beat", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		r.beats++
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	r.hookURL = fmt.Sprintf("http://host.docker.internal:%d/hook", port)
	r.beatURL = fmt.Sprintf("http://host.docker.internal:%d/beat", port)
	return r
}

func (r *liveReceiver) handleHook(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !verifySignature(r.secret, req.Header.Get(signatureHeader), body, time.Now(), 5*time.Minute) {
		r.bad++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var ev Event
	if jerr := json.Unmarshal(body, &ev); jerr != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.events = append(r.events, ev)
	w.WriteHeader(http.StatusOK)
}

func (r *liveReceiver) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *liveReceiver) waitFor(t *testing.T, what string, match func(Event) bool) Event {
	t.Helper()
	var found Event
	if !pollUntil(liveNotifyWait, func() bool {
		for _, e := range r.snapshot() {
			if match(e) {
				found = e
				return true
			}
		}
		return false
	}) {
		t.Fatalf("timed out waiting for %s; saw %s", what, r.summary())
	}
	return found
}

func (r *liveReceiver) waitBeats(t *testing.T, n int) {
	t.Helper()
	if !pollUntil(liveNotifyWait, func() bool { return r.beatCount() >= n }) {
		t.Fatalf("saw %d heartbeats, want %d", r.beatCount(), n)
	}
}

func (r *liveReceiver) summary() string {
	var parts []string
	for _, e := range r.snapshot() {
		part := string(e.Type) + ":" + e.targetLabel()
		if e.Type == EventDown {
			part += fmt.Sprintf("[layer=%s error=%q]", e.Layer, truncate(e.Error, 90))
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

func (r *liveReceiver) hasRecovery(downID string) bool {
	return slices.ContainsFunc(r.snapshot(), func(e Event) bool {
		return e.Type == EventRecovered && e.DownEventID == downID
	})
}

func (r *liveReceiver) countDowns(target string) int {
	n := 0
	for _, e := range r.snapshot() {
		if e.Type == EventDown && e.targetLabel() == target {
			n++
		}
	}
	return n
}

func (r *liveReceiver) countType(typ EventType) int {
	n := 0
	for _, e := range r.snapshot() {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func (r *liveReceiver) eventCount() int { return len(r.snapshot()) }

func (r *liveReceiver) beatCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beats
}

func (r *liveReceiver) badSignatures() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bad
}
