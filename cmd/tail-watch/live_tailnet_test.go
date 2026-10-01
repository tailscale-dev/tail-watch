package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	liveHostPrefix     = "tail-watch-live"
	liveCleanupTimeout = 60 * time.Second
	liveRouterPrefix   = "10.10.50.0/24"
	liveRouterAddr     = "10.10.50.1"
)

// TestLiveTailnetDocker brings up examples/compose.yaml with an ephemeral
// auth key from `pulumi env`, then runs the tail-watch service once. It is off
// unless TAIL_WATCH_LIVE_TAILNET=1 because it joins a tailnet.
//
// Credentials come from TAIL_WATCH_PULUMI_ENV, or tailscale-phase-2/tailscale.
// The collector key is tagged TAIL_WATCH_LIVE_TAG (default tag:monitor, empty for
// an untagged node), so the tailnet policy must own that tag and grant it
// the peer and subnet; testdata/snippet.hujson has those policy additions.
// TAIL_WATCH_LIVE_PEER and TAIL_WATCH_LIVE_SUBNET override the discovered targets.
// When no connected device has an enabled subnet route, the test merges
// testdata/compose.live-router.yaml, which adds a router for 10.10.50.0/24
// that answers 10.10.50.1. tsping and icmp must pass. The TCP check must
// print a line; a closed port on the peer is still a result.
func TestLiveTailnetDocker(t *testing.T) {
	t.Parallel()
	if os.Getenv("TAIL_WATCH_LIVE_TAILNET") == "" {
		t.Skip("set TAIL_WATCH_LIVE_TAILNET=1 to run the Docker tailnet test")
	}
	requireLiveTools(t)

	api := liveCreds(t)
	runID := liveHostPrefix + "-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	t.Cleanup(func() { api.deleteRun(t, runID) })

	peer, subnet := liveTargets(t, api)
	stack := newLiveStack(t, runID, api.authKey(t, liveTags()))
	routerHost := ""
	if subnet == "" {
		routerHost = runID + "-router"
		stack.addRouter(t, routerHost, api.authKey(t, nil))
		subnet = liveRouterAddr
	}
	stack.env["TAIL_WATCH_CONFIG_FILE"] = writeLiveConfig(t, peer, subnet)

	stack.start(t)
	if routerHost != "" {
		enableLiveRoute(t, api, routerHost, liveRouterPrefix)
	}
	requireKernelMode(t, stack)
	waitLiveRoute(t, stack, subnet)

	stdout, stderr, err := stack.runOnce(t)
	assertLiveOutput(t, peer.name, stdout, stderr, err)
}

func liveTags() []string {
	tag, ok := os.LookupEnv("TAIL_WATCH_LIVE_TAG")
	if !ok {
		tag = "tag:monitor"
	}
	if tag == "" {
		return nil
	}
	return []string{tag}
}

func writeLiveConfig(t *testing.T, peer livePeer, subnet string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "tail-watch.yaml")
	body := fmt.Sprintf(`
targets:
  - name: %s
    addr: %s
    network: tailnet
    interval: 8s
    timeout: 5s
    checks: [tsping, tcp:22]
  - name: subnet
    addr: %s
    network: tailnet
    interval: 8s
    timeout: 5s
    checks: [icmp]
`, peer.name, peer.addr, subnet)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestFirstPrivateIPv4Host(t *testing.T) {
	t.Parallel()
	tests := []struct {
		prefix string
		want   string
	}{
		{"10.10.50.0/24", "10.10.50.1"},
		{"10.10.50.7/24", "10.10.50.1"},
		{"192.168.0.0/16", "192.168.0.1"},
		{"10.0.0.8/31", "10.0.0.8"},
		{"10.0.0.9/32", "10.0.0.9"},
		{"8.8.8.0/24", ""},
		{"fd00::/64", ""},
		{"0.0.0.0/0", ""},
		{"not-a-prefix", ""},
	}
	for _, tt := range tests {
		if got := firstPrivateIPv4Host(tt.prefix); got != tt.want {
			t.Errorf("firstPrivateIPv4Host(%q) = %q, want %q", tt.prefix, got, tt.want)
		}
	}
}

func TestChosenSubnetIPSkipsUnusableRoutes(t *testing.T) {
	devices := []tsDevice{
		{Hostname: "advertised-only", Connected: true, AdvertisedRoutes: []string{"10.1.0.0/24"},
			ID:            "",
			Addresses:     nil,
			EnabledRoutes: nil,
		},
		{Hostname: "offline", Connected: false, EnabledRoutes: []string{"10.2.0.0/24"},
			ID:               "",
			Addresses:        nil,
			AdvertisedRoutes: nil,
		},
		{Hostname: "tail-watch-live-1-router", Connected: true, EnabledRoutes: []string{"10.3.0.0/24"},
			ID:               "",
			Addresses:        nil,
			AdvertisedRoutes: nil,
		},
		{Hostname: "exit", Connected: true, EnabledRoutes: []string{"0.0.0.0/0"},
			ID:               "",
			Addresses:        nil,
			AdvertisedRoutes: nil,
		},
		{Hostname: "router", Connected: true, EnabledRoutes: []string{"10.4.0.0/24"},
			ID:               "",
			Addresses:        nil,
			AdvertisedRoutes: nil,
		},
	}
	t.Setenv("TAIL_WATCH_LIVE_SUBNET", "")
	if got := chosenSubnetIP(devices); got != "10.4.0.1" {
		t.Errorf("chosenSubnetIP = %q, want 10.4.0.1", got)
	}
	if got := chosenSubnetIP(devices[:4]); got != "" {
		t.Errorf("chosenSubnetIP without a usable route = %q, want empty", got)
	}
}

func TestIsRunDevice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		hostname string
		want     bool
	}{
		{"tail-watch-live-42", true},
		{"TAIL-WATCH-LIVE-42", true},
		{"tail-watch-live-42-router", true},
		{"tail-watch-live-421", false},
		{"tail-watch-live-7-router", false},
		{"splunk-relay", false},
	}
	for _, tt := range tests {
		if got := isRunDevice(tt.hostname, "tail-watch-live-42"); got != tt.want {
			t.Errorf("isRunDevice(%q) = %v, want %v", tt.hostname, got, tt.want)
		}
	}
}

func requireLiveTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"docker", "pulumi"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("%s is required for the live tailnet test: %v", bin, err)
		}
	}
	if err := exec.CommandContext(t.Context(), "docker", "compose", "version").Run(); err != nil {
		t.Fatalf("docker compose is required for the live tailnet test: %v", err)
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Fatal("kernel-mode tailscaled needs /dev/net/tun")
	}
}

// assertLiveOutput accepts a non-zero exit because --once exits 1 when any
// check fails, and a closed tcp:22 is allowed. Results are printed only after
// preflight passes, so a non-zero exit with empty stdout is a preflight
// failure.
func assertLiveOutput(t *testing.T, peer, stdout, stderr string, runErr error) {
	t.Helper()
	t.Logf("stdout:\n%s", stdout)
	t.Logf("stderr:\n%s", redactSecrets(stderr))
	if runErr != nil && strings.TrimSpace(stdout) == "" {
		t.Fatalf("preflight failed: %v\n%s", runErr, redactSecrets(stderr))
	}
	if !strings.Contains(stderr, "collector identity") {
		t.Fatal("preflight did not log the collector identity")
	}
	for _, want := range []string{peer + " tsping ok ", peer + " tcp:22 ", "subnet icmp ok "} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q\n%s", want, stdout)
		}
	}
}

type livePeer struct {
	name string
	addr string
}

type tsDevice struct {
	ID               string   `json:"id"`
	Hostname         string   `json:"hostname"`
	Connected        bool     `json:"connectedToControl"`
	Addresses        []string `json:"addresses"`
	AdvertisedRoutes []string `json:"advertisedRoutes"`
	EnabledRoutes    []string `json:"enabledRoutes"`
}

// liveAPI is a minimal Tailscale control-plane client for test setup. It is
// not collector traffic, so it does not go through netEnv.
type liveAPI struct {
	key     string
	tailnet string
	client  *http.Client
}

func liveCreds(t *testing.T) *liveAPI {
	t.Helper()
	envName := os.Getenv("TAIL_WATCH_PULUMI_ENV")
	if envName == "" {
		envName = "tailscale-phase-2/tailscale"
	}
	out, err := exec.CommandContext(t.Context(), "pulumi", "env", "open", envName, "--format", "json").Output()
	if err != nil {
		t.Fatalf("pulumi env open %s: %v", envName, err)
	}
	var env struct {
		Tailscale struct {
			APIKey  string `json:"apiKey"`
			Tailnet string `json:"tailnet"`
		} `json:"tailscale"`
	}
	if uerr := json.Unmarshal(out, &env); uerr != nil {
		t.Fatal(uerr)
	}
	if env.Tailscale.APIKey == "" || env.Tailscale.Tailnet == "" {
		t.Fatalf("%s has no tailscale.apiKey and tailscale.tailnet", envName)
	}
	return &liveAPI{
		key:     env.Tailscale.APIKey,
		tailnet: env.Tailscale.Tailnet,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *liveAPI) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.tailscale.com"+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func (a *liveAPI) mustJSON(t *testing.T, method, path string, body, dest any) {
	t.Helper()
	status, raw, err := a.call(t.Context(), method, path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if status < 200 || status >= 300 {
		t.Fatalf("%s %s: HTTP %d: %s", method, path, status, redactSecrets(string(raw)))
	}
	if dest != nil {
		if uerr := json.Unmarshal(raw, dest); uerr != nil {
			t.Fatal(uerr)
		}
	}
}

func (a *liveAPI) devices(ctx context.Context) ([]tsDevice, error) {
	status, raw, err := a.call(ctx, "GET", "/api/v2/tailnet/"+a.tailnet+"/devices?fields=all", nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("list devices: HTTP %d: %s", status, redactSecrets(string(raw)))
	}
	var listed struct {
		Devices []tsDevice `json:"devices"`
	}
	if uerr := json.Unmarshal(raw, &listed); uerr != nil {
		return nil, uerr
	}
	return listed.Devices, nil
}

func (a *liveAPI) authKey(t *testing.T, tags []string) string {
	t.Helper()
	create := map[string]any{"reusable": false, "ephemeral": true, "preauthorized": true}
	if len(tags) > 0 {
		create["tags"] = tags
	}
	body := map[string]any{
		"capabilities":  map[string]any{"devices": map[string]any{"create": create}},
		"expirySeconds": 900,
	}
	var created struct {
		Key string `json:"key"`
	}
	a.mustJSON(t, "POST", "/api/v2/tailnet/"+a.tailnet+"/keys", body, &created)
	if !strings.HasPrefix(created.Key, "tskey-auth-") {
		t.Fatal("tailscale did not return an auth key")
	}
	return created.Key
}

// deleteRun removes only the devices this run created, so concurrent runs on
// the same tailnet keep their nodes. It runs from t.Cleanup, where t.Context
// is already cancelled, and reports errors without aborting.
func (a *liveAPI) deleteRun(t *testing.T, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveCleanupTimeout)
	defer cancel()
	pollUntil(liveCleanupTimeout, func() bool {
		devices, err := a.devices(ctx)
		if err != nil {
			t.Logf("cleanup: %v", err)
			return false
		}
		remaining := 0
		for _, d := range devices {
			if !isRunDevice(d.Hostname, runID) {
				continue
			}
			remaining++
			status, _, derr := a.call(ctx, "DELETE", "/api/v2/device/"+d.ID, nil)
			if derr != nil || (status != 200 && status != 404) {
				t.Logf("cleanup: delete %s: HTTP %d %v", d.Hostname, status, derr)
			}
		}
		return remaining == 0
	})
}

func isRunDevice(hostname, runID string) bool {
	return strings.EqualFold(hostname, runID) || strings.HasPrefix(strings.ToLower(hostname), runID+"-")
}

func liveTargets(t *testing.T, api *liveAPI) (livePeer, string) {
	t.Helper()
	devices, err := api.devices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	peer := chosenPeer(devices)
	subnetIP := chosenSubnetIP(devices)
	if peer.addr == "" {
		t.Fatal("no connected tailnet peer to probe")
	}
	t.Logf("peer %s %s subnet %s", peer.name, peer.addr, subnetIP)
	return peer, subnetIP
}

func chosenPeer(devices []tsDevice) livePeer {
	if forced := os.Getenv("TAIL_WATCH_LIVE_PEER"); forced != "" {
		return livePeer{name: "peer", addr: forced}
	}
	for _, d := range devices {
		if strings.HasPrefix(d.Hostname, liveHostPrefix) || !d.Connected {
			continue
		}
		if ip := firstIPv4(d.Addresses); ip != "" {
			return livePeer{name: sanitizeTarget(d.Hostname), addr: ip}
		}
	}
	return livePeer{}
}

// chosenSubnetIP returns the first host of a private IPv4 route that is
// enabled on a connected device. Advertised-but-unapproved routes are not
// reachable, so they fall through to the ephemeral router.
func chosenSubnetIP(devices []tsDevice) string {
	if forced := os.Getenv("TAIL_WATCH_LIVE_SUBNET"); forced != "" {
		return forced
	}
	for _, d := range devices {
		if !d.Connected || strings.HasPrefix(d.Hostname, liveHostPrefix) {
			continue
		}
		for _, route := range d.EnabledRoutes {
			if ip := firstPrivateIPv4Host(route); ip != "" {
				return ip
			}
		}
	}
	return ""
}

// liveStack is one run's `docker compose` project over examples/compose.yaml.
type liveStack struct {
	project  string
	files    []string
	env      map[string]string
	services []string
}

func newLiveStack(t *testing.T, runID, authKey string) *liveStack {
	t.Helper()
	root := repoRoot(t)
	s := &liveStack{
		project:  runID,
		files:    []string{filepath.Join(root, "examples", "compose.yaml")},
		env:      map[string]string{"TS_AUTHKEY": authKey, "TS_HOSTNAME": runID},
		services: []string{"tailscale"},
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), liveCleanupTimeout)
		defer cancel()
		if out, err := s.command(ctx, "down", "--volumes", "--remove-orphans").CombinedOutput(); err != nil {
			t.Logf("cleanup: compose down: %v\n%s", err, redactSecrets(string(out)))
		}
	})
	return s
}

func (s *liveStack) addRouter(t *testing.T, hostname, authKey string) {
	t.Helper()
	s.files = append(s.files, filepath.Join(packageDir(t), "testdata", "compose.live-router.yaml"))
	s.env["TS_ROUTER_HOSTNAME"] = hostname
	s.env["TS_ROUTER_AUTHKEY"] = authKey
	s.services = append(s.services, "router")
}

func (s *liveStack) command(ctx context.Context, args ...string) *exec.Cmd {
	full := []string{"compose", "--project-name", s.project}
	for _, f := range s.files {
		full = append(full, "--file", f)
	}
	cmd := exec.CommandContext(ctx, "docker", append(full, args...)...)
	cmd.Env = os.Environ()
	for k, v := range s.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

func (s *liveStack) output(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := s.command(t.Context(), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, redactSecrets(string(out)))
	}
	return out
}

// start builds the shipped tail-watch image and starts the tailscale services.
// It does not start the tail-watch service; runOnce runs that with --once.
func (s *liveStack) start(t *testing.T) {
	t.Helper()
	s.output(t, "build", "tail-watch")
	args := append([]string{"up", "--detach", "--wait", "--wait-timeout", "90"}, s.services...)
	if out, err := s.command(t.Context(), args...).CombinedOutput(); err != nil {
		logs, _ := s.command(t.Context(), "logs", "--no-color").CombinedOutput()
		t.Fatalf("docker compose up: %v\n%s\n%s", err, redactSecrets(string(out)), redactSecrets(string(logs)))
	}
}

func (s *liveStack) runOnce(t *testing.T) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := s.command(ctx, "run", "--rm", "--no-deps", "--no-TTY",
		"tail-watch", "--once", "--log-level", "info")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func enableLiveRoute(t *testing.T, api *liveAPI, hostname, prefix string) {
	t.Helper()
	var last string
	ok := pollUntil(45*time.Second, func() bool {
		devices, err := api.devices(t.Context())
		if err != nil {
			last = err.Error()
			return false
		}
		i := slices.IndexFunc(devices, func(d tsDevice) bool { return strings.EqualFold(d.Hostname, hostname) })
		if i < 0 {
			last = hostname + " not registered"
			return false
		}
		d := devices[i]
		last = fmt.Sprintf("advertised %v enabled %v", d.AdvertisedRoutes, d.EnabledRoutes)
		if slices.Contains(d.EnabledRoutes, prefix) {
			return true
		}
		if slices.Contains(d.AdvertisedRoutes, prefix) {
			api.mustJSON(t, "POST", "/api/v2/device/"+d.ID+"/routes",
				map[string]any{"routes": []string{prefix}}, nil)
			return true
		}
		return false
	})
	if !ok {
		t.Fatalf("route %s on %s was not advertised\n%s", prefix, hostname, redactSecrets(last))
	}
}

func waitLiveRoute(t *testing.T, stack *liveStack, ip string) {
	t.Helper()
	var last []byte
	ok := pollUntil(40*time.Second, func() bool {
		out, err := stack.command(t.Context(), "exec", "--no-TTY", "tailscale", "ip", "route", "get", ip).
			CombinedOutput()
		last = out
		return err == nil && strings.Contains(string(out), "tailscale")
	})
	if !ok {
		t.Fatalf("no tailscale route to %s\n%s", ip, last)
	}
	t.Logf("route %s: %s", ip, strings.TrimSpace(string(last)))
}

// requireKernelMode fails unless the collector node is Running with a TUN
// device, since tail-watch refuses userspace networking.
func requireKernelMode(t *testing.T, stack *liveStack) {
	t.Helper()
	out := stack.output(t, "exec", "--no-TTY", "tailscale", "tailscale", "status", "--json")
	var st struct {
		BackendState string `json:"BackendState"`
		TUN          bool   `json:"TUN"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		t.Fatalf("tailscale status: %v", err)
	}
	if st.BackendState != "Running" || !st.TUN {
		t.Fatalf("collector node is %s with TUN=%v, want Running with TUN", st.BackendState, st.TUN)
	}
}

// pollUntil calls done every two seconds until it returns true or timeout
// elapses, and reports whether it succeeded.
func pollUntil(timeout time.Duration, done func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if done() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Second)
	}
}

func firstIPv4(addrs []string) string {
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err == nil && ip.Is4() {
			return ip.String()
		}
	}
	return ""
}

// firstPrivateIPv4Host returns the first usable host in a private IPv4
// prefix, or "" for anything else. The prefix is masked first because the
// API may return routes with host bits set.
func firstPrivateIPv4Host(prefix string) string {
	p, err := netip.ParsePrefix(prefix)
	if err != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() {
		return ""
	}
	p = p.Masked()
	if p.Bits() >= 31 {
		return p.Addr().String()
	}
	return p.Addr().Next().String()
}

func sanitizeTarget(name string) string {
	if name == "" {
		return "peer"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "peer"
	}
	return b.String()
}

var secretRE = regexp.MustCompile(`tskey-[A-Za-z0-9_-]+|https://hooks\.slack\.com/\S+`)

func redactSecrets(s string) string {
	return secretRE.ReplaceAllString(s, "redacted")
}
