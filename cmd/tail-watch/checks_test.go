package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tstest"
	"tailscale.com/tstime"
	"tailscale.com/types/key"
)

func TestTCPOpenAndRefused(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	env := &fakeEnv{}
	port := ln.Addr().(*net.TCPAddr).Port
	res, err := tcpCheck{port: port}.Run(context.Background(), env, &Target{Name: "up", Addr: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Err != nil || res.Latency < 0 {
		t.Fatalf("result %+v", res)
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	_, err = tcpCheck{port: closedPort}.Run(context.Background(), env, &Target{Name: "down", Addr: "127.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err %v", err)
	}
}

//nolint:paralleltest,tparallel // Subtests share request counts to verify that redirects are not followed.
func TestHTTPChecks(t *testing.T) {
	t.Parallel()
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, "hello DSM")
		case "/bounce":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/missing":
			_, _ = io.WriteString(w, strings.Repeat("a", 400))
		default:
			http.Error(w, "nope", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	env := &fakeEnv{}
	host := strings.TrimPrefix(srv.URL, "http://")
	addr, _, _ := strings.Cut(host, ":")

	t.Run("200", func(t *testing.T) {
		chk := mustCheck(t, "http:"+srv.URL+"/ok|DSM")
		if _, err := chk.Run(context.Background(), env, &Target{Addr: addr}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("302", func(t *testing.T) {
		before := hits["/ok"]
		chk := mustCheck(t, "http:"+srv.URL+"/bounce")
		if _, err := chk.Run(context.Background(), env, &Target{Addr: addr}); err != nil {
			t.Fatal(err)
		}
		if hits["/ok"] != before {
			t.Fatalf("redirect was followed, /ok hits %d -> %d", before, hits["/ok"])
		}
	})
	t.Run("500", func(t *testing.T) {
		chk := mustCheck(t, "http:"+srv.URL+"/fail")
		_, err := chk.Run(context.Background(), env, &Target{Addr: addr})
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatal(err)
		}
	})
	t.Run("missing text", func(t *testing.T) {
		chk := mustCheck(t, "http:"+srv.URL+"/missing|DSM")
		_, err := chk.Run(context.Background(), env, &Target{Addr: addr})
		if err == nil || !strings.Contains(err.Error(), "DSM") ||
			!strings.Contains(err.Error(), strings.Repeat("a", 300)) {
			t.Fatal(err)
		}
	})

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer tlsSrv.Close()
	tlsHost := tlsSrv.Listener.Addr().String()
	ip, _, _ := strings.Cut(tlsHost, ":")
	t.Run("tls rejected", func(t *testing.T) {
		chk := mustCheck(t, "http:"+tlsSrv.URL+"/")
		_, err := chk.Run(context.Background(), env, &Target{Addr: ip})
		if err == nil {
			t.Fatal("expected tls error")
		}
	})
	t.Run("insecure tls", func(t *testing.T) {
		chk := mustCheck(t, "http:"+tlsSrv.URL+"/")
		if _, err := chk.Run(context.Background(), env, &Target{Addr: ip, InsecureTLS: true}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestICMPPermissionAndFamily(t *testing.T) {
	t.Parallel()
	env := &fakeEnv{listenErr: errors.New("listen ip4:icmp 0.0.0.0: socket: operation not permitted")}
	_, err := icmpCheck{}.Run(context.Background(), env, &Target{Name: "lo", Addr: "127.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "net.ipv4.ping_group_range") ||
		!strings.Contains(err.Error(), "CAP_NET_RAW") {
		t.Fatalf("err %v", err)
	}
	if env.lastListen != "ip4:icmp" {
		t.Fatalf("listen network %s", env.lastListen)
	}
	env6 := &fakeEnv{listenErr: errors.New("listen ip6:icmp: socket: operation not permitted")}
	_, _ = icmpCheck{}.Run(context.Background(), env6, &Target{Name: "lo", Addr: "::1"})
	if env6.lastListen != "ip6:icmp" {
		t.Fatalf("listen network %s", env6.lastListen)
	}
}

func TestICMPLoopback(t *testing.T) {
	t.Parallel()
	env := newKernelEnv("")
	if !canICMP(env) {
		t.Skip("host allows no ICMP socket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := icmpCheck{}.Run(ctx, env, &Target{Name: "lo", Addr: "127.0.0.1"})
	if err != nil {
		if strings.Contains(err.Error(), "net.ipv4.ping_group_range") {
			t.Skipf("host allows no ICMP socket: %v", err)
		}
		t.Fatal(err)
	}
	if res.Latency <= 0 {
		t.Fatalf("latency %s", res.Latency)
	}
}

func canICMP(env *kernelEnv) bool {
	if env.UnprivilegedICMP() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c, err := env.ListenPacket(ctx, "ip4:icmp", "0.0.0.0")
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func TestRunCheckRecordsTimeout(t *testing.T) {
	t.Parallel()
	env := &fakeEnv{dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	a := &app{env: env, clock: tstime.StdClock{}, pub: newPublisher()}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := a.runCheck(ctx, &Target{Name: "t", Addr: "127.0.0.1"}, tcpCheck{port: 9})
	if time.Since(start) > time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "timeout") {
		t.Fatalf("err %v", res.Err)
	}
}

func TestMatchPeer(t *testing.T) {
	t.Parallel()
	v4 := netip.MustParseAddr("100.64.0.2")
	v6 := netip.MustParseAddr("fd7a:115c:a1e0::2")
	other := netip.MustParseAddr("100.64.0.9")
	st := &ipnstate.Status{
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "tailnet.ts.net"},
		Self: &ipnstate.PeerStatus{
			HostName:     "self",
			DNSName:      "self.tailnet.ts.net.",
			TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{},
	}
	k1 := key.NewNode().Public()
	k2 := key.NewNode().Public()
	k3 := key.NewNode().Public()
	st.Peer[k1] = &ipnstate.PeerStatus{
		HostName:     "nyc-router",
		DNSName:      "nyc-router.tailnet.ts.net.",
		TailscaleIPs: []netip.Addr{v6, v4},
	}
	st.Peer[k2] = &ipnstate.PeerStatus{
		HostName:     "dup",
		DNSName:      "dup-a.tailnet.ts.net.",
		TailscaleIPs: []netip.Addr{other},
	}
	st.Peer[k3] = &ipnstate.PeerStatus{
		HostName:     "dup",
		DNSName:      "dup-b.tailnet.ts.net.",
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.10")},
	}

	tests := []struct {
		name    string
		addr    string
		want    netip.Addr
		warn    bool
		missing bool
	}{
		{name: "dns with dot stripped", addr: "nyc-router.tailnet.ts.net", want: v4},
		{name: "short name", addr: "nyc-router", want: v4},
		{name: "hostname", addr: "self", want: netip.MustParseAddr("100.64.0.1")},
		{name: "exact dns beats other hostname", addr: "dup-a.tailnet.ts.net", want: other},
		{name: "ambiguous hostname", addr: "dup", want: other, warn: true},
		{name: "no match", addr: "missing", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ip, warn, ok := matchPeer(tt.addr, st)
			if tt.missing {
				if ok {
					t.Fatalf("found %s", ip)
				}
				return
			}
			if !ok || ip != tt.want {
				t.Fatalf("got %s ok=%v want %s", ip, ok, tt.want)
			}
			if tt.warn != (warn != "") {
				t.Fatalf("warn %q", warn)
			}
		})
	}
}

func TestStatusCache(t *testing.T) {
	t.Parallel()
	clk := tstest.NewClock(tstest.ClockOpts{Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	var calls int
	cache := &statusCache{
		clock: clk,
		ttl:   statusCacheTTL,
		fetch: func(context.Context) (*ipnstate.Status, error) {
			calls++
			return &ipnstate.Status{BackendState: "Running"}, nil
		},
	}
	if _, err := cache.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	clk.Advance(31 * time.Second)
	if _, err := cache.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}

func TestInterpretPing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pr      *ipnstate.PingResult
		err     error
		kind    string
		detail  string
		wantErr string
	}{
		{
			name:   "direct",
			pr:     &ipnstate.PingResult{Endpoint: "1.2.3.4:41641", LatencySeconds: 0.01},
			kind:   "direct",
			detail: "1.2.3.4:41641",
		},
		{
			name:   "derp",
			pr:     &ipnstate.PingResult{DERPRegionCode: "nyc", LatencySeconds: 0.05},
			kind:   "derp",
			detail: "nyc",
		},
		{
			name:   "peer relay",
			pr:     &ipnstate.PingResult{PeerRelay: "5.6.7.8:443:vni:1", LatencySeconds: 0.02},
			kind:   "peer_relay",
			detail: "5.6.7.8:443:vni:1",
		},
		{
			name:    "err set",
			pr:      &ipnstate.PingResult{Err: "timeout", Endpoint: "1.2.3.4:1"},
			wantErr: "timeout",
		},
		{
			name:    "no matching peer",
			pr:      &ipnstate.PingResult{Err: "no matching peer"},
			wantErr: "no peer handles 10.20.0.50; check grants to tag:monitor",
		},
		{
			name:    "error no matching peer",
			err:     errors.New("ping: no matching peer"),
			wantErr: "no peer handles 10.20.0.50; check grants to tag:monitor",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res, err := interpretPing("10.20.0.50", tt.pr, tt.err)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			kind, detail := pathKind(res.Ping)
			if kind != tt.kind || detail != tt.detail {
				t.Fatalf("path %s %s", kind, detail)
			}
			if res.Latency <= 0 {
				t.Fatalf("latency %s", res.Latency)
			}
		})
	}
}

func mustCheck(t *testing.T, s string) check {
	t.Helper()
	c, err := parseCheck(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
