# Design

## Context

`add-tsmon-collector` delivers `Result` values through
`resultConsumer.Observe`, one target at a time and in completion order (its
design, D6). That change's `tsping` check resolves names through a 30s
`Status()` cache (its D5). This change sits between the checks and the
alerting layer. The shapes in `tailscale.com` v1.102.5 were checked against
the module cache:

### Watching the IPN bus

- `local.Client.WatchIPNBus(ctx, mask)` returns an `*IPNBusWatcher` with
  `Next() (ipn.Notify, error)` and `Close()`.
- `ipn.NotifyInitialStatus` puts an `*ipnstate.Status` in the first
  message's `InitialStatus`. `ipn.NotifyPeerPatches` implies
  `NotifyPeerChanges`, so the watcher also receives `PeersChanged` and
  `PeersRemoved`.
- Both bits are in `NotifyRateLimitIncompatibleBits`, so `NotifyRateLimit`
  must not be set.
- `Notify.State *ipn.State` reports backend state changes.

### Incremental updates

- `Notify.PeerChangedPatch []*tailcfg.PeerChange` carries narrow patches.
  The struct has `NodeID`, `DERPRegion`, `Cap`, `CapMap`, `Endpoints`,
  `Key`, `KeySignature`, `DiscoKey`, `Online *bool`, `LastSeen` and
  `KeyExpiry`. The docs say the type may grow, and that a patch with
  unrecognized fields should be answered with `local.Client.PeerByID`.
- `Notify.PeersChanged []*tailcfg.Node` carries full nodes: `ID NodeID`,
  `Online *bool`, `Addresses`, `AllowedIPs`, `PrimaryRoutes`, `Tags`,
  `ComputedName` and `Hostinfo`. Route and tag changes, including HA
  primary failover, arrive here.
- `Notify.PeersRemoved []tailcfg.NodeID` lists removed peers.

### The initial snapshot

- `ipnstate.Status.Peer` is keyed by `key.NodePublic`.
- Each `PeerStatus` has both `ID` (a `StableNodeID`) and `NodeID`
  (`tailcfg.NodeID`), plus `Online bool`, `TailscaleIPs`, `AllowedIPs` and
  `PrimaryRoutes` (`*views.Slice[netip.Prefix]`), `Tags`, `HostName` and
  `DNSName`.

### Pings

- `ipnstate.PingResult.NodeIP` is the handling node's Tailscale IP.
- tailscaled answers a ping to an unrouted IP with the error
  `no matching peer`.

## Goals / Non-Goals

**Goals:**

- Stable seams for `add-alert-state-machine`:
  - `Classified` results through `classifiedConsumer.ObserveClassified`
  - `presence.Online(nodeID)`
  - `classifier.Parent(target)`, which returns a configured or implicit
    target name, or "" when no parent is known
  - `presence.TagsFor(target)`
  - `learner.FirstPassDone()` (attempt completion only) and authoritative
    topology reconciliation notifications
  - a `targetRegistry` that keeps prober and the alerter in step
- One disco ping per failing router per round, however many devices sit
  behind it.
- Everything decidable in unit tests from synthetic `Notify`, `PingResult`
  and presence values.

**Non-Goals:**

- Suppression, alert state and paging decisions. Change 3 owns those.
- Latency baselines and the `degraded` layer, which are phase 2.

## Decisions

### D1. Presence keyed by `tailcfg.NodeID`

Patches and removals carry `tailcfg.NodeID`, so the map uses the same key.
Seeding copies `PeerStatus.NodeID`, not `PeerStatus.ID`, which is a
`StableNodeID`. Each entry is:

```go
type peerInfo struct {
    NodeID     tailcfg.NodeID
    Name       string // MagicDNS short name (first label of DNSName/ComputedName), else HostName
    DNSName    string
    HostName   string
    IPs        []netip.Addr
    Routes     []netip.Prefix // PrimaryRoutes
    AllowedIPs []netip.Prefix
    Tags       []string
    Online     opt.Bool       // unset = unknown
}
```

`Online` uses `tailscale.com/types/opt.Bool`, whose unset state is
"unknown". A full node with `Online == nil` resets the field to unknown. A
patch whose `Online` is nil leaves it as it was.

### D2. Watcher loop and reconnect

`presence.run(ctx)` calls:

```go
WatchIPNBus(ctx, ipn.NotifyInitialStatus|ipn.NotifyPeerPatches)
```

Before reseeding it clears the view. If `Next()` returns an error, or
`Notify.State` reports anything other than `Running`, it marks the view as
unseeded, so every query reports `known=false`. It then retries with
`backoff.NewBackoff("presence", logf, 30*time.Second)`.

Patches apply only `Online`. None of the current `PeerChange` fields carry
addresses, routes, tags or names; those arrive as full nodes in
`PeersChanged`. Only the `Online` field affects presence, so other patch
fields are ignored, and presence needs no `PeerByID` refresh.

The view sits behind a `sync.RWMutex`. Readers use:

```go
Online(tailcfg.NodeID) (online, known bool)
ByIP(netip.Addr) (peerInfo, bool)    // Tailscale IP match
Route(netip.Addr) (peerInfo, bool)   // longest PrimaryRoutes prefix
ByName(string) (peerInfo, bool)      // exact DNSName, then short name, then HostName
Peers() []peerInfo
TagsFor(target string) []string
```

`TagsFor` needs to know which node a target maps to, and presence does not
know targets. The classifier therefore installs a resolver hook,
`presence.setHandler(func(target string) (tailcfg.NodeID, bool))`, backed by
D5's handling-node resolution. A Tailscale-node target maps to its own node,
and a LAN target to its handling node. Change 4's `match.tags` filter calls
`TagsFor`.

Alternative considered: polling `Status()` every N seconds. It is simpler,
but it adds latency and load, and the design doc chose the bus.

### D3. `tsping` resolution moves to presence

The check from change 1 (its D5) resolves names through `presence.ByName`
instead of its own 30s `Status()` cache. When presence is unseeded, it falls
back to the cache path. The external behavior in `reachability-checks` does
not change. This is the only edit to change 1 code.

### D4. Classifier as a `resultConsumer`

```go
type layer string // "", "control", "tunnel", "lan", "service", "unrouted"

type pathInfo struct {
    Kind     string // "direct", "derp", "peer_relay", "" if unknown
    Endpoint string // ip:port for direct, relay addr for peer_relay
    DERP     string // region code for derp
    Latency  time.Duration
}

type Classified struct {
    Result
    Layer  layer
    Parent string // target name, "" if none
    Path   pathInfo
}

type classifiedConsumer interface{ ObserveClassified(Classified) }
```

`classifier.Observe(Result)` classifies the result synchronously, then fans
it out to the `classifiedConsumer`s. Because change 1's `publish` holds the
per-target lock around `Observe`, per-target ordering carries through. A
supplemental probe blocks only that target's delivery, and only for up to
the target's timeout.

### D5. Handling node resolution

For target `t`, `network: local` bypasses tailnet handling-node resolution
and has only its explicit parent, if any. Local classification uses device
evidence for `lan` or `service`, never `unrouted`, `control` or `tunnel`.

For a tailnet target:

1. If `t.Addr` resolves to a peer via `ByIP` or `ByName`, `t` is a
   Tailscale-node target and that peer is the handling node.
2. Otherwise, resolve the actual handling node through learned data and
   `Route(addr)`. Explicit suppression parents do not replace that node: a
   switch dependency and the Tailscale router are separate facts.
3. A current primary-route change invalidates a stale learned router and
   triggers dependency reconciliation, without waiting for ten minutes.

`Parent(t)` returns a target name, configured or implicit: the config
parent, or else the
configured or implicit target mapped to the handling node.

### D6. Layer decision

The decision is a pure function, so table tests need no network:

```go
decide(in classifyInput) layer
// classifyInput: kind, errClass (refused|timeout|http|tls|other),
//   nodeTarget bool, handler known bool, handlerOnline opt.Bool,
//   tunnelOK opt.Bool, deviceICMPOK opt.Bool
```

`classifier.Observe` fills `classifyInput` lazily. It runs the disco ping
only when the decision still needs `tunnelOK`, and the ICMP echo only when
it still needs `deviceICMPOK`. Error classes come from `errors.Is` and
`errors.As`:

- `refused`: `syscall.ECONNREFUSED`
- `timeout`: `context.DeadlineExceeded`, or a `net.Error` with `Timeout()`
- `http`: the HTTP check's status or body-mismatch error type
- `tls`: `tls.RecordHeaderError`, `x509.*Error` or `tls.CertificateVerificationError`

### D7. Supplemental probe cache

`probeCache[K]` stores results keyed by `tailcfg.NodeID` for disco pings and
by `netip.Addr` for ICMP echoes. It is built on
`tailscale.com/util/singleflight`, the generic fork, plus a TTL map read
through the `tstime.Clock`. The TTL is `min(10s, t.Interval)`.

A target's own fresh `icmp` or `tsping` pass (age < TTL) seeds the cache,
so the classifier reuses it instead of sending another probe. Supplemental
probes call the same check implementations from change 1 directly. They do
not go through prober, so they stay out of prober metrics and the status
page.

### D8. Dependency learning loop

`learner.run(ctx)` fires at startup, after presence is first seeded or 10s
have passed, whichever comes first. It then runs every 10 minutes on the
clock.

- Per remote (`network: tailnet`) LAN target it sends
  `LocalClient().Ping(ctx, ip, tailcfg.PingDisco)`, at most 8 at a time.
  A name address is resolved first through the host resolver.
- It maps `NodeIP` to a peer through `presence.ByIP`.
- If the ping fails, a changed primary route supersedes stale learning;
  otherwise retain the learned node or fall back to `presence.Route(ip)`.
- It rebuilds the implicit-target set through a `targetRegistry`:

  ```go
  type targetRegistry interface {
      AddTarget(*Target)
      RemoveTarget(name string)
      IsDown(name string) bool
    UpdateParents(map[string]string) error
  }
  ```

  `tsmon.go` registers alert state, applying any restore, before scheduling
  each new target. Removal first checks alert eligibility, stops checks, then
  unregisters state. A topology transaction validates the combined graph and
  calls `alerter.UpdateParents(graph)` before publishing the new parent map. `IsDown` asks the
  alerter. Until change 3 lands, it returns false. Tests use a fake
  registry.
- A target to be removed while `IsDown` is true stays registered. The
  learner logs `implicit target <name> has no dependents but is down;
  deferring removal` once, and retries every round.
- After the first round finishes, it closes the channel returned by
  `FirstPassDone() <-chan struct{}`, guarded by `sync.Once`. The round ends
  when every LAN target's ping has returned or hit its timeout. When there
  are no LAN targets, the channel is closed in the learner's constructor.
  It never authorizes incident closure. A separate complete reconciliation
  requires continuously seeded presence and a resolved handler for every
  configured remote LAN target. With none configured, config alone suffices. Incomplete passes retain restored implicit
  targets and retry on presence reseed and subsequent rounds. Change 4 closes
  unresolved implicit incidents only after a complete pass proves that their
  stable node identity is no longer a desired dependency. Explicitly removed
  configured targets do not require topology discovery.
- It updates the grant-warning state per target and logs only on the
  transition into the unhandled state.

### D9. Dedicated reachability targets and graph updates

A learned parent reuses a configured target only when it resolves to the same
node and its checks are exactly `[tsping]`; lexical name order breaks ties.
Otherwise learning creates a separate implicit `[tsping]` target, even when a
service target already covers that node. Explicit parents must have exactly
one `icmp` or `tsping` check. Service health cannot suppress device alerts.

Implicit targets retain `Implicit: true`, stable node identity, address,
resolved defaults and one reachability check. Their display name starts with
the short hostname; collisions append stable node ID and then a numeric suffix
until unique across all registered targets. Notification persistence retains
that identity and target definition so startup can recreate an open implicit
incident's target before discovery, even when presence is unavailable.

Validate each proposed graph against configured and learned edges together.
A cycle rejects the update, logs its path and retains the previous graph.
Handling-node lookup does not recursively follow suppression parents. Apply
accepted graph changes atomically to alerting. A child moved from a down router
to a healthy one is released immediately, even if neither router changes state.
Down unused implicit targets remain until recovery, except an authoritative
reconciliation may explicitly close a restored unresolved incident as removed.

### D10. Metrics

The classifier's constructor takes a `prometheus.Registerer`. `tsmon.go`
passes `prometheus.DefaultRegisterer`, the registry prober uses. Tests pass a
private `prometheus.NewRegistry()`, which avoids the duplicate-registration
panics that change 1 hit with prober.

- `tsmon_failure_layer_total` is a `CounterVec`.
- `tsmon_path` is a `GaugeVec`. All three kinds are written on every
  `tsping` result.
- `tsmon_target_parent` is a `GaugeVec`. The old label pair is deleted when
  a target's parent changes.

## Deviations from the design doc

- **New layer `unrouted`.** The doc's table has no case for "no peer handles
  the address". That is the ACLs section's missing-grant condition, and
  labeling it `tunnel` or `lan` would send operators to the wrong place.
- **`control` requires known-offline.** The doc says "offline in the
  collector's netmap". While the bus is down, presence is unknown rather
  than offline, so the classifier never blames control because of its own
  broken bus.
- **Tailscale-node targets never get `lan`.** The doc's `lan` row assumes a
  device behind a router. For a node target, disco succeeding while IP
  fails means the host's stack or service, which the doc calls `service`.
- **Implicit parent targets.** The doc implies the router is paged, but
  learned parents may not be configured targets. Implicit targets make "page
  once for the router" work without hand-written config.
- **Learning fallback to routes.** The doc learns only from pings. A router
  that is already down at startup would leave its devices parentless and
  paging individually, so learning falls back to `PrimaryRoutes`.
- **`tsmon_target_parent` metric.** It replaces the doc's `parent` label on
  prober series, as recorded in change 1.
- **Deferred from the doc's table.** The `degraded` layer, and the 3x
  latency baseline in it, are deferred to phase 2, per the doc's Phases
  section.

## Risks / Trade-offs

- **[Risk] A classification blocks the target's result delivery for up to
  one timeout while a supplemental probe runs.** → The delay is bounded by
  the target timeout and shared through singleflight. Other targets are
  unaffected, because the lock is per target.
- **[Risk] Router names change.** → Stable node identity, not its current
  hostname, reconciles restored targets. Keep an open incident's target name
  until it closes; a rename must not create a second incident.
- **[Risk] `PeerChange` grows a field that affects presence, such as
  addresses or routes.** → A test fails on upgrade: it asserts the exact
  `PeerChange` field set this design was written against. The watcher then
  gets a `PeerByID` refresh for that field.
- **[Trade-off] Periodic learning may lag an HA failover.** → Primary-route
  updates trigger reconciliation and invalidate a stale learned router, then
  update the suppression graph atomically.
- **[Risk] `NotifyPeerPatches` semantics change in a future
  `tailscale.com`.** → Presence tests feed recorded-shape `ipn.Notify`
  sequences, and CLAUDE.md already limits upgrades to `tailscale.com`, so
  a behavior change shows up as a test failure.

## Migration Plan

This is additive. It lands after `add-tsmon-collector` and before
`add-alert-state-machine`. Until change 3 lands, `ObserveClassified` has one
consumer, a debug logger. Rollback is a revert with no data to migrate.
