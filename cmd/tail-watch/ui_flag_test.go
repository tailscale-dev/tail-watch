package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUIFlagOverridesConfig(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	tests := []struct {
		name string
		yaml string
		flag *bool
		want bool
	}{
		{"unset", "targets: []\n", nil, false},
		{"config on", "collector: {ui: true}\ntargets: []\n", nil, true},
		{"config off", "collector: {ui: false}\ntargets: []\n", nil, false},
		{"flag on over config off", "collector: {ui: false}\ntargets: []\n", &yes, true},
		{"flag off over config on", "collector: {ui: true}\ntargets: []\n", &no, false},
		{"flag on without config", "targets: []\n", &yes, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseConfig([]byte(tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.UIEnabled(tt.flag); got != tt.want {
				t.Fatalf("UIEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}

//nolint:paralleltest // run installs a process-wide default logger.
func TestUIWithoutStateDirFailsBeforePreflight(t *testing.T) {
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
	cfgPath := writeCfg(t, "targets: [{name: a, addr: 10.0.0.1, checks: [icmp]}]\n")
	tests := []struct {
		name string
		args []string
	}{
		{"flag", []string{"--ui"}},
		{"config key", nil},
	}
	for _, tt := range tests {
		path := cfgPath
		if tt.name == "config key" {
			path = writeCfg(t, "collector: {ui: true}\ntargets: [{name: a, addr: 10.0.0.1, checks: [icmp]}]\n")
		}
		var stderr bytes.Buffer
		args := append([]string{"--config", path, "--socket", sock}, tt.args...)
		code := run(args, &bytes.Buffer{}, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "collector.state_dir") {
			t.Fatalf("%s: code %d stderr %s", tt.name, code, stderr.String())
		}
	}
	select {
	case <-accepted:
		t.Fatal("dialed tailscaled before rejecting the UI without state_dir")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestOnceIgnoresUI(t *testing.T) {
	t.Parallel()
	bin := buildTailWatch(t)
	sock, closeAPI := fakeTailscaled(t, runningStatus(), nil)
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
	port := ln.Addr().(*net.TCPAddr).Port
	path := writeCfg(t, fmt.Sprintf(`
collector:
  listen: 127.0.0.1:%d
targets:
  - name: loop
    addr: 127.0.0.1
    interval: 200ms
    timeout: 100ms
    checks: ["tcp:%d"]
`, freePort(t), port))
	out, errOut, code := runBin(t, bin, nil, "--once", "--ui", "--config", path, "--socket", sock)
	if code != 0 {
		t.Fatalf("code %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	if !strings.Contains(out, "loop tcp:") || !strings.Contains(out, " ok ") {
		t.Fatalf("stdout %s", out)
	}
	if strings.Contains(errOut, `"listening"`) || strings.Contains(errOut, "write API") {
		t.Fatalf("--once started a server: %s", errOut)
	}
}
