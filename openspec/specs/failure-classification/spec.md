# failure-classification Specification

## Purpose

Assigns every failed check result a failure layer (control, tunnel, LAN,
service, or unrouted) using the tailnet's own view, so alerts can say why a
target is unreachable and which failure is the root cause.

## Requirements

### Requirement: Classified results

Every check result SHALL pass through classification before reaching alerting.
A passing result SHALL carry an empty layer. A failing result SHALL carry
exactly one of the layers `control`, `tunnel`, `lan`, `service` or `unrouted`.
Each classified result SHALL also carry the target's current parent (a target
name, or empty) and, when known, path details: kind (`direct`, `derp` or
`peer_relay`), endpoint or DERP region, and latency. Per-target delivery order
SHALL be preserved.

#### Scenario: Passing result

- **WHEN** a `tcp:445` check on `nyc-nas` passes
- **THEN** the classified result has an empty layer and parent `nyc-router`

### Requirement: Layer decision order

For a `network: local` failing result, the classifier SHALL use device evidence
to choose `service` or `lan` (steps 4 and 5 below), without consulting tailnet
presence, routes or disco probes.

For a `network: tailnet` failing result, the classifier SHALL decide the layer from the bottom up
and stop at the first match:

1. `unrouted`: the target's handling node is unknown: there is no learned
   handler and no peer owns or routes the address. An explicit suppression
   parent does not prove a tailnet handler exists.
2. `control`: presence reports the handling node as known and offline.
3. `tunnel`: a disco ping to the handling node fails.
4. `service`: the device demonstrably answered. Either the failure is a TCP
   refusal, an HTTP status or body mismatch, or a TLS error, or an ICMP echo to
   the device succeeds.
5. `lan`: the tunnel to the handling node works but the device does not answer.

For a Tailscale-node target, the classifier SHALL use `service` where it would
otherwise use `lan`, because there is no LAN behind it.

#### Scenario: Router offline in control

- **WHEN** `nyc-nas`'s `icmp` check fails and presence reports `nyc-router`
  known and offline
- **THEN** the layer is `control`

#### Scenario: Router online but no WireGuard path

- **WHEN** `nyc-nas`'s `icmp` check fails, `nyc-router` is online, and a disco
  ping to `nyc-router` fails
- **THEN** the layer is `tunnel`

#### Scenario: Device off the LAN

- **WHEN** `nyc-core-switch`'s `icmp` check fails and a disco ping to
  `nyc-router` succeeds
- **THEN** the layer is `lan`

#### Scenario: Port refused

- **WHEN** `nyc-nas`'s `tcp:445` check fails with "connection refused" and the
  tunnel to `nyc-router` works
- **THEN** the layer is `service` and no ICMP probe is sent

#### Scenario: Port times out but host pings

- **WHEN** `nyc-nas`'s `tcp:445` check times out, the tunnel works, and an ICMP
  echo to `10.20.0.50` succeeds
- **THEN** the layer is `service`

#### Scenario: Tailscale node service down

- **WHEN** target `build-1` (a Tailscale node) fails `http:` and a disco ping to
  `build-1` succeeds
- **THEN** the layer is `service`

#### Scenario: Presence unknown

- **WHEN** the IPN bus is disconnected and `nyc-nas`'s `icmp` check fails
- **THEN** the classifier does not choose `control` and continues with the
  disco ping to decide between `tunnel` and `lan`

#### Scenario: Missing handling node

- **WHEN** `lab-pdu`'s address is not handled by any peer and its `icmp` check
  fails
- **THEN** the layer is `unrouted`

### Requirement: Failed tsping checks

When the failing check is itself `tsping`, the classifier SHALL treat that
result as the disco ping to the handling node. It SHALL choose `control` when
presence reports the node offline, and `tunnel` otherwise, without sending
another ping.

#### Scenario: Router tsping fails while online

- **WHEN** `nyc-subnet-router`'s `tsping` check fails and presence reports
  `nyc-router` online
- **THEN** the layer is `tunnel`

### Requirement: Supplemental probes

Supplemental disco pings and ICMP echoes SHALL go through the collector's
network access and the local tailscaled, like regular checks. They SHALL use
the timeout of the target being classified. Results SHALL be shared through a
per-key cache:

- a disco-ping result per handling node
- an ICMP result per target address

A cached result SHALL be reused for 10 seconds or the triggering target's
interval, whichever is shorter. At most one supplemental probe per key SHALL be
in flight at a time, and concurrent callers SHALL wait for and share its
result. A fresh passing result from the target's own `icmp` or `tsping` check
SHALL be used in place of a supplemental probe.

#### Scenario: Forty devices behind one router

- **WHEN** 40 targets behind `nyc-router` fail within the same second
- **THEN** exactly one disco ping to `nyc-router` is sent, and all 40 results
  use its outcome

#### Scenario: Supplemental probes are not checks

- **WHEN** a supplemental probe runs
- **THEN** it does not appear as a check on the status page and does not
  change the prober result metrics

### Requirement: Path reporting

For every `tsping` result, whether from a configured check or a supplemental
probe of that target, the classifier SHALL record the path. It SHALL export
`tailwatch_path{target,kind}` with value 1 for the current kind and 0 for the other
kinds (`direct`, `derp`, `peer_relay`). A path change SHALL NOT make a
result fail.

#### Scenario: Fallback to DERP

- **WHEN** `nyc-subnet-router`'s `tsping` succeeds via DERP region `nyc` after
  previously being direct
- **THEN** `tailwatch_path{target="nyc-subnet-router",kind="derp"}` is 1, the
  `direct` series is 0, and the result still passes

### Requirement: Layer metric

The classifier SHALL increment `tailwatch_failure_layer_total{target,layer}` once
for every failing result, using the assigned layer.

#### Scenario: Count by layer

- **WHEN** `nyc-nas` fails three times with layer `service`
- **THEN** `tailwatch_failure_layer_total{target="nyc-nas",layer="service"}`
  increases by 3
