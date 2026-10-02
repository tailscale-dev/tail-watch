package main

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// learner reconciles remote LAN parents on a clock.
type learner struct {
	c       *classifier
	done    chan struct{}
	once    sync.Once
	onArmed func()
	onRound func(time.Time)
	changed <-chan struct{}
}

func newLearner(c *classifier) *learner {
	l := &learner{c: c, done: make(chan struct{}),
		once:    sync.Once{},
		onArmed: nil,
		onRound: nil,
		changed: nil,
	}
	if c == nil || !c.hasRemoteLAN() {
		l.finish()
		if c != nil {
			c.finishRound(context.Background(), nil, true)
		}
		return l
	}
	return l
}

// FirstPassDone fires when the first learning pass has attempted every
// remote LAN target. It does not mean the topology was authoritative.
func (l *learner) FirstPassDone() <-chan struct{} {
	if l == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return l.done
}

func (l *learner) finish() {
	l.once.Do(func() { close(l.done) })
}

func (l *learner) run(ctx context.Context) {
	if l == nil || l.c == nil {
		return
	}
	l.changed = l.c.presence.changed()
	if l.c.hasRemoteLAN() {
		if !l.wait(ctx, learnInitialWait, true) {
			return
		}
		l.pass(ctx)
		l.finish()
	}
	for {
		if !l.wait(ctx, learnInterval, false) {
			return
		}
		l.pass(ctx)
	}
}

func (l *learner) pass(ctx context.Context) {
	select {
	case <-l.changed:
	default:
	}
	if l.onRound != nil {
		l.onRound(l.c.clock.Now())
	}
	l.c.learnRound(ctx)
}

func (l *learner) wait(ctx context.Context, d time.Duration, alsoSeed bool) bool {
	timer, ch := l.c.clock.NewTimer(d)
	defer timer.Stop()
	if l.onArmed != nil {
		l.onArmed()
	}
	var seeded <-chan struct{}
	if l.c.presence != nil && alsoSeed {
		seeded = l.c.presence.whenSeeded()
	}
	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	case <-seeded:
		return true
	case <-l.changed:
		return true
	}
}

func (c *classifier) hasRemoteLAN() bool {
	return len(c.remoteLANTargets()) > 0
}

func (c *classifier) remoteLANTargets() []*Target {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Target
	for _, t := range c.configured {
		if c.isRemoteLAN(t) {
			out = append(out, t)
		}
	}
	return out
}

func (c *classifier) learnRound(ctx context.Context) {
	targets := c.remoteLANTargets()
	startGen, startSeeded := c.presenceGen()
	c.pingAll(ctx, targets)
	endGen, endSeeded := c.presenceGen()
	continuous := startSeeded && endSeeded && startGen == endGen
	complete := len(targets) == 0 || (continuous && c.allResolved(targets))
	c.finishRound(ctx, targets, complete)
}

func (c *classifier) presenceGen() (uint64, bool) {
	if c.presence == nil {
		return 0, false
	}
	return c.presence.generation()
}

func (c *classifier) allResolved(targets []*Target) bool {
	for _, t := range targets {
		if _, ok := c.handlerPeer(t); !ok {
			return false
		}
	}
	return true
}

func (c *classifier) pingAll(ctx context.Context, targets []*Target) {
	sem := make(chan struct{}, learnParallel)
	var wg sync.WaitGroup
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t *Target) {
			defer wg.Done()
			defer func() { <-sem }()
			c.learnOne(ctx, t)
		}(t)
	}
	wg.Wait()
}

func (c *classifier) learnOne(ctx context.Context, t *Target) {
	ip, err := resolveLearnAddr(ctx, t.Addr)
	if err != nil {
		c.mu.Lock()
		_, learned := c.learned[t.Name]
		c.mu.Unlock()
		if !learned {
			c.noteGrant(ctx, t, false)
		}
		return
	}
	pr, pingErr := c.ping(ctx, ip, t.Timeout)
	if peer, ok := peerFromPong(c, pr, pingErr); ok {
		c.remember(t.Name, peer, ip)
		c.noteGrant(ctx, t, true)
		return
	}
	c.fallback(ctx, t, ip)
}

func resolveLearnAddr(ctx context.Context, addr string) (netip.Addr, error) {
	return lookupAddr(ctx, addr)
}

func peerFromPong(c *classifier, pr *ipnstate.PingResult, err error) (peerInfo, bool) {
	if err != nil || pr == nil || pr.Err != "" || pr.NodeIP == "" || c == nil || c.presence == nil {
		return peerInfo{}, false
	}
	ip, parseErr := netip.ParseAddr(pr.NodeIP)
	if parseErr != nil {
		return peerInfo{}, false
	}
	return c.presence.ByIP(ip)
}

func (c *classifier) fallback(ctx context.Context, t *Target, ip netip.Addr) {
	c.mu.Lock()
	rec, had := c.learned[t.Name]
	c.mu.Unlock()
	var route peerInfo
	hasRoute := false
	if c.presence != nil {
		route, hasRoute = c.presence.Route(ip)
	}
	if had && !routeReplaced(rec, route, hasRoute) {
		c.noteGrant(ctx, t, true)
		return
	}
	if hasRoute {
		c.remember(t.Name, route, ip)
		c.noteGrant(ctx, t, true)
		return
	}
	c.noteGrant(ctx, t, false)
}

func routeReplaced(rec handlerRec, route peerInfo, has bool) bool {
	if !has {
		return false
	}
	if rec.RouteID != 0 {
		return route.NodeID != rec.RouteID
	}
	if rec.NodeID == 0 {
		return true
	}
	return route.NodeID != rec.NodeID
}

func (c *classifier) remember(name string, peer peerInfo, ip netip.Addr) {
	var routeID tailcfg.NodeID
	if c.presence != nil {
		if route, ok := c.presence.Route(ip); ok {
			routeID = route.NodeID
		}
	}
	c.mu.Lock()
	c.learned[name] = handlerRec{NodeID: peer.NodeID, RouteID: routeID, IP: ip}
	c.mu.Unlock()
}

func (c *classifier) noteGrant(ctx context.Context, t *Target, handled bool) {
	if t == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.grants[t.Name]
	if handled {
		c.grants[t.Name] = grantState{
			unhandled: false,
		}
		return
	}
	if st.unhandled {
		return
	}
	c.grants[t.Name] = grantState{unhandled: true}
	if c.log != nil {
		c.log.WarnContext(
			ctx,
			fmt.Sprintf("target %s: no peer handles %s; check grants to tag:monitor", t.Name, t.Addr),
		)
	}
}

func (c *classifier) finishRound(ctx context.Context, targets []*Target, complete bool) {
	c.ensureParents(ctx, targets)
	c.publishMu.Lock()
	next := c.proposedParents()
	if path, cyc := graphCycle(next); cyc {
		c.publishMu.Unlock()
		if c.log != nil {
			c.log.WarnContext(ctx, "dependency cycle rejected", "path", path)
		}
		c.setReconcile(complete, nil)
		return
	}
	err := c.publishParents(ctx, next)
	c.publishMu.Unlock()
	if err != nil {
		if c.log != nil {
			c.log.WarnContext(ctx, "dependency update rejected", "err", err)
		}
		c.setReconcile(complete, nil)
		return
	}
	removed := c.sweep(ctx, next, complete)
	c.setReconcile(complete, removed)
}

func (c *classifier) setReconcile(complete bool, removed []string) {
	c.mu.Lock()
	c.lastReconcile = reconcileResult{Complete: complete, Removed: append([]string{}, removed...)}
	c.mu.Unlock()
}

// LastReconciliation reports the latest learning pass.
func (c *classifier) LastReconciliation() reconcileResult {
	if c == nil {
		return reconcileResult{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.lastReconcile
	out.Removed = append([]string{}, c.lastReconcile.Removed...)
	return out
}

func (c *classifier) ensureParents(ctx context.Context, targets []*Target) {
	for _, t := range targets {
		peer, ok := c.handlerPeer(t)
		if !ok || t.Parent != "" {
			continue
		}
		c.ensureParentTarget(ctx, peer, c.dependentsOf(peer))
	}
}

func (c *classifier) dependentsOf(peer peerInfo) []*Target {
	var out []*Target
	for _, t := range c.configured {
		if t.Parent != "" || !c.isRemoteLAN(t) {
			continue
		}
		got, ok := c.handlerPeer(t)
		if ok && got.NodeID == peer.NodeID {
			out = append(out, t)
		}
	}
	return out
}

func (c *classifier) ensureParentTarget(ctx context.Context, peer peerInfo, deps []*Target) *Target {
	if existing := c.configuredReachability(peer); existing != nil {
		return existing
	}
	c.mu.Lock()
	if st := c.implicit[peer.NodeID]; st != nil {
		st.restored = false
		c.mu.Unlock()
		return st.target
	}
	c.mu.Unlock()
	t := c.newImplicit(peer, deps)
	if c.log != nil {
		c.log.InfoContext(ctx, "implicit target created", "target", t.Name, "addr", t.Addr)
	}
	if c.registry != nil {
		if err := c.registry.AddTarget(t); err != nil {
			if c.log != nil {
				c.log.WarnContext(ctx, "implicit target registration failed", "target", t.Name, "err", err)
			}
			c.mu.Lock()
			delete(c.implicit, peer.NodeID)
			delete(c.implicitByName, t.Name)
			delete(c.byName, t.Name)
			c.mu.Unlock()
			return nil
		}
	}
	return t
}

func (c *classifier) newImplicit(peer peerInfo, deps []*Target) *Target {
	base := builtinTiming()
	ip, _ := peer.tailscaleIP()
	t := &Target{
		Dynamic:          false,
		Name:             allocImplicitName(peer.Name, string(peer.StableID), c.takenNames()),
		Addr:             ip.String(),
		Site:             sharedSite(deps),
		Severity:         SeverityPage,
		Interval:         base.Interval,
		Timeout:          base.Timeout,
		FailThreshold:    base.FailThreshold,
		RecoverThreshold: base.RecoverThreshold,
		FlapLimit:        base.FlapLimit,
		FlapWindow:       base.FlapWindow,
		Network:          NetworkTailnet,
		Checks: []check{tspingCheck{
			resolver: nil,
		}},
		Implicit: true,
		StableID: peer.StableID,
		NodeID:   peer.NodeID,

		Parent:      "",
		InsecureTLS: false, Route: "",
	}
	st := &implicitState{target: t,
		restored:    false,
		deferLogged: false,
	}
	c.mu.Lock()
	c.implicit[peer.NodeID] = st
	c.implicitByName[t.Name] = st
	c.byName[t.Name] = t
	c.mu.Unlock()
	return t
}

func (c *classifier) configuredReachability(peer peerInfo) *Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	var names []string
	for _, t := range c.configured {
		if tspingOnly(t) && c.sameNodeLocked(t, peer) {
			names = append(names, t.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	return c.byName[names[0]]
}

func tspingOnly(t *Target) bool {
	return t != nil && !t.Implicit && len(t.Checks) == 1 && t.Checks[0].Kind() == checkTSPing
}

func (c *classifier) sameNodeLocked(t *Target, peer peerInfo) bool {
	if t.NodeID != 0 && t.NodeID == peer.NodeID {
		return true
	}
	if ip, err := netip.ParseAddr(t.Addr); err == nil {
		for _, cand := range peer.IPs {
			if cand.Unmap() == ip.Unmap() {
				return true
			}
		}
	}
	return strings.EqualFold(t.Addr, peer.Name) ||
		strings.EqualFold(t.Addr, peer.DNSName) ||
		strings.EqualFold(t.Addr, peer.HostName)
}

func (c *classifier) takenNames() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	taken := make(map[string]bool, len(c.byName))
	for name := range c.byName {
		taken[name] = true
	}
	return taken
}

func (c *classifier) mappedParentName(peer peerInfo) string {
	if t := c.configuredReachability(peer); t != nil {
		return t.Name
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.implicit[peer.NodeID]; st != nil && st.target != nil {
		return st.target.Name
	}
	return ""
}

func (c *classifier) proposedParents() map[string]string {
	out := map[string]string{}
	for _, t := range c.allTargets() {
		if t.Parent != "" {
			out[t.Name] = t.Parent
			continue
		}
		if t.Network == NetworkLocal || c.isNodeTarget(t) {
			continue
		}
		peer, ok := c.handlerPeer(t)
		if !ok {
			continue
		}
		if name := c.mappedParentName(peer); name != "" && name != t.Name {
			out[t.Name] = name
		}
	}
	return out
}

func (c *classifier) allTargets() []*Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Target, 0, len(c.byName))
	for _, t := range c.byName {
		out = append(out, t)
	}
	return out
}

func (c *classifier) publishParents(ctx context.Context, next map[string]string) error {
	c.mu.Lock()
	prev := maps.Clone(c.parentOf)
	c.mu.Unlock()
	if maps.Equal(prev, next) {
		return nil
	}
	if c.registry != nil {
		if err := c.registry.UpdateParents(maps.Clone(next)); err != nil {
			return err
		}
	}
	c.logParentChanges(ctx, prev, next)
	c.swapParentMetrics(prev, next)
	c.mu.Lock()
	c.parentOf = maps.Clone(next)
	c.mu.Unlock()
	return nil
}

func (c *classifier) logParentChanges(ctx context.Context, prev, next map[string]string) {
	if c.log == nil {
		return
	}
	for name, parent := range next {
		if prev[name] == parent {
			continue
		}
		c.log.InfoContext(ctx, "parent changed", "target", name, "from", prev[name], "to", parent)
	}
}

func (c *classifier) swapParentMetrics(prev, next map[string]string) {
	for target, parent := range prev {
		if parent != "" && next[target] != parent {
			c.parents.DeleteLabelValues(target, parent)
		}
	}
	for target, parent := range next {
		if parent != "" {
			c.parents.WithLabelValues(target, parent).Set(1)
		}
	}
}

func (c *classifier) sweep(ctx context.Context, edges map[string]string, complete bool) []string {
	desired := c.desiredNodes()
	c.mu.Lock()
	states := make([]*implicitState, 0, len(c.implicit))
	for _, st := range c.implicit {
		states = append(states, st)
	}
	c.mu.Unlock()
	var removed []string
	for _, st := range states {
		if st == nil || st.target == nil {
			continue
		}
		if dependentCount(edges, st.target.Name) > 0 {
			c.mu.Lock()
			st.deferLogged = false
			c.mu.Unlock()
			continue
		}
		if c.keepImplicit(ctx, st, desired, complete) {
			continue
		}
		if c.removeImplicit(st) {
			removed = append(removed, st.target.Name)
		}
	}
	slices.Sort(removed)
	return removed
}

func (c *classifier) keepImplicit(
	ctx context.Context,
	st *implicitState,
	desired map[tailcfg.NodeID]bool,
	complete bool,
) bool {
	if st.restored && complete && !desired[st.target.NodeID] {
		return false
	}
	if st.restored && !complete {
		return true
	}
	if c.registry != nil && c.registry.IsDown(st.target.Name) {
		c.logDefer(ctx, st)
		return true
	}
	return false
}

func (c *classifier) logDefer(ctx context.Context, st *implicitState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st.deferLogged || c.log == nil {
		return
	}
	st.deferLogged = true
	c.log.WarnContext(ctx, implicitDeferPrefix+st.target.Name+implicitDeferSuffix)
}

func (c *classifier) desiredNodes() map[tailcfg.NodeID]bool {
	out := map[tailcfg.NodeID]bool{}
	for _, t := range c.configured {
		if t.Implicit || t.Parent != "" || !c.isRemoteLAN(t) {
			continue
		}
		peer, ok := c.handlerPeer(t)
		if ok {
			out[peer.NodeID] = true
		}
	}
	return out
}

func dependentCount(edges map[string]string, name string) int {
	n := 0
	for _, parent := range edges {
		if parent == name {
			n++
		}
	}
	return n
}

func (c *classifier) removeImplicit(st *implicitState) bool {
	if st == nil || st.target == nil {
		return false
	}
	name := st.target.Name
	if c.registry != nil {
		if err := c.registry.RemoveTarget(name); err != nil {
			return false
		}
	}
	c.mu.Lock()
	delete(c.implicit, st.target.NodeID)
	delete(c.implicitByName, name)
	delete(c.byName, name)
	if parent := c.parentOf[name]; parent != "" {
		c.parents.DeleteLabelValues(name, parent)
	}
	delete(c.parentOf, name)
	c.mu.Unlock()
	return true
}

// RestoreImplicit keeps a previously open implicit target until an
// authoritative reconciliation shows it is no longer a dependency.
func (c *classifier) RestoreImplicit(t *Target) {
	if c == nil || t == nil {
		return
	}
	t.Implicit = true
	st := &implicitState{target: t, restored: true,
		deferLogged: false,
	}
	c.mu.Lock()
	c.implicit[t.NodeID] = st
	c.implicitByName[t.Name] = st
	c.byName[t.Name] = t
	c.mu.Unlock()
	if c.registry != nil {
		_ = c.registry.AddTarget(t)
	}
}

func allocImplicitName(base, stable string, taken map[string]bool) string {
	base = sanitizeTargetName(base)
	if base == "" {
		base = "node"
	}
	if !taken[base] && targetNameRE.MatchString(base) {
		return base
	}
	suffix := sanitizeTargetName(stable)
	if suffix == "" {
		suffix = "id"
	}
	candidate := base + "-" + suffix
	if !taken[candidate] && targetNameRE.MatchString(candidate) {
		return candidate
	}
	n := 2
	for {
		next := fmt.Sprintf("%s-%d", candidate, n)
		if !taken[next] {
			return next
		}
		n++
	}
}

func sanitizeTargetName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sharedSite(deps []*Target) string {
	if len(deps) == 0 {
		return ""
	}
	site := deps[0].Site
	for _, dep := range deps[1:] {
		if dep.Site != site {
			return ""
		}
	}
	return site
}

func graphCycle(edges map[string]string) (string, bool) {
	const (
		unseen = 0
		active = 1
		done   = 2
	)
	state := map[string]int{}
	var stack []string
	var cycle string
	var walk func(string)
	walk = func(name string) {
		if cycle != "" || state[name] == done {
			return
		}
		if state[name] == active {
			cycle = cyclePath(stack, name)
			return
		}
		state[name] = active
		stack = append(stack, name)
		if parent := edges[name]; parent != "" {
			walk(parent)
		}
		stack = stack[:len(stack)-1]
		state[name] = done
	}
	names := make([]string, 0, len(edges))
	for name := range edges {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		walk(name)
		if cycle != "" {
			return cycle, true
		}
	}
	return "", false
}

func cyclePath(stack []string, name string) string {
	i := 0
	for i < len(stack) && stack[i] != name {
		i++
	}
	parts := append([]string{}, stack[i:]...)
	parts = append(parts, name)
	return strings.Join(parts, " -> ")
}
