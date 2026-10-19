package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/tsaddr"
	"tailscale.com/tailcfg"
	"tailscale.com/tstime"
	"tailscale.com/types/opt"
	"tailscale.com/util/singleflight"
)

const (
	metricNamespace = "tailwatch"
	labelTarget     = "target"

	probeShareMax    = 10 * time.Second
	learnInitialWait = 10 * time.Second
	learnInterval    = 10 * time.Minute
	learnParallel    = 8

	pathDirect = "direct"
	pathDERP   = "derp"
	pathRelay  = "peer_relay"

	implicitDeferPrefix = "implicit target "
	implicitDeferSuffix = " has no dependents but is down; deferring removal"
)

// layer is the failure class attached to a check result. Empty means the
// check passed.
type layer string

const (
	layerNone     layer = ""
	layerControl  layer = "control"
	layerTunnel   layer = "tunnel"
	layerLAN      layer = "lan"
	layerService  layer = "service"
	layerUnrouted layer = "unrouted"
)

type errClass string

const (
	errNone    errClass = ""
	errRefused errClass = "refused"
	errTimeout errClass = "timeout"
	errHTTP    errClass = "http"
	errTLS     errClass = "tls"
	errOther   errClass = "other"
)

// pathInfo is how a disco pong reached a node.
type pathInfo struct {
	Kind     string
	Endpoint string
	DERP     string
	Latency  time.Duration
}

// Classified is a check result plus its failure layer, parent, and path.
type Classified struct {
	Result

	Layer  layer
	Parent string
	Path   pathInfo
}

type classifiedConsumer interface {
	ObserveClassified(Classified)
}

// targetRegistry keeps check scheduling and alerting on the same target set.
type targetRegistry interface {
	AddTarget(*Target) error
	RemoveTarget(name string) error
	IsDown(name string) bool
	UpdateParents(parents map[string]string) error
}

type classifyInput struct {
	local         bool
	tsping        bool
	errClass      errClass
	nodeTarget    bool
	handlerKnown  bool
	handlerOnline opt.Bool
	tunnelOK      opt.Bool
	deviceICMPOK  opt.Bool
}

type handlerRec struct {
	NodeID  tailcfg.NodeID
	RouteID tailcfg.NodeID
	IP      netip.Addr
}

type implicitState struct {
	target      *Target
	restored    bool
	deferLogged bool
}

type grantState struct {
	unhandled bool
}

// reconcileResult is one dependency-learning pass. Complete is true only
// when the pass can authorize removal of restored implicit targets.
type reconcileResult struct {
	Complete bool
	Removed  []string
}

type probeHit struct {
	at   time.Time
	ok   bool
	path pathInfo
}

type probeCache[K comparable] struct {
	mu    sync.Mutex
	clock tstime.Clock
	group singleflight.Group[K, probeHit]
	items map[K]probeHit
}

func newProbeCache[K comparable](clock tstime.Clock) *probeCache[K] {
	if clock == nil {
		clock = tstime.StdClock{}
	}
	return &probeCache[K]{clock: clock, items: map[K]probeHit{},
		mu:    sync.Mutex{},
		group: singleflight.Group[K, probeHit]{},
	}
}

func probeTTL(interval time.Duration) time.Duration {
	if interval <= 0 || interval > probeShareMax {
		return probeShareMax
	}
	return interval
}

func (c *probeCache[K]) store(key K, ok bool, path pathInfo) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.items[key] = probeHit{at: c.clock.Now(), ok: ok, path: path}
	c.mu.Unlock()
}

func (c *probeCache[K]) fresh(key K, interval time.Duration) (probeHit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.items[key]
	if !ok || c.clock.Since(hit.at) >= probeTTL(interval) {
		return probeHit{}, false
	}
	return hit, true
}

func (c *probeCache[K]) get(key K, interval time.Duration, fn func() (bool, pathInfo)) (bool, pathInfo) {
	if hit, ok := c.fresh(key, interval); ok {
		return hit.ok, hit.path
	}
	hit, _, _ := c.group.Do(key, func() (probeHit, error) {
		if cached, ok := c.fresh(key, interval); ok {
			return cached, nil
		}
		ok, path := fn()
		stored := probeHit{at: c.clock.Now(), ok: ok, path: path}
		c.mu.Lock()
		c.items[key] = stored
		c.mu.Unlock()
		return stored, nil
	})
	return hit.ok, hit.path
}

// classifier turns check results into classified results and learns parents.
type classifier struct {
	presence *presence
	env      netEnv
	log      *slog.Logger
	clock    tstime.Clock
	registry targetRegistry
	disco    *probeCache[tailcfg.NodeID]
	icmp     *probeCache[netip.Addr]
	layers   *prometheus.CounterVec
	paths    *prometheus.GaugeVec
	parents  *prometheus.GaugeVec

	pingFn func(context.Context, netip.Addr, time.Duration) (*ipnstate.PingResult, error)
	icmpFn func(context.Context, netip.Addr, time.Duration) bool

	// publishMu serializes computing and publishing the parent graph, so a
	// learning round and a runtime add cannot overwrite each other's edges.
	publishMu      sync.Mutex
	mu             sync.Mutex
	configured     []*Target
	byName         map[string]*Target
	learned        map[string]handlerRec
	parentOf       map[string]string
	implicit       map[tailcfg.NodeID]*implicitState
	implicitByName map[string]*implicitState
	grants         map[string]grantState
	consumers      []classifiedConsumer
	lastReconcile  reconcileResult
}

func newClassifier(
	pres *presence,
	env netEnv,
	log *slog.Logger,
	clock tstime.Clock,
	reg prometheus.Registerer,
	targets []*Target,
	registry targetRegistry,
) *classifier {
	if clock == nil {
		clock = tstime.StdClock{}
	}
	c := &classifier{
		presence:       pres,
		env:            env,
		log:            log,
		clock:          clock,
		registry:       registry,
		disco:          newProbeCache[tailcfg.NodeID](clock),
		icmp:           newProbeCache[netip.Addr](clock),
		layers:         newLayerCounter(),
		paths:          newPathGauge(),
		parents:        newParentGauge(),
		configured:     targets,
		byName:         map[string]*Target{},
		learned:        map[string]handlerRec{},
		parentOf:       map[string]string{},
		implicit:       map[tailcfg.NodeID]*implicitState{},
		implicitByName: map[string]*implicitState{},
		grants:         map[string]grantState{},

		pingFn:    nil,
		icmpFn:    nil,
		publishMu: sync.Mutex{},
		mu:        sync.Mutex{},
		consumers: nil,
		lastReconcile: reconcileResult{
			Complete: false,
			Removed:  nil,
		},
	}
	for _, t := range targets {
		if t != nil && t.Name != "" {
			c.byName[t.Name] = t
		}
	}
	if reg != nil {
		reg.MustRegister(c.layers, c.paths, c.parents)
	}
	return c
}

func newLayerCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Name:      "failure_layer_total",
		Help:      "Failed check results by assigned failure layer.",
	}, []string{labelTarget, "layer"})
}

func newPathGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      "path",
		Help:      "Current tsping path kind for a target. 1 is the active kind.",
	}, []string{labelTarget, "kind"})
}

func newParentGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      "target_parent",
		Help:      "Current parent of a target. The series is 1 while that parent applies.",
	}, []string{labelTarget, "parent"})
}

func (c *classifier) addConsumer(cons classifiedConsumer) {
	if c == nil || cons == nil {
		return
	}
	c.mu.Lock()
	c.consumers = append(c.consumers, cons)
	c.mu.Unlock()
}

// Observe classifies res and delivers it to the classified consumers.
func (c *classifier) Observe(res Result) {
	if c == nil {
		return
	}
	if res.Err == nil {
		c.seedPassing(res)
	}
	out := c.classify(context.Background(), res)
	if res.Err != nil && res.Target != nil && out.Layer != "" {
		c.layers.WithLabelValues(res.Target.Name, string(out.Layer)).Inc()
	}
	if res.Kind == checkTSPing && res.Target != nil {
		c.writePath(res.Target.Name, out.Path)
	}
	for _, cons := range c.consumerSnapshot() {
		cons.ObserveClassified(out)
	}
}

func (c *classifier) consumerSnapshot() []classifiedConsumer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.consumers)
}

func (c *classifier) classify(ctx context.Context, res Result) Classified {
	out := Classified{Result: res, Path: pathFromResult(res),
		Layer:  "",
		Parent: "",
	}
	if res.Target == nil {
		return out
	}
	out.Parent = c.Parent(res.Target.Name)
	if res.Err == nil {
		return out
	}
	in := c.baseInput(res)
	if needsTunnel(in) {
		in.tunnelOK = c.tunnelState(ctx, res)
	}
	if needsICMP(in) {
		in.deviceICMPOK = c.deviceState(ctx, res)
	}
	out.Layer = decideLayer(in)
	return out
}

func pathFromResult(res Result) pathInfo {
	if res.Kind != checkTSPing {
		return pathInfo{}
	}
	return pathFromPing(res.Ping)
}

// Parent returns the published parent target name. Configured parents apply
// before the first learning round. A rejected graph update leaves the
// previous parent in place.
func (c *classifier) Parent(name string) string {
	if c == nil {
		return ""
	}
	t := c.target(name)
	if t == nil {
		return ""
	}
	c.mu.Lock()
	parent := c.parentOf[name]
	c.mu.Unlock()
	if parent != "" {
		return parent
	}
	return t.Parent
}

func (c *classifier) target(name string) *Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byName[name]
}

// nodeForTarget is the presence tag hook. Local targets and unknown
// handlers return no node.
func (c *classifier) nodeForTarget(name string) (tailcfg.NodeID, bool) {
	t := c.target(name)
	if t == nil || t.Network == NetworkLocal {
		return 0, false
	}
	peer, ok := c.handlerPeer(t)
	if !ok {
		return 0, false
	}
	return peer.NodeID, true
}

func (c *classifier) baseInput(res Result) classifyInput {
	in := classifyInput{
		local:      res.Target.Network == NetworkLocal,
		tsping:     res.Kind == checkTSPing,
		errClass:   classifyErr(res.Err),
		nodeTarget: c.isNodeTarget(res.Target),

		handlerKnown:  false,
		handlerOnline: "",
		tunnelOK:      "",
		deviceICMPOK:  "",
	}
	if res.Kind == checkICMP {
		in.deviceICMPOK = opt.False
	}
	if in.local {
		return in
	}
	if res.Kind == checkTSPing {
		in.tunnelOK = opt.False
	}
	peer, ok := c.handlerPeer(res.Target)
	in.handlerKnown = ok
	if !ok || c.presence == nil {
		return in
	}
	online, known := c.presence.Online(peer.NodeID)
	if known {
		in.handlerOnline = opt.NewBool(online)
	}
	return in
}

func (c *classifier) handlerPeer(t *Target) (peerInfo, bool) {
	if t == nil || t.Network == NetworkLocal || c.presence == nil {
		return peerInfo{}, false
	}
	if peer, ok := c.peerForAddr(t.Addr); ok {
		return peer, true
	}
	c.mu.Lock()
	rec, had := c.learned[t.Name]
	c.mu.Unlock()
	if had && !c.routeSupersedes(t, rec) {
		if peer, ok := c.presence.peer(rec.NodeID); ok {
			return peer, true
		}
	}
	if route, ok := c.routeFor(t, rec.IP); ok {
		return route, true
	}
	if had {
		if peer, ok := c.presence.peer(rec.NodeID); ok {
			return peer, true
		}
	}
	return peerInfo{}, false
}

func (c *classifier) routeSupersedes(t *Target, rec handlerRec) bool {
	route, ok := c.routeFor(t, rec.IP)
	if !ok {
		return false
	}
	if rec.RouteID != 0 {
		return route.NodeID != rec.RouteID
	}
	return route.NodeID != rec.NodeID
}

func (c *classifier) peerForAddr(addr string) (peerInfo, bool) {
	if c.presence == nil {
		return peerInfo{}, false
	}
	if ip, err := netip.ParseAddr(addr); err == nil {
		return c.presence.ByIP(ip.Unmap())
	}
	return c.presence.ByName(addr)
}

func (c *classifier) routeFor(t *Target, learnedIP netip.Addr) (peerInfo, bool) {
	if c.presence == nil || t == nil {
		return peerInfo{}, false
	}
	ip, ok := literalOrLearnedIP(t.Addr, learnedIP)
	if !ok {
		return peerInfo{}, false
	}
	return c.presence.Route(ip)
}

func literalOrLearnedIP(addr string, learned netip.Addr) (netip.Addr, bool) {
	if ip, err := netip.ParseAddr(addr); err == nil {
		return ip.Unmap(), true
	}
	if learned.IsValid() {
		return learned, true
	}
	return netip.Addr{}, false
}

func (c *classifier) isNodeTarget(t *Target) bool {
	if t == nil || t.Network != NetworkTailnet {
		return false
	}
	if t.Implicit {
		return true
	}
	if ip, err := netip.ParseAddr(t.Addr); err == nil && tsaddr.IsTailscaleIP(ip.Unmap()) {
		return true
	}
	if c.presence == nil || !c.presence.seededNow() {
		return false
	}
	_, ok := c.peerForAddr(t.Addr)
	return ok
}

func (c *classifier) isRemoteLAN(t *Target) bool {
	if t == nil || t.Implicit || t.Network != NetworkTailnet || c.isNodeTarget(t) {
		return false
	}
	ip, err := netip.ParseAddr(t.Addr)
	if err != nil {
		return true
	}
	ip = ip.Unmap()
	return !ip.IsLoopback() && !tsaddr.IsTailscaleIP(ip)
}

func (c *classifier) tunnelState(ctx context.Context, res Result) opt.Bool {
	peer, ok := c.handlerPeer(res.Target)
	if !ok {
		return opt.False
	}
	up, path, ok := c.discoProbe(ctx, peer, res.Target)
	if !ok {
		return opt.False
	}
	if path.Kind != "" && c.isNodeTarget(res.Target) {
		c.writePath(res.Target.Name, path)
	}
	return opt.NewBool(up)
}

func (c *classifier) discoProbe(ctx context.Context, peer peerInfo, t *Target) (bool, pathInfo, bool) {
	if hit, fresh := c.disco.fresh(peer.NodeID, t.Interval); fresh {
		return hit.ok, hit.path, true
	}
	ip, ok := peer.tailscaleIP()
	if !ok {
		return false, pathInfo{}, false
	}
	up, path := c.disco.get(peer.NodeID, t.Interval, func() (bool, pathInfo) {
		return c.probeDisco(ctx, ip, t.Timeout)
	})
	return up, path, true
}

func (c *classifier) deviceState(ctx context.Context, res Result) opt.Bool {
	ip, ok := c.targetIP(res.Target)
	if !ok {
		return opt.False
	}
	if hit, fresh := c.icmp.fresh(ip, res.Target.Interval); fresh {
		return opt.NewBool(hit.ok)
	}
	up, _ := c.icmp.get(ip, res.Target.Interval, func() (bool, pathInfo) {
		return c.probeICMP(ctx, ip, res.Target.Timeout), pathInfo{}
	})
	return opt.NewBool(up)
}

func (c *classifier) targetIP(t *Target) (netip.Addr, bool) {
	if t == nil {
		return netip.Addr{}, false
	}
	if ip, err := netip.ParseAddr(t.Addr); err == nil {
		return ip.Unmap(), true
	}
	c.mu.Lock()
	rec := c.learned[t.Name]
	c.mu.Unlock()
	if rec.IP.IsValid() {
		return rec.IP, true
	}
	return netip.Addr{}, false
}

func (c *classifier) seedPassing(res Result) {
	if res.Target == nil {
		return
	}
	switch res.Kind {
	case checkICMP:
		if ip, ok := c.targetIP(res.Target); ok {
			c.icmp.store(ip, true, pathInfo{Latency: res.Latency,
				Kind:     "",
				Endpoint: "",
				DERP:     "",
			})
		}
	case checkTSPing:
		peer, ok := c.handlerPeer(res.Target)
		if ok {
			c.disco.store(peer.NodeID, true, pathFromPing(res.Ping))
		}
	}
}

func (c *classifier) probeDisco(ctx context.Context, ip netip.Addr, timeout time.Duration) (bool, pathInfo) {
	pr, err := c.ping(ctx, ip, timeout)
	if err != nil || pr == nil || pr.Err != "" {
		return false, pathInfo{}
	}
	return true, pathFromPing(pr)
}

func (c *classifier) ping(
	ctx context.Context,
	ip netip.Addr,
	timeout time.Duration,
) (*ipnstate.PingResult, error) {
	if c.pingFn != nil {
		return c.pingFn(ctx, ip, timeout)
	}
	if c.env == nil {
		return nil, errors.New("tailscaled local client is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := (tspingCheck{
		resolver: nil,
	}).Run(ctx, c.env, &Target{Dynamic: false, Addr: ip.String(), Timeout: timeout,
		Name:             "",
		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		Interval:         0,
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
		return res.Ping, err
	}
	if res.Err != nil {
		return res.Ping, res.Err
	}
	return res.Ping, nil
}

func (c *classifier) probeICMP(ctx context.Context, ip netip.Addr, timeout time.Duration) bool {
	if c.icmpFn != nil {
		return c.icmpFn(ctx, ip, timeout)
	}
	if c.env == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := (icmpCheck{}).Run(ctx, c.env, &Target{Dynamic: false, Addr: ip.String(), Timeout: timeout,
		Name:             "",
		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		Interval:         0,
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
	return err == nil
}

func (c *classifier) writePath(target string, path pathInfo) {
	for _, kind := range []string{pathDirect, pathDERP, pathRelay} {
		val := 0.0
		if path.Kind == kind {
			val = 1
		}
		c.paths.WithLabelValues(target, kind).Set(val)
	}
}

func needsTunnel(in classifyInput) bool {
	if in.local || in.tsping || !in.handlerKnown {
		return false
	}
	if offline, ok := in.handlerOnline.Get(); ok && !offline {
		return false
	}
	_, known := in.tunnelOK.Get()
	return !known
}

func needsICMP(in classifyInput) bool {
	if in.tsping || answeredByError(in.errClass) {
		return false
	}
	if !in.local {
		if !in.handlerKnown || in.nodeTarget {
			return false
		}
		if offline, ok := in.handlerOnline.Get(); ok && !offline {
			return false
		}
		up, known := in.tunnelOK.Get()
		if !known || !up {
			return false
		}
	}
	_, known := in.deviceICMPOK.Get()
	return !known
}

func answeredByError(class errClass) bool {
	switch class {
	case errRefused, errHTTP, errTLS:
		return true
	case errNone, errTimeout, errOther:
		return false
	}
	return false
}

func decideLayer(in classifyInput) layer {
	if in.local {
		return decideDevice(in)
	}
	if in.tsping {
		if offline, ok := in.handlerOnline.Get(); ok && !offline {
			return layerControl
		}
		return layerTunnel
	}
	if !in.handlerKnown {
		return layerUnrouted
	}
	if offline, ok := in.handlerOnline.Get(); ok && !offline {
		return layerControl
	}
	if up, ok := in.tunnelOK.Get(); !ok || !up {
		return layerTunnel
	}
	if in.nodeTarget {
		return layerService
	}
	return decideDevice(in)
}

func decideDevice(in classifyInput) layer {
	if answeredByError(in.errClass) {
		return layerService
	}
	if ok, known := in.deviceICMPOK.Get(); known && ok {
		return layerService
	}
	if in.nodeTarget {
		return layerService
	}
	return layerLAN
}

func classifyErr(err error) errClass {
	if err == nil {
		return errNone
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return errRefused
	}
	if errors.Is(err, context.DeadlineExceeded) || netTimeout(err) {
		return errTimeout
	}
	var status *httpStatusError
	var body *httpBodyError
	if errors.As(err, &status) || errors.As(err, &body) {
		return errHTTP
	}
	if certErr(err) {
		return errTLS
	}
	return errOther
}

func netTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func certErr(err error) bool {
	var (
		ua x509.UnknownAuthorityError
		ci x509.CertificateInvalidError
		hn x509.HostnameError
		sr x509.SystemRootsError
		cv x509.ConstraintViolationError
		ve *tls.CertificateVerificationError
		rh tls.RecordHeaderError
	)
	return errors.As(err, &ua) || errors.As(err, &ci) || errors.As(err, &hn) ||
		errors.As(err, &sr) || errors.As(err, &cv) || errors.As(err, &ve) ||
		errors.As(err, &rh)
}

// pathFromPing maps a disco pong onto a path. A result with Err set has
// no path.
func pathFromPing(pr *ipnstate.PingResult) pathInfo {
	if pr == nil || pr.Err != "" {
		return pathInfo{}
	}
	out := pathInfo{Latency: time.Duration(pr.LatencySeconds * float64(time.Second)),
		Kind:     "",
		Endpoint: "",
		DERP:     "",
	}
	switch {
	case pr.Endpoint != "":
		out.Kind = pathDirect
		out.Endpoint = pr.Endpoint
	case pr.PeerRelay != "":
		out.Kind = pathRelay
		out.Endpoint = pr.PeerRelay
	case pr.DERPRegionCode != "":
		out.Kind = pathDERP
		out.DERP = pr.DERPRegionCode
	}
	return out
}

// AddConfigured registers a non-implicit target created after startup, such
// as a dynamic monitor, and publishes the parent graph so its parent edge
// reaches the alerter. It is undone if the graph cannot be published.
func (c *classifier) AddConfigured(ctx context.Context, t *Target) error {
	if c == nil || t == nil {
		return nil
	}
	c.mu.Lock()
	if _, taken := c.byName[t.Name]; taken {
		c.mu.Unlock()
		return fmt.Errorf("target %s is already registered", t.Name)
	}
	c.configured = append(slices.Clone(c.configured), t)
	c.byName[t.Name] = t
	c.mu.Unlock()
	if err := c.republishParents(ctx); err != nil {
		c.dropConfigured(t.Name)
		return err
	}
	return nil
}

// RemoveConfigured forgets a target added with AddConfigured and republishes
// the parent graph without it. parentOf keeps the old edge until that publish
// so the diff removes the edge and its metric series.
func (c *classifier) RemoveConfigured(ctx context.Context, name string) {
	if c == nil {
		return
	}
	c.dropConfigured(name)
	_ = c.republishParents(ctx)
}

func (c *classifier) dropConfigured(name string) {
	c.mu.Lock()
	c.configured = slices.DeleteFunc(slices.Clone(c.configured), func(t *Target) bool { return t.Name == name })
	delete(c.byName, name)
	delete(c.learned, name)
	delete(c.grants, name)
	c.mu.Unlock()
	c.layers.DeletePartialMatch(prometheus.Labels{labelTarget: name})
	c.paths.DeletePartialMatch(prometheus.Labels{labelTarget: name})
}

func (c *classifier) republishParents(ctx context.Context) error {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	next := c.proposedParents()
	if path, cyc := graphCycle(next); cyc {
		return errCycle(path)
	}
	return c.publishParents(ctx, next)
}
