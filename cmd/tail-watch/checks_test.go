package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tstest"
	"tailscale.com/tstime"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
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
	env := new(fakeEnv)

	port := ln.Addr().(*net.TCPAddr).Port
	res, err := tcpCheck{
		port: port,
	}.Run(
		context.Background(),
		env,
		&Target{Dynamic: false, Name: "up", Addr: "127.0.0.1",
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
			Network:          "",
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0, Route: "",
		},
	)
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
	_, err = tcpCheck{
		port: closedPort,
	}.Run(
		context.Background(),
		env,
		&Target{Dynamic: false, Name: "down", Addr: "127.0.0.1",
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
			Network:          "",
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0, Route: "",
		},
	)
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
	env := new(fakeEnv)

	host := strings.TrimPrefix(srv.URL, "http://")
	addr, _, _ := strings.Cut(host, ":")

	t.Run("200", func(t *testing.T) {
		chk := mustCheck(t, "http:"+srv.URL+"/ok|DSM")
		if _, err := chk.Run(context.Background(), env, &Target{Dynamic: false, Addr: addr,
			Name:             "",
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
			Network:          "",
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0, Route: "",
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("302", func(t *testing.T) {
		before := hits["/ok"]
		chk := mustCheck(t, "http:"+srv.URL+"/bounce")
		if _, err := chk.Run(context.Background(), env, &Target{Dynamic: false, Addr: addr,
			Name:             "",
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
			Network:          "",
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0, Route: "",
		}); err != nil {
			t.Fatal(err)
		}
		if hits["/ok"] != before {
			t.Fatalf("redirect was followed, /ok hits %d -> %d", before, hits["/ok"])
		}
	})
	t.Run("500", func(t *testing.T) {
		chk := mustCheck(t, "http:"+srv.URL+"/fail")
		_, err := chk.Run(context.Background(), env, &Target{Dynamic: false, Addr: addr,
			Name:             "",
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
			Network:          "",
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0, Route: "",
		})
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatal(err)
		}
	})
	t.Run("missing text", func(t *testing.T) {
		chk := mustCheck(t, "http:"+srv.URL+"/missing|DSM")
		_, err := chk.Run(context.Background(), env, &Target{Dynamic: false, Addr: addr,
			Name:             "",
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
			Network:          "",
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0, Route: "",
		})
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
		_, err := chk.Run(context.Background(), env, &Target{Dynamic: false, Addr: ip,
			Name:             "",
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
			Network:          "",
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0, Route: "",
		})
		if err == nil {
			t.Fatal("expected tls error")
		}
	})
	t.Run("insecure tls", func(t *testing.T) {
		chk := mustCheck(t, "http:"+tlsSrv.URL+"/")
		if _, err := chk.Run(context.Background(), env, &Target{Dynamic: false, Addr: ip, InsecureTLS: true,
			Name:             "",
			Site:             "",
			Severity:         "",
			Parent:           "",
			Interval:         0,
			Timeout:          0,
			FailThreshold:    0,
			RecoverThreshold: 0,
			FlapLimit:        0,
			FlapWindow:       0,
			Network:          "",
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0, Route: "",
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestICMPPermissionAndFamily(t *testing.T) {
	t.Parallel()
	env := new(fakeEnv)
	env.listenErr = errors.New("listen ip4:icmp 0.0.0.0: socket: operation not permitted")
	_, err := icmpCheck{}.Run(context.Background(), env, &Target{Dynamic: false, Name: "lo", Addr: "127.0.0.1",
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
		Network:          "",
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0, Route: "",
	})
	if err == nil || !strings.Contains(err.Error(), "net.ipv4.ping_group_range") ||
		!strings.Contains(err.Error(), "CAP_NET_RAW") {
		t.Fatalf("err %v", err)
	}
	if env.lastListen != "ip4:icmp" {
		t.Fatalf("listen network %s", env.lastListen)
	}
	env6 := new(fakeEnv)
	env6.listenErr = errors.New("listen ip6:icmp: socket: operation not permitted")
	_, _ = icmpCheck{}.Run(context.Background(), env6, &Target{Dynamic: false, Name: "lo", Addr: "::1",
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
		Network:          "",
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0, Route: "",
	})
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
	res, err := icmpCheck{}.Run(ctx, env, &Target{Dynamic: false, Name: "lo", Addr: "127.0.0.1",
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
		Network:          "",
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0, Route: "",
	})
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
	env := new(fakeEnv)
	env.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	a := &app{env: env, clock: tstime.StdClock{}, pub: newPublisher(),
		cfg:      nil,
		log:      nil,
		stdout:   nil,
		socket:   "",
		listen:   "",
		ui:       false,
		dynamic:  nil,
		uiRoutes: nil,
		registry: nil,
		resolver: nil,
		presence: nil,
		class:    nil,
		learner:  nil,
		alerts:   nil,
		metrics:  nil,
		watch:    nil,
		probeMu:  sync.Mutex{},
		probes:   nil, notify: nil, collector: CollectorInfo{Name: "", TailscaleIP: ""},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := a.runCheck(ctx, &Target{Dynamic: false, Name: "t", Addr: "127.0.0.1",
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
		Network:          "",
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0, Route: "",
	}, tcpCheck{port: 9})
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
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "tailnet.ts.net",
			Name:            "",
			MagicDNSEnabled: false,
		},
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
		{name: "dns with dot stripped", addr: "nyc-router.tailnet.ts.net", want: v4,
			warn:    false,
			missing: false,
		},
		{name: "short name", addr: "nyc-router", want: v4,
			warn:    false,
			missing: false,
		},
		{name: "hostname", addr: "self", want: netip.MustParseAddr("100.64.0.1"),
			warn:    false,
			missing: false,
		},
		{name: "exact dns beats other hostname", addr: "dup-a.tailnet.ts.net", want: other,
			warn:    false,
			missing: false,
		},
		{name: "ambiguous hostname", addr: "dup", want: other, warn: true,
			missing: false,
		},
		{name: "no match", addr: "missing", missing: true,
			want: netip.Addr{},
			warn: false,
		},
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
	clk := tstest.NewClock(tstest.ClockOpts{Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Step:             0,
		TimerChannelSize: 0,
		FollowRealTime:   false,
	})
	var calls int
	cache := &statusCache{
		clock: clk,
		ttl:   statusCacheTTL,
		fetch: func(context.Context) (*ipnstate.Status, error) {
			calls++
			return &ipnstate.Status{BackendState: "Running"}, nil
		},

		mu: sync.Mutex{},
		at: time.Time{},
		st: nil,
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
			name: "direct",
			pr: &ipnstate.PingResult{Endpoint: "1.2.3.4:41641", LatencySeconds: 0.01,
				IP:             "",
				NodeIP:         "",
				NodeName:       "",
				Err:            "",
				PeerRelay:      "",
				DERPRegionID:   0,
				DERPRegionCode: "",
				PeerAPIPort:    0,
				PeerAPIURL:     "",
				IsLocalIP:      false,
			},
			kind:   "direct",
			detail: "1.2.3.4:41641",

			err:     nil,
			wantErr: "",
		},
		{
			name: "derp",
			pr: &ipnstate.PingResult{DERPRegionCode: "nyc", LatencySeconds: 0.05,
				IP:           "",
				NodeIP:       "",
				NodeName:     "",
				Err:          "",
				Endpoint:     "",
				PeerRelay:    "",
				DERPRegionID: 0,
				PeerAPIPort:  0,
				PeerAPIURL:   "",
				IsLocalIP:    false,
			},
			kind:   "derp",
			detail: "nyc",

			err:     nil,
			wantErr: "",
		},
		{
			name: "peer relay",
			pr: &ipnstate.PingResult{PeerRelay: "5.6.7.8:443:vni:1", LatencySeconds: 0.02,
				IP:             "",
				NodeIP:         "",
				NodeName:       "",
				Err:            "",
				Endpoint:       "",
				DERPRegionID:   0,
				DERPRegionCode: "",
				PeerAPIPort:    0,
				PeerAPIURL:     "",
				IsLocalIP:      false,
			},
			kind:   "peer_relay",
			detail: "5.6.7.8:443:vni:1",

			err:     nil,
			wantErr: "",
		},
		{
			name: "err set",
			pr: &ipnstate.PingResult{Err: "timeout", Endpoint: "1.2.3.4:1",
				IP:             "",
				NodeIP:         "",
				NodeName:       "",
				LatencySeconds: 0,
				PeerRelay:      "",
				DERPRegionID:   0,
				DERPRegionCode: "",
				PeerAPIPort:    0,
				PeerAPIURL:     "",
				IsLocalIP:      false,
			},
			wantErr: "timeout",

			err:    nil,
			kind:   "",
			detail: "",
		},
		{
			name: "no matching peer",
			pr: &ipnstate.PingResult{Err: "no matching peer",
				IP:             "",
				NodeIP:         "",
				NodeName:       "",
				LatencySeconds: 0,
				Endpoint:       "",
				PeerRelay:      "",
				DERPRegionID:   0,
				DERPRegionCode: "",
				PeerAPIPort:    0,
				PeerAPIURL:     "",
				IsLocalIP:      false,
			},
			wantErr: "no peer handles 10.20.0.50; check grants to tag:monitor",

			err:    nil,
			kind:   "",
			detail: "",
		},
		{
			name:    "error no matching peer",
			err:     errors.New("ping: no matching peer"),
			wantErr: "no peer handles 10.20.0.50; check grants to tag:monitor",

			pr:     nil,
			kind:   "",
			detail: "",
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

func TestTLSCheck(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := tstest.NewClock(tstest.ClockOpts{
		Start:            start,
		Step:             0,
		TimerChannelSize: 0,
		FollowRealTime:   false,
	})
	ca := newTestCA(t, start.Add(-48*time.Hour), start.Add(365*24*time.Hour))

	nas := &ipnstate.PeerStatus{
		NodeID:       8,
		HostName:     "nas",
		DNSName:      "nas.tailnet.ts.net.",
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.8")},
	}
	routerRoutes := views.SliceOf([]netip.Prefix{netip.MustParsePrefix("10.20.0.0/24")})
	router := &ipnstate.PeerStatus{
		NodeID:        7,
		HostName:      "nyc-router",
		DNSName:       "nyc-router.tailnet.ts.net.",
		TailscaleIPs:  []netip.Addr{netip.MustParseAddr("100.64.0.7")},
		PrimaryRoutes: &routerRoutes,
	}
	withPeers := statusWith(nas, router)
	noPeers := statusWith()

	leaf := func(dns string, notBefore, notAfter time.Time) tls.Certificate {
		t.Helper()
		return ca.leaf(t, dns, notBefore, notAfter)
	}
	day := 24 * time.Hour
	valid := leaf("nas.tailnet.ts.net", start.Add(-time.Hour), start.Add(30*day))

	peers := statusResolver(withPeers)
	none := statusResolver(noPeers)
	tests := []tlsTrial{
		tlsTrialOf("leaf beyond 7 days", "nas.tailnet.ts.net", valid, peers, false, ""),
		tlsTrialOf("leaf exactly 7 days", "nas.tailnet.ts.net",
			leaf("nas.tailnet.ts.net", start.Add(-time.Hour), start.Add(tlsExpiryWindow)), peers, false, ""),
		tlsTrialOf("leaf inside 7 days", "nas.tailnet.ts.net",
			leaf("nas.tailnet.ts.net", start.Add(-time.Hour), start.Add(6*day)), peers, false,
			"certificate expires "+start.Add(6*day).UTC().Format(time.RFC3339)),
		tlsTrialOf("leaf already expired", "nas.tailnet.ts.net",
			leaf("nas.tailnet.ts.net", start.Add(-48*time.Hour), start.Add(-time.Hour)), peers, false, "expired"),
		tlsTrialOf("tailscale ip uses peer dns name", "100.64.0.8", valid, peers, false, ""),
		tlsTrialOf("short name uses peer dns name", "nas", valid, peers, false, ""),
		tlsTrialOf("full name uses peer dns name", "nas.tailnet.ts.net", valid, peers, false, ""),
		tlsTrialOf("subnet route does not supply the name", "10.20.0.50",
			leaf("nyc-router.tailnet.ts.net", start.Add(-time.Hour), start.Add(30*day)), peers, false, "certificate"),
		tlsTrialOf("service name with no peer", "web.tailnet.ts.net",
			leaf("web.tailnet.ts.net", start.Add(-time.Hour), start.Add(30*day)), none, false, ""),
		tlsTrialOf("trailing dot is stripped", "web.tailnet.ts.net.",
			leaf("web.tailnet.ts.net", start.Add(-time.Hour), start.Add(30*day)), none, false, ""),
		tlsTrialOf("short name with no peer stays short", "nas", valid, none, false, "certificate"),
		tlsTrialOf("nil resolver dials the dns name", "web.example.ts.net",
			leaf("web.example.ts.net", start.Add(-time.Hour), start.Add(30*day)), nil, false, ""),
		tlsTrialOf("status error still dials a dns name", "web.example.ts.net",
			leaf("web.example.ts.net", start.Add(-time.Hour), start.Add(30*day)),
			newTSResolver(clk, nil, func(context.Context) (*ipnstate.Status, error) {
				return nil, errors.New("status down")
			}), false, ""),
		tlsTrialOf("insecure self-signed leaf beyond the window", "nas",
			selfSignedLeaf(t, "nas", start.Add(-time.Hour), start.Add(30*day)), nil, true, ""),
		tlsTrialOf("insecure self-signed leaf inside the window", "nas",
			selfSignedLeaf(t, "nas", start.Add(-time.Hour), start.Add(2*day)), nil, true,
			"certificate expires "+start.Add(2*day).UTC().Format(time.RFC3339)),
		tlsTrialOf("insecure leaf names the subject when it has no san", "nas",
			selfSignedLeaf(t, "", start.Add(-time.Hour), start.Add(2*day)), nil, true, "CN=nas-device"),
		tlsTrialOf("insecure leaf not yet valid", "nas",
			selfSignedLeaf(t, "nas", start.Add(time.Hour), start.Add(30*day)), nil, true,
			"certificate is not valid before "+start.Add(time.Hour).UTC().Format(time.RFC3339)),
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runTLSTrial(t, clk, ca, tt)
		})
	}

	t.Run("seeded presence owns the ip and ignores the subnet", func(t *testing.T) {
		t.Parallel()
		pres := newPresence(nil, clk, nil)
		pres.apply(ipn.Notify{InitialStatus: withPeers})
		resolver := statusResolver(noPeers)
		resolver.presence = pres

		env := startTLS(t, valid)
		owned := mustCheck(t, "tls:443").(tlsCheck)
		owned.clock = clk
		owned.resolver = resolver
		owned.roots = ca.pool
		if _, err := owned.Run(context.Background(), env, tlsTarget("100.64.0.8", false)); err != nil {
			t.Fatal(err)
		}

		routed := startTLS(t, leaf("nyc-router.tailnet.ts.net", start.Add(-time.Hour), start.Add(30*day)))
		miss := owned
		if _, err := miss.Run(context.Background(), routed, tlsTarget("10.20.0.50", false)); err == nil {
			t.Fatal("subnet route supplied the certificate name")
		}
	})

	t.Run("expired leaf fails verification, not the window", func(t *testing.T) {
		t.Parallel()
		env := startTLS(t, leaf("nas.tailnet.ts.net", start.Add(-48*time.Hour), start.Add(-time.Hour)))
		chk := mustCheck(t, "tls:443").(tlsCheck)
		chk.clock = clk
		chk.roots = ca.pool
		_, err := chk.Run(context.Background(), env, tlsTarget("nas.tailnet.ts.net", false))
		var expiry *certExpiryError
		if err == nil || errors.As(err, &expiry) {
			t.Fatalf("err %v, want a verification error", err)
		}
		if classifyErr(err) != errTLS {
			t.Fatalf("class %v, want errTLS", classifyErr(err))
		}
	})

	t.Run("seeded presence miss falls back to status for self", func(t *testing.T) {
		t.Parallel()
		pres := newPresence(nil, clk, nil)
		pres.apply(ipn.Notify{InitialStatus: withPeers})
		withSelf := statusWith(nas)
		withSelf.Self = &ipnstate.PeerStatus{
			NodeID:       1,
			HostName:     "collector",
			DNSName:      "collector.tailnet.ts.net.",
			TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		}
		resolver := statusResolver(withSelf)
		resolver.presence = pres

		env := startTLS(t, leaf("collector.tailnet.ts.net", start.Add(-time.Hour), start.Add(30*day)))
		chk := mustCheck(t, "tls:443").(tlsCheck)
		chk.clock = clk
		chk.resolver = resolver
		chk.roots = ca.pool
		if _, err := chk.Run(context.Background(), env, tlsTarget("100.64.0.1", false)); err != nil {
			t.Fatal(err)
		}
	})
}

type tlsTrial struct {
	name     string
	addr     string
	insecure bool
	cert     tls.Certificate
	resolver *tsResolver
	wantErr  string
}

func tlsTrialOf(name, addr string, cert tls.Certificate, resolver *tsResolver, insecure bool, wantErr string) tlsTrial {
	return tlsTrial{
		name: name, addr: addr, insecure: insecure,
		cert: cert, resolver: resolver, wantErr: wantErr,
	}
}

func runTLSTrial(t *testing.T, clk tstime.Clock, ca testCA, tt tlsTrial) {
	t.Helper()
	env := startTLS(t, tt.cert)
	chk := mustCheck(t, "tls:443").(tlsCheck)
	chk.clock = clk
	chk.resolver = tt.resolver
	if !tt.insecure {
		chk.roots = ca.pool
	}
	res, err := chk.Run(context.Background(), env, tlsTarget(tt.addr, tt.insecure))
	if tt.wantErr == "" {
		if err != nil {
			t.Fatal(err)
		}
		if res.Check != "tls:443" || res.Latency <= 0 {
			t.Fatalf("result %+v", res)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
		t.Fatalf("err %v, want %q", err, tt.wantErr)
	}
}

func statusWith(peers ...*ipnstate.PeerStatus) *ipnstate.Status {
	tailnet := new(ipnstate.TailnetStatus)
	tailnet.MagicDNSSuffix = "tailnet.ts.net"
	st := &ipnstate.Status{
		CurrentTailnet: tailnet,
		Peer:           map[key.NodePublic]*ipnstate.PeerStatus{},
	}
	for _, ps := range peers {
		st.Peer[key.NewNode().Public()] = ps
	}
	return st
}

func statusResolver(st *ipnstate.Status) *tsResolver {
	return newTSResolver(tstime.StdClock{}, nil, func(context.Context) (*ipnstate.Status, error) {
		return st, nil
	})
}

func tlsTarget(addr string, insecure bool) *Target {
	return &Target{
		Dynamic:          false,
		Name:             "",
		Addr:             addr,
		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      insecure,
		Interval:         0,
		Timeout:          0,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Network:          "",
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
		Route:            "",
	}
}

func startTLS(t *testing.T, cert tls.Certificate) *fakeEnv {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	return dialingEnv(func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, ln.Addr().String())
	})
}

type testCA struct {
	cert *x509.Certificate
	key  crypto.Signer
	pool *x509.CertPool
}

func newTestCA(t *testing.T, notBefore, notAfter time.Time) testCA {
	t.Helper()
	key := p256Key(t)
	var subject pkix.Name
	subject.CommonName = "test-ca"
	tmpl := new(x509.Certificate)
	tmpl.SerialNumber = big.NewInt(1)
	tmpl.Subject = subject
	tmpl.NotBefore = notBefore
	tmpl.NotAfter = notAfter
	tmpl.IsCA = true
	tmpl.KeyUsage = x509.KeyUsageCertSign
	tmpl.BasicConstraintsValid = true
	der := createCert(t, tmpl, tmpl, key.Public(), key)
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return testCA{cert: cert, key: key, pool: pool}
}

func (ca testCA) leaf(t *testing.T, dns string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key := p256Key(t)
	var subject pkix.Name
	subject.CommonName = dns
	tmpl := new(x509.Certificate)
	tmpl.SerialNumber = big.NewInt(2)
	tmpl.Subject = subject
	tmpl.NotBefore = notBefore
	tmpl.NotAfter = notAfter
	tmpl.DNSNames = []string{dns}
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	der := createCert(t, tmpl, ca.cert, key.Public(), ca.key)
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	var out tls.Certificate
	out.Certificate = [][]byte{der, ca.cert.Raw}
	out.PrivateKey = key
	out.Leaf = parsed
	return out
}

func selfSignedLeaf(t *testing.T, dns string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key := p256Key(t)
	cn := dns
	if cn == "" {
		cn = "nas-device"
	}
	var subject pkix.Name
	subject.CommonName = cn
	tmpl := new(x509.Certificate)
	tmpl.SerialNumber = big.NewInt(3)
	tmpl.Subject = subject
	tmpl.NotBefore = notBefore
	tmpl.NotAfter = notAfter
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	if dns != "" {
		tmpl.DNSNames = []string{dns}
	}
	der := createCert(t, tmpl, tmpl, key.Public(), key)
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	var out tls.Certificate
	out.Certificate = [][]byte{der}
	out.PrivateKey = key
	out.Leaf = parsed
	return out
}

func p256Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func createCert(t *testing.T, tmpl, parent *x509.Certificate, pub any, priv any) []byte {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
