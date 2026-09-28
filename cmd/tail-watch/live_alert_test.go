package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	liveAlertPrefix = "10.10.52.0/24"
	liveAlertAddr   = "10.10.52.1"
)

// TestLiveTailnetAlerts stops and restarts its own ephemeral subnet router.
// It verifies transition logs as well as the suppression and recovery gauges.
func TestLiveTailnetAlerts(t *testing.T) {
	t.Parallel()
	if os.Getenv("TAIL_WATCH_LIVE_TAILNET") == "" {
		t.Skip("set TAIL_WATCH_LIVE_TAILNET=1 to run the Docker tailnet test")
	}
	requireLiveTools(t)
	api := liveCreds(t)
	runID := liveHostPrefix + "-alert-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	t.Cleanup(func() { api.deleteRun(t, runID) })
	routerHost := runID + "-router"
	stack := newLiveStack(t, runID, api.authKey(t, liveTags()))
	stack.addRouter(t, routerHost, api.authKey(t, nil))
	stack.env["TS_ROUTER_PREFIX"] = liveAlertPrefix
	stack.env["TS_ROUTER_ADDR"] = liveAlertAddr
	stack.env["TAIL_WATCH_CONFIG_FILE"] = writeAlertConfig(t, routerHost)
	stack.start(t)
	enableLiveRoute(t, api, routerHost, liveAlertPrefix)
	requireKernelMode(t, stack)
	waitLiveRoute(t, stack, liveAlertAddr)
	stack.output(t, "up", "--detach", "--no-deps", "tail-watch")
	waitAlertStates(t, stack, StateUp, StateUp)
	stack.output(t, "stop", "router")
	waitAlertStates(t, stack, StateDown, StateSuppressed)
	stack.output(t, "start", "router")
	waitAlertStates(t, stack, StateUp, StateUp)
	logs := string(stack.output(t, "logs", "--no-color", "--no-log-prefix", "tail-watch"))
	var transitions []liveTransition
	for line := range strings.SplitSeq(logs, "\n") {
		var tr liveTransition
		if json.Unmarshal([]byte(line), &tr) == nil && tr.Message == "alert transition" {
			transitions = append(transitions, tr)
		}
	}
	if len(transitions) != 2 {
		t.Fatalf("want one down and one recovery, got %+v\n%s", transitions, redactSecrets(logs))
	}
	for i, tr := range transitions {
		want := StateDown.String()
		if i == 1 {
			want = StateUp.String()
		}
		if tr.Target != "router" || tr.To != want || tr.Flapping ||
			!slices.Equal(tr.Dependents, []string{"device-a", "device-b"}) {
			t.Fatalf("unexpected transition: %+v", tr)
		}
	}
	t.Logf("verified one router DOWN and one UP, both listing device-a/device-b; dependents recovered silently")
}

type liveTransition struct {
	Message    string   `json:"msg"`
	Target     string   `json:"target"`
	To         string   `json:"to"`
	Flapping   bool     `json:"flapping"`
	Dependents []string `json:"dependents"`
}

func writeAlertConfig(t *testing.T, routerHost string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "tail-watch.yaml")
	body := fmt.Sprintf(`
targets:
  - name: router
    addr: %s
    network: tailnet
    interval: 5s
    timeout: 3s
    fail_threshold: 2
    recover_threshold: 2
    checks: [tsping]
  - name: device-a
    addr: %s
    network: tailnet
    parent: router
    interval: 2s
    timeout: 1s
    fail_threshold: 2
    checks: [icmp]
  - name: device-b
    addr: %s
    network: tailnet
    parent: router
    interval: 2s
    timeout: 1s
    fail_threshold: 2
    checks: [icmp]
`, routerHost, liveAlertAddr, liveAlertAddr)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func waitAlertStates(t *testing.T, stack *liveStack, parent, child State) {
	t.Helper()
	var last liveMetrics
	ok := pollUntil(liveFaultTimeout, func() bool {
		metrics, err := stack.tryMetrics(t)
		if err != nil {
			return false
		}
		last = metrics
		return metrics.value("tailwatch_target_state", "target", "router") == float64(parent) &&
			metrics.value("tailwatch_target_state", "target", "device-a") == float64(child) &&
			metrics.value("tailwatch_target_state", "target", "device-b") == float64(child)
	})
	if !ok {
		t.Fatalf(
			"states never reached %s/%s\n%s\n%s",
			parent,
			child,
			last.lines("tailwatch_target_state"),
			stack.collectorLogs(t),
		)
	}
}
