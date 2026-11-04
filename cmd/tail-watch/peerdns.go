package main

import (
	"context"
	"net/netip"
	"slices"
	"strings"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

// peerDNS is the MagicDNS name of the peer that owns addr, from the live peer
// view when it lists addr and from the status cache otherwise.
func (r *tsResolver) peerDNS(ctx context.Context, addr string) (string, bool) {
	if r == nil {
		return "", false
	}
	if r.presence != nil && r.presence.seededNow() {
		if name, ok := presenceDNS(r.presence, addr); ok {
			return name, true
		}
	}
	st, err := r.cache.get(ctx)
	if err != nil {
		return "", false
	}
	return statusDNS(st, addr)
}

// presenceDNS is the MagicDNS name of the peer that owns addr. A subnet
// router does not own the addresses it routes.
func presenceDNS(p *presence, addr string) (string, bool) {
	if p == nil {
		return "", false
	}
	addr = strings.TrimSpace(addr)
	if ip, err := netip.ParseAddr(addr); err == nil {
		info, ok := p.ByIP(ip)
		if !ok {
			return "", false
		}
		return trimmedDNS(info.DNSName)
	}
	info, ok := p.ByName(addr)
	if !ok {
		return "", false
	}
	return trimmedDNS(info.DNSName)
}

// statusDNS is presenceDNS against a Status snapshot, including Self.
func statusDNS(st *ipnstate.Status, addr string) (string, bool) {
	if st == nil {
		return "", false
	}
	addr = strings.TrimSpace(addr)
	if ip, err := netip.ParseAddr(addr); err == nil {
		return peerDNSAt(st, ip.Unmap())
	}
	ip, _, ok := matchPeer(addr, st)
	if !ok {
		return "", false
	}
	return peerDNSAt(st, ip)
}

func peerDNSAt(st *ipnstate.Status, ip netip.Addr) (string, bool) {
	peers := make([]*ipnstate.PeerStatus, 0, 1+len(st.Peer))
	if st.Self != nil {
		peers = append(peers, st.Self)
	}
	keys := make([]key.NodePublic, 0, len(st.Peer))
	for k := range st.Peer {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key.NodePublic) int { return a.Compare(b) })
	for _, k := range keys {
		peers = append(peers, st.Peer[k])
	}
	names := make([]string, 0, 1)
	for _, ps := range peers {
		if ps == nil || !peerOwnsIP(ps, ip) {
			continue
		}
		if name, ok := trimmedDNS(ps.DNSName); ok {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", false
	}
	slices.Sort(names)
	return names[0], true
}

func peerOwnsIP(ps *ipnstate.PeerStatus, ip netip.Addr) bool {
	for _, cand := range ps.TailscaleIPs {
		if cand.Unmap() == ip {
			return true
		}
	}
	return false
}

func trimmedDNS(dns string) (string, bool) {
	name := strings.TrimSuffix(strings.TrimSpace(dns), ".")
	return name, name != ""
}
