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
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
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
		!strings.Contains(stdout.String(), "checks=icmp") {
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
	subnet := []Target{{Name: "sw", Addr: "10.20.0.1", Network: NetworkTailnet, Checks: []check{icmpCheck{}},
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
	}}
	loop := []Target{{Name: "lo", Addr: "127.0.0.1", Network: NetworkTailnet, Checks: []check{tcpCheck{port: 9}},
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
	}}

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
