package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"tailscale.com/client/tailscale/apitype"
)

// TestLiveAuthzWhoIs asks the lab tailnet's tailscaled, inside the live-stack
// Docker node, what WhoIs and WhoIsForService return for TAIL_WATCH_LIVE_CALLER.
// It is off unless TAIL_WATCH_LIVE_AUTHZ=1. It never uses the host tailscale
// client. The policy and the three caller states are in
// docs/contributing/live-authz-test.md.
func TestLiveAuthzWhoIs(t *testing.T) {
	t.Parallel()
	if os.Getenv("TAIL_WATCH_LIVE_AUTHZ") == "" {
		t.Skip("set TAIL_WATCH_LIVE_AUTHZ=1 to ask a lab tailnet whois")
	}
	caller := os.Getenv("TAIL_WATCH_LIVE_CALLER")
	if caller == "" {
		t.Fatal("TAIL_WATCH_LIVE_AUTHZ=1 needs TAIL_WATCH_LIVE_CALLER")
	}
	service := os.Getenv("TAIL_WATCH_LIVE_SERVICE")
	if service == "" {
		service = "svc:tail-watch"
	}
	container := liveAuthzContainer(t)

	status, body := liveWhoIs(t, container, caller, "")
	t.Logf("WhoIs %s status %d caps %s", caller, status, capSummary(body))
	scopedStatus, scopedBody := liveWhoIs(t, container, caller, service)
	t.Logf("WhoIsForService %s %s status %d caps %s", service, caller, scopedStatus, capSummary(scopedBody))

	v4, v6 := whoisAddrs(body)
	forms := []string{}
	if v4.IsValid() {
		forms = append(forms, v4.String(), v4.String()+":1")
	}
	if v6.IsValid() {
		forms = append(forms, v6.String())
	}
	if extra := os.Getenv("TAIL_WATCH_LIVE_CALLER6"); extra != "" && !v6.IsValid() {
		forms = append(forms, extra)
	}
	if len(forms) < 3 {
		t.Fatalf("need a bare IPv4, a bare IPv6 and an ip:port; got %v from %s", forms, caller)
	}
	for _, form := range forms {
		st, raw := liveWhoIs(t, container, form, "")
		t.Logf("WhoIs form %q status %d caps %s", form, st, capSummary(raw))
	}
}

func liveAuthzContainer(t *testing.T) string {
	t.Helper()
	if name := os.Getenv("TAIL_WATCH_LIVE_CONTAINER"); name != "" {
		return name
	}
	project := os.Getenv("TAIL_WATCH_LIVE_PROJECT")
	if project == "" {
		project = "tail-watch-live-stack"
	}
	out, err := exec.Command(
		"docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+project,
		"--filter", "label=com.docker.compose.service=tailscale",
	).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" || strings.Contains(id, "\n") {
		t.Fatalf("want one tailscale container for project %s, got %q", project, id)
	}
	return id
}

// liveWhoIs calls LocalAPI inside the live-stack tailscale container. The
// container's 127.0.0.1:9002 listener is only the image health port, so the
// call goes through `tailscale debug localapi` on the node's own socket.
func liveWhoIs(t *testing.T, container, addr, svc string) (int, []byte) {
	t.Helper()
	q := url.Values{}
	q.Set("addr", addr)
	if svc != "" {
		q.Set("svc_name", svc)
	}
	path := "/localapi/v0/whois?" + q.Encode()
	cmd := exec.Command("docker", "exec", container, "tailscale", "debug", "localapi", path)
	out, err := cmd.CombinedOutput()
	status := whoisHTTPStatus(out)
	if status == 0 && bytes.Contains(out, []byte("{")) {
		status = http.StatusOK
	}
	if status == 0 && err != nil {
		t.Fatalf("whois %s: %v\n%s", addr, err, out)
	}
	return status, out
}

func whoisHTTPStatus(out []byte) int {
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "HTTP/") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var status int
		if _, err := fmt.Sscan(fields[1], &status); err == nil {
			return status
		}
	}
	return 0
}

func capSummary(body []byte) string {
	idx := strings.Index(string(body), "{")
	if idx < 0 {
		return strings.TrimSpace(string(body))
	}
	var resp apitype.WhoIsResponse
	if err := json.Unmarshal(body[idx:], &resp); err != nil {
		return strings.TrimSpace(string(body[idx:]))
	}
	if len(resp.CapMap) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(resp.CapMap)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func whoisAddrs(body []byte) (netip.Addr, netip.Addr) {
	var v4, v6 netip.Addr
	idx := strings.Index(string(body), "{")
	if idx < 0 {
		return v4, v6
	}
	var resp apitype.WhoIsResponse
	if err := json.Unmarshal(body[idx:], &resp); err != nil || resp.Node == nil {
		return v4, v6
	}
	for _, pfx := range resp.Node.Addresses {
		addr := pfx.Addr()
		if addr.Is4() && !v4.IsValid() {
			v4 = addr
		}
		if addr.Is6() && !v6.IsValid() {
			v6 = addr
		}
	}
	return v4, v6
}
