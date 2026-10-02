# Failure layers

A monitoring alert that says only "nyc-nas is down" leaves the hard part to
you. On a tailnet with subnet routers, a device can be unreachable because
the control plane thinks its router is offline, because the WireGuard tunnel
to the router is broken, because the LAN behind the router lost the device,
or because the device is fine and one service on it is not. Each cause has a
different owner and a different fix. tail-watch labels every failed check
with one of five layers so the alert starts the diagnosis instead of
postponing it.

This page explains how the label is chosen, why it is chosen in that order,
and where it can mislead you. The label values and metric are listed in the
[metrics reference](../reference/metrics.md) and the
[event payload reference](../reference/payload.md).

## The five layers

A passing check has no layer. A failing check has exactly one, and the value
appears as the `layer` label on `tailwatch_failure_layer_total` and as the
`layer` field on `target.down` and `target.flapping` events.

| Layer | What it says |
| --- | --- |
| `unrouted` | No peer owns or routes the address. |
| `control` | Tailscale's control plane reports the handling node offline. |
| `tunnel` | The handling node looks online, but a disco ping to it fails. |
| `service` | The device demonstrably answered, or the target is a node. |
| `lan` | The tunnel works, but the device behind the node does not answer. |

The "handling node" is the tailnet node that carries traffic for the target.
For a target that is itself a Tailscale node, that is the node. For a device
on a subnet, it is the subnet router that advertises the route. Nothing else
in the model makes sense without this idea: tail-watch never reasons about the
device directly until it has established that the path to the handling node is
sound.

## Deciding from the bottom up

The classifier walks the stack from the lowest layer to the highest and stops
at the first layer that explains the failure. The order matters, because a
failure low in the stack makes every higher check fail too. If the router is
offline, the device's TCP port also refuses to connect, and reporting that as
a service problem would send you to the wrong team.

1. If no peer owns the address or advertises a route that covers it, nothing
   could ever have carried the traffic. That is `unrouted`. It usually points
   at a missing route approval or a missing grant rather than at an outage,
   which is why tail-watch also logs a hint about grants to `tag:monitor` the
   first time it notices (see the
   [ACL how-to](../how-to/acls.md)).
2. If the handling node exists and the control plane says it is offline, that
   is `control`. The claim is cheap to check and it is authoritative about
   what Tailscale believes, so it comes before any active probe.
3. If the node is not known to be offline, tail-watch sends a disco ping to
   it. A failed ping is `tunnel`: the coordination server thinks the node is
   fine, but no WireGuard path works. Typical causes are a firewall that
   blocks UDP, a node whose tailscaled has hung, or a path that flaps.
4. If the tunnel works, the failure is on the far side of the handling node.
   When the device gave evidence that it is alive, the layer is `service`. A
   refused TCP connection, an HTTP status or body mismatch, and a TLS error
   are all answers, just not the answer the check wanted. A timed-out check
   is ambiguous, so tail-watch also sends one ICMP echo to the device. If
   the echo succeeds, the host is up and the layer is `service`.
5. If the tunnel works and the device gives no sign of life, the layer is
   `lan`. The path into the site is fine and the device is not on it.

A target that is itself a Tailscale node never gets `lan`, because there is no
LAN behind it. Where the walk would otherwise say `lan`, it says `service`.

### Failed `tsping` checks

A `tsping` check is a disco ping, which is the same probe the classifier would
send in step 3. Rather than probing twice, tail-watch treats the failed check
as that probe. The result is `control` when presence shows the node offline
and `tunnel` otherwise.

### Local targets

A target with `network: local` is not reached through the tailnet, so there is
no handling node to consult. Presence, routes and disco do not apply, and the
only layers available are `service` (the device answered in some way) and
`lan` (it did not).

Local checks can also run where there is no tailscaled at all. With
`collector.local_only`, tail-watch skips the LocalAPI and the Kubernetes
ProxyGroup sidecar uses this to probe workload Services from the proxy pod's
network namespace. Those failures are always `service` or `lan` — the pod has
no tailnet layers to blame — which is exactly the claim the sidecar makes:
the path from proxy to workload is broken. See
[Watch a Kubernetes ProxyGroup](../how-to/kubernetes-proxygroup.md).

## What the classifier knows, and what it does not

The classifier uses the tailnet's own view of itself. That view comes from
the local tailscaled over its IPN bus, which reports which peers exist,
whether the control plane counts them online, and which routes they serve.
tail-watch keeps a live copy of it.

The copy has one property that shapes the layers: it distinguishes "known
offline" from "unknown". When the bus connection is down, or has not yet
delivered its first snapshot, every peer is unknown. The classifier does not
guess `control` from missing data. It carries on to the disco ping and
decides between `tunnel` and the layers above it. So a tailscaled restart
degrades precision (a genuinely offline router shows up as `tunnel` for a
short time) but never invents a control-plane claim.

Presence never raises an alert by itself. A router flipping offline while
every check still passes produces no failure and no layer. The layer only
describes a check that has already failed.

## Learning what a device depends on

`unrouted`, `control` and `tunnel` all depend on knowing the handling node,
and you should not have to declare it by hand for every device. For each
remote LAN target, tail-watch sends one disco ping at startup, every 10
minutes, and when primary routes change. The pong names the node that
actually carried it. That node becomes the target's parent for classification
and for suppression (see [Alert state](alert-state.md)).

The design choices behind that:

- **The pong is authoritative, routes are the fallback.** In a
  high-availability pair, the route table names the primary, but the node that
  answers is the one that matters. The learned parent follows whichever router
  the pong reports, so a failover changes the parent without configuration.
  When the ping fails, a changed primary route is used, and otherwise the
  previously learned node is kept, so a router that is down at the moment of
  learning is still the parent you would want to blame.
- **A parent must be a reachability target.** A learned parent maps only to a
  configured target whose sole check is `tsping`. A router that also runs a
  monitored web interface has a `tsping` target and a separate service target,
  and only the first can suppress devices. Otherwise a broken admin page on a
  router would silence every device behind it. When no such target exists,
  tail-watch creates an implicit `tsping` target named after the node and
  removes it when nothing depends on it, although an implicit target that is
  still down stays until it recovers, so its incident is not orphaned.
- **A configured `parent` always wins.** It is for suppression only. It does
  not change which node the classifier treats as the handler, because the
  handler is a fact about routing and the parent is a statement about blame.
  A device behind a second switch can name that switch as its parent without
  pretending that the switch carries its tailnet traffic.
- **Cycles are rejected.** A learned edge that would close a loop with the
  configured graph is refused and the previous graph stays.

If no peer handles an address at all, tail-watch logs the missing grant once,
and logs again only if the address resolves and is later lost, so a permanent
misconfiguration does not fill the log.

## Path kind is not a layer

For every `tsping` result, tail-watch also records how the pong arrived:
`direct`, `derp`, or `peer_relay`, on `tailwatch_path{target,kind}`. A path
change never fails a check and never changes a layer. A node that falls back
to DERP is slower, not broken, and an alert would be noise. The metric exists
so that you can graph the slowdown and correlate it with a later `tunnel`
failure.

## Cost of supplemental probes

Step 3 and step 4 can need an extra disco ping or ICMP echo. When forty
devices behind one router fail in the same second, sending forty pings
to the same router would add load exactly when the network is in trouble and
would tell you nothing new. Supplemental results are cached per handling node
(for disco) and per address (for ICMP), reused for 10 seconds or the target's
interval, whichever is shorter, and at most one probe per key is in flight;
concurrent callers wait for it and share the result. A fresh passing result
from the target's own `icmp` or `tsping` check replaces a supplemental probe.
These probes are not checks, so they do not appear on the status page or in
the prober result metrics.

## Limits of the labels

- The layer is a best explanation from the vantage point of one collector. A
  `tunnel` verdict means that this collector cannot reach the node, not that
  nobody can. That is one reason to run more than one collector (see the
  quorum rule in [Alert state](alert-state.md)).
- `service` means "something answered", not "the service is healthy". An ICMP
  reply with a timed-out TCP check is `service`, on the reasoning that a
  live host with a dead port is a service problem for the owner of the
  service.
- `lan` is a conclusion by elimination. tail-watch cannot see the LAN. It
  sees that the tunnel works and the device is silent, and that combination
  is the definition.
- The layer on an alert is the layer of the check that tripped the threshold.
  A later failure of a different check does not rewrite it.

## Related pages

- [Checks reference](../reference/checks.md) for what each check tests.
- [Alert state](alert-state.md) for how layers decide whether an alert is held
  back behind a parent.
- [Troubleshooting](../how-to/troubleshoot.md) for what to do about each
  layer.
