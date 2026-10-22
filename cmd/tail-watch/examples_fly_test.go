package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var tailscaleImagePattern = regexp.MustCompile(`tailscale/tailscale:(v[0-9.]+)`)

// TestFlyExampleAgrees keeps examples/fly consistent: its config parses,
// its state_dir sits on the volume fly.toml mounts and start.sh locks down,
// and its tailscaled matches the Compose example.
func TestFlyExampleAgrees(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), "examples", "fly")
	read := func(path ...string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{dir}, path...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	cfg, err := loadConfig(filepath.Join(dir, "tail-watch.yaml"))
	if err != nil {
		t.Fatalf("examples/fly/tail-watch.yaml: %v", err)
	}

	mount := regexp.MustCompile(`destination\s*=\s*"([^"]+)"`).FindStringSubmatch(read("fly.toml"))
	if mount == nil || !strings.HasPrefix(cfg.Collector.StateDir, mount[1]+"/") {
		t.Errorf("state_dir %q is not under the fly.toml mount %v", cfg.Collector.StateDir, mount)
	}
	if want := "chmod 700 " + cfg.Collector.StateDir; !strings.Contains(read("start.sh"), want) {
		t.Errorf("start.sh does not contain %q", want)
	}

	compose := tailscaleImagePattern.FindStringSubmatch(readExample(t, "compose.yaml"))
	flyfile := tailscaleImagePattern.FindStringSubmatch(read("Dockerfile"))
	if compose == nil || flyfile == nil || compose[1] != flyfile[1] {
		t.Errorf("tailscale tag: compose.yaml %v, examples/fly/Dockerfile %v", compose, flyfile)
	}
}

func readExample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
