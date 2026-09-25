# Proposal

## Why

A plain poller only knows that a target is down. Operators need to know why:
the subnet router lost control, the WireGuard path broke, the device fell off
the LAN, or only the service stopped. They also need one page for a dead
router, not one per device behind it. See `tailnet-reachability-monitor-design.md`,
sections "What a failed check means", "Dependency learning" and "Presence
watcher". `add-tsmon-collector` produces raw check results. This change turns
them into classified results that `add-alert-state-machine` can act on.

This is change 2 of 4 in phase 1 (MVP).

## What Changes

- Add a presence watcher that keeps a live, `NodeID`-keyed view of peer
  online state, Tailscale IPs, primary routes and tags from the tailscaled IPN
  bus. It reconnects with backoff. Presence never raises an alert on its own.
- Add a classifier that sorts every failed check result into one layer:
  `control`, `tunnel`, `lan`, `service` or `unrouted`. It runs cached,
  rate-limited supplemental probes (a disco ping to the handling node, an ICMP
  echo to the device) when a result alone cannot decide the layer.
- Learn dependencies for remote LAN targets at startup, every ten minutes
  and on topology updates. Reuse only dedicated `[tsping]` router targets;
  otherwise create an implicit reachability target with stable node identity.
  Local targets have no learned router parent. Validate configured and learned
  edges together and notify alerting atomically on changes.
- Separate attempt completion from authoritative reconciliation, retaining
  unresolved implicit incidents through failed discovery.
- At startup, log once when no peer handles a target's address, naming the
  address and pointing at grants to `tag:monitor`.
- Add metrics: `tsmon_failure_layer_total{target,layer}`,
  `tsmon_path{target,kind}` and `tsmon_target_parent{target,parent}`.

Non-goals:

- Phase 2: the `degraded` layer, latency baselines and `target.degraded`
  warnings. Tag-based discovery (`discover:`) is also phase 2.
- Phase 3: quorum across collectors, devices-API discovery.
- Alert states, suppression and notifications belong to
  `add-alert-state-machine` and `add-notifications`. This change only labels
  results.

## Capabilities

### New Capabilities

- `peer-presence`: A live view of tailnet peers from the IPN bus. Covers
  online state, addresses, routes and tags, and behavior while the bus is
  disconnected.
- `failure-classification`: How each check result is assigned a failure
  layer. Covers the supplemental probes, path reporting and the related
  metrics.
- `dependency-learning`: How each target's parent is determined. Covers
  config override, learned handling node, route lookup, implicit parent
  targets, re-learning and the missing-grant log.

### Modified Capabilities

None. The `reachability-checks` and `collector-runtime` capabilities from
`add-tsmon-collector` keep their requirements. This change only consumes
their results.

## Impact

- **Code**: new `cmd/tsmon/presence.go` and `cmd/tsmon/classify.go`, which
  includes dependency learning. `tsmon.go` wiring inserts the classifier
  between check results and downstream consumers. The `tsping` name
  resolution in `checks.go` switches its 30s `Status()` cache to the
  presence view.
- **tailscaled load**: one long-lived `WatchIPNBus` connection, plus disco
  pings for learning (one per LAN target every 10m) and cached
  supplemental probes on failure.
- **Metrics**: three new series in the `tsmon` namespace.
- **Dependencies**: none new. It uses `tailscale.com/ipn`, `ipn/ipnstate`,
  `tailcfg`, `client/local` and `util/backoff`.
