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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	vmHostPrefix        = liveHostPrefix + "-vm"
	vmInstallTimeout    = 8 * time.Minute
	vmReadyTimeout      = 90 * time.Second
	vmFixtureSocketWait = 30 * time.Second
	vmSpikeWait         = 45 * time.Second
	vmDiskQueueWait     = 30 * time.Second
)

// vmRun carries everything the TestVMSystemd subtests need about the one
// guest they share.
type vmRun struct {
	v        *vm
	rcv      *liveReceiver
	fxIP     string
	fxPrefix string
}

// TestVMSystemd checks the systemd deployment on a throwaway QEMU/KVM VM: the
// shipped examples/tail-watch.service unit and the documented commands in
// docs/how-to/deploy.md run against a real kernel-mode tailscaled in the
// guest, with a second tailscaled as the monitored fixture. It is off unless TAIL_WATCH_VM_SYSTEMD=1 because it boots
// a VM and joins a tailnet.
//
// Credentials and cleanup work as in TestLiveTailnetDocker: the collector key
// is tagged TAIL_WATCH_LIVE_TAG (default tag:monitor) and the fixture key is
// untagged, so every probe stays inside the grants in testdata/snippet.hujson.
// Both nodes share one VM and one NAT, so a green run does not prove DERP or
// direct-path behavior on separate hosts.
//
//nolint:paralleltest // The subtests share one VM and must run in order.
func TestVMSystemd(t *testing.T) {
	if os.Getenv("TAIL_WATCH_VM_SYSTEMD") == "" {
		t.Skip("set TAIL_WATCH_VM_SYSTEMD=1 to run the QEMU systemd test")
	}
	vmPreflight(t)

	api := liveCreds(t)
	runID := vmHostPrefix + "-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	t.Cleanup(func() { api.deleteRun(t, runID) })

	// Mint keys before booting so API errors fail fast.
	collectorKey := api.authKey(t, liveTags())
	fixtureKey := api.authKey(t, nil)

	dir := t.TempDir()
	bin := buildCollector(t, dir)
	pub, signer := vmSSHKey(t)
	qcow := mkLayeredQcow(t, ubuntuImage(t), dir)

	rcv := &liveReceiver{
		hookURL: "", beatURL: "", secret: vmHookSecret(t),
		mu: sync.Mutex{}, events: nil, beats: 0, bad: 0,
	}
	seedPort := startVMHTTP(t, pub, runID, rcv)
	hookURL := fmt.Sprintf("http://10.0.2.2:%d/hook", seedPort)

	sshPort := probablyFreePort(t)
	v := launchVM(t, dir, qcow, sshPort, seedPort)
	v.secrets = []string{rcv.secret, collectorKey, fixtureKey}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("serial console tail:\n%s", v.Console())
		}
	})
	dialVM(t, v, sshPort, signer)

	waitCloudInit(t, v)
	installTailscale(t, v)
	vmTailscaleUp(t, v, "", "", collectorKey, runID)
	requireVMKernelMode(t, v)

	fxIP, fxPrefix := vmFixture(t, v, api, runID, fixtureKey)
	startFixtureListener(t, v, fxPrefix)

	cfgPath := renderVMConfig(t, dir, bin, fxIP)
	stageRelease(t, v, bin, cfgPath, "OPS_WEBHOOK_URL="+hookURL+"\nOPS_WEBHOOK_SECRET="+rcv.secret+"\n")

	run := &vmRun{v: v, rcv: rcv, fxIP: fxIP, fxPrefix: fxPrefix}
	t.Run("docs-commands", run.docsCommands)
	t.Run("active", run.active)
	t.Run("icmp-needs-capability", run.icmpCapability)
	t.Run("checks-and-queue", run.checksAndQueue)
	t.Run("clean-stop", run.cleanStop)
}

// vmHookSecret returns the random hex key the webhook sink signs with. It is
// never logged.
func vmHookSecret(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

// startVMHTTP serves the nocloud-net seed (user-data and meta-data) and the
// webhook sink's /hook endpoint on one listener, bound on all interfaces so
// slirp's 10.0.2.2 reaches it from the guest. The user-data only plants the
// throwaway SSH key and the hostname; it holds no secrets. The seed URL is
// fetched once here so a broken server fails before the VM boots.
func startVMHTTP(t *testing.T, pubKey, hostname string, rcv *liveReceiver) int {
	t.Helper()
	userData := "#cloud-config\n" +
		"hostname: " + hostname + "\n" +
		"disable_root: false\n" +
		"users:\n" +
		"  - name: root\n" +
		"    ssh_authorized_keys:\n" +
		"      - " + pubKey + "\n"
	metaData := "instance-id: " + hostname + "\nlocal-hostname: " + hostname + "\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/user-data", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, userData)
	})
	mux.HandleFunc("/meta-data", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, metaData)
	})
	mux.HandleFunc("/hook", rcv.handleHook)
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/user-data", port))
	if err != nil {
		t.Fatalf("preflight fetch of the seed server: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "#cloud-config") {
		t.Fatalf("seed server preflight: status %s, err %v", resp.Status, err)
	}
	t.Logf("seed and webhook server on 0.0.0.0:%d", port)
	return port
}

// waitCloudInit blocks until cloud-init finishes. The SSH key is already
// planted by the time dialVM succeeds; this waits out the remaining modules.
// "degraded" is accepted: noble logs the nocloud-net deprecation as an error,
// and the seed only plants a key and a hostname, which the SSH dial proved.
func waitCloudInit(t *testing.T, v *vm) {
	t.Helper()
	v.Eventually("cloud-init done", vmReadyTimeout, func() error {
		out, _, _ := v.Run("cloud-init status")
		if strings.Contains(out, "status: done") || strings.Contains(out, "status: degraded") {
			return nil
		}
		return fmt.Errorf("cloud-init %s", strings.TrimSpace(out))
	})
}

// installTailscale installs Tailscale from the official script, as an
// operator would, and waits for tailscaled to be running.
func installTailscale(t *testing.T, v *vm) {
	t.Helper()
	if _, stderr, err := v.RunTimeout(
		vmInstallTimeout,
		"curl -fsSL https://tailscale.com/install.sh | sh",
	); err != nil {
		t.Fatalf("tailscale install.sh: %v\n%s\nconsole:\n%s", err, v.redact(stderr), v.Console())
	}
	v.Eventually("tailscaled active", vmReadyTimeout, func() error {
		_, _, err := v.Run("systemctl is-active --quiet tailscaled")
		return err
	})
}

// vmTailscaleUp brings a node up with an ephemeral auth key. The key travels
// in a 0600 file, never on a command line. execPrefix ("ip netns exec fx ")
// and socket select a fixture daemon; both empty means the system tailscaled.
func vmTailscaleUp(t *testing.T, v *vm, execPrefix, socket, key, hostname string) {
	t.Helper()
	keyFile := "/root/tskey." + hostname
	v.PutContent([]byte(key+"\n"), keyFile, 0o600)
	cli := "tailscale"
	if socket != "" {
		cli += " --socket=" + socket
	}
	if _, stderr, err := v.Run(
		execPrefix + cli + " up --auth-key=file:" + keyFile + " --hostname=" + hostname,
	); err != nil {
		t.Fatalf("tailscale up %s: %v\n%s", hostname, err, v.redact(stderr))
	}
	v.MustRun("rm -f " + keyFile)
}

// requireVMKernelMode fails unless the collector node is Running with a TUN
// device, since tail-watch refuses userspace networking.
func requireVMKernelMode(t *testing.T, v *vm) {
	t.Helper()
	v.Eventually("collector Running with TUN", vmReadyTimeout, func() error {
		out, _, err := v.Run("tailscale status --json")
		if err != nil {
			return err
		}
		var st struct {
			BackendState string `json:"BackendState"`
			TUN          bool   `json:"TUN"`
		}
		if uerr := json.Unmarshal([]byte(out), &st); uerr != nil {
			return uerr
		}
		if st.BackendState != "Running" || !st.TUN {
			return fmt.Errorf("BackendState=%s TUN=%v", st.BackendState, st.TUN)
		}
		return nil
	})
}

// vmFixture starts a second tailscaled as the monitored fixture and returns
// its tailnet IPv4 address and the exec prefix commands for it need ("" for
// userspace mode on the host netns, "ip netns exec fx " for the kernel-mode
// fallback).
//
// It first runs the fixture with --tun=userspace-networking and performs the
// step 0 spike: ping the fixture's own tailnet IP from the kernel-mode
// collector. If the userspace node does not answer ICMP echo, the fixture is
// redone kernel-mode inside netns fx so its ICMP path matches a real peer.
func vmFixture(t *testing.T, v *vm, api *liveAPI, runID, key string) (string, string) {
	t.Helper()
	v.MustRun("mkdir -p /var/lib/tsfx /run/tsfx")
	v.MustRun("nohup tailscaled --tun=userspace-networking" +
		" --state=/var/lib/tsfx/tailscaled.state --socket=/run/tsfx/tailscaled.sock --port=0" +
		" >/var/log/tsfx.log 2>&1 </dev/null &")
	v.Eventually("fixture tailscaled socket", vmFixtureSocketWait, func() error {
		_, _, err := v.Run("test -S /run/tsfx/tailscaled.sock")
		return err
	})
	vmTailscaleUp(t, v, "", "/run/tsfx/tailscaled.sock", key, runID+"-fx")
	ip := vmFixtureIP(t, v, "", "/run/tsfx/tailscaled.sock")

	// Step 0 spike. The first echo needs the wireguard handshake, so give the
	// cold path a few tries before concluding userspace mode cannot answer.
	spike := pollUntil(vmSpikeWait, func() bool {
		_, _, err := v.Run("ping -c1 -W3 " + ip)
		return err == nil
	})
	t.Logf("step 0 spike: userspace tailscaled at %s answers ICMP echo from a kernel-mode peer: %t",
		ip, spike)
	if spike {
		return ip, ""
	}
	t.Log("step 0 spike says no: redoing the fixture kernel-mode in netns fx")
	// The bracket keeps pkill from matching this shell's own command line.
	v.MustRun("pkill -f '[/]run/tsfx/tailscaled.sock' || true")
	vmKernelFixtureNetns(t, v)
	v.MustRun("ip netns exec fx bash -c 'nohup tailscaled" +
		" --state=/var/lib/tsfxk/tailscaled.state --socket=/run/tsfxk/tailscaled.sock" +
		" >/var/log/tsfxk.log 2>&1 </dev/null &'")
	v.Eventually("kernel fixture tailscaled socket", vmFixtureSocketWait, func() error {
		_, _, err := v.Run("test -S /run/tsfxk/tailscaled.sock")
		return err
	})
	fallbackKey := api.authKey(t, nil)
	v.secrets = append(v.secrets, fallbackKey)
	vmTailscaleUp(t, v, "ip netns exec fx ", "/run/tsfxk/tailscaled.sock", fallbackKey, runID+"-fx")
	return vmFixtureIP(t, v, "ip netns exec fx ", "/run/tsfxk/tailscaled.sock"), "ip netns exec fx "
}

// vmKernelFixtureNetns wires netns fx to the guest's network over a veth pair
// with NAT, so the kernel-mode fixture tailscaled has outbound connectivity.
// Only used when the step 0 spike rejects userspace mode.
func vmKernelFixtureNetns(t *testing.T, v *vm) {
	t.Helper()
	out := v.MustRun("ip route show default")
	var dev string
	if f := strings.Fields(out); len(f) >= 5 {
		dev = f[4]
	}
	if dev == "" {
		t.Fatalf("no default route device in %q", out)
	}
	v.MustRun("mkdir -p /var/lib/tsfxk /run/tsfxk")
	v.MustRun("ip netns add fx")
	v.MustRun("ip link add veth-fx type veth peer name veth-fxp netns fx")
	v.MustRun("ip addr add 10.200.200.1/30 dev veth-fx")
	v.MustRun("ip link set veth-fx up")
	v.MustRun("sysctl -qw net.ipv4.ip_forward=1")
	v.MustRun("iptables -t nat -A POSTROUTING -s 10.200.200.0/30 -o " + dev + " -j MASQUERADE")
	v.MustRun("ip netns exec fx ip link set lo up")
	v.MustRun("ip netns exec fx ip addr add 10.200.200.2/30 dev veth-fxp")
	v.MustRun("ip netns exec fx ip link set veth-fxp up")
	v.MustRun("ip netns exec fx ip route add default via 10.200.200.1")
}

// vmFixtureIP polls tailscale ip -4 until the fixture has an address.
func vmFixtureIP(t *testing.T, v *vm, execPrefix, socket string) string {
	t.Helper()
	var ip string
	v.Eventually("fixture tailnet address", vmReadyTimeout, func() error {
		out, _, err := v.Run(execPrefix + "tailscale --socket=" + socket + " ip -4")
		if err != nil {
			return err
		}
		ip = firstIPv4(strings.Fields(out))
		if ip == "" {
			return fmt.Errorf("no address yet")
		}
		return nil
	})
	return ip
}

// startFixtureListener runs a TCP listener on the fixture's :9000 before the
// collector starts, so the fx-tcp target begins up and the down event in
// checks-and-queue is deterministic.
func startFixtureListener(t *testing.T, v *vm, fxPrefix string) {
	t.Helper()
	v.MustRun(fxPrefix + "bash -c 'nohup python3 -m http.server 9000 --bind 0.0.0.0" +
		" >/var/log/fx9000.log 2>&1 </dev/null &'")
	v.Eventually("fixture tcp:9000 listening", vmFixtureSocketWait, func() error {
		_, _, err := v.Run(fxPrefix + "curl -sf -o /dev/null http://127.0.0.1:9000/")
		return err
	})
}

// renderVMConfig renders the config template with the fixture address and
// validates it with the built binary's check-config, as the docs recommend,
// before it goes to the guest.
func renderVMConfig(t *testing.T, dir, bin, fxIP string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(packageDir(t), "testdata", "tail-watch.vm-systemd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.ReplaceAll(string(raw), "FIXTURE_ADDR", fxIP)
	if strings.Contains(rendered, "FIXTURE_ADDR") {
		t.Fatal("config template still contains FIXTURE_ADDR")
	}
	cfgPath := filepath.Join(dir, "tail-watch.yaml")
	if werr := os.WriteFile(cfgPath, []byte(rendered), 0o644); werr != nil {
		t.Fatal(werr)
	}
	stdout, stderr, code := runBin(t, bin, nil, "check-config", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("check-config rejected the rendered config:\n%s\n%s", stdout, redactSecrets(stderr))
	}
	t.Logf("check-config:\n%s", stdout)
	return cfgPath
}

// stageRelease puts the files where the docs expect them: a directory holding
// the binary, config, and unit as if unpacked from a release archive, plus the
// rendered environment file that replaces the interactive sudoedit step.
func stageRelease(t *testing.T, v *vm, bin, cfgPath, envContent string) {
	t.Helper()
	root := repoRoot(t)
	v.MustRun("mkdir -p /root/release/examples")
	v.Put(bin, "/root/release/tail-watch", 0o755)
	v.Put(cfgPath, "/root/release/examples/tail-watch.yaml", 0o644)
	v.Put(filepath.Join(root, "examples", "tail-watch.service"), "/root/release/examples/tail-watch.service", 0o644)
	v.PutContent([]byte(envContent), "/root/release/tail-watch.env", 0o600)
}

// systemdDocBlocks extracts the fenced bash blocks under "Run under systemd"
// in docs/how-to/deploy.md, in order, so the test fails when the docs drift.
func systemdDocBlocks(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "how-to", "deploy.md"))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	var cur strings.Builder
	inSection, inBlock := false, false
	for line := range strings.Lines(string(raw)) {
		if strings.HasPrefix(line, "## ") {
			inSection = strings.HasPrefix(line, "## Run under systemd")
		}
		if !inSection {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```bash"):
			inBlock = true
			cur.Reset()
		case inBlock && strings.HasPrefix(trimmed, "```"):
			inBlock = false
			blocks = append(blocks, cur.String())
		case inBlock:
			cur.WriteString(line)
		}
	}
	if len(blocks) < 5 {
		t.Fatalf("deploy.md Run under systemd has %d bash blocks, want at least 5", len(blocks))
	}
	return blocks
}

// docScript turns one docs block into a script runnable as root in the
// staging directory. The interactive sudoedit of the environment file is
// replaced by installing the env file the test rendered, which is what the
// operator would type into the editor.
func (r *vmRun) docScript(block string) string {
	var b strings.Builder
	b.WriteString("set -euo pipefail\ncd /root/release\n")
	for line := range strings.Lines(block) {
		if strings.HasPrefix(strings.TrimSpace(line), "sudoedit ") {
			b.WriteString("sudo install -m 0600 /root/release/tail-watch.env /etc/tail-watch/tail-watch.env\n")
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// docsCommands runs every documented step of "Run under systemd" against the
// staged files, then verifies the installed unit in the guest. The release
// download block is replaced by the staged local build, which is the same
// binary a release would install.
func (r *vmRun) docsCommands(t *testing.T) {
	defer vmDiag(t, r.v)
	for i, block := range systemdDocBlocks(t) {
		t.Logf("docs block %d:\n%s", i, block)
		script := r.docScript(block)
		if strings.Contains(block, "systemctl status") {
			r.v.Eventually("docs verify block", vmReadyTimeout, func() error {
				_, stderr, err := r.v.Run(script)
				if err != nil {
					return fmt.Errorf("%w: %s", err, r.v.redact(stderr))
				}
				return nil
			})
			continue
		}
		if _, stderr, err := r.v.Run(script); err != nil {
			t.Fatalf("docs block %d failed: %v\nstderr: %s", i, err, r.v.redact(stderr))
		}
	}
	out, stderr, err := r.v.Run("systemd-analyze verify /etc/systemd/system/tail-watch.service")
	if err != nil || strings.TrimSpace(out+stderr) != "" {
		t.Fatalf("systemd-analyze verify: %v\n%s", err, r.v.redact(out+stderr))
	}
}

// active checks that the unit runs the collector as the unprivileged
// tail-watch user, healthy, without tailscale operator rights.
func (r *vmRun) active(t *testing.T) {
	defer vmDiag(t, r.v)
	if out := strings.TrimSpace(r.v.MustRun("systemctl is-active tail-watch")); out != "active" {
		t.Fatalf("systemctl is-active = %q, want active", out)
	}
	r.v.Eventually("healthz answers ok", vmReadyTimeout, func() error {
		out, _, err := r.v.Run("curl -sf http://127.0.0.1:9109/healthz")
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) != "ok" {
			return fmt.Errorf("healthz = %q", out)
		}
		return nil
	})
	if out := strings.TrimSpace(r.v.MustRun("systemctl show tail-watch -p User --value")); out != "tail-watch" {
		t.Fatalf("User = %q, want tail-watch", out)
	}
	pid := strings.TrimSpace(r.v.MustRun("systemctl show tail-watch -p MainPID --value"))
	uid := strings.TrimSpace(r.v.MustRun("ps -o uid= -p " + pid))
	if n, err := strconv.Atoi(uid); err != nil || n == 0 {
		t.Fatalf("collector uid = %q (pid %s), want non-root", uid, pid)
	}
	prefs := r.v.MustRun("tailscale debug prefs")
	var p map[string]any
	if err := json.Unmarshal([]byte(prefs), &p); err != nil {
		t.Fatalf("tailscale debug prefs: %v", err)
	}
	if op, _ := p["OperatorUser"].(string); op != "" {
		t.Fatalf("OperatorUser = %q; tail-watch must work without tailscale set --operator", op)
	}
}

// icmpCapability checks the unit's CAP_NET_RAW grant: a positive and a negative
// control prove CAP_NET_RAW is what lets the service user ping, then the
// collector's own icmp target must report up. ping_group_range is forced to
// "1 0" so the unprivileged datagram-socket path cannot mask the capability,
// and the ping binary is copied without its file capabilities.
func (r *vmRun) icmpCapability(t *testing.T) {
	defer vmDiag(t, r.v)
	pgr := strings.TrimSpace(r.v.MustRun("sysctl -n net.ipv4.ping_group_range"))
	t.Logf("guest net.ipv4.ping_group_range = %s (forcing 1 0 for the controls)", pgr)
	r.v.MustRun(`install -m 0755 "$(command -v ping)" /tmp/ping-nocap`)
	r.v.MustRun(`sysctl -w net.ipv4.ping_group_range="1 0"`)

	r.v.MustRun("systemd-run --wait --pipe --uid=tail-watch" +
		" -p AmbientCapabilities=CAP_NET_RAW /tmp/ping-nocap -c1 -W3 " + r.fxIP)
	t.Log("positive control: ping with ambient CAP_NET_RAW as tail-watch succeeded")

	_, stderr, err := r.v.Run("systemd-run --wait --pipe --uid=tail-watch /tmp/ping-nocap -c1 -W3 " + r.fxIP)
	if err == nil {
		t.Fatal("negative control: ping without CAP_NET_RAW succeeded; the capability is not what makes icmp work")
	}
	t.Logf("negative control failed as expected: %s", r.v.redact(strings.TrimSpace(stderr)))

	waitTargetUp(t, r.v, "fx-icmp")
}

// checksAndQueue checks that icmp, tsping and tcp:9000 targets on the fixture
// report up, stopping the fixture's :9000 listener produces a signed
// target.down at the webhook, the disk queue writes under
// /var/lib/tail-watch, and restarting the listener produces the recovery.
func (r *vmRun) checksAndQueue(t *testing.T) {
	defer vmDiag(t, r.v)
	waitTargetUp(t, r.v, "fx-icmp")
	waitTargetUp(t, r.v, "fx-tsping")
	waitTargetUp(t, r.v, "fx-tcp")

	// The bracket keeps pkill from matching this shell's own command line.
	r.v.MustRun(r.fxPrefix + "pkill -f 'http[.]server 9000' || true")
	down := r.rcv.waitFor(t, "fx-tcp down", func(e Event) bool {
		return e.Type == EventDown && e.targetLabel() == "fx-tcp"
	})
	t.Logf("fx-tcp down: layer=%q check=%q error=%q", down.Layer, down.Check, down.Error)

	r.v.Eventually("disk queue state file", vmDiskQueueWait, func() error {
		_, _, err := r.v.Run("test -s /var/lib/tail-watch/notification-state.json")
		return err
	})
	if owner := strings.TrimSpace(
		r.v.MustRun("stat -c %U /var/lib/tail-watch/notification-state.json"),
	); owner != "tail-watch" {
		t.Fatalf("notification-state.json owned by %q, want tail-watch", owner)
	}
	t.Logf("state dir:\n%s", r.v.MustRun("ls -la /var/lib/tail-watch"))

	startFixtureListener(t, r.v, r.fxPrefix)
	r.rcv.waitFor(t, "fx-tcp recovered", func(e Event) bool {
		return e.Type == EventRecovered && e.DownEventID == down.ID
	})
	if bad := r.rcv.badSignatures(); bad != 0 {
		t.Fatalf("%d webhook requests failed signature verification", bad)
	}
}

// cleanStop checks that SIGTERM stops the collector cleanly, it never
// restarted, and its journal has no error-level lines.
func (r *vmRun) cleanStop(t *testing.T) {
	defer vmDiag(t, r.v)
	r.v.MustRun("systemctl stop tail-watch")
	show := r.v.MustRun("systemctl show tail-watch -p ExecMainStatus -p Result -p NRestarts")
	for _, want := range []string{"ExecMainStatus=0", "Result=success", "NRestarts=0"} {
		if !strings.Contains(show, want) {
			t.Errorf("missing %s in:\n%s", want, show)
		}
	}
	out := r.v.MustRun("journalctl -u tail-watch -p err --quiet --no-pager")
	if strings.TrimSpace(out) != "" {
		t.Errorf("error-level lines in journalctl -u tail-watch:\n%s", r.v.redact(out))
	}
}

// waitTargetUp waits until tailwatch_target_state for target is 1 (up; the
// gauge is 0 unknown, 2 down, 3 suppressed, 4 flapping).
func waitTargetUp(t *testing.T, v *vm, target string) {
	t.Helper()
	line := fmt.Sprintf(`tailwatch_target_state{target="%s"} 1`, target)
	v.Eventually(line, vmReadyTimeout, func() error {
		out, _, err := v.Run("curl -sf http://127.0.0.1:9109/metrics")
		if err != nil {
			return err
		}
		for l := range strings.Lines(out) {
			if strings.TrimRight(l, "\n") == line {
				return nil
			}
		}
		return fmt.Errorf("no %q yet", line)
	})
}

// vmDiag dumps the collector journal, tailscale status, and the serial console
// tail when a subtest failed.
func vmDiag(t *testing.T, v *vm) {
	if !t.Failed() {
		return
	}
	if out, _, err := v.Run("journalctl -u tail-watch --no-pager -n 100"); err == nil {
		t.Logf("journalctl -u tail-watch:\n%s", v.redact(out))
	}
	if out, _, err := v.Run("tailscale status"); err == nil {
		t.Logf("tailscale status:\n%s", v.redact(out))
	}
	t.Logf("serial console tail:\n%s", v.Console())
}
