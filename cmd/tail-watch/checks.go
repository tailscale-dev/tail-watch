package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/ping"
	"tailscale.com/tailcfg"
	"tailscale.com/tstime"
	"tailscale.com/types/key"
)

const (
	checkICMP      = "icmp"
	checkTSPing    = "tsping"
	httpBodyLimit  = 1 << 20
	httpExcerpt    = 300
	statusCacheTTL = 30 * time.Second

	icmpPermissionMsg = "cannot open an ICMP socket: set net.ipv4.ping_group_range or grant CAP_NET_RAW"
)

type check interface {
	String() string
	Kind() string
	Run(ctx context.Context, env netEnv, t *Target) (Result, error)
}

func parseCheck(s string) (check, error) {
	switch {
	case s == checkICMP:
		return icmpCheck{}, nil
	case s == checkTSPing:
		return tspingCheck{}, nil
	case strings.HasPrefix(s, "tls:"):
		return nil, errors.New("tls checks are not supported in this version")
	case strings.HasPrefix(s, "tcp:"):
		portStr := strings.TrimPrefix(s, "tcp:")
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid TCP port %q", portStr)
		}
		return tcpCheck{port: port}, nil
	case strings.HasPrefix(s, "http:"):
		rest := strings.TrimPrefix(s, "http:")
		urlPart, text, hasText := strings.Cut(rest, "|")
		u, err := url.Parse(urlPart)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("invalid HTTP URL %q", urlPart)
		}
		return httpCheck{raw: s, url: urlPart, host: u.Hostname(), text: text, match: hasText}, nil
	default:
		return nil, fmt.Errorf("unknown check type %q", s)
	}
}

type icmpCheck struct{}

func (icmpCheck) String() string { return checkICMP }
func (icmpCheck) Kind() string   { return checkICMP }

func (c icmpCheck) Run(ctx context.Context, env netEnv, t *Target) (Result, error) {
	res := Result{Target: t, Check: c.String(), Kind: c.Kind(),
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Err:        nil,
		Ping:       nil,
	}
	ip, err := lookupAddr(ctx, t.Addr)
	if err != nil {
		res.Err = err
		return res, err
	}
	p := ping.New(ctx, func(format string, args ...any) {
		//nolint:sloglint // Checks use the CLI-configured process logger.
		slog.DebugContext(ctx, fmt.Sprintf(format, args...))
	}, env)
	p.Unprivileged = env.UnprivilegedICMP()
	defer func() { _ = p.Close() }()
	start := time.Now()
	d, err := p.Send(ctx, &net.IPAddr{IP: ip.AsSlice(), Zone: ip.Zone()}, nil)
	if err != nil {
		err = classifyProbeErr(err, !env.UnprivilegedICMP())
		res.Err = err
		return res, err
	}
	if d <= 0 {
		d = time.Since(start)
	}
	res.Latency = d
	return res, nil
}

func classifyProbeErr(err error, rawICMP bool) error {
	if err == nil {
		return nil
	}
	if isTimeout(err) {
		return fmt.Errorf("timeout: %w", err)
	}
	if isICMPPermission(err) || (rawICMP && isListenFailure(err)) {
		return fmt.Errorf("%s: %w", icmpPermissionMsg, err)
	}
	return err
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isICMPPermission(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "operation not permitted") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "socket: permission")
}

func isListenFailure(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "listen" {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "listen")
}

type tcpCheck struct {
	port int
}

func (c tcpCheck) String() string { return fmt.Sprintf("tcp:%d", c.port) }
func (c tcpCheck) Kind() string   { return "tcp" }

func (c tcpCheck) Run(ctx context.Context, env netEnv, t *Target) (Result, error) {
	res := Result{Target: t, Check: c.String(), Kind: c.Kind(),
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Err:        nil,
		Ping:       nil,
	}
	start := time.Now()
	conn, err := env.Dial(ctx, "tcp", net.JoinHostPort(t.Addr, strconv.Itoa(c.port)))
	res.Latency = time.Since(start)
	if err != nil {
		err = classifyProbeErr(err, false)
		res.Err = err
		return res, err
	}
	if cerr := conn.Close(); cerr != nil {
		//nolint:sloglint // Checks use the CLI-configured process logger.
		slog.DebugContext(ctx, "close tcp", "err", cerr)
	}
	return res, nil
}

type httpCheck struct {
	raw   string
	url   string
	host  string
	text  string
	match bool
}

func (c httpCheck) String() string { return c.raw }
func (c httpCheck) Kind() string   { return "http" }

// httpStatusError is an HTTP check that received a status outside 2xx and 3xx.
type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	if e == nil {
		return "HTTP status"
	}
	return fmt.Sprintf("HTTP status %d", e.code)
}

// httpBodyError is an HTTP check whose body did not contain the required text.
type httpBodyError struct {
	text    string
	excerpt string
}

func (e *httpBodyError) Error() string {
	if e == nil {
		return "body missing"
	}
	return fmt.Sprintf("body missing %q; first 300 bytes: %q", e.text, e.excerpt)
}

func (c httpCheck) Run(ctx context.Context, env netEnv, t *Target) (Result, error) {
	res := Result{Target: t, Check: c.String(), Kind: c.Kind(),
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Err:        nil,
		Ping:       nil,
	}
	start := time.Now()
	transport := &http.Transport{
		Proxy:             nil, // Probe the target directly, ignoring environment proxies.
		DialContext:       env.Dial,
		DisableKeepAlives: true,
		ForceAttemptHTTP2: false,
	}
	if t != nil && t.InsecureTLS {
		// Devices such as a NAS often present a self-signed certificate.
		// Certificate expiry is the phase-2 tls check.
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // Per-target insecure_tls opt-in.
		}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		res.Err = err
		return res, err
	}
	resp, err := client.Do(req)
	res.Latency = time.Since(start)
	if err != nil {
		err = classifyProbeErr(err, false)
		res.Err = err
		return res, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		err = &httpStatusError{code: resp.StatusCode}
		res.Err = err
		return res, err
	}
	if !c.match {
		return res, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpBodyLimit))
	if err != nil {
		res.Err = err
		return res, err
	}
	if !strings.Contains(string(body), c.text) {
		excerpt := body
		if len(excerpt) > httpExcerpt {
			excerpt = excerpt[:httpExcerpt]
		}
		err = &httpBodyError{text: c.text, excerpt: string(excerpt)}
		res.Err = err
		return res, err
	}
	return res, nil
}

type tspingCheck struct {
	resolver *tsResolver
}

func (tspingCheck) String() string { return checkTSPing }
func (tspingCheck) Kind() string   { return checkTSPing }

func (c tspingCheck) Run(ctx context.Context, env netEnv, t *Target) (Result, error) {
	r := c.resolver
	if r == nil {
		r = newTSResolver(tstime.StdClock{}, slog.Default(), func(ctx context.Context) (*ipnstate.Status, error) {
			return env.LocalClient().Status(ctx)
		})
	}
	ip, err := r.resolve(ctx, t.Addr)
	if err != nil {
		res := Result{Target: t, Check: c.String(), Kind: c.Kind(), Err: err,
			Start:      time.Time{},
			Generation: 0,
			Latency:    0,
			Ping:       nil,
		}
		return res, err
	}
	pr, err := env.LocalClient().Ping(ctx, ip, tailcfg.PingDisco)
	res, err := interpretPing(t.Addr, pr, err)
	res.Target = t
	res.Check = c.String()
	res.Kind = c.Kind()
	return res, err
}

func interpretPing(addr string, pr *ipnstate.PingResult, callErr error) (Result, error) {
	res := Result{Ping: pr,
		Target:     nil,
		Check:      "",
		Kind:       "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Err:        nil,
	}
	if noMatchingPeer(callErr, pr) {
		err := fmt.Errorf("no peer handles %s; check grants to tag:monitor", addr)
		res.Err = err
		return res, err
	}
	if callErr != nil {
		res.Err = callErr
		return res, callErr
	}
	if pr == nil {
		err := errors.New("tsping returned no result")
		res.Err = err
		return res, err
	}
	if pr.Err != "" {
		err := errors.New(pr.Err)
		res.Err = err
		return res, err
	}
	res.Latency = time.Duration(pr.LatencySeconds * float64(time.Second))
	return res, nil
}

func noMatchingPeer(err error, pr *ipnstate.PingResult) bool {
	if err != nil && strings.Contains(err.Error(), "no matching peer") {
		return true
	}
	return pr != nil && strings.Contains(pr.Err, "no matching peer")
}

// pathKind reports direct, peer_relay, or derp from a pong.
func pathKind(pr *ipnstate.PingResult) (string, string) {
	if pr == nil {
		return "", ""
	}
	if pr.Endpoint != "" {
		return "direct", pr.Endpoint
	}
	if pr.PeerRelay != "" {
		return "peer_relay", pr.PeerRelay
	}
	if pr.DERPRegionCode != "" {
		return "derp", pr.DERPRegionCode
	}
	return "", ""
}

func lookupAddr(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	ip, ok := preferIP(ips)
	if !ok {
		return netip.Addr{}, fmt.Errorf("no address for %s", host)
	}
	return ip, nil
}

func preferIP(ips []netip.Addr) (netip.Addr, bool) {
	var v6 netip.Addr
	for _, ip := range ips {
		ip = ip.Unmap()
		if ip.Is4() {
			return ip, true
		}
		if ip.Is6() && !v6.IsValid() {
			v6 = ip
		}
	}
	if v6.IsValid() {
		return v6, true
	}
	return netip.Addr{}, false
}

type tsResolver struct {
	log      *slog.Logger
	cache    *statusCache
	presence *presence
}

type statusCache struct {
	mu    sync.Mutex
	clock tstime.Clock
	ttl   time.Duration
	fetch func(context.Context) (*ipnstate.Status, error)
	at    time.Time
	st    *ipnstate.Status
}

func newTSResolver(
	clock tstime.Clock,
	log *slog.Logger,
	fetch func(context.Context) (*ipnstate.Status, error),
) *tsResolver {
	if clock == nil {
		clock = tstime.StdClock{}
	}
	return &tsResolver{
		log: log,
		cache: &statusCache{
			clock: clock,
			ttl:   statusCacheTTL,
			fetch: fetch,

			mu: sync.Mutex{},
			at: time.Time{},
			st: nil,
		},

		presence: nil,
	}
}

func (r *tsResolver) resolve(ctx context.Context, addr string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(addr); err == nil {
		return ip.Unmap(), nil
	}
	if r.presence != nil && r.presence.seededNow() {
		if peer, found := r.presence.ByName(addr); found {
			if ip, ok := peer.tailscaleIP(); ok {
				return ip, nil
			}
		}
		return lookupAddr(ctx, addr)
	}
	st, err := r.cache.get(ctx)
	if err == nil {
		if ip, warn, ok := matchPeer(addr, st); ok {
			if warn != "" && r.log != nil {
				r.log.WarnContext(ctx, warn, "addr", addr)
			}
			return ip, nil
		}
	}
	ips, lerr := net.DefaultResolver.LookupNetIP(ctx, "ip", addr)
	if lerr != nil {
		if err != nil {
			return netip.Addr{}, err
		}
		return netip.Addr{}, lerr
	}
	ip, ok := preferIP(ips)
	if !ok {
		return netip.Addr{}, fmt.Errorf("no address for %s", addr)
	}
	return ip, nil
}

func (c *statusCache) get(ctx context.Context) (*ipnstate.Status, error) {
	c.mu.Lock()
	now := c.clock.Now()
	if c.st != nil && now.Sub(c.at) < c.ttl {
		st := c.st
		c.mu.Unlock()
		return st, nil
	}
	c.mu.Unlock()
	st, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.st = st
	c.at = c.clock.Now()
	c.mu.Unlock()
	return st, nil
}

type peerHit struct {
	ip   netip.Addr
	dns  string
	host string
	prio int
}

// matchPeerName ranks a peer's full DNS name, short DNS name, and hostname.
func matchPeerName(ps *ipnstate.PeerStatus, want, suffix string) (peerHit, bool) {
	if ps == nil {
		return peerHit{}, false
	}
	ip, ok := peerTailscaleIP(ps)
	if !ok {
		return peerHit{}, false
	}
	dns := strings.TrimSuffix(ps.DNSName, ".")
	short := dns
	if suffix != "" {
		suf := "." + suffix
		if strings.HasSuffix(strings.ToLower(short), strings.ToLower(suf)) {
			short = short[:len(short)-len(suf)]
		}
	}
	hit := peerHit{ip: ip, dns: dns, host: ps.HostName,
		prio: 0,
	}
	switch {
	case dns != "" && strings.EqualFold(dns, want):
		hit.prio = 0
	case short != "" && !strings.EqualFold(short, dns) && strings.EqualFold(short, want):
		hit.prio = 1
	case ps.HostName != "" && strings.EqualFold(ps.HostName, want):
		hit.prio = 2
	default:
		return peerHit{}, false
	}
	return hit, true
}

func matchPeer(addr string, st *ipnstate.Status) (netip.Addr, string, bool) {
	if st == nil {
		return netip.Addr{}, "", false
	}
	want := strings.TrimSuffix(strings.TrimSpace(addr), ".")
	if want == "" {
		return netip.Addr{}, "", false
	}
	suffix := strings.TrimSuffix(magicSuffix(st), ".")
	var hits []peerHit
	consider := func(ps *ipnstate.PeerStatus) {
		if hit, ok := matchPeerName(ps, want, suffix); ok {
			hits = append(hits, hit)
		}
	}
	consider(st.Self)
	keys := make([]key.NodePublic, 0, len(st.Peer))
	for k := range st.Peer {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key.NodePublic) int { return a.Compare(b) })
	for _, k := range keys {
		consider(st.Peer[k])
	}
	if len(hits) == 0 {
		return netip.Addr{}, "", false
	}
	best := hits[0].prio
	for _, h := range hits {
		if h.prio < best {
			best = h.prio
		}
	}
	chosen := make([]peerHit, 0, len(hits))
	for _, h := range hits {
		if h.prio == best {
			chosen = append(chosen, h)
		}
	}
	slices.SortFunc(chosen, func(a, b peerHit) int {
		if n := strings.Compare(strings.ToLower(a.dns), strings.ToLower(b.dns)); n != 0 {
			return n
		}
		if n := strings.Compare(strings.ToLower(a.host), strings.ToLower(b.host)); n != 0 {
			return n
		}
		return strings.Compare(a.ip.String(), b.ip.String())
	})
	warn := ""
	if len(chosen) > 1 {
		warn = fmt.Sprintf("ambiguous peer name %q matches %d nodes", addr, len(chosen))
	}
	return chosen[0].ip, warn, true
}

func magicSuffix(st *ipnstate.Status) string {
	if st == nil || st.CurrentTailnet == nil {
		return ""
	}
	return st.CurrentTailnet.MagicDNSSuffix
}

func peerTailscaleIP(ps *ipnstate.PeerStatus) (netip.Addr, bool) {
	return preferIP(ps.TailscaleIPs)
}
