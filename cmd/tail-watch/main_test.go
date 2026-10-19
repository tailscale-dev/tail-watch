package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tstime"
	"tailscale.com/types/views"
)

func TestVersionAndCheckConfig(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("empty version")
	}

	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(ok, []byte("targets: [{name: a, addr: 10.0.0.1, checks: [icmp]}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"check-config", "--config", ok}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "a interval=30s timeout=5s") ||
		!strings.Contains(stdout.String(), "checks=icmp") ||
		!strings.Contains(stdout.String(), "route=none sinks=none") {
		t.Fatalf("stdout %s", stdout.String())
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("targets: [{name: a, checks: [icmp]}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"check-config", "--config", bad}, &stdout, &stderr); code != 1 {
		t.Fatalf("code %d stderr %s", code, stderr.String())
	}
}

func TestCheckConfigPrintsRoutes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yaml")
	body := []byte(`
sinks:
  - {name: db-hook, type: webhook, url_env: U}
  - {name: ops-slack, type: slack, url_env: S}
routes:
  - {name: db-team, sinks: [db-hook]}
targets:
  - {name: pg-primary, addr: 10.0.0.1, checks: [icmp], route: db-team}
  - {name: nyc-printer, addr: 10.0.0.2, checks: [icmp], severity: warn}
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check-config", "--config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"route db-team severity=page sinks=db-hook",
		"pg-primary interval=30s timeout=5s network=tailnet checks=icmp route=db-team sinks=db-hook",
		"nyc-printer interval=30s timeout=5s network=tailnet checks=icmp route=none sinks=db-hook,ops-slack",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

//nolint:paralleltest // run installs a process-wide default logger.
func TestMissingConfigDoesNotDial(t *testing.T) {
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	sock := filepath.Join(t.TempDir(), "tailscaled.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan struct{}, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()
	var stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	code := run([]string{"--config", missing, "--socket", sock}, &bytes.Buffer{}, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), missing) {
		t.Fatalf("code %d stderr %s", code, stderr.String())
	}
	select {
	case <-accepted:
		t.Fatal("dialed tailscaled before reporting the missing config")
	case <-time.After(200 * time.Millisecond):
	}
}

//nolint:gocognit // Independent preflight scenarios keep their assertions next to the inputs.
func TestPreflight(t *testing.T) {
	t.Parallel()
	running := func() *ipnstate.Status {
		tags := views.SliceOf([]string{"tag:monitor"})
		return &ipnstate.Status{
			BackendState: ipn.Running.String(),
			TUN:          true,
			TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.8")},
			Self: &ipnstate.PeerStatus{
				HostName: "monitor",
				Tags:     &tags,
			},
		}
	}
	subnet := []Target{
		{Dynamic: false, Name: "sw", Addr: "10.20.0.1", Network: NetworkTailnet, Checks: []check{icmpCheck{}},
			Site:             "",
			Severity:         "",
			Parent:           "",
			InsecureTLS:      false,
			Interval:         0,
			Timeout:          0,
			FailThreshold:    0,
			RecoverThreshold: 0,
			FlapLimit:        0,
			FlapWindow:       0,
			Implicit:         false,
			StableID:         "",
			NodeID:           0,
			Route:            "",
		},
	}
	loop := []Target{
		{Dynamic: false, Name: "lo", Addr: "127.0.0.1", Network: NetworkTailnet, Checks: []check{tcpCheck{port: 9}},
			Site:             "",
			Severity:         "",
			Parent:           "",
			InsecureTLS:      false,
			Interval:         0,
			Timeout:          0,
			FailThreshold:    0,
			RecoverThreshold: 0,
			FlapLimit:        0,
			FlapWindow:       0,
			Implicit:         false,
			StableID:         "",
			NodeID:           0,
			Route:            "",
		},
	}

	t.Run("needs login", func(t *testing.T) {
		t.Parallel()
		_, err := evaluatePreflight(
			preflightInput{Status: &ipnstate.Status{BackendState: ipn.NeedsLogin.String()}, Socket: "/run/ts.sock",
				Prefs:   nil,
				Targets: nil,
				GOOS:    "",
			},
		)
		if err == nil || !strings.Contains(err.Error(), "tailscale up") ||
			!strings.Contains(err.Error(), "NeedsLogin") {
			t.Fatal(err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()
		_, err := evaluatePreflight(preflightInput{Socket: "/run/ts.sock",
			Status:  nil,
			Prefs:   nil,
			Targets: nil,
			GOOS:    "",
		})
		if err == nil || !strings.Contains(err.Error(), "not reachable") ||
			!strings.Contains(err.Error(), "/run/ts.sock") {
			t.Fatal(err)
		}
	})
	t.Run("userspace", func(t *testing.T) {
		t.Parallel()
		st := running()
		st.TUN = false
		_, err := evaluatePreflight(preflightInput{Status: st, GOOS: "linux",
			Prefs:   nil,
			Targets: nil,
			Socket:  "",
		})
		if err == nil || !strings.Contains(err.Error(), "userspace") {
			t.Fatal(err)
		}
	})
	t.Run("accept routes", func(t *testing.T) {
		t.Parallel()
		_, err := evaluatePreflight(preflightInput{
			Status: running(),
			Prefs: &ipn.Prefs{RouteAll: false,
				ControlURL:             "",
				ExitNodeID:             "",
				ExitNodeIP:             netip.Addr{},
				AutoExitNode:           "",
				InternalExitNodePrior:  "",
				ExitNodeAllowLANAccess: false,
				CorpDNS:                false,
				RunSSH:                 false,
				RunWebClient:           false,
				WantRunning:            false,
				LoggedOut:              false,
				ShieldsUp:              false,
				AdvertiseTags:          nil,
				Hostname:               "",
				NotepadURLs:            false,
				ForceDaemon:            false,
				Egg:                    false,
				AdvertiseRoutes:        nil,
				AdvertiseServices:      nil,
				Sync:                   "",
				NoSNAT:                 false,
				NoStatefulFiltering:    "",
				NetfilterMode:          0,
				OperatorUser:           "",
				ProfileName:            "",
				AutoUpdate: ipn.AutoUpdatePrefs{
					Check: false,
					Apply: "",
				},
				AppConnector: ipn.AppConnectorPrefs{
					Advertise: false,
				},
				PostureChecking:            false,
				NetfilterKind:              "",
				RemoteConfig:               false,
				DriveShares:                nil,
				RelayServerPort:            nil,
				RelayServerStaticEndpoints: nil,
				Persist:                    nil,
			},
			Targets: subnet,
			GOOS:    "linux",

			Socket: "",
		})
		if err == nil || !strings.Contains(err.Error(), "tailscale set --accept-routes") {
			t.Fatal(err)
		}
	})
	t.Run("routes accepted", func(t *testing.T) {
		t.Parallel()
		res, err := evaluatePreflight(preflightInput{
			Status: running(),
			Prefs: &ipn.Prefs{RouteAll: true,
				ControlURL:             "",
				ExitNodeID:             "",
				ExitNodeIP:             netip.Addr{},
				AutoExitNode:           "",
				InternalExitNodePrior:  "",
				ExitNodeAllowLANAccess: false,
				CorpDNS:                false,
				RunSSH:                 false,
				RunWebClient:           false,
				WantRunning:            false,
				LoggedOut:              false,
				ShieldsUp:              false,
				AdvertiseTags:          nil,
				Hostname:               "",
				NotepadURLs:            false,
				ForceDaemon:            false,
				Egg:                    false,
				AdvertiseRoutes:        nil,
				AdvertiseServices:      nil,
				Sync:                   "",
				NoSNAT:                 false,
				NoStatefulFiltering:    "",
				NetfilterMode:          0,
				OperatorUser:           "",
				ProfileName:            "",
				AutoUpdate: ipn.AutoUpdatePrefs{
					Check: false,
					Apply: "",
				},
				AppConnector: ipn.AppConnectorPrefs{
					Advertise: false,
				},
				PostureChecking:            false,
				NetfilterKind:              "",
				RemoteConfig:               false,
				DriveShares:                nil,
				RelayServerPort:            nil,
				RelayServerStaticEndpoints: nil,
				Persist:                    nil,
			},
			Targets: subnet,
			GOOS:    "linux",

			Socket: "",
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Hostname != "monitor" || len(res.IPs) != 1 ||
			!strings.Contains(strings.Join(res.Tags, ","), "tag:monitor") {
			t.Fatalf("identity %+v", res)
		}
	})
	t.Run("loopback does not need routes", func(t *testing.T) {
		t.Parallel()
		if _, err := evaluatePreflight(preflightInput{Status: running(), Targets: loop, GOOS: "linux",
			Prefs:  nil,
			Socket: "",
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("exit node", func(t *testing.T) {
		t.Parallel()
		st := running()
		st.ExitNodeStatus = &ipnstate.ExitNodeStatus{ID: "exit",
			Online:       false,
			TailscaleIPs: nil,
		}
		res, err := evaluatePreflight(preflightInput{Status: st, GOOS: "linux",
			Prefs:   nil,
			Targets: nil,
			Socket:  "",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "exit node") {
			t.Fatalf("warnings %v", res.Warnings)
		}
	})
	t.Run("missing tag", func(t *testing.T) {
		t.Parallel()
		st := running()
		st.Self.Tags = nil
		res, err := evaluatePreflight(preflightInput{Status: st, GOOS: "linux", Targets: loop,
			Prefs:  nil,
			Socket: "",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "tag:monitor") {
			t.Fatalf("warnings %v", res.Warnings)
		}
	})
}

//nolint:gocognit // End-to-end command scenarios share listener and config setup.
func TestCommandIntegration(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), &ipn.Prefs{RouteAll: true,
		ControlURL:             "",
		ExitNodeID:             "",
		ExitNodeIP:             netip.Addr{},
		AutoExitNode:           "",
		InternalExitNodePrior:  "",
		ExitNodeAllowLANAccess: false,
		CorpDNS:                false,
		RunSSH:                 false,
		RunWebClient:           false,
		WantRunning:            false,
		LoggedOut:              false,
		ShieldsUp:              false,
		AdvertiseTags:          nil,
		Hostname:               "",
		NotepadURLs:            false,
		ForceDaemon:            false,
		Egg:                    false,
		AdvertiseRoutes:        nil,
		AdvertiseServices:      nil,
		Sync:                   "",
		NoSNAT:                 false,
		NoStatefulFiltering:    "",
		NetfilterMode:          0,
		OperatorUser:           "",
		ProfileName:            "",
		AutoUpdate: ipn.AutoUpdatePrefs{
			Check: false,
			Apply: "",
		},
		AppConnector: ipn.AppConnectorPrefs{
			Advertise: false,
		},
		PostureChecking:            false,
		NetfilterKind:              "",
		RemoteConfig:               false,
		DriveShares:                nil,
		RelayServerPort:            nil,
		RelayServerStaticEndpoints: nil,
		Persist:                    nil,
	})
	t.Cleanup(closeAPI)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	openPort := ln.Addr().(*net.TCPAddr).Port
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()

	passPath := writeCfg(t, fmt.Sprintf(`
targets:
  - name: loop
    addr: 127.0.0.1
    interval: 200ms
    timeout: 100ms
    checks: ["tcp:%d"]
`, openPort))
	failPath := writeCfg(t, fmt.Sprintf(`
targets:
  - name: loop
    addr: 127.0.0.1
    interval: 200ms
    timeout: 100ms
    checks: ["tcp:%d"]
`, closedPort))

	t.Run("once pass", func(t *testing.T) {
		t.Parallel()
		out, errOut, code := runBin(t, bin, nil, "--once", "--config", passPath, "--socket", sock)
		if code != 0 {
			t.Fatalf("code %d\nstdout %s\nstderr %s", code, out, errOut)
		}
		if !strings.Contains(out, "loop tcp:") || !strings.Contains(out, " ok ") {
			t.Fatalf("stdout %s", out)
		}
	})
	t.Run("once fail", func(t *testing.T) {
		t.Parallel()
		out, errOut, code := runBin(t, bin, nil, "--once", "--config", failPath, "--socket", sock)
		if code != 1 {
			t.Fatalf("code %d\nstdout %s\nstderr %s", code, out, errOut)
		}
		if !strings.Contains(out, "fail") || !strings.Contains(out, "connection refused") {
			t.Fatalf("stdout %s", out)
		}
	})
	t.Run("debug logs", func(t *testing.T) {
		t.Parallel()
		_, errOut, code := runBin(
			t,
			bin,
			[]string{"TAIL_WATCH_LOG_LEVEL=debug"},
			"--once",
			"--config",
			passPath,
			"--socket",
			sock,
		)
		if code != 0 {
			t.Fatalf("code %d stderr %s", code, errOut)
		}
		if !strings.Contains(errOut, `"level":"DEBUG"`) || !strings.Contains(errOut, "loop") {
			t.Fatalf("stderr %s", errOut)
		}
	})

	httpPort := freePort(t)
	httpPath := writeCfg(t, fmt.Sprintf(`
collector:
  listen: 127.0.0.1:%d
targets:
  - name: loop
    addr: 127.0.0.1
    site: lab
    interval: 30s
    timeout: 5s
    checks: ["tcp:%d"]
`, httpPort, openPort))
	cmd := exec.CommandContext(t.Context(), bin, "--config", httpPath, "--socket", sock)
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	waitHTTP(t, base+"/healthz")
	health := httpGet(t, base+"/healthz")
	if strings.TrimSpace(health) != "ok" {
		t.Fatalf("health %q", health)
	}
	metrics := httpGet(t, base+"/metrics")
	if !strings.Contains(metrics, "tailwatch_") || !strings.Contains(metrics, `target="loop"`) ||
		!strings.Contains(metrics, `site="lab"`) ||
		!strings.Contains(metrics, `class="tcp"`) {
		t.Fatalf("metrics %s", metrics)
	}
	status := httpGet(t, base+"/")
	if !strings.Contains(status, "loop") {
		t.Fatalf("status page missing target: %s", status[:min(200, len(status))])
	}

	if signalErr := cmd.Process.Signal(syscall.SIGTERM); signalErr != nil {
		t.Fatal(signalErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			t.Fatalf("SIGTERM exit %v stderr %s", waitErr, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process did not exit after SIGTERM")
	}
}

func TestSIGTERM(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), &ipn.Prefs{RouteAll: true,
		ControlURL:             "",
		ExitNodeID:             "",
		ExitNodeIP:             netip.Addr{},
		AutoExitNode:           "",
		InternalExitNodePrior:  "",
		ExitNodeAllowLANAccess: false,
		CorpDNS:                false,
		RunSSH:                 false,
		RunWebClient:           false,
		WantRunning:            false,
		LoggedOut:              false,
		ShieldsUp:              false,
		AdvertiseTags:          nil,
		Hostname:               "",
		NotepadURLs:            false,
		ForceDaemon:            false,
		Egg:                    false,
		AdvertiseRoutes:        nil,
		AdvertiseServices:      nil,
		Sync:                   "",
		NoSNAT:                 false,
		NoStatefulFiltering:    "",
		NetfilterMode:          0,
		OperatorUser:           "",
		ProfileName:            "",
		AutoUpdate: ipn.AutoUpdatePrefs{
			Check: false,
			Apply: "",
		},
		AppConnector: ipn.AppConnectorPrefs{
			Advertise: false,
		},
		PostureChecking:            false,
		NetfilterKind:              "",
		RemoteConfig:               false,
		DriveShares:                nil,
		RelayServerPort:            nil,
		RelayServerStaticEndpoints: nil,
		Persist:                    nil,
	})
	t.Cleanup(closeAPI)
	port := freePort(t)
	path := writeCfg(t, fmt.Sprintf(`
collector:
  listen: 127.0.0.1:%d
targets:
  - name: loop
    addr: 127.0.0.1
    interval: 30s
    timeout: 1s
    checks: ["tcp:9"]
`, port))
	cmd := exec.Command(bin, "--config", path, "--socket", sock)
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if signalErr := cmd.Process.Signal(syscall.SIGTERM); signalErr != nil {
		t.Fatal(signalErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit %v stderr %s", err, stderr.String())
		}
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("SIGTERM did not exit 0")
	}
}

// countingLocalEnv counts LocalClient lookups so a local-only run can prove
// it never reaches for the LocalAPI.
type countingLocalEnv struct {
	netEnv

	calls *atomic.Int32
}

func (e countingLocalEnv) LocalClient() *local.Client {
	e.calls.Add(1)
	return e.netEnv.LocalClient()
}

//nolint:paralleltest // localOnlyIdentity reads the process hostname and runOnce installs signal handlers.
func TestLocalOnlyRuntime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	cfg, err := parseConfig([]byte(fmt.Sprintf(`
collector: {local_only: true}
targets:
  - {name: web, addr: 127.0.0.1, interval: 1s, timeout: 500ms, checks: ['tcp:%d']}
`, port)))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	missing := filepath.Join(t.TempDir(), "tailscaled.sock")
	var stdout bytes.Buffer
	a := &app{
		cfg:    cfg,
		env:    countingLocalEnv{netEnv: newKernelEnv(missing), calls: &calls},
		log:    slog.New(slog.DiscardHandler),
		clock:  tstime.StdClock{},
		pub:    newPublisher(),
		stdout: &stdout,
		socket: missing,
		listen: "127.0.0.1:0",
		ui:     false,

		dynamic:  nil,
		uiRoutes: nil,
		registry: nil,
		resolver: nil,
		presence: nil,
		class:    nil,
		learner:  nil,
		alerts:   nil,
		notify:   nil,
		metrics:  nil,
		watch:    nil,
		probeMu:  sync.Mutex{},
		probes:   nil,

		collector: CollectorInfo{Name: "", TailscaleIP: ""},
	}
	a.localOnlyIdentity()
	host, hostErr := os.Hostname()
	if hostErr != nil {
		t.Fatal(hostErr)
	}
	if a.collector.Name != host || a.collector.TailscaleIP != "" {
		t.Fatalf("identity %+v, want hostname %q and no Tailscale IP", a.collector, host)
	}
	if code := a.runOnce(); code != 0 {
		t.Fatalf("runOnce code %d stdout %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "web tcp:") || !strings.Contains(stdout.String(), " ok ") {
		t.Fatalf("stdout %s", stdout.String())
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("LocalAPI client requested %d times in local-only mode", got)
	}
}

//nolint:gocognit // Independent CLI scenarios keep their setup next to the assertions.
func TestLocalOnlyCLI(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	missing := filepath.Join(t.TempDir(), "tailscaled.sock")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	openPort := ln.Addr().(*net.TCPAddr).Port
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()

	localCfg := func(port int) string {
		return writeCfg(t, fmt.Sprintf(`
collector: {local_only: true}
targets:
  - name: loop
    addr: 127.0.0.1
    interval: 200ms
    timeout: 100ms
    checks: ["tcp:%d"]
`, port))
	}

	t.Run("check-config rejects tsping", func(t *testing.T) {
		t.Parallel()
		path := writeCfg(t, `
collector: {local_only: true}
targets:
  - {name: nas, addr: 100.64.0.1, checks: [tsping]}
`)
		_, errOut, code := runBin(t, bin, nil, "check-config", "--config", path)
		if code != 1 || !strings.Contains(errOut, "tsping is not valid in local-only mode") {
			t.Fatalf("code %d stderr %s", code, errOut)
		}
	})
	t.Run("ui flag rejected", func(t *testing.T) {
		t.Parallel()
		path := writeCfg(t, fmt.Sprintf(`
collector: {local_only: true, state_dir: %s}
targets:
  - {name: loop, addr: 127.0.0.1, checks: ["tcp:%d"]}
`, t.TempDir(), openPort))
		_, errOut, code := runBin(t, bin, nil, "--config", path, "--ui")
		if code != 1 || !strings.Contains(errOut, "the UI is unavailable in local-only mode") {
			t.Fatalf("code %d stderr %s", code, errOut)
		}
	})
	t.Run("once pass without tailscaled", func(t *testing.T) {
		t.Parallel()
		out, errOut, code := runBin(t, bin, nil, "--once", "--config", localCfg(openPort), "--socket", missing)
		if code != 0 {
			t.Fatalf("code %d\nstdout %s\nstderr %s", code, out, errOut)
		}
		if !strings.Contains(out, "loop tcp:") || !strings.Contains(out, " ok ") {
			t.Fatalf("stdout %s", out)
		}
	})
	t.Run("once fail without tailscaled", func(t *testing.T) {
		t.Parallel()
		out, errOut, code := runBin(t, bin, nil, "--once", "--config", localCfg(closedPort), "--socket", missing)
		if code != 1 {
			t.Fatalf("code %d\nstdout %s\nstderr %s", code, out, errOut)
		}
		if !strings.Contains(out, "fail") || !strings.Contains(out, "connection refused") {
			t.Fatalf("stdout %s", out)
		}
	})
	t.Run("server schedules checks without tailscaled", func(t *testing.T) {
		t.Parallel()
		listen := freePort(t)
		cmd := exec.Command(bin, "--config", localCfg(openPort), "--socket", missing,
			"--listen", fmt.Sprintf("127.0.0.1:%d", listen))
		var stderr syncBuffer
		cmd.Stderr = &stderr
		if startErr := cmd.Start(); startErr != nil {
			t.Fatal(startErr)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		metrics := fmt.Sprintf("http://127.0.0.1:%d/metrics", listen)
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(httpBody(metrics), `tailwatch_target_state{target="loop"} 1`) {
			if time.Now().After(deadline) {
				t.Fatalf("loop never reached up\nmetrics %s\nstderr %s", httpBody(metrics), stderr.String())
			}
			time.Sleep(100 * time.Millisecond)
		}
		host, hostErr := os.Hostname()
		if hostErr != nil {
			t.Fatal(hostErr)
		}
		want := fmt.Sprintf(`"msg":"collector identity","hostname":%q,"local_only":true`, host)
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr lacks %s\n%s", want, stderr.String())
		}
		if signalErr := cmd.Process.Signal(syscall.SIGTERM); signalErr != nil {
			t.Fatal(signalErr)
		}
		if waitErr := cmd.Wait(); waitErr != nil {
			t.Fatalf("exit %v\nstderr %s", waitErr, stderr.String())
		}
	})
}

// syncBuffer is a [bytes.Buffer] safe to read while a child process writes it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func httpBody(url string) string {
	resp, err := http.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func runningStatus() *ipnstate.Status {
	tags := views.SliceOf([]string{"tag:monitor"})
	return &ipnstate.Status{
		BackendState: ipn.Running.String(),
		TUN:          true,
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.8")},
		Self: &ipnstate.PeerStatus{
			HostName: "monitor",
			DNSName:  "monitor.example.ts.net.",
			Tags:     &tags,
		},
	}
}

func fakeTailscaled(t *testing.T, st *ipnstate.Status, prefs *ipn.Prefs) (string, func()) {
	t.Helper()
	statusBody, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	prefsBody, err := json.Marshal(prefs)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "tailscaled.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/localapi/v0/status"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statusBody)
		case r.URL.Path == "/localapi/v0/prefs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(prefsBody)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	return sock, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func writeCfg(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tail-watch.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func buildTailWatch(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "tail-watch")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = packageDir(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return binPath
}

func packageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Dir(file)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(filepath.Dir(packageDir(t)))
}

func runBin(t *testing.T, bin string, extraEnv []string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run: %v\nstderr %s", err, stderr.String())
		}
	}
	return stdout.String(), stderr.String(), code
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wait %s: %v", url, last)
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

//nolint:paralleltest // run installs a process-wide logger and these tests stub setec startup.
func TestRunSetecStore(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	sock := filepath.Join(t.TempDir(), "missing.sock")
	target := "targets:\n  - {name: a, addr: 10.0.0.1, checks: [icmp]}\n"
	t.Run("no setec keys", func(t *testing.T) {
		called := false
		stubOpenSetec(t, func(
			context.Context, netEnv, setecSettings, []string, *slog.Logger,
		) (*setecStore, error) {
			called = true
			return nil, errors.New("setec store must not be built")
		})
		path := writeCfg(t, target)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--config", path, "--socket", sock}, &stdout, &stderr); code == 0 || called {
			t.Fatalf("exit %d called %v\n%s", code, called, stderr.String())
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		withSetecTimeout(t, 500*time.Millisecond)
		path := writeCfg(t, `
collector:
  setec:
    server: https://127.0.0.1:1
sinks:
  - {name: hook, type: webhook, url_setec: prod/hook/url}
`+target)
		var stdout, stderr bytes.Buffer
		start := time.Now()
		code := run([]string{"--config", path, "--socket", sock}, &stdout, &stderr)
		if code == 0 || time.Since(start) > 5*time.Second {
			t.Fatalf("exit %d after %s\n%s", code, time.Since(start), stderr.String())
		}
		msg := stderr.String()
		if !strings.Contains(msg, "prod/hook/url") || !strings.Contains(msg, "127.0.0.1:1") {
			t.Fatalf("stderr %s", msg)
		}
		if strings.Contains(msg, "tailscaled") {
			t.Fatalf("preflight ran before setec:\n%s", msg)
		}
	})
	t.Run("once", func(t *testing.T) {
		called := false
		stubOpenSetec(t, func(
			context.Context, netEnv, setecSettings, []string, *slog.Logger,
		) (*setecStore, error) {
			called = true
			return nil, errors.New("setec store must not be built")
		})
		path := writeCfg(t, `
collector:
  setec:
    server: https://setec.example.ts.net
sinks:
  - {name: hook, type: webhook, url_setec: prod/hook/url}
`+target)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--once", "--config", path, "--socket", sock}, &stdout, &stderr); called {
			t.Fatalf("exit %d\n%s", code, stderr.String())
		}
	})
}

func TestCollectorFromPrefersIPv4(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ips  []string
		want string
	}{
		{"v4 first", []string{"100.64.0.8", "fd7a:115c:a1e0::1"}, "100.64.0.8"},
		{"v4 after v6", []string{"fd7a:115c:a1e0::1", "100.64.0.8"}, "100.64.0.8"},
		{"v6 only", []string{"fd7a:115c:a1e0::1"}, "fd7a:115c:a1e0::1"},
		{"none", nil, ""},
	}
	for _, tt := range tests {
		got := collectorFrom(preflightResult{Hostname: "monitor", IPs: tt.ips, Tags: nil, Warnings: nil})
		if got.Name != "monitor" || got.TailscaleIP != tt.want {
			t.Errorf("%s: got %+v want ip %q", tt.name, got, tt.want)
		}
	}
}

//nolint:paralleltest // run installs a process-wide logger.
func TestRunUIFalseRejectsAuthorization(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	path := writeCfg(t, `
collector:
  ui: true
  state_dir: /var/lib/tail-watch
  authorization:
    capability: example.com/cap/tail-watch
targets:
  - {name: a, addr: 10.0.0.1, checks: [icmp]}
`)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--config", path, "--ui=false"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "collector.authorization") {
		t.Fatalf("code %d\n%s", code, stderr.String())
	}
}

func TestRunAuthorizationStartup(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), nil)
	t.Cleanup(closeAPI)
	dir := t.TempDir()
	off := writeCfg(t, fmt.Sprintf(`
collector:
  ui: true
  state_dir: %q
  listen: 127.0.0.1:%d
targets:
  - {name: a, addr: 127.0.0.1, checks: [icmp]}
`, dir, freePort(t)))
	offLog := startTailWatch(t, bin, sock, off)
	if !strings.Contains(offLog, "write API enabled") || strings.Contains(offLog, "ui authorization") ||
		strings.Contains(offLog, "direct connections") {
		t.Fatalf("authorization off changed the startup log:\n%s", offLog)
	}

	onPort := freePort(t)
	on := writeCfg(t, fmt.Sprintf(`
collector:
  ui: true
  state_dir: %q
  listen: 0.0.0.0:%d
  authorization:
    capability: example.com/cap/tail-watch
    service: svc:tail-watch
targets:
  - {name: a, addr: 127.0.0.1, checks: [icmp]}
`, dir, onPort))
	onLog := startTailWatch(t, bin, sock, on)
	for _, want := range []string{
		"write API enabled", "controlled only by the network",
		"ui authorization enabled", "example.com/cap/tail-watch", `"service":true`,
		"direct connections have no identity and are denied",
	} {
		if !strings.Contains(onLog, want) {
			t.Errorf("startup log missing %s:\n%s", want, onLog)
		}
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", onPort)
	waitHTTP(t, base+"/healthz")
	if got := httpGet(t, base+"/metrics"); !strings.Contains(got, "tailwatch_") {
		t.Fatalf("/metrics with authorization on: %s", got[:min(200, len(got))])
	}
	resp, err := http.Get(base + "/api/v1/targets")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("/api/v1/targets without identity = %d, want 403", resp.StatusCode)
	}
}

func TestCheckConfigAuthorizationUI(t *testing.T) {
	t.Parallel()
	path := writeCfg(t, "collector:\n  authorization:\n    capability: example.com/cap/tail-watch\n")
	for _, tt := range []struct {
		args []string
		want int
	}{
		{[]string{"check-config", "--config", path}, 1},
		{[]string{"check-config", "--config", path, "--ui=false"}, 1},
		{[]string{"check-config", "--config", path, "--ui"}, 0},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(tt.args, &stdout, &stderr); code != tt.want {
			t.Errorf("%v: code %d, want %d\n%s", tt.args, code, tt.want, stderr.String())
		}
	}
}

func startTailWatch(t *testing.T, bin, sock, cfg string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, "--config", cfg, "--socket", sock)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "write API enabled") || strings.Contains(stderr.String(), "http server") {
			return stderr.String()
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("collector did not log startup:\n%s", stderr.String())
	return ""
}
