package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// role is the access a tailnet grant gives a caller of the monitor API.
type role string

const (
	roleNone   role = ""
	roleViewer role = "viewer"
	roleEditor role = "editor"
)

// errNoIdentity is returned when tailscaled cannot identify the caller.
var errNoIdentity = errors.New("caller could not be identified")

const (
	rankNone   = 0
	rankViewer = 1
	rankEditor = 2
)

func (r role) rank() int {
	switch r {
	case roleEditor:
		return rankEditor
	case roleViewer:
		return rankViewer
	case roleNone:
	}
	return rankNone
}

// allows reports whether r satisfies need. editor satisfies viewer.
func (r role) allows(need role) bool {
	return need != roleNone && r.rank() >= need.rank()
}

type capGrant struct {
	Role string `json:"role"`
}

// roleFromCaps returns the highest viewer or editor grant in caps for
// capability. Malformed values and unknown roles are ignored.
func roleFromCaps(caps tailcfg.PeerCapMap, capability string) role {
	if capability == "" || len(caps) == 0 {
		return roleNone
	}
	peerCap := tailcfg.PeerCapability(capability)
	raws, ok := caps[peerCap]
	if !ok || len(raws) == 0 {
		return roleNone
	}
	best := roleNone
	for _, raw := range raws {
		one := make(tailcfg.PeerCapMap)
		one[peerCap] = []tailcfg.RawMessage{raw}
		got, err := tailcfg.UnmarshalCapJSON[capGrant](one, peerCap)
		if err != nil || len(got) != 1 {
			continue
		}
		parsed := role(got[0].Role)
		if parsed.rank() > best.rank() {
			best = parsed
		}
	}
	return best
}

// caller is who tailscaled says made a request.
type caller struct {
	User string
	Node string
	Tags []string
	Role role
}

// identitySource is the LocalAPI whois surface the resolver uses.
// *local.Client implements it. Tests use a fake.
type identitySource interface {
	WhoIs(ctx context.Context, addr string) (*apitype.WhoIsResponse, error)
	WhoIsForService(ctx context.Context, addr string, svc tailcfg.ServiceName) (*apitype.WhoIsResponse, error)
}

// resolver turns a request into a caller by asking tailscaled.
type resolver struct {
	src        identitySource
	capability string
	service    string
	enabled    bool
}

func newResolver(src identitySource, auth Authorization) *resolver {
	return &resolver{
		src:        src,
		capability: auth.Capability,
		service:    auth.Service,
		enabled:    auth.On(),
	}
}

// resolve reads X-Forwarded-For only on a loopback connection and asks
// tailscaled who that address is. service selects WhoIsForService.
func (rs *resolver) resolve(r *http.Request) (caller, error) {
	if rs == nil || rs.src == nil || r == nil {
		return noCaller(), errNoIdentity
	}
	addr, err := callerAddr(r)
	if err != nil {
		return noCaller(), errNoIdentity
	}
	var resp *apitype.WhoIsResponse
	if rs.service != "" {
		resp, err = rs.src.WhoIsForService(r.Context(), addr, tailcfg.ServiceName(rs.service))
	} else {
		resp, err = rs.src.WhoIs(r.Context(), addr)
	}
	if err != nil {
		return noCaller(), errNoIdentity
	}
	return callerFrom(resp, rs.capability), nil
}

func noCaller() caller {
	return caller{User: "", Node: "", Tags: nil, Role: roleNone}
}

func callerFrom(resp *apitype.WhoIsResponse, capability string) caller {
	if resp == nil || resp.Node == nil {
		return noCaller()
	}
	n := resp.Node
	user := ""
	if !n.IsTagged() && resp.UserProfile != nil {
		user = resp.UserProfile.LoginName
	}
	return caller{
		User: user,
		Node: nodeName(n),
		Tags: append([]string(nil), n.Tags...),
		Role: roleFromCaps(resp.CapMap, capability),
	}
}

func nodeName(n *tailcfg.Node) string {
	if n == nil {
		return ""
	}
	if n.ComputedName != "" {
		return n.ComputedName
	}
	return strings.TrimSuffix(n.Name, ".")
}

// callerAddr is the address WhoIs should see. It is the first
// X-Forwarded-For entry, and only when the connection itself is loopback.
// A bare IP is returned as an IP. An ip:port is returned in host:port form,
// with brackets around an IPv6 host.
func callerAddr(r *http.Request) (string, error) {
	if r == nil || !loopbackRemote(r.RemoteAddr) {
		return "", errNoIdentity
	}
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
	return normalizeWhoIsAddr(strings.TrimSpace(first))
}

func loopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func normalizeWhoIsAddr(raw string) (string, error) {
	if raw == "" {
		return "", errNoIdentity
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr.String(), nil
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", errNoIdentity
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "", errNoIdentity
	}
	if _, convErr := strconv.Atoi(port); convErr != nil || port == "" {
		return "", errNoIdentity
	}
	return net.JoinHostPort(addr.String(), port), nil
}

// decide reports whether c may perform an operation that needs need.
// The error names the role that was required, or says that none was granted.
func decide(need role, c caller) error {
	if c.Role.allows(need) {
		return nil
	}
	if c.Role == roleNone {
		return errors.New("no role was granted")
	}
	return fmt.Errorf("role %s is required", need)
}

var _ identitySource = (*local.Client)(nil)
