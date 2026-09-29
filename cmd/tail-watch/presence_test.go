package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tstime"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestPresenceLookups(t *testing.T) {
	t.Parallel()
	p := newPresence(nil, nil, nil)
	p.apply(ipn.Notify{InitialStatus: presenceStatus()})

	owner, found := p.ByIP(netip.MustParseAddr("100.64.0.7"))
	if !found || owner.Name != "nyc-router" {
		t.Fatalf("owner %+v found=%v", owner, found)
	}
	if _, found = p.ByIP(netip.MustParseAddr("192.168.99.1")); found {
		t.Fatal("unowned address matched a tailscale IP")
	}

	wide, found := p.Route(netip.MustParseAddr("10.20.0.50"))
	if !found || wide.Name != "nyc-router" {
		t.Fatalf("wide route %+v", wide)
	}
	narrow, found := p.Route(netip.MustParseAddr("10.20.5.9"))
	if !found || narrow.Name != "nyc-lab" {
		t.Fatalf("longest prefix %+v", narrow)
	}
	if _, found = p.Route(netip.MustParseAddr("192.168.99.1")); found {
		t.Fatal("unhandled address matched a route")
	}

	tests := []struct {
		name string
		want string
		miss bool
	}{
		{name: "nyc-router.tailnet.ts.net", want: "nyc-router",
			miss: false,
		},
		{name: "nyc-router", want: "nyc-router",
			miss: false,
		},
		{name: "nyc-router-host", want: "nyc-router",
			miss: false,
		},
		{name: "missing", miss: true,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, matched := p.ByName(tt.name)
			if tt.miss {
				if matched {
					t.Fatalf("found %s", got.Name)
				}
				return
			}
			if !matched || got.Name != tt.want {
				t.Fatalf("got %+v matched=%v", got, matched)
			}
		})
	}
}

func TestPresenceApply(t *testing.T) {
	t.Parallel()
	p := newPresence(nil, nil, nil)
	nycID := tailcfg.NodeID(1)
	sfoID := tailcfg.NodeID(2)

	p.apply(ipn.Notify{InitialStatus: presenceStatus()})
	if online, known := p.Online(nycID); !online || !known {
		t.Fatalf("nyc online=%v known=%v", online, known)
	}
	if online, known := p.Online(sfoID); online || !known {
		t.Fatalf("sfo online=%v known=%v", online, known)
	}

	off := false
	p.apply(ipn.Notify{PeerChangedPatch: []*tailcfg.PeerChange{{
		NodeID: nycID,
		Online: &off,

		DERPRegion:   0,
		Cap:          0,
		CapMap:       nil,
		Endpoints:    nil,
		Key:          nil,
		KeySignature: nil,
		DiscoKey:     nil,
		LastSeen:     nil,
		KeyExpiry:    nil,
	}}})
	peer, ok := p.peer(nycID)
	if !ok || peer.DNSName != "nyc-router.tailnet.ts.net" {
		t.Fatalf("patch changed identity %+v", peer)
	}
	if online, known := p.Online(nycID); online || !known {
		t.Fatalf("after patch online=%v known=%v", online, known)
	}

	p.apply(ipn.Notify{PeersChanged: []*tailcfg.Node{nycNode(false)}})
	peer, ok = p.peer(nycID)
	if !ok || len(peer.Routes) != 1 || peer.Routes[0].String() != "10.20.8.0/24" {
		t.Fatalf("replaced routes %+v", peer.Routes)
	}
	if online, known := p.Online(nycID); online || !known {
		t.Fatalf("replaced online=%v known=%v", online, known)
	}

	p.apply(ipn.Notify{PeersChanged: []*tailcfg.Node{nycNodeUnsetOnline()}})
	if _, known := p.Online(nycID); known {
		t.Fatal("nil online read as known")
	}

	p.apply(ipn.Notify{PeersRemoved: []tailcfg.NodeID{sfoID}})
	if _, still := p.peer(sfoID); still {
		t.Fatal("removed peer still present")
	}

	stopped := ipn.Stopped
	p.apply(ipn.Notify{State: &stopped})
	if _, known := p.Online(nycID); known {
		t.Fatal("known after tailscaled left Running")
	}
}

func TestPeerChangeFields(t *testing.T) {
	t.Parallel()
	var got []string
	for field := range reflect.TypeFor[tailcfg.PeerChange]().Fields() {
		if !field.IsExported() {
			continue
		}
		got = append(got, field.Name)
	}
	want := []string{
		"NodeID",
		"DERPRegion",
		"Cap",
		"CapMap",
		"Endpoints",
		"Key",
		"KeySignature",
		"DiscoKey",
		"Online",
		"LastSeen",
		"KeyExpiry",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("PeerChange fields %v", got)
	}
}

func TestPresenceRunReconnect(t *testing.T) {
	t.Parallel()
	var logs lockedBuffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug,
		AddSource:   false,
		ReplaceAttr: nil,
	}))
	bus := &ctlBus{watches: make(chan ipnWatch)}
	p := newPresence(log, tstime.StdClock{}, bus.Watch)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.run(ctx)
		close(done)
	}()

	first := &ctlWatch{ch: make(chan watchResult)}
	bus.watches <- first
	first.ch <- watchResult{n: ipn.Notify{InitialStatus: presenceStatus()},
		err: nil,
	}
	waitPresence(t, func() bool { return p.seededNow() })
	first.ch <- watchResult{err: errors.New("disconnected"),
		n: ipn.Notify{},
	}
	waitPresence(t, func() bool { return !p.seededNow() })
	if _, known := p.Online(tailcfg.NodeID(1)); known {
		t.Fatal("peer stayed known while the bus was down")
	}
	waitPresence(t, func() bool { return strings.Count(logs.String(), "presence bus disconnected") >= 1 })
	if strings.Count(logs.String(), "presence bus disconnected") != 1 {
		t.Fatalf("logs %s", logs.String())
	}

	second := &ctlWatch{ch: make(chan watchResult, 1)}
	bus.watches <- second
	second.ch <- watchResult{n: ipn.Notify{InitialStatus: presenceStatus()},
		err: nil,
	}
	waitPresence(t, func() bool { return p.seededNow() })
	if strings.Count(logs.String(), "presence bus disconnected") != 1 {
		t.Fatalf("logged more than the one disconnect: %s", logs.String())
	}
	cancel()
	second.ch <- watchResult{err: context.Canceled,
		n: ipn.Notify{},
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return")
	}
}

func TestPresenceTagsFor(t *testing.T) {
	t.Parallel()
	p := newPresence(nil, nil, nil)
	p.apply(ipn.Notify{InitialStatus: presenceStatus()})
	p.setHandler(func(target string) (tailcfg.NodeID, bool) {
		switch target {
		case "build-1", "nyc-router":
			return 1, true
		case "nyc-nas":
			return 1, true
		default:
			return 0, false
		}
	})
	if got := strings.Join(p.TagsFor("build-1"), ","); got != "tag:router" {
		t.Fatalf("node tags %q", got)
	}
	if got := strings.Join(p.TagsFor("nyc-nas"), ","); got != "tag:router" {
		t.Fatalf("lan tags %q", got)
	}
	if got := p.TagsFor("nyc-router"); len(got) != 1 || got[0] != "tag:router" {
		t.Fatalf("implicit tags %v", got)
	}
	if tags := p.TagsFor("lab-pdu"); tags != nil {
		t.Fatalf("unknown handler tags %v", tags)
	}
}

func TestResolveUsesSeededPresence(t *testing.T) {
	t.Parallel()
	p := newPresence(nil, nil, nil)
	p.apply(ipn.Notify{InitialStatus: presenceStatus()})
	var fetches int
	r := newTSResolver(tstime.StdClock{}, nil, func(context.Context) (*ipnstate.Status, error) {
		fetches++
		return nil, errors.New("status should not be called")
	})
	r.presence = p
	ip, err := r.resolve(context.Background(), "nyc-router")
	if err != nil {
		t.Fatal(err)
	}
	if ip != netip.MustParseAddr("100.64.0.7") {
		t.Fatalf("ip %s", ip)
	}
	if fetches != 0 {
		t.Fatalf("status fetches %d", fetches)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type watchResult struct {
	n   ipn.Notify
	err error
}

type ctlWatch struct {
	ch chan watchResult
}

func (w *ctlWatch) Next() (ipn.Notify, error) {
	r := <-w.ch
	return r.n, r.err
}

func (w *ctlWatch) Close() error { return nil }

type ctlBus struct {
	watches chan ipnWatch
}

func (b *ctlBus) Watch(ctx context.Context, _ ipn.NotifyWatchOpt) (ipnWatch, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case w := <-b.watches:
		return w, nil
	}
}

func waitPresence(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for presence")
}

func presenceStatus() *ipnstate.Status {
	wide := views.SliceOf([]netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")})
	narrow := views.SliceOf([]netip.Prefix{netip.MustParsePrefix("10.20.5.0/24")})
	tags := views.SliceOf([]string{"tag:router"})
	return &ipnstate.Status{
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): {
				ID:            "nNYC",
				NodeID:        1,
				HostName:      "nyc-router-host",
				DNSName:       "nyc-router.tailnet.ts.net.",
				TailscaleIPs:  []netip.Addr{netip.MustParseAddr("100.64.0.7")},
				PrimaryRoutes: &wide,
				Tags:          &tags,
				Online:        true,
			},
			key.NewNode().Public(): {
				ID:            "nLAB",
				NodeID:        3,
				HostName:      "nyc-lab-host",
				DNSName:       "nyc-lab.tailnet.ts.net.",
				TailscaleIPs:  []netip.Addr{netip.MustParseAddr("100.64.0.9")},
				PrimaryRoutes: &narrow,
				Online:        true,
			},
			key.NewNode().Public(): {
				ID:           "nSFO",
				NodeID:       2,
				HostName:     "sfo-router",
				DNSName:      "sfo-router.tailnet.ts.net.",
				TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.8")},
				Online:       false,
			},
		},
	}
}

func nycNode(online bool) *tailcfg.Node {
	return &tailcfg.Node{
		ID:            1,
		StableID:      "nNYC",
		Name:          "nyc-router.tailnet.ts.net.",
		ComputedName:  "nyc-router",
		Addresses:     []netip.Prefix{netip.MustParsePrefix("100.64.0.7/32")},
		PrimaryRoutes: []netip.Prefix{netip.MustParsePrefix("10.20.8.0/24")},
		Tags:          []string{"tag:router"},
		Online:        &online,
	}
}

func nycNodeUnsetOnline() *tailcfg.Node {
	n := nycNode(true)
	n.Online = nil
	return n
}

func TestTagsForIgnoresLocalTargets(t *testing.T) {
	t.Parallel()
	remote := alertTarget("nyc-nas", "icmp")
	remote.Addr = "10.20.0.50"
	local := alertTarget("lab-nas", "icmp")
	local.Addr = "10.20.0.50"
	local.Network = NetworkLocal
	_, pres := newTestClassifier(t, new(fakeEnv), remote, local)
	pres.apply(ipn.Notify{InitialStatus: presenceStatus()})
	if got := pres.TagsFor("nyc-nas"); len(got) != 1 || got[0] != "tag:router" {
		t.Fatalf("tailnet target tags %v", got)
	}
	if got := pres.TagsFor("lab-nas"); got != nil {
		t.Fatalf("a local target must not inherit tailnet tags, got %v", got)
	}
}
