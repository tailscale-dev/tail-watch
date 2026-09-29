package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func sinkConfig(port, listen int, extra string) string {
	return fmt.Sprintf(`
collector:
  listen: 127.0.0.1:%d
%s
defaults:
  fail_threshold: 1
  recover_threshold: 1
targets:
  - name: loop
    addr: 127.0.0.1
    interval: 200ms
    timeout: 100ms
    checks: ["tcp:%d"]
sinks:
  - name: hook
    type: webhook
    url_env: TW_IT_HOOK
    secret_env: TW_IT_SECRET
heartbeat:
  url_env: TW_IT_BEAT
  interval: 1s
`, listen, extra, port)
}

func TestServerDeliversNotificationsAndHeartbeat(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), nil)
	t.Cleanup(closeAPI)
	hook := newReceiver(t)
	beat := newReceiver(t)
	listen := freePort(t)
	path := writeCfg(t, sinkConfig(closedPort(t), listen, ""))

	cmd := exec.Command(bin, "--config", path, "--socket", sock)
	cmd.Env = append(os.Environ(),
		"TW_IT_HOOK="+hook.srv.URL, "TW_IT_SECRET=integration-secret", "TW_IT_BEAT="+beat.srv.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	eventually(t, func() bool { return hook.count() >= 2 })
	types := map[string]bool{}
	for i := range hook.count() {
		body := string(hook.request(i).Body)
		switch {
		case strings.Contains(body, `"type":"target.down"`):
			types["down"] = true
			if !strings.Contains(body, `"name":"monitor"`) || !strings.Contains(body, `"tailscale_ip":"100.64.0.8"`) {
				t.Fatalf("collector identity missing: %s", body)
			}
		case strings.Contains(body, `"type":"collector.started"`):
			types["started"] = true
		}
		if hook.request(i).Header.Get(signatureHeader) == "" {
			t.Fatal("configured secret must sign every request")
		}
	}
	if !types["down"] || !types["started"] {
		t.Fatalf("got %v\nstderr:\n%s", types, stderr.String())
	}
	eventually(t, func() bool { return beat.count() >= 1 })

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit %v\n%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not stop the collector")
	}
	settled := beat.count()
	time.Sleep(1500 * time.Millisecond)
	if beat.count() != settled {
		t.Fatal("heartbeat continued after shutdown")
	}
}

func TestOnceWithSinksSendsNothing(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), nil)
	t.Cleanup(closeAPI)
	hook := newReceiver(t)
	beat := newReceiver(t)
	path := writeCfg(t, sinkConfig(closedPort(t), freePort(t), ""))
	out, errOut, code := runBin(t, bin,
		[]string{"TW_IT_HOOK=" + hook.srv.URL, "TW_IT_SECRET=x", "TW_IT_BEAT=" + beat.srv.URL},
		"--once", "--config", path, "--socket", sock)
	if code != 1 || !strings.Contains(out, "loop") {
		t.Fatalf("--once against a down target: exit %d\n%s\n%s", code, out, errOut)
	}
	time.Sleep(300 * time.Millisecond)
	if hook.count() != 0 || beat.count() != 0 {
		t.Fatalf("--once must not notify or heartbeat: %d %d", hook.count(), beat.count())
	}
}

func TestOnceDoesNotNeedSecrets(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), nil)
	t.Cleanup(closeAPI)
	path := writeCfg(t, sinkConfig(closedPort(t), freePort(t), ""))
	_, errOut, code := runBin(t, bin, nil, "--once", "--config", path, "--socket", sock)
	if code != 1 || strings.Contains(errOut, "TW_IT_HOOK") {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
}

func TestServerFailsFastOnUnsetSecret(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), nil)
	t.Cleanup(closeAPI)
	path := writeCfg(t, sinkConfig(closedPort(t), freePort(t), ""))
	_, errOut, code := runBin(t, bin, nil, "--config", path, "--socket", sock)
	if code == 0 || !strings.Contains(errOut, "hook") || !strings.Contains(errOut, "TW_IT_HOOK") {
		t.Fatalf("exit %d, stderr must name the sink and variable:\n%s", code, errOut)
	}
}

func TestServerRefusesCorruptState(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), nil)
	t.Cleanup(closeAPI)
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/"+stateFileName, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeCfg(t, sinkConfig(closedPort(t), freePort(t), "  state_dir: "+dir))
	_, errOut, code := runBin(
		t,
		bin,
		[]string{"TW_IT_HOOK=http://127.0.0.1:1", "TW_IT_SECRET=x", "TW_IT_BEAT=http://127.0.0.1:1"},
		"--config",
		path,
		"--socket",
		sock,
	)
	if code == 0 || !strings.Contains(errOut, "corrupt") {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
}
