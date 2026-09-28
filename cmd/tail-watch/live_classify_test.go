package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The classification test uses its own prefix so that, when both live tests
// run in parallel, their routers never form an HA pair for one route.
const (
	liveClassifyPrefix = "10.10.51.0/24"
	liveClassifyAddr   = "10.10.51.1"
	liveFaultTimeout   = 3 * time.Minute
	// Control marks a stopped node disconnected within seconds, but peers'
	// netmaps have been seen to receive Online=false only after two to more
	// than three minutes. Until then the collector correctly reports tunnel.
	liveControlTimeout = 8 * time.Minute
	liveMetricsURL     = "http://127.0.0.1:9109/metrics"
	liveLayerMetric    = "tailwatch_failure_layer_total"
)

// TestLiveTailnetClassification runs the collector as a daemon beside an
// ephemeral subnet router and injects two faults, reading the layer from
// tailwatch_failure_layer_total:
//
//   - the router's LAN address is removed, so the device behind the router
//     fails while the router stays reachable: subnet reports lan and the
//     router target reports nothing.
//   - the router container is stopped: the router target and the subnet
//     target both reach control, after reporting tunnel until control's
//     Online=false reaches the collector's netmap, which can take minutes.
//
// It is off unless TAIL_WATCH_LIVE_TAILNET=1 and has the same credential and
// policy requirements as TestLiveTailnetDocker.
func TestLiveTailnetClassification(t *testing.T) {
	t.Parallel()
	if os.Getenv("TAIL_WATCH_LIVE_TAILNET") == "" {
		t.Skip("set TAIL_WATCH_LIVE_TAILNET=1 to run the Docker tailnet test")
	}
	requireLiveTools(t)

	api := liveCreds(t)
	runID := liveHostPrefix + "-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	t.Cleanup(func() { api.deleteRun(t, runID) })

	routerHost := runID + "-router"
	stack := newLiveStack(t, runID, api.authKey(t, liveTags()))
	stack.addRouter(t, routerHost, api.authKey(t, nil))
	stack.env["TS_ROUTER_PREFIX"] = liveClassifyPrefix
	stack.env["TS_ROUTER_ADDR"] = liveClassifyAddr
	stack.env["TAIL_WATCH_CONFIG_FILE"] = writeClassifyConfig(t, routerHost)

	stack.start(t)
	enableLiveRoute(t, api, routerHost, liveClassifyPrefix)
	requireKernelMode(t, stack)
	waitLiveRoute(t, stack, liveClassifyAddr)
	stack.output(t, "up", "--detach", "--no-deps", "tail-watch")
	waitProbesPass(t, stack, "router/tsping", "subnet/icmp")

	before := stack.metrics(t)
	stack.output(t, "exec", "--no-TTY", "router", "ip", "addr", "del", liveClassifyAddr+"/32", "dev", "lo")
	waitLayer(t, stack, before, "subnet", layerLAN)
	lanPhase := stack.metrics(t)
	if got := lanPhase.failures("router") - before.failures("router"); got != 0 {
		t.Errorf("router target recorded %v failures while only the LAN device was down\n%s",
			got, lanPhase.lines(liveLayerMetric))
	}
	t.Logf("after LAN fault:\n%s", lanPhase.lines(liveLayerMetric))

	stack.output(t, "exec", "--no-TTY", "router", "ip", "addr", "add", liveClassifyAddr+"/32", "dev", "lo")
	waitProbesPass(t, stack, "subnet/icmp")

	before = stack.metrics(t)
	stack.output(t, "stop", "router")
	waitLayerWithin(t, stack, before, "router", layerControl, liveControlTimeout)
	waitLayerWithin(t, stack, before, "subnet", layerControl, liveControlTimeout)
	downPhase := stack.metrics(t)
	for _, target := range []string{"router", "subnet"} {
		for _, l := range []layer{layerLAN, layerService, layerUnrouted} {
			if got := downPhase.layer(target, l) - before.layer(target, l); got != 0 {
				t.Errorf("%s recorded %v %s failures while the router was down", target, got, l)
			}
		}
	}
	t.Logf("after router stop:\n%s", downPhase.lines(liveLayerMetric))
}

func writeClassifyConfig(t *testing.T, routerHost string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "tail-watch.yaml")
	body := fmt.Sprintf(`
targets:
  - name: router
    addr: %s
    network: tailnet
    interval: 5s
    timeout: 3s
    checks: [tsping]
  - name: subnet
    addr: %s
    network: tailnet
    parent: router
    interval: 5s
    timeout: 3s
    checks: [icmp]
`, routerHost, liveClassifyAddr)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// metrics scrapes the collector from inside the tailscale service's network
// namespace, which the tail-watch service shares.
func (s *liveStack) metrics(t *testing.T) liveMetrics {
	t.Helper()
	return liveMetrics(s.output(t, "exec", "--no-TTY", "tailscale", "wget", "-q", "-O", "-", liveMetricsURL))
}

func (s *liveStack) tryMetrics(t *testing.T) (liveMetrics, error) {
	out, err := s.command(t.Context(), "exec", "--no-TTY", "tailscale", "wget", "-q", "-O", "-", liveMetricsURL).
		Output()
	return liveMetrics(out), err
}

func waitProbesPass(t *testing.T, stack *liveStack, probes ...string) {
	t.Helper()
	var last liveMetrics
	ok := pollUntil(liveFaultTimeout, func() bool {
		m, err := stack.tryMetrics(t)
		if err != nil {
			return false
		}
		last = m
		for _, p := range probes {
			if m.value("tailwatch_result", "name", p) != 1 {
				return false
			}
		}
		return true
	})
	if !ok {
		t.Fatalf("probes %v did not pass\n%s\n%s", probes, last.lines("tailwatch_result"), stack.collectorLogs(t))
	}
}

func waitLayer(t *testing.T, stack *liveStack, before liveMetrics, target string, want layer) {
	t.Helper()
	waitLayerWithin(t, stack, before, target, want, liveFaultTimeout)
}

func waitLayerWithin(
	t *testing.T, stack *liveStack, before liveMetrics, target string, want layer, timeout time.Duration,
) {
	t.Helper()
	start := time.Now()
	var last liveMetrics
	ok := pollUntil(timeout, func() bool {
		m, err := stack.tryMetrics(t)
		if err != nil {
			return false
		}
		last = m
		return m.layer(target, want) > before.layer(target, want)
	})
	if !ok {
		t.Fatalf("%s never classified %s\n%s\n%s", target, want, last.lines(liveLayerMetric), stack.collectorLogs(t))
	}
	t.Logf("%s classified %s after %s", target, want, time.Since(start).Round(time.Second))
}

func (s *liveStack) collectorLogs(t *testing.T) string {
	out, _ := s.command(t.Context(), "logs", "--no-color", "--tail", "50", "tail-watch").CombinedOutput()
	return redactSecrets(string(out))
}

// liveMetrics is a Prometheus text exposition scraped from the collector.
type liveMetrics string

func (m liveMetrics) layer(target string, l layer) float64 {
	return m.value(liveLayerMetric, "target", target, "layer", string(l))
}

func (m liveMetrics) failures(target string) float64 {
	return m.value(liveLayerMetric, "target", target)
}

// value sums the samples of name whose labels include every key/value pair
// in kv.
func (m liveMetrics) value(name string, kv ...string) float64 {
	var sum float64
	for line := range strings.SplitSeq(string(m), "\n") {
		labels, ok := strings.CutPrefix(line, name+"{")
		if !ok {
			continue
		}
		before, after, ok := strings.CutLast(labels, "}")
		if !ok {
			continue
		}
		matched := true
		for i := 0; i+1 < len(kv); i += 2 {
			if !strings.Contains(before, kv[i]+`="`+kv[i+1]+`"`) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		fields := strings.Fields(after)
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
			sum += v
		}
	}
	return sum
}

func (m liveMetrics) lines(name string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(string(m), "\n") {
		if strings.HasPrefix(line, name+"{") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TestLiveMetricsValue(t *testing.T) {
	t.Parallel()
	m := liveMetrics(`# HELP tailwatch_failure_layer_total Failed results by layer.
# TYPE tailwatch_failure_layer_total counter
tailwatch_failure_layer_total{layer="control",target="router"} 2
tailwatch_failure_layer_total{layer="tunnel",target="router"} 1
tailwatch_failure_layer_total{layer="control",target="subnet"} 4
tailwatch_failure_layer_total{layer="lan",target="subnet-2"} 9
tailwatch_result{class="",name="subnet/icmp"} 1
`)
	tests := []struct {
		name string
		kv   []string
		want float64
	}{
		{liveLayerMetric, []string{"target", "router", "layer", "control"}, 2},
		{liveLayerMetric, []string{"target", "router"}, 3},
		{liveLayerMetric, []string{"target", "subnet"}, 4},
		{liveLayerMetric, []string{"target", "subnet", "layer", "lan"}, 0},
		{"tailwatch_result", []string{"name", "subnet/icmp"}, 1},
		{"tailwatch_result", []string{"name", "router/tsping"}, 0},
	}
	for _, tt := range tests {
		if got := m.value(tt.name, tt.kv...); got != tt.want {
			t.Errorf("value(%s, %v) = %v, want %v", tt.name, tt.kv, got, tt.want)
		}
	}
}
