package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest"
	"tailscale.com/tstime"
	"tailscale.com/types/opt"
)

func TestDecideLayers(t *testing.T) {
	t.Parallel()
	offline := opt.False
	online := opt.True
	unset := opt.Bool("")
	up := opt.True
	down := opt.False
	tests := []struct {
		name string
		in   classifyInput
		want layer
	}{
		{
			name: "offline router",
			in: classifyInput{errClass: errTimeout, handlerKnown: true, handlerOnline: offline,
				local:        false,
				tsping:       false,
				nodeTarget:   false,
				tunnelOK:     "",
				deviceICMPOK: "",
			},
			want: layerControl,
		},
		{
			name: "tunnel down",
			in: classifyInput{errClass: errTimeout, handlerKnown: true, handlerOnline: online, tunnelOK: down,
				local:        false,
				tsping:       false,
				nodeTarget:   false,
				deviceICMPOK: "",
			},
			want: layerTunnel,
		},
		{
			name: "device off lan",
			in: classifyInput{
				errClass:      errTimeout,
				handlerKnown:  true,
				handlerOnline: online,
				tunnelOK:      up,
				deviceICMPOK:  down,

				local:      false,
				tsping:     false,
				nodeTarget: false,
			},
			want: layerLAN,
		},
		{
			name: "port refused",
			in: classifyInput{errClass: errRefused, handlerKnown: true, handlerOnline: online, tunnelOK: up,
				local:        false,
				tsping:       false,
				nodeTarget:   false,
				deviceICMPOK: "",
			},
			want: layerService,
		},
		{
			name: "timeout but host pings",
			in: classifyInput{
				errClass:      errTimeout,
				handlerKnown:  true,
				handlerOnline: online,
				tunnelOK:      up,
				deviceICMPOK:  up,

				local:      false,
				tsping:     false,
				nodeTarget: false,
			},
			want: layerService,
		},
		{
			name: "node service",
			in: classifyInput{
				errClass:      errHTTP,
				nodeTarget:    true,
				handlerKnown:  true,
				handlerOnline: online,
				tunnelOK:      up,

				local:        false,
				tsping:       false,
				deviceICMPOK: "",
			},
			want: layerService,
		},
		{
			name: "node lan becomes service",
			in: classifyInput{
				errClass:      errTimeout,
				nodeTarget:    true,
				handlerKnown:  true,
				handlerOnline: online,
				tunnelOK:      up,
				deviceICMPOK:  down,

				local:  false,
				tsping: false,
			},
			want: layerService,
		},
		{
			name: "presence unknown tunnel",
			in: classifyInput{errClass: errTimeout, handlerKnown: true, handlerOnline: unset, tunnelOK: down,
				local:        false,
				tsping:       false,
				nodeTarget:   false,
				deviceICMPOK: "",
			},
			want: layerTunnel,
		},
		{
			name: "presence unknown lan",
			in: classifyInput{
				errClass:      errTimeout,
				handlerKnown:  true,
				handlerOnline: unset,
				tunnelOK:      up,
				deviceICMPOK:  down,

				local:      false,
				tsping:     false,
				nodeTarget: false,
			},
			want: layerLAN,
		},
		{name: "missing handler", in: classifyInput{errClass: errTimeout,
			local:         false,
			tsping:        false,
			nodeTarget:    false,
			handlerKnown:  false,
			handlerOnline: "",
			tunnelOK:      "",
			deviceICMPOK:  "",
		}, want: layerUnrouted},
		{name: "missing node ip", in: classifyInput{errClass: errOther,
			local:         false,
			tsping:        false,
			nodeTarget:    false,
			handlerKnown:  false,
			handlerOnline: "",
			tunnelOK:      "",
			deviceICMPOK:  "",
		}, want: layerUnrouted},
		{
			name: "tsping online",
			in: classifyInput{tsping: true, handlerKnown: true, handlerOnline: online, tunnelOK: down,
				local:        false,
				errClass:     "",
				nodeTarget:   false,
				deviceICMPOK: "",
			},
			want: layerTunnel,
		},
		{
			name: "tsping offline",
			in: classifyInput{tsping: true, handlerKnown: true, handlerOnline: offline,
				local:        false,
				errClass:     "",
				nodeTarget:   false,
				tunnelOK:     "",
				deviceICMPOK: "",
			},
			want: layerControl,
		},
		{
			name: "tsping presence unknown",
			in: classifyInput{tsping: true, handlerKnown: true, handlerOnline: unset,
				local:        false,
				errClass:     "",
				nodeTarget:   false,
				tunnelOK:     "",
				deviceICMPOK: "",
			},
			want: layerTunnel,
		},
		{name: "local refused", in: classifyInput{local: true, errClass: errRefused,
			tsping:        false,
			nodeTarget:    false,
			handlerKnown:  false,
			handlerOnline: "",
			tunnelOK:      "",
			deviceICMPOK:  "",
		}, want: layerService},
		{
			name: "local timeout",
			in: classifyInput{local: true, errClass: errTimeout, deviceICMPOK: down,
				tsping:        false,
				nodeTarget:    false,
				handlerKnown:  false,
				handlerOnline: "",
				tunnelOK:      "",
			},
			want: layerLAN,
		},
		{
			name: "local host up",
			in: classifyInput{local: true, errClass: errTimeout, deviceICMPOK: up,
				tsping:        false,
				nodeTarget:    false,
				handlerKnown:  false,
				handlerOnline: "",
				tunnelOK:      "",
			},
			want: layerService,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := decide(tt.in); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyErr(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	conn, dialErr := net.Dial("tcp", addr)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("expected refused dial")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	t.Cleanup(cancel)
	<-ctx.Done()
	tests := []struct {
		name string
		err  error
		want errClass
	}{
		{name: "refused", err: dialErr, want: errRefused},
		{name: "deadline", err: ctx.Err(), want: errTimeout},
		{name: "http status", err: &httpStatusError{code: 500}, want: errHTTP},
		{name: "http body", err: &httpBodyError{text: "DSM", excerpt: "nope"}, want: errHTTP},
		{
			name: "x509",
			err: x509.CertificateInvalidError{Cert: new(x509.Certificate), Reason: x509.Expired,
				Detail: "",
			},
			want: errTLS,
		},
		{name: "other", err: errors.New("no route"), want: errOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyErr(tt.err); got != tt.want {
				t.Fatalf("got %q want %q from %v", got, tt.want, tt.err)
			}
		})
	}
}

func TestPathFromPing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pr   *ipnstate.PingResult
		kind string
	}{
		{name: "direct", pr: &ipnstate.PingResult{Endpoint: "1.2.3.4:41641", LatencySeconds: 0.01,
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
		}, kind: pathDirect},
		{name: "derp", pr: &ipnstate.PingResult{DERPRegionCode: "nyc", LatencySeconds: 0.05,
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
		}, kind: pathDERP},
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
			kind: pathRelay,
		},
		{name: "err set", pr: &ipnstate.PingResult{Err: "timeout", Endpoint: "1.2.3.4:1",
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
			kind: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pathFromPing(tt.pr)
			if got.Kind != tt.kind {
				t.Fatalf("kind %q", got.Kind)
			}
			if tt.kind == "" && (got.Endpoint != "" || got.DERP != "") {
				t.Fatalf("path %+v", got)
			}
		})
	}
}

func TestProbeCacheTTLAndSingleflight(t *testing.T) {
	t.Parallel()
	clk := tstest.NewClock(tstest.ClockOpts{Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Step:             0,
		TimerChannelSize: 0,
		FollowRealTime:   false,
	})
	cache := newProbeCache[string](clk)
	var calls atomic.Int32
	fn := func() (bool, pathInfo) {
		calls.Add(1)
		return true, pathInfo{Kind: pathDirect,
			Endpoint: "",
			DERP:     "",
			Latency:  0,
		}
	}
	if ok, _ := cache.get("a", 30*time.Second, fn); !ok || calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	if _, _ = cache.get("a", 30*time.Second, fn); calls.Load() != 1 {
		t.Fatalf("cached calls %d", calls.Load())
	}
	clk.Advance(probeShareMax)
	if _, _ = cache.get("a", 30*time.Second, fn); calls.Load() != 2 {
		t.Fatalf("expired calls %d", calls.Load())
	}

	hold := make(chan struct{})
	var flight atomic.Int32
	blocking := func() (bool, pathInfo) {
		flight.Add(1)
		<-hold
		return true, pathInfo{Kind: pathDirect,
			Endpoint: "",
			DERP:     "",
			Latency:  0,
		}
	}
	var wg sync.WaitGroup
	const callers = 40
	for range callers {
		wg.Go(func() {
			cache.get("b", time.Second, blocking)
		})
	}
	waitCalls(t, &flight, 1)
	close(hold)
	wg.Wait()
	if flight.Load() != 1 {
		t.Fatalf("flight %d", flight.Load())
	}
}

func TestFreshICMPSkipsSupplementalProbe(t *testing.T) {
	t.Parallel()
	env := new(countingEnv)

	target := &Target{
		Dynamic:  false,
		Name:     "printer",
		Addr:     "127.0.0.1",
		Network:  NetworkLocal,
		Interval: 30 * time.Second,
		Timeout:  time.Second,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, _ := newTestClassifier(t, env, target)
	c.Observe(Result{Target: target, Kind: checkICMP, Latency: time.Millisecond,
		Check:      "",
		Start:      time.Time{},
		Generation: 0,
		Err:        nil,
		Ping:       nil,
	})
	c.Observe(Result{Target: target, Kind: "tcp", Check: "tcp:9", Err: context.DeadlineExceeded,
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Ping:       nil,
	})
	if env.packets.Load() != 0 {
		t.Fatalf("supplemental ICMP packets %d", env.packets.Load())
	}
	got := c.observeOne(Result{Target: target, Kind: "tcp", Check: "tcp:9", Err: context.DeadlineExceeded,
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Ping:       nil,
	})
	if got.Layer != layerService {
		t.Fatalf("layer %q", got.Layer)
	}
}

func TestObserveLayers(t *testing.T) {
	t.Parallel()
	router := peerTarget("nyc-subnet-router", "nyc-router")
	nas := &Target{
		Dynamic:  false,
		Name:     "nyc-nas",
		Addr:     "10.20.0.50",
		Network:  NetworkTailnet,
		Interval: 30 * time.Second,
		Timeout:  time.Second,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	local := &Target{
		Dynamic:  false,
		Name:     "printer",
		Addr:     "192.168.1.20",
		Network:  NetworkLocal,
		Interval: 30 * time.Second,
		Timeout:  time.Second,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, pres := newTestClassifier(t, new(fakeEnv), router, nas, local)
	seedRouter(pres)
	var pings atomic.Int32
	var icmps atomic.Int32
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		pings.Add(1)
		return &ipnstate.PingResult{Endpoint: "1.2.3.4:1", LatencySeconds: 0.01, NodeIP: "100.64.0.7",
			IP:             "",
			NodeName:       "",
			Err:            "",
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c.icmpFn = func(context.Context, netip.Addr, time.Duration) bool {
		icmps.Add(1)
		return false
	}

	c.learnRound(context.Background())
	pass := c.observeOne(Result{Target: nas, Kind: "tcp", Check: "tcp:445",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Err:        nil,
		Ping:       nil,
	})
	if pass.Layer != "" || pass.Parent != "nyc-subnet-router" {
		t.Fatalf("pass %+v", pass)
	}
	c.resetProbes()

	pres.apply(offlineRouter())
	off := c.observeOne(Result{Target: nas, Kind: checkICMP, Err: context.DeadlineExceeded,
		Check:      "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Ping:       nil,
	})
	if off.Layer != layerControl {
		t.Fatalf("control %q", off.Layer)
	}
	seedRouter(pres)
	c.resetProbes()
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		pings.Add(1)
		return nil, errors.New("no pong")
	}
	tun := c.observeOne(Result{Target: nas, Kind: checkICMP, Err: context.DeadlineExceeded,
		Check:      "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Ping:       nil,
	})
	if tun.Layer != layerTunnel {
		t.Fatalf("tunnel %q", tun.Layer)
	}
	c.resetProbes()
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{Endpoint: "1.2.3.4:1", NodeIP: "100.64.0.7",
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	beforeICMP := icmps.Load()
	refused := c.observeOne(Result{Target: nas, Kind: "tcp", Err: dialRefused(),
		Check:      "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Ping:       nil,
	})
	if refused.Layer != layerService || icmps.Load() != beforeICMP {
		t.Fatalf("refused layer %q icmps %d", refused.Layer, icmps.Load())
	}
	lan := c.observeOne(Result{Target: nas, Kind: checkICMP, Err: context.DeadlineExceeded,
		Check:      "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Ping:       nil,
	})
	if lan.Layer != layerLAN {
		t.Fatalf("lan %q", lan.Layer)
	}
	lonely := &Target{
		Dynamic:  false,
		Name:     "lab-pdu",
		Addr:     "10.99.0.5",
		Network:  NetworkTailnet,
		Interval: time.Minute,
		Timeout:  time.Second,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c.configured = append(c.configured, lonely)
	c.byName[lonely.Name] = lonely
	unrouted := c.observeOne(Result{Target: lonely, Kind: checkICMP, Err: context.DeadlineExceeded,
		Check:      "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Ping:       nil,
	})
	if unrouted.Layer != layerUnrouted {
		t.Fatalf("unrouted %q", unrouted.Layer)
	}
	localFail := c.observeOne(Result{Target: local, Kind: checkICMP, Err: context.DeadlineExceeded,
		Check:      "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Ping:       nil,
	})
	if localFail.Layer != layerLAN {
		t.Fatalf("local %q", localFail.Layer)
	}
	if tags := c.presence.TagsFor("printer"); tags != nil {
		t.Fatalf("local tags %v", tags)
	}
}

func TestLearningFallbackHAAndLocal(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	router := peerTarget("nyc-subnet-router", "nyc-router")
	backup := peerTarget("nyc-subnet-router-b", "nyc-router-b")
	nas := &Target{
		Dynamic:  false,
		Name:     "nyc-nas",
		Addr:     "10.20.0.50",
		Site:     "nyc",
		Network:  NetworkTailnet,
		Interval: time.Minute,
		Timeout:  time.Second,

		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	local := &Target{
		Dynamic:  false,
		Name:     "printer",
		Addr:     "192.168.1.9",
		Network:  NetworkLocal,
		Interval: time.Minute,
		Timeout:  time.Second,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, pres := newTestClassifier(t, new(fakeEnv), router, backup, nas, local)
	c.log = log
	seedPair(pres)
	var localPings atomic.Int32
	c.pingFn = func(_ context.Context, ip netip.Addr, _ time.Duration) (*ipnstate.PingResult, error) {
		if ip.String() == local.Addr {
			localPings.Add(1)
		}
		return nil, errors.New("timeout")
	}
	c.learnRound(context.Background())
	if c.Parent("nyc-nas") != "nyc-subnet-router" {
		t.Fatalf("fallback parent %q", c.Parent("nyc-nas"))
	}
	if localPings.Load() != 0 {
		t.Fatal("learned a local target")
	}
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{NodeIP: "100.64.0.11", Endpoint: "9.9.9.9:1",
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c.learnRound(context.Background())
	if c.Parent("nyc-nas") != "nyc-subnet-router-b" {
		t.Fatalf("ha parent %q", c.Parent("nyc-nas"))
	}
	if !strings.Contains(logs.String(), "parent changed") {
		t.Fatalf("logs %s", logs.String())
	}
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{Err: "timeout",
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
		}, nil
	}
	c.learnRound(context.Background())
	if c.Parent("nyc-nas") != "nyc-subnet-router-b" {
		t.Fatalf("kept parent %q", c.Parent("nyc-nas"))
	}
}

func TestMissingNodeIPUsesRoute(t *testing.T) {
	t.Parallel()
	router := peerTarget("nyc-subnet-router", "nyc-router")
	nas := &Target{
		Dynamic: false,
		Name:    "nyc-nas", Addr: "10.20.0.50", Network: NetworkTailnet,
		Timeout: time.Second, Interval: time.Minute,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, pres := newTestClassifier(t, new(fakeEnv), router, nas)
	seedRouter(pres)
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{Endpoint: "1.2.3.4:1",
			IP:             "",
			NodeIP:         "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c.learnRound(context.Background())
	if c.Parent("nyc-nas") != "nyc-subnet-router" {
		t.Fatalf("parent %q", c.Parent("nyc-nas"))
	}
}

func TestLearningConcurrencyCap(t *testing.T) {
	t.Parallel()
	targets := make([]*Target, learnParallel+4)
	for i := range targets {
		targets[i] = &Target{
			Dynamic: false,
			Name:    fmt.Sprintf("lan-%02d", i), Addr: fmt.Sprintf("10.30.0.%d", i+1),
			Network: NetworkTailnet, Timeout: time.Second, Interval: time.Minute,

			Site:             "",
			Severity:         "",
			Parent:           "",
			InsecureTLS:      false,
			FailThreshold:    0,
			RecoverThreshold: 0,
			FlapLimit:        0,
			FlapWindow:       0,
			Checks:           nil,
			Implicit:         false,
			StableID:         "",
			NodeID:           0,
		}
	}
	c, _ := newTestClassifier(t, new(fakeEnv), targets...)
	var inflight atomic.Int32
	var peak atomic.Int32
	hold := make(chan struct{})
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		n := inflight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-hold
		inflight.Add(-1)
		return nil, errors.New("timeout")
	}
	done := make(chan struct{})
	go func() {
		c.pingAll(context.Background(), targets)
		close(done)
	}()
	waitCalls(t, &inflight, int32(learnParallel))
	if peak.Load() > int32(learnParallel) {
		t.Fatalf("peak %d", peak.Load())
	}
	close(hold)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("learning did not finish")
	}
}

func TestImplicitTargetsAndGrantLog(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	mixed := &Target{
		Dynamic: false,
		Name:    "nyc-router", Addr: "nyc-router", Network: NetworkTailnet,
		Checks: []check{tspingCheck{
			resolver: nil,
		}, tcpCheck{port: 443}},

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
	}
	nas := &Target{
		Dynamic:  false,
		Name:     "nyc-nas",
		Addr:     "10.20.0.50",
		Site:     "nyc",
		Network:  NetworkTailnet,
		Timeout:  time.Second,
		Interval: time.Minute,

		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	other := &Target{
		Dynamic:  false,
		Name:     "other-nas",
		Addr:     "10.20.0.51",
		Site:     "sfo",
		Network:  NetworkTailnet,
		Timeout:  time.Second,
		Interval: time.Minute,

		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, pres := newTestClassifier(t, new(fakeEnv), mixed, nas, other)
	c.log = log
	seedRouter(pres)
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{NodeIP: "100.64.0.7",
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			Endpoint:       "",
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c.learnRound(context.Background())
	parent := c.Parent("nyc-nas")
	if parent == "nyc-router" || !strings.HasPrefix(parent, "nyc-router-") {
		t.Fatalf("parent %q", parent)
	}
	imp := c.byName[parent]
	if imp == nil || !imp.Implicit || len(imp.Checks) != 1 || imp.Checks[0].Kind() != checkTSPing {
		t.Fatalf("implicit %+v", imp)
	}
	if imp.Site != "" {
		t.Fatalf("mixed site %q", imp.Site)
	}
	if !strings.Contains(logs.String(), "implicit target created") {
		t.Fatalf("logs %s", logs.String())
	}

	reg := c.registry.(*memRegistry)
	reg.setDown(parent, true)
	c.mu.Lock()
	delete(c.byName, nas.Name)
	delete(c.byName, other.Name)
	c.configured = []*Target{mixed}
	c.mu.Unlock()
	c.finishRound(context.Background(), nil, true)
	if _, ok := reg.targets[parent]; !ok {
		t.Fatal("removed a down implicit target")
	}
	if strings.Count(logs.String(), "deferring removal") != 1 {
		t.Fatalf("defer logs %s", logs.String())
	}
	c.finishRound(context.Background(), nil, true)
	if strings.Count(logs.String(), "deferring removal") != 1 {
		t.Fatal("logged deferral twice")
	}
	reg.setDown(parent, false)
	c.finishRound(context.Background(), nil, true)
	if _, ok := reg.targets[parent]; ok {
		t.Fatal("implicit target remained after recovery")
	}
}

func TestGrantWarningRounds(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	pdu := &Target{
		Dynamic:  false,
		Name:     "lab-pdu",
		Addr:     "10.99.0.5",
		Network:  NetworkTailnet,
		Timeout:  time.Second,
		Interval: time.Minute,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, pres := newTestClassifier(t, new(fakeEnv), pdu)
	c.log = log
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return nil, errors.New("timeout")
	}
	c.learnRound(context.Background())
	c.learnRound(context.Background())
	if strings.Count(logs.String(), "no peer handles 10.99.0.5") != 1 {
		t.Fatalf("logs %s", logs.String())
	}
	seedRouter(pres)
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{NodeIP: "100.64.0.7",
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			Endpoint:       "",
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c.learnRound(context.Background())
	if strings.Count(logs.String(), "no peer handles 10.99.0.5") != 1 {
		t.Fatalf("logged after resolution %s", logs.String())
	}
	pres.apply(ipn.Notify{PeersRemoved: []tailcfg.NodeID{1}})
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return nil, errors.New("timeout")
	}
	c.mu.Lock()
	delete(c.learned, pdu.Name)
	c.mu.Unlock()
	c.learnRound(context.Background())
	if strings.Count(logs.String(), "no peer handles 10.99.0.5") != 2 {
		t.Fatalf("logs %s", logs.String())
	}
}

func TestLearnerSchedule(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := tstest.NewClock(tstest.ClockOpts{Start: start,
		Step:             0,
		TimerChannelSize: 0,
		FollowRealTime:   false,
	})
	nas := &Target{
		Dynamic:  false,
		Name:     "nyc-nas",
		Addr:     "10.20.0.50",
		Network:  NetworkTailnet,
		Timeout:  time.Second,
		Interval: time.Minute,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, _ := newTestClassifier(t, new(fakeEnv), nas)
	c.clock = clk
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return nil, errors.New("timeout")
	}
	l := newLearner(c)
	armed := make(chan struct{}, 1)
	var rounds []time.Time
	var mu sync.Mutex
	l.onArmed = func() { armed <- struct{}{} }
	l.onRound = func(at time.Time) {
		mu.Lock()
		rounds = append(rounds, at)
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go l.run(ctx)
	<-armed
	clk.Advance(learnInitialWait)
	waitRounds(t, &mu, &rounds, 1)
	select {
	case <-l.FirstPassDone():
	case <-time.After(time.Second):
		t.Fatal("first pass did not finish")
	}
	<-armed
	clk.Advance(learnInterval)
	waitRounds(t, &mu, &rounds, 2)
	mu.Lock()
	defer mu.Unlock()
	if !rounds[0].Equal(start.Add(learnInitialWait)) || !rounds[1].Equal(start.Add(learnInitialWait+learnInterval)) {
		t.Fatalf("rounds %v", rounds)
	}
}

func TestReconciliationRetainsThenRemoves(t *testing.T) {
	t.Parallel()
	nas := &Target{
		Dynamic:  false,
		Name:     "nyc-nas",
		Addr:     "10.20.0.50",
		Network:  NetworkTailnet,
		Timeout:  time.Second,
		Interval: time.Minute,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, pres := newTestClassifier(t, new(fakeEnv), nas)
	restored := &Target{
		Dynamic: false,
		Name:    "old-router", Addr: "100.64.0.9", Network: NetworkTailnet, Implicit: true,
		NodeID: 9, StableID: "nOLD", Checks: []check{tspingCheck{
			resolver: nil,
		}},

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
	}
	c.RestoreImplicit(restored)
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return nil, errors.New("timeout")
	}
	l := newLearner(c)
	l.pass(context.Background())
	l.finish()
	select {
	case <-l.FirstPassDone():
	default:
		t.Fatal("first pass open")
	}
	if !c.registry.(*memRegistry).has("old-router") {
		t.Fatal("restored target removed after a failed pass")
	}
	if c.LastReconciliation().Complete {
		t.Fatal("failed pass was authoritative")
	}
	seedRouter(pres)
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{NodeIP: "100.64.0.7",
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			Endpoint:       "",
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c.learnRound(context.Background())
	if !c.LastReconciliation().Complete {
		t.Fatal("resolved pass was not authoritative")
	}
	if c.registry.(*memRegistry).has("old-router") {
		t.Fatal("restored target survived authoritative removal")
	}
}

func TestNoRemoteLANCompletesImmediately(t *testing.T) {
	t.Parallel()
	node := &Target{
		Dynamic: false,
		Name:    "build-1",
		Addr:    "100.64.0.4",
		Network: NetworkTailnet,
		Checks: []check{tspingCheck{
			resolver: nil,
		}},
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
	}
	c, _ := newTestClassifier(t, new(fakeEnv), node)
	restored := &Target{
		Dynamic:  false,
		Name:     "ghost",
		Addr:     "100.64.0.9",
		Implicit: true,
		NodeID:   9,
		StableID: "nGHOST",
		Checks: []check{tspingCheck{
			resolver: nil,
		}},

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
	}
	c.RestoreImplicit(restored)
	l := newLearner(c)
	select {
	case <-l.FirstPassDone():
	default:
		t.Fatal("first pass was not immediate")
	}
	if c.registry.(*memRegistry).has("ghost") {
		t.Fatal("restored router kept when no LAN target needs one")
	}
	if !c.LastReconciliation().Complete {
		t.Fatal("config-only reconciliation was not complete")
	}
}

func TestGraphCycleAndHARelease(t *testing.T) {
	t.Parallel()
	router := &Target{
		Dynamic: false,
		Name:    "r",
		Addr:    "nyc-router",
		Parent:  "switch",
		Network: NetworkTailnet,
		Checks: []check{tspingCheck{
			resolver: nil,
		}},

		Site:             "",
		Severity:         "",
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
	}
	sw := &Target{
		Dynamic:          false,
		Name:             "switch",
		Addr:             "10.20.0.1",
		Network:          NetworkTailnet,
		Checks:           []check{icmpCheck{}},
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
	}
	c, pres := newTestClassifier(t, new(fakeEnv), router, sw)
	seedRouter(pres)
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{NodeIP: "100.64.0.7",
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			Endpoint:       "",
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c.learnRound(context.Background())
	if c.Parent("switch") == "r" {
		t.Fatal("accepted a cyclic parent")
	}
	reg := c.registry.(*memRegistry)
	if len(reg.graphs) != 0 {
		t.Fatalf("published %v", reg.graphs)
	}
	peer, ok := c.handlerPeer(sw)
	if !ok || peer.Name != "nyc-router" {
		t.Fatalf("handler %+v ok=%v", peer, ok)
	}

	a := peerTarget("router-a", "nyc-router")
	b := peerTarget("router-b", "nyc-router-b")
	child := &Target{
		Dynamic:  false,
		Name:     "cam",
		Addr:     "10.20.0.50",
		Network:  NetworkTailnet,
		Timeout:  time.Second,
		Interval: time.Minute,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c2, pres2 := newTestClassifier(t, new(fakeEnv), a, b, child)
	seedPair(pres2)
	which := "100.64.0.7" // router A
	c2.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{NodeIP: which,
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			Endpoint:       "",
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c2.learnRound(context.Background())
	which = "100.64.0.11"
	c2.learnRound(context.Background())
	reg2 := c2.registry.(*memRegistry)
	reg2.mu.Lock()
	defer reg2.mu.Unlock()
	if len(reg2.graphs) != 2 || reg2.graphs[1]["cam"] != "router-b" {
		t.Fatalf("graphs %v", reg2.graphs)
	}
}

func TestSwitchParentDoesNotReplaceHandler(t *testing.T) {
	t.Parallel()
	sw := &Target{
		Dynamic:          false,
		Name:             "nyc-core-switch",
		Addr:             "10.20.0.1",
		Network:          NetworkTailnet,
		Checks:           []check{icmpCheck{}},
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
	}
	cam := &Target{
		Dynamic: false,
		Name:    "nyc-cam-3",
		Addr:    "10.20.0.50",
		Parent:  "nyc-core-switch",
		Network: NetworkTailnet,
		Timeout: time.Second,

		Site:             "",
		Severity:         "",
		InsecureTLS:      false,
		Interval:         0,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	router := peerTarget("nyc-subnet-router", "nyc-router")
	c, pres := newTestClassifier(t, new(fakeEnv), router, sw, cam)
	seedRouter(pres)
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{NodeIP: "100.64.0.7",
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			Endpoint:       "",
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	c.learnRound(context.Background())
	if c.Parent("nyc-cam-3") != "nyc-core-switch" {
		t.Fatalf("parent %q", c.Parent("nyc-cam-3"))
	}
	peer, ok := c.handlerPeer(cam)
	if !ok || peer.NodeID != 1 {
		t.Fatalf("handler %+v", peer)
	}
}

func TestLayerAndParentMetrics(t *testing.T) {
	t.Parallel()
	nas := &Target{
		Dynamic:  false,
		Name:     "nyc-nas",
		Addr:     "10.20.0.50",
		Network:  NetworkTailnet,
		Interval: 30 * time.Second,
		Timeout:  time.Second,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
	c, pres := newTestClassifier(t, new(fakeEnv), nas)
	seedRouter(pres)
	c.pingFn = func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error) {
		return &ipnstate.PingResult{Endpoint: "1.2.3.4:1", NodeIP: "100.64.0.7",
			IP:             "",
			NodeName:       "",
			Err:            "",
			LatencySeconds: 0,
			PeerRelay:      "",
			DERPRegionID:   0,
			DERPRegionCode: "",
			PeerAPIPort:    0,
			PeerAPIURL:     "",
			IsLocalIP:      false,
		}, nil
	}
	for range 3 {
		c.Observe(Result{Target: nas, Kind: "tcp", Err: &httpStatusError{code: 500},
			Check:      "",
			Start:      time.Time{},
			Generation: 0,
			Latency:    0,
			Ping:       nil,
		})
	}
	if got := testutil.ToFloat64(c.layers.WithLabelValues("nyc-nas", "service")); got != 3 {
		t.Fatalf("layer counter %v", got)
	}
	c.Observe(Result{
		Target: nas, Kind: checkTSPing,
		Ping: &ipnstate.PingResult{DERPRegionCode: "nyc", LatencySeconds: 0.05,
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

		Check:      "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Err:        nil,
	})
	for _, kind := range []string{pathDirect, pathDERP, pathRelay} {
		want := 0.0
		if kind == pathDERP {
			want = 1
		}
		if got := testutil.ToFloat64(c.paths.WithLabelValues("nyc-nas", kind)); got != want {
			t.Fatalf("path %s=%v", kind, got)
		}
	}
	if err := c.publishParents(context.Background(), map[string]string{"nyc-nas": "nyc-router"}); err != nil {
		t.Fatal(err)
	}
	if err := c.publishParents(context.Background(), map[string]string{"nyc-nas": "nyc-router-b"}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(c.parents.WithLabelValues("nyc-nas", "nyc-router-b")); got != 1 {
		t.Fatalf("new parent %v", got)
	}
	out := metricText(t, c.parents)
	if strings.Contains(out, `value:"nyc-router"`) || !strings.Contains(out, `value:"nyc-router-b"`) {
		t.Fatalf("parent series %s", out)
	}
}

func TestRegisterBeforeSchedule(t *testing.T) {
	t.Parallel()
	var order []string
	alert := &memRegistry{order: &order, mark: "alert",
		mu:      sync.Mutex{},
		targets: nil,
		down:    nil,
		graphs:  nil,
	}
	start := &orderStarter{order: &order}
	reg := &runtimeRegistry{alert: alert, start: start, probes: map[string][]func() error{},
		app:    nil,
		mu:     sync.Mutex{},
		byName: nil,
		known:  nil,
		pool:   nil,
	}
	t.Helper()
	if err := reg.AddTarget(&Target{Dynamic: false, Name: "nyc-router", Checks: []check{tspingCheck{
		resolver: nil,
	}},
		Addr:             "",
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
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "alert nyc-router,schedule nyc-router" {
		t.Fatalf("order %v", order)
	}
}

type countingEnv struct {
	fakeEnv

	packets atomic.Int32
}

func (e *countingEnv) ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) {
	e.packets.Add(1)
	return e.fakeEnv.ListenPacket(ctx, network, addr)
}

type memRegistry struct {
	mu      sync.Mutex
	targets map[string]*Target
	down    map[string]bool
	graphs  []map[string]string
	order   *[]string
	mark    string
}

func (m *memRegistry) AddTarget(t *Target) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.targets == nil {
		m.targets = map[string]*Target{}
	}
	m.targets[t.Name] = t
	if m.order != nil {
		*m.order = append(*m.order, m.mark+" "+t.Name)
	}
	return nil
}

func (m *memRegistry) RemoveTarget(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.targets, name)
	return nil
}

func (m *memRegistry) IsDown(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.down[name]
}

func (m *memRegistry) UpdateParents(parents map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.graphs = append(m.graphs, parents)
	return nil
}

func (m *memRegistry) setDown(name string, down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down == nil {
		m.down = map[string]bool{}
	}
	m.down[name] = down
}

func (m *memRegistry) has(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.targets[name]
	return ok
}

type orderStarter struct {
	order *[]string
}

func (s orderStarter) Start(t *Target, _ check, _ func(context.Context) error) (func() error, error) {
	*s.order = append(*s.order, "schedule "+t.Name)
	return func() error { return nil }, nil
}

func newTestClassifier(t *testing.T, env netEnv, targets ...*Target) (*classifier, *presence) {
	t.Helper()
	pres := newPresence(nil, tstime.StdClock{}, nil)
	reg := prometheus.NewRegistry()
	mem := new(memRegistry)

	c := newClassifier(pres, env, nil, tstime.StdClock{}, reg, targets, mem)
	pres.setHandler(c.nodeForTarget)
	return c, pres
}

func (c *classifier) observeOne(res Result) Classified {
	var got Classified
	c.addConsumer(captureConsumer{fn: func(cl Classified) { got = cl }})
	c.Observe(res)
	return got
}

type captureConsumer struct {
	fn func(Classified)
}

func (c captureConsumer) ObserveClassified(cl Classified) { c.fn(cl) }

func peerTarget(name, addr string) *Target {
	return &Target{
		Dynamic: false,
		Name:    name,
		Addr:    addr,
		Network: NetworkTailnet,
		Checks: []check{tspingCheck{
			resolver: nil,
		}},
		Interval: time.Minute,
		Timeout:  time.Second,

		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}
}

func seedRouter(p *presence) {
	p.apply(ipn.Notify{InitialStatus: presenceStatus()})
}

func offlineRouter() ipn.Notify {
	off := false
	return ipn.Notify{PeerChangedPatch: []*tailcfg.PeerChange{{NodeID: 1, Online: &off,
		DERPRegion:   0,
		Cap:          0,
		CapMap:       nil,
		Endpoints:    nil,
		Key:          nil,
		KeySignature: nil,
		DiscoKey:     nil,
		LastSeen:     nil,
		KeyExpiry:    nil,
	}}}
}

func seedPair(p *presence) {
	p.apply(ipn.Notify{InitialStatus: presenceStatus()})
	on := true
	p.apply(ipn.Notify{PeersChanged: []*tailcfg.Node{{
		ID: 4, StableID: "nB", Name: "nyc-router-b.tailnet.ts.net.", ComputedName: "nyc-router-b",
		Addresses: []netip.Prefix{netip.MustParsePrefix("100.64.0.11/32")}, Online: &on,
	}}})
}

func dialRefused() error {
	return &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED,
		Net:    "",
		Source: nil,
		Addr:   nil,
	}
}

func (c *classifier) resetProbes() {
	c.disco = newProbeCache[tailcfg.NodeID](c.clock)
	c.icmp = newProbeCache[netip.Addr](c.clock)
}

func waitCalls(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if n.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("calls %d", n.Load())
}

func waitRounds(t *testing.T, mu *sync.Mutex, rounds *[]time.Time, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*rounds)
		mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for a learning round")
}

func metricText(t *testing.T, col prometheus.Collector) string {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(col); err != nil {
		t.Fatal(err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, fam := range families {
		for _, m := range fam.GetMetric() {
			buf.WriteString(m.String())
			buf.WriteByte('\n')
		}
	}
	return buf.String()
}

type classifiedSink struct {
	mu  sync.Mutex
	got []Classified
}

func (c *classifiedSink) ObserveClassified(cl Classified) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, cl)
}

func TestPresenceChangeAloneProducesNoFailure(t *testing.T) {
	t.Parallel()
	router := peerTarget("nyc-subnet-router", "nyc-router")
	c, pres := newTestClassifier(t, new(fakeEnv), router)
	seedRouter(pres)
	var cons classifiedSink
	c.addConsumer(&cons)

	pres.apply(offlineRouter())
	c.Observe(Result{Target: router, Kind: checkTSPing, Check: "tsping",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Err:        nil,
		Ping:       nil,
	})

	if len(cons.got) != 1 || cons.got[0].Layer != "" {
		t.Fatalf("classified %+v", cons.got)
	}
	if out := metricText(t, c.layers); out != "" {
		t.Fatalf("failure layer series recorded: %s", out)
	}
}

func TestNodeTargetSkipsICMPProbe(t *testing.T) {
	t.Parallel()
	in := classifyInput{
		handlerKnown: true,
		nodeTarget:   true,
		tunnelOK:     opt.NewBool(true),
		errClass:     errTimeout,

		local:         false,
		tsping:        false,
		handlerOnline: "",
		deviceICMPOK:  "",
	}
	if needsICMP(in) {
		t.Fatal("node target should not send an ICMP probe")
	}
	in.nodeTarget = false
	if !needsICMP(in) {
		t.Fatal("LAN target with a working tunnel should probe ICMP")
	}
}
