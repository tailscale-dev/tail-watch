# Spec Delta

## Purpose

Determines, for every target, which tailnet node its reachability depends on,
without hand-written parent config, so that a dead subnet router is reported
once and its dependents can be suppressed.

## ADDED Requirements

### Requirement: Tailscale-node and LAN targets

The collector SHALL exclude `network: local` targets from tailnet dependency
learning. Their only possible parent is an explicit config override. For
`network: tailnet`, the collector SHALL treat a target as a Tailscale-node target when its address
resolves to a peer's Tailscale IP, and as a LAN target otherwise. A
Tailscale-node target's handling node SHALL be that peer, and it SHALL have no
learned parent.

#### Scenario: Node target

- **WHEN** target `nyc-subnet-router` has `addr: nyc-router`, a peer name
- **THEN** it is a Tailscale-node target handled by `nyc-router`, with no
  parent

### Requirement: Learned parent

At startup and every 10 minutes, and on presence reseed or primary-route change,
the collector SHALL reconcile dependencies and send one disco ping per remote
LAN target, whether or not the target has a `tsping` check. It SHALL record
the handling node reported by the pong as that target's parent. When the ping
fails, it SHALL use a changed primary route when presence reports one, else
keep the previously learned parent if there is one. Otherwise
it SHALL use the peer that presence reports as primary for the target's
address. When a subnet is served by a high-availability pair, the learned
parent SHALL follow whichever router the pong reports.

#### Scenario: Learn from pong

- **WHEN** a disco ping to `10.20.0.50` returns handling node `100.64.0.7`,
  which is `nyc-router`
- **THEN** target `nyc-nas`'s parent becomes `nyc-router`

#### Scenario: Router already down at startup

- **WHEN** the learning ping to `10.20.0.50` fails at startup and presence
  shows `nyc-router` as primary for `10.20.0.0/16`
- **THEN** `nyc-nas`'s parent is `nyc-router`

#### Scenario: HA failover

- **WHEN** a later learning round's pong for `10.20.0.50` reports
  `nyc-router-b`
- **THEN** the parent changes to `nyc-router-b` and the change is logged at
  info level

### Requirement: Config override

A target's `parent:` config value SHALL take precedence over any learned
parent and SHALL NOT be replaced by learning.

#### Scenario: Device behind a second switch

- **WHEN** target `nyc-cam-3` sets `parent: nyc-core-switch`
- **THEN** its parent is `nyc-core-switch` regardless of learning results

### Requirement: Parent is a target

A learned parent SHALL map only to a configured target whose address resolves
to the handling node and whose checks are exactly `[tsping]`. When more than
one qualifies, the lexically first name SHALL be chosen. Service targets on
that node SHALL NOT qualify. When no configured target qualifies, the collector
SHALL create an implicit target with these properties:

- its name is the node's MagicDNS short name; on collision with any registered
  target, append the stable node ID, then a numeric suffix if still needed
- its stable node ID and Tailscale address are retained for restart reconciliation
- it has one `tsping` check
- it uses the default interval, timeout and thresholds
- its severity is `page`
- its site is the one shared by all its dependents, or empty

The collector SHALL log each implicit target when it is created. Implicit
targets SHALL appear on the status page and in metrics like configured ones.
Creating or removing an implicit target SHALL register or unregister it with
both check scheduling and alerting, so that alerting tracks the same target
set as the checks.

#### Scenario: Router not in config

- **WHEN** `nyc-nas` learns handling node `nyc-router` and no configured target
  addresses `nyc-router`
- **THEN** an implicit target `nyc-router` with a single `tsping` check is
  created, alerting starts tracking it, and `nyc-nas`'s parent is `nyc-router`

#### Scenario: Router has a service target

- **WHEN** the only configured target for `nyc-router` has `[tsping, tcp:443]`
- **THEN** learning creates a separate reachability target with `[tsping]`, and
  only that target can suppress the router's LAN dependents

#### Scenario: Router already configured

- **WHEN** configured target `nyc-subnet-router` has `addr: nyc-router` and
  checks exactly `[tsping]`
- **THEN** `nyc-nas`'s parent is `nyc-subnet-router` and no implicit target is
  created

### Requirement: Implicit target removal

After a learning round in which no target depends on an implicit target, the
collector SHALL remove it from check scheduling and alerting. When alerting
reports that implicit target as down at that moment, the collector SHALL defer
the removal, log that it was deferred, and remove it after the first learning
round in which it has no dependents and is no longer down.

#### Scenario: Unused implicit target

- **WHEN** `nyc-router`'s dependents move to `nyc-router-b` and `nyc-router` is
  up
- **THEN** implicit target `nyc-router` is removed from checks and alerting

#### Scenario: Unused implicit target still down

- **WHEN** a learning round leaves implicit target `nyc-router` with no
  dependents while it is down
- **THEN** it keeps running its check, the deferral is logged, and it is
  removed after the first later round in which it has recovered

### Requirement: Parent lookup

For any target, the collector SHALL report its current parent as the name of a
configured or implicit target. It SHALL report no parent (an empty name) when no explicit override exists and the target is local, is a Tailscale-node
target, or has no learned or route-derived handling node.

#### Scenario: Parent is implicit

- **WHEN** `nyc-nas`'s handling node `nyc-router` is covered only by an
  implicit target
- **THEN** the parent lookup for `nyc-nas` returns `nyc-router`

#### Scenario: No parent known

- **WHEN** `lab-pdu`'s address is not handled by any peer and it has no
  `parent:`
- **THEN** the parent lookup for `lab-pdu` returns an empty name

### Requirement: First learning pass signal

The collector SHALL signal once when the first dependency-learning pass has
finished, whether every learning ping succeeded, failed, or timed out. When
there are no LAN targets, the signal SHALL fire immediately at startup. The
signal SHALL fire at most once per process and SHALL NOT be delayed by later
rounds.

#### Scenario: Restart with implicit routers

- **WHEN** tsmon restarts with LAN targets behind `nyc-router`
- **THEN** the signal fires after the first round has attempted every remote LAN
  target; `nyc-router` exists only if the pass could resolve its handling node

#### Scenario: Learning pings time out

- **WHEN** every first-round learning ping times out
- **THEN** the signal still fires once the round ends

#### Scenario: No LAN targets

- **WHEN** every configured target is a Tailscale-node target
- **THEN** the signal fires immediately at startup

### Requirement: Missing grant report

When a remote LAN target's address is neither answered by a learning ping nor covered
by any peer's routes, the collector SHALL log one warning of the form `target
<name>: no peer handles <addr>; check grants to tag:monitor`. It SHALL NOT
repeat that warning on later rounds while the condition persists. It SHALL log
again only after the address was resolved and then lost.

#### Scenario: Missing grant at startup

- **WHEN** no peer handles `10.99.0.5` for target `lab-pdu`
- **THEN** tsmon logs `target lab-pdu: no peer handles 10.99.0.5; check grants
  to tag:monitor` once and keeps running

#### Scenario: Fixed later

- **WHEN** the grant is added and a later learning round resolves `10.99.0.5`
- **THEN** the target gets its parent and no further warning is logged

### Requirement: Parent metric

The collector SHALL export `tsmon_target_parent{target,parent}` with value 1
for each target's current parent. When a target's parent changes, it SHALL
remove the series for the old parent.

#### Scenario: Parent changes

- **WHEN** `nyc-nas`'s parent changes from `nyc-router` to `nyc-router-b`
- **THEN** `tsmon_target_parent{target="nyc-nas",parent="nyc-router-b"}` is 1
  and the `nyc-router` series is gone

### Requirement: Authoritative topology reconciliation

With no remote LAN targets configured, config alone SHALL establish that no
learned router is required. Otherwise first-pass completion SHALL mean attempts
have finished, not that missing nodes were removed. A separate reconciliation result SHALL identify whether presence
was seeded and stayed connected throughout the pass and every configured remote
LAN target had a handling node resolved. Only such a complete pass SHALL be
used to close unresolved implicit incidents as removed. A failed or partial pass
SHALL retain restored implicit targets and incidents and retry reconciliation
on presence reseed and later learning rounds. Configured targets explicitly
removed from the config SHALL be distinguishable from implicit targets.

#### Scenario: Restart with unavailable presence

- **WHEN** presence stays unseeded and all first-pass learning pings fail
- **THEN** the first-pass signal fires, but restored implicit incidents stay open
  and no `reason: removed` recovery is generated

#### Scenario: Authoritative removal

- **WHEN** a complete reconciliation shows that no configured target depends on
  a restored implicit router that has not been rediscovered
- **THEN** that router's incident may be closed with `reason: removed`

### Requirement: Atomic dependency updates

Before publishing a learned edge, the collector SHALL validate the combined
configured and learned graph for cycles. A cyclic update SHALL be rejected,
logged and leave the previous graph intact. Each accepted update SHALL notify
alerting atomically with its old and new graph; alerting SHALL release targets
whose blocking ancestry disappeared and suppress targets newly blocked by a
down reachability parent. Topology updates SHALL NOT wait for another check.

#### Scenario: Mixed-edge cycle

- **WHEN** configured router `r` has `parent: switch` and learning would set
  `switch`'s parent to `r`
- **THEN** the learned edge is rejected and neither traversal nor handling-node
  resolution recurses through that cycle

#### Scenario: HA parent change

- **WHEN** a suppressed target moves from down router A to up router B
- **THEN** alerting is notified and releases the target without waiting for A
  to recover or B to change state
