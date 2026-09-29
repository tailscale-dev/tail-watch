# dependency-suppression Specification

## Purpose

Makes a failed parent (usually a subnet router) produce one alert that names
the targets behind it, instead of one alert per dependent, and resolves those
dependents promptly when the parent comes back.

## Requirements

### Requirement: Parent resolution

A target's parent SHALL be the configured `parent` override when set, and
otherwise the target, configured or implicit, that
`add-failure-classification` learned handles the target's address. The parent
SHALL be read from the latest atomically installed graph on every result; graph
changes SHALL also be processed immediately without a result. A target with no known parent SHALL
never be suppressed or held.

#### Scenario: Learned parent

- **WHEN** `nyc-core-switch` has no `parent` override and classification maps
  it to configured target `nyc-subnet-router`
- **THEN** `nyc-subnet-router` is its parent for suppression

#### Scenario: Implicit parent

- **WHEN** a target's handling node is not in the config and classification
  maps it to the implicit target `nyc-router`
- **THEN** `nyc-router` is its parent for suppression

#### Scenario: No known parent

- **WHEN** classification knows no parent for a target
- **THEN** that target goes `DOWN` normally on reaching `fail_threshold`

### Requirement: Suppression when the parent is down

A dependency parent SHALL represent reachability: learned router parents have
only `[tsping]`, and explicit parents have only `[icmp]` or `[tsping]`. A
service target SHALL NOT be used as a suppression parent. A parent counts as
down when it is `DOWN` or `SUPPRESSED`. When a parent goes
down, every target that has it as a direct or transitive parent and is in
`UNKNOWN` or `UP` SHALL move to `SUPPRESSED` at the same time. A dependent that
is already `DOWN` SHALL stay `DOWN`. While a target has a down parent, any
move that would take it to `DOWN` SHALL instead take it to `SUPPRESSED`.
Moves into `SUPPRESSED` SHALL emit no transition. Releases to `UP` SHALL be
silent unless a recovery is owed for a previously emitted `DOWN`, as below.
A released target still failing SHALL emit `SUPPRESSED -> DOWN` unless its
original incident remains open.
Checks on suppressed targets SHALL keep running.

#### Scenario: Router dies

- **WHEN** `nyc-subnet-router` goes `DOWN` and 14 `UP` targets list it as
  parent
- **THEN** all 14 become `SUPPRESSED`, no transition is emitted for any of
  them, and the router's `UP -> DOWN` transition lists all 14 names

#### Scenario: Already down dependent

- **WHEN** `nyc-printer` is already `DOWN` and its parent then goes `DOWN`
- **THEN** `nyc-printer` stays `DOWN` and is not listed as a dependent

### Requirement: Grace hold for parent-caused failures

When a target with a known parent reaches `fail_threshold` with a
tripping failure in layer `control` or `tunnel`, and the parent is not yet
down, the target SHALL NOT go `DOWN` immediately. It SHALL wait up to its
parent's `interval × fail_threshold + timeout`. If the parent goes down within
that time, the target SHALL become `SUPPRESSED` and be listed on the parent's
transition. Otherwise the target SHALL go `DOWN` when the wait ends, provided
its tripping check is still failing. Failures in layer `unrouted`, `lan` or
`service` SHALL NOT be held.

#### Scenario: Device check trips before the router's

- **WHEN** `nyc-core-switch` reaches `fail_threshold` with layer `control` at
  14:03:00, and `nyc-subnet-router` (interval 15s, fail_threshold 3, timeout
  5s) goes `DOWN` at 14:03:30
- **THEN** no transition is emitted for `nyc-core-switch`, and the router's
  transition lists it as a dependent

#### Scenario: Parent never goes down

- **WHEN** a held target's parent is still `UP` 50s later and the target's
  tripping check is still failing
- **THEN** the target goes `DOWN` and emits a transition with the original
  layer

#### Scenario: LAN failure is not held

- **WHEN** a target reaches `fail_threshold` with layer `lan`
- **THEN** it goes `DOWN` immediately

#### Scenario: Unrouted failure is not held

- **WHEN** a target reaches `fail_threshold` with layer `unrouted`
- **THEN** it goes `DOWN` immediately, because no node handles its address

### Requirement: Release when the parent recovers

When a parent stops being down, is removed, or a dependency update leaves a
suppressed target with no down ancestor, the collector SHALL enqueue all checks
of each newly released target within one second, without waiting for the schedule.
Execution SHALL use a FIFO queue with at most eight in-flight rechecks, each
bounded by its target timeout; starting every check within one second is not
required. The collector SHALL release only targets with no remaining down
ancestor, and SHALL reset their counters first. A released dependent SHALL move to `UP`
without a transition once every one of its checks has one success, except for
an outstanding recovery described below. It SHALL move to `DOWN` when any check
reaches `fail_threshold` consecutive failures counted from the release, emitting
`SUPPRESSED -> DOWN` unless the original incident remains open. The
parent's recovery transition SHALL list the dependents that were `SUPPRESSED`
when it recovered.

If flap damping left an emitted `DOWN` unresolved before suppression, release
SHALL retain that incident's trip details and `DownSince`. Returning to `UP`
SHALL emit its `DOWN -> UP` recovery once flap damping permits emission; if
the mark is still set, the target SHALL first remain `UP` for `flap_window`.
Continued failures SHALL retain the original incident without another `DOWN`
transition. A recovery lost to FIFO overflow SHALL remain available through
the dirty snapshot until acknowledged.

#### Scenario: Router comes back

- **WHEN** `nyc-subnet-router` recovers with 14 suppressed dependents
- **THEN** each dependent's checks are enqueued within one second, the router's
  `DOWN -> UP` transition lists all 14 names, and each dependent that passes
  becomes `UP` silently

#### Scenario: Device still dead after router recovery

- **WHEN** after the router recovers, `nyc-nas`'s `tcp:445` fails on the
  immediate re-check and on the next two scheduled runs, with
  `fail_threshold: 3`
- **THEN** a `SUPPRESSED -> DOWN` transition is emitted for `nyc-nas`

#### Scenario: Failover releases an independently failed child

- **WHEN** learning moves suppressed `nyc-nas` from down router A to up router B,
  and its TCP service continues failing
- **THEN** it is released immediately and emits `SUPPRESSED -> DOWN` after
  `fail_threshold` new failures, even if A never recovers

#### Scenario: Router management service fails

- **WHEN** a router's separate HTTP service target goes DOWN but its `[tsping]`
  reachability target stays UP
- **THEN** its LAN devices remain independently alertable

#### Scenario: Release preserves a damped recovery

- **WHEN** a child with an unresolved emitted `DOWN` is suppressed, its parent
  recovers, and each child check succeeds after release
- **THEN** exactly one `DOWN -> UP` recovery retains the original trip and
  `DownSince`, after a full stable window if still flapping or immediately if
  the flap mark already cleared while suppressed

#### Scenario: Continued failure preserves the original incident

- **WHEN** a child with an unresolved emitted `DOWN` is released and reaches
  `fail_threshold` with fresh failures before returning to `UP`
- **THEN** no additional `DOWN` is emitted, and its eventual recovery resolves
  the original incident with the original trip and `DownSince`
