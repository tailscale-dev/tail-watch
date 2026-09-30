package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// TestUIRouteOwner runs the address-to-router matching from app.js under bun
// or node, so the subnet-route hint is checked without a browser.
func TestUIRouteOwner(t *testing.T) {
	t.Parallel()
	runtime, err := exec.LookPath("bun")
	if err != nil {
		if runtime, err = exec.LookPath("node"); err != nil {
			t.Skip("neither bun nor node is installed")
		}
	}
	src, err := os.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(src), "function peerValue")
	end := strings.Index(string(src), "// updateRouteHint")
	if start < 0 || end < start {
		t.Fatal("route matching functions not found in app.js")
	}
	funcs := regexp.MustCompile(`(?m)^//.*\n`).ReplaceAllString(string(src[start:end]), "")

	peers := []map[string]any{
		{"name": "nyc-router", "ips": []string{"100.64.0.1"}, "routes": []string{"10.20.0.0/16", "fd00:20::/32"}},
		{"name": "lab-router", "ips": []string{"100.64.0.2"}, "routes": []string{"10.20.5.0/24"}},
		{"name": "", "ips": []string{"100.64.0.3"}, "routes": []string{"172.16.0.0/12"}},
		{"name": "lab-nas", "ips": []string{"100.64.0.4"}},
	}
	tests := []struct{ text, want string }{
		{"10.20.0.50", "nyc-router"},
		{" 10.20.0.50 ", "nyc-router"},
		{"10.20.5.9", "lab-router"},
		{"192.168.99.1", ""},
		{"172.20.1.1", "100.64.0.3"},
		{"fd00:20::7", "nyc-router"},
		{"fd00:21::7", ""},
		{"nas.lab", ""},
		{"10.20.0.256", ""},
		{"", ""},
	}
	peersJSON, _ := json.Marshal(peers)
	var inputs []string
	for _, tc := range tests {
		b, _ := json.Marshal(tc.text)
		inputs = append(inputs, string(b))
	}
	script := funcs + "\nconst peers = " + string(peersJSON) +
		";\nconsole.log(JSON.stringify([" + strings.Join(inputs, ",") + "].map((t) => routeOwner(t, peers))));\n"
	out, err := exec.CommandContext(t.Context(), runtime, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got []string
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatalf("bad output %q: %v", out, err)
	}
	for i, tc := range tests {
		if got[i] != tc.want {
			t.Errorf("routeOwner(%q) = %q, want %q", tc.text, got[i], tc.want)
		}
	}
}
