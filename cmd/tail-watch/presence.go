package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tstime"
	"tailscale.com/types/logger"
	"tailscale.com/types/opt"
	"tailscale.com/types/views"
	"tailscale.com/util/backoff"
)

const presenceBackoffMax = 30 * time.Second

const (
	namePrioDNS = iota
	namePrioShort
	namePrioHost
	namePrioNone
)

// peerInfo is one tailnet peer keyed by node ID.
type peerInfo struct {
	NodeID     tailcfg.NodeID
	StableID   tailcfg.StableNodeID
	Name       string
	DNSName    string
	HostName   string
	IPs        []netip.Addr
	Routes     []netip.Prefix
	AllowedIPs []netip.Prefix
	Tags       []string
	Online     opt.Bool
}

func (p peerInfo) clone() peerInfo {
	p.IPs = slices.Clone(p.IPs)
	p.Routes = slices.Clone(p.Routes)
	p.AllowedIPs = slices.Clone(p.AllowedIPs)
	p.Tags = slices.Clone(p.Tags)
	return p
}

func (p peerInfo) tailscaleIP() (netip.Addr, bool) {
	return preferIP(p.IPs)
}

// ipnWatch is the subset of a LocalAPI bus watcher presence needs.
type ipnWatch interface {
	Next() (ipn.Notify, error)
	Close() error
}

// presence is the collector's live peer view.
type presence struct {
	log   *slog.Logger
	clock tstime.Clock
	watch func(context.Context, ipn.NotifyWatchOpt) (ipnWatch, error)

	mu      sync.RWMutex
	peers   map[tailcfg.NodeID]peerInfo
	seeded  bool
	gen     uint64
	handler func(string) (tailcfg.NodeID, bool)
	subs    []chan struct{}
	waiters []chan struct{}
}

func newPresence(
	log *slog.Logger,
	clock tstime.Clock,
	watch func(context.Context, ipn.NotifyWatchOpt) (ipnWatch, error),
) *presence {
	if clock == nil {
		clock = tstime.StdClock{}
	}
	return &presence{
		log:   log,
		clock: clock,
		watch: watch,
		peers: map[tailcfg.NodeID]peerInfo{},
	}
}

// Online reports whether control says id is connected, and whether that
// answer is known. A disconnected bus, a missing peer, and a peer whose
// online bit was never set are all unknown.
func (p *presence) Online(id tailcfg.NodeID) (bool, bool) {
	if p == nil {
		return false, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.seeded {
		return false, false
	}
	peer, ok := p.peers[id]
	if !ok {
		return false, false
	}
	return peer.Online.Get()
}

func (p *presence) seededNow() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.seeded
}

// generation is the reseed and primary-route generation, with whether the
// view is currently seeded.
func (p *presence) generation() (uint64, bool) {
	if p == nil {
		return 0, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.gen, p.seeded
}

// ByIP returns the peer that owns ip as a Tailscale address.
func (p *presence) ByIP(ip netip.Addr) (peerInfo, bool) {
	if p == nil {
		return peerInfo{}, false
	}
	ip = ip.Unmap()
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.matchLocked(func(peer peerInfo) bool {
		return slices.ContainsFunc(peer.IPs, func(cand netip.Addr) bool {
			return cand.Unmap() == ip
		})
	})
}

// Route returns the peer that is primary for the longest matching route.
func (p *presence) Route(ip netip.Addr) (peerInfo, bool) {
	if p == nil {
		return peerInfo{}, false
	}
	ip = ip.Unmap()
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best peerInfo
	bestBits := -1
	found := false
	for _, peer := range p.peers {
		for _, pref := range peer.Routes {
			if !pref.Contains(ip) || pref.Bits() < bestBits {
				continue
			}
			if pref.Bits() == bestBits && peer.Name >= best.Name {
				continue
			}
			best = peer
			bestBits = pref.Bits()
			found = true
		}
	}
	if !found {
		return peerInfo{}, false
	}
	return best.clone(), true
}

// ByName returns the peer whose MagicDNS name, short name, or hostname
// matches. Full DNS names win over short names, which win over hostnames.
func (p *presence) ByName(name string) (peerInfo, bool) {
	if p == nil {
		return peerInfo{}, false
	}
	want := strings.TrimSuffix(strings.TrimSpace(name), ".")
	if want == "" {
		return peerInfo{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	bestPrio := namePrioNone
	var chosen []peerInfo
	for _, peer := range p.peers {
		prio, ok := namePriority(peer, want)
		if !ok || prio > bestPrio {
			continue
		}
		if prio < bestPrio {
			bestPrio = prio
			chosen = chosen[:0]
		}
		chosen = append(chosen, peer)
	}
	if len(chosen) == 0 {
		return peerInfo{}, false
	}
	slices.SortFunc(chosen, func(a, b peerInfo) int {
		if n := strings.Compare(strings.ToLower(a.DNSName), strings.ToLower(b.DNSName)); n != 0 {
			return n
		}
		return strings.Compare(strings.ToLower(a.HostName), strings.ToLower(b.HostName))
	})
	return chosen[0].clone(), true
}

func namePriority(peer peerInfo, want string) (int, bool) {
	switch {
	case peer.DNSName != "" && strings.EqualFold(peer.DNSName, want):
		return namePrioDNS, true
	case peer.Name != "" && !strings.EqualFold(peer.Name, peer.DNSName) && strings.EqualFold(peer.Name, want):
		return namePrioShort, true
	case peer.HostName != "" && strings.EqualFold(peer.HostName, want):
		return namePrioHost, true
	default:
		return namePrioNone, false
	}
}

// Peers returns a copy of the current view, ordered by short name.
func (p *presence) Peers() []peerInfo {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]peerInfo, 0, len(p.peers))
	for _, peer := range p.peers {
		out = append(out, peer.clone())
	}
	slices.SortFunc(out, func(a, b peerInfo) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func (p *presence) peer(id tailcfg.NodeID) (peerInfo, bool) {
	if p == nil {
		return peerInfo{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	peer, ok := p.peers[id]
	if !ok {
		return peerInfo{}, false
	}
	return peer.clone(), true
}

func (p *presence) setHandler(fn func(string) (tailcfg.NodeID, bool)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.handler = fn
	p.mu.Unlock()
}

// TagsFor reports the ACL tags for a target. The classifier installs the
// handler that maps a target name to its tailnet node.
func (p *presence) TagsFor(target string) []string {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	fn := p.handler
	seeded := p.seeded
	p.mu.RUnlock()
	if fn == nil || !seeded {
		return nil
	}
	id, ok := fn(target)
	if !ok {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.seeded {
		return nil
	}
	peer, ok := p.peers[id]
	if !ok {
		return nil
	}
	return slices.Clone(peer.Tags)
}

// whenSeeded fires when the view is seeded. The channel is closed
// immediately when presence is already seeded.
func (p *presence) whenSeeded() <-chan struct{} {
	ch := make(chan struct{})
	if p == nil {
		close(ch)
		return ch
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seeded {
		close(ch)
		return ch
	}
	p.waiters = append(p.waiters, ch)
	return ch
}

// changed reports reseed and primary-route updates. The channel has
// capacity one and drops signals that arrive while the previous one is
// unread.
func (p *presence) changed() <-chan struct{} {
	ch := make(chan struct{}, 1)
	if p == nil {
		return ch
	}
	p.mu.Lock()
	p.subs = append(p.subs, ch)
	p.mu.Unlock()
	return ch
}

func (p *presence) matchLocked(pred func(peerInfo) bool) (peerInfo, bool) {
	var found []peerInfo
	for _, peer := range p.peers {
		if pred(peer) {
			found = append(found, peer)
		}
	}
	if len(found) == 0 {
		return peerInfo{}, false
	}
	slices.SortFunc(found, func(a, b peerInfo) int {
		return strings.Compare(a.Name, b.Name)
	})
	return found[0].clone(), true
}

// apply folds one bus message into the view.
func (p *presence) apply(n ipn.Notify) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if n.InitialStatus != nil {
		p.seedLocked(n.InitialStatus)
	}
	for _, patch := range n.PeerChangedPatch {
		p.applyPatchLocked(patch)
	}
	for _, node := range n.PeersChanged {
		p.upsertNodeLocked(node)
	}
	for _, id := range n.PeersRemoved {
		delete(p.peers, id)
	}
	if n.State != nil && *n.State != ipn.Running {
		p.seeded = false
	}
}

func (p *presence) seedLocked(st *ipnstate.Status) {
	next := map[tailcfg.NodeID]peerInfo{}
	if st != nil {
		for _, ps := range st.Peer {
			info, ok := peerFromStatus(ps)
			if !ok {
				continue
			}
			next[info.NodeID] = info
		}
	}
	p.peers = next
	p.seeded = true
	p.gen++
	p.signalLocked()
}

func (p *presence) applyPatchLocked(ch *tailcfg.PeerChange) {
	if ch == nil || ch.Online == nil {
		return
	}
	peer, ok := p.peers[ch.NodeID]
	if !ok {
		return
	}
	peer.Online = opt.NewBool(*ch.Online)
	p.peers[ch.NodeID] = peer
}

func (p *presence) upsertNodeLocked(n *tailcfg.Node) {
	info, ok := peerFromNode(n)
	if !ok {
		return
	}
	prev, had := p.peers[info.NodeID]
	p.peers[info.NodeID] = info
	if had && samePrefixes(prev.Routes, info.Routes) {
		return
	}
	p.gen++
	p.signalLocked()
}

func (p *presence) signalLocked() {
	for _, ch := range p.waiters {
		close(ch)
	}
	p.waiters = nil
	for _, ch := range p.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (p *presence) markUnseeded() {
	p.mu.Lock()
	p.seeded = false
	p.mu.Unlock()
}

// run watches the IPN bus until ctx is canceled. A disconnect or a
// tailscaled state other than Running marks the view unknown and retries
// with backoff capped at 30 seconds.
func (p *presence) run(ctx context.Context) {
	if p == nil || p.watch == nil {
		return
	}
	bo := backoff.NewBackoff("presence", p.backoffLogf(), presenceBackoffMax)
	bo.Clock = p.clock
	for {
		if ctx.Err() != nil {
			return
		}
		start := p.clock.Now()
		err := p.watchOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if p.clock.Since(start) >= presenceBackoffMax {
			bo.BackOff(ctx, nil)
		}
		p.markUnseeded()
		if p.log != nil {
			p.log.WarnContext(ctx, "presence bus disconnected", "err", err)
		}
		bo.BackOff(ctx, err)
	}
}

func (p *presence) watchOnce(ctx context.Context) error {
	w, err := p.watch(ctx, ipn.NotifyInitialStatus|ipn.NotifyPeerPatches)
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	for {
		n, nextErr := w.Next()
		if nextErr != nil {
			return nextErr
		}
		p.apply(n)
		if n.State != nil && *n.State != ipn.Running {
			return fmt.Errorf("tailscaled state %s", n.State)
		}
	}
}

func (p *presence) backoffLogf() logger.Logf {
	return func(format string, args ...any) {
		if p.log != nil {
			p.log.Debug(fmt.Sprintf(format, args...))
		}
	}
}

func peerFromStatus(ps *ipnstate.PeerStatus) (peerInfo, bool) {
	if ps == nil || ps.NodeID == 0 {
		return peerInfo{}, false
	}
	dns := strings.TrimSuffix(ps.DNSName, ".")
	name := shortDNSName(dns)
	if name == "" {
		name = ps.HostName
	}
	return peerInfo{
		NodeID:     ps.NodeID,
		StableID:   ps.ID,
		Name:       name,
		DNSName:    dns,
		HostName:   ps.HostName,
		IPs:        slices.Clone(ps.TailscaleIPs),
		Routes:     prefixSlice(ps.PrimaryRoutes),
		AllowedIPs: prefixSlice(ps.AllowedIPs),
		Tags:       stringSlice(ps.Tags),
		Online:     opt.NewBool(ps.Online),
	}, true
}

func peerFromNode(n *tailcfg.Node) (peerInfo, bool) {
	if n == nil || n.ID == 0 {
		return peerInfo{}, false
	}
	dns := strings.TrimSuffix(n.Name, ".")
	name := n.ComputedName
	if name == "" {
		name = shortDNSName(dns)
	}
	host := ""
	if n.Hostinfo.Valid() {
		host = n.Hostinfo.Hostname()
	}
	if name == "" {
		name = host
	}
	online := opt.Bool("")
	if n.Online != nil {
		online = opt.NewBool(*n.Online)
	}
	return peerInfo{
		NodeID:     n.ID,
		StableID:   n.StableID,
		Name:       name,
		DNSName:    dns,
		HostName:   host,
		IPs:        prefixAddrs(n.Addresses),
		Routes:     slices.Clone(n.PrimaryRoutes),
		AllowedIPs: slices.Clone(n.AllowedIPs),
		Tags:       slices.Clone(n.Tags),
		Online:     online,
	}, true
}

func shortDNSName(dns string) string {
	dns = strings.TrimSuffix(strings.TrimSpace(dns), ".")
	if dns == "" {
		return ""
	}
	label, _, _ := strings.Cut(dns, ".")
	return label
}

func prefixAddrs(prefs []netip.Prefix) []netip.Addr {
	out := make([]netip.Addr, 0, len(prefs))
	for _, pref := range prefs {
		if pref.IsSingleIP() {
			out = append(out, pref.Addr().Unmap())
		}
	}
	return out
}

func prefixSlice(s *views.Slice[netip.Prefix]) []netip.Prefix {
	if s == nil {
		return nil
	}
	return s.AsSlice()
}

func stringSlice(s *views.Slice[string]) []string {
	if s == nil {
		return nil
	}
	return s.AsSlice()
}

func samePrefixes(a, b []netip.Prefix) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
