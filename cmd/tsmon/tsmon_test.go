package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/views"
)

func TestVersionAndCheckConfig(t *testing.T) {
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
	if !strings.Contains(stdout.String(), "a interval=30s timeout=5s") || !strings.Contains(stdout.String(), "checks=icmp") {
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

func TestMissingConfigDoesNotDial(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "tailscaled.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
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

func TestPreflight(t *testing.T) {
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
	subnet := []Target{{Name: "sw", Addr: "10.20.0.1", Network: NetworkTailnet, Checks: []check{icmpCheck{}}}}
	loop := []Target{{Name: "lo", Addr: "127.0.0.1", Network: NetworkTailnet, Checks: []check{tcpCheck{port: 9}}}}

	t.Run("needs login", func(t *testing.T) {
		_, err := evaluatePreflight(preflightInput{Status: &ipnstate.Status{BackendState: ipn.NeedsLogin.String()}, Socket: "/run/ts.sock"})
		if err == nil || !strings.Contains(err.Error(), "tailscale up") || !strings.Contains(err.Error(), "NeedsLogin") {
			t.Fatal(err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		_, err := evaluatePreflight(preflightInput{Socket: "/run/ts.sock"})
		if err == nil || !strings.Contains(err.Error(), "not reachable") || !strings.Contains(err.Error(), "/run/ts.sock") {
			t.Fatal(err)
		}
	})
	t.Run("userspace", func(t *testing.T) {
		st := running()
		st.TUN = false
		_, err := evaluatePreflight(preflightInput{Status: st, GOOS: "linux"})
		if err == nil || !strings.Contains(err.Error(), "userspace") {
			t.Fatal(err)
		}
	})
	t.Run("accept routes", func(t *testing.T) {
		_, err := evaluatePreflight(preflightInput{
			Status:  running(),
			Prefs:   &ipn.Prefs{RouteAll: false},
			Targets: subnet,
			GOOS:    "linux",
		})
		if err == nil || !strings.Contains(err.Error(), "tailscale set --accept-routes") {
			t.Fatal(err)
		}
	})
	t.Run("routes accepted", func(t *testing.T) {
		res, err := evaluatePreflight(preflightInput{
			Status:  running(),
			Prefs:   &ipn.Prefs{RouteAll: true},
			Targets: subnet,
			GOOS:    "linux",
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Hostname != "monitor" || len(res.IPs) != 1 || !strings.Contains(strings.Join(res.Tags, ","), "tag:monitor") {
			t.Fatalf("identity %+v", res)
		}
	})
	t.Run("loopback does not need routes", func(t *testing.T) {
		if _, err := evaluatePreflight(preflightInput{Status: running(), Targets: loop, GOOS: "linux"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("exit node", func(t *testing.T) {
		st := running()
		st.ExitNodeStatus = &ipnstate.ExitNodeStatus{ID: "exit"}
		res, err := evaluatePreflight(preflightInput{Status: st, GOOS: "linux"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "exit node") {
			t.Fatalf("warnings %v", res.Warnings)
		}
	})
	t.Run("missing tag", func(t *testing.T) {
		st := running()
		st.Self.Tags = nil
		res, err := evaluatePreflight(preflightInput{Status: st, GOOS: "linux", Targets: loop})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "tag:monitor") {
			t.Fatalf("warnings %v", res.Warnings)
		}
	})
}

func TestCommandIntegration(t *testing.T) {
	bin := buildTsmon(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), &ipn.Prefs{RouteAll: true})
	defer closeAPI()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
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
		out, errOut, code := runBin(t, bin, nil, "--once", "--config", passPath, "--socket", sock)
		if code != 0 {
			t.Fatalf("code %d\nstdout %s\nstderr %s", code, out, errOut)
		}
		if !strings.Contains(out, "loop tcp:") || !strings.Contains(out, " ok ") {
			t.Fatalf("stdout %s", out)
		}
	})
	t.Run("once fail", func(t *testing.T) {
		out, errOut, code := runBin(t, bin, nil, "--once", "--config", failPath, "--socket", sock)
		if code != 1 {
			t.Fatalf("code %d\nstdout %s\nstderr %s", code, out, errOut)
		}
		if !strings.Contains(out, "fail") || !strings.Contains(out, "connection refused") {
			t.Fatalf("stdout %s", out)
		}
	})
	t.Run("debug logs", func(t *testing.T) {
		_, errOut, code := runBin(t, bin, []string{"TSMON_LOG_LEVEL=debug"}, "--once", "--config", passPath, "--socket", sock)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--config", httpPath, "--socket", sock)
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
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
	if !strings.Contains(metrics, "tsmon_") || !strings.Contains(metrics, `target="loop"`) || !strings.Contains(metrics, `site="lab"`) || !strings.Contains(metrics, `class="tcp"`) {
		t.Fatalf("metrics %s", metrics)
	}
	status := httpGet(t, base+"/")
	if !strings.Contains(status, "loop") {
		t.Fatalf("status page missing target: %s", status[:min(200, len(status))])
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SIGTERM exit %v stderr %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process did not exit after SIGTERM")
	}
}

func TestSIGTERM(t *testing.T) {
	bin := buildTsmon(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), &ipn.Prefs{RouteAll: true})
	defer closeAPI()
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
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
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
	path := filepath.Join(t.TempDir(), "tsmon.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func buildTsmon(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tsmon-bin")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "tsmon")
		cmd := exec.Command("go", "build", "-o", binPath, ".")
		cmd.Dir = packageDir(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("build: %w\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
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
		var ee *exec.ExitError
		if errors.As(err, &ee) {
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
