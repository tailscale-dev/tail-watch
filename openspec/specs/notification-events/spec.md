# notification-events Specification

## Purpose

Defines the events tail-watch emits when alert state changes, their severities, the
versioned JSON payload that receivers depend on, and how each sink chooses
which events it receives.

## Requirements

### Requirement: Event types

tail-watch SHALL emit exactly these event types in phase 1:

- `target.down` on `UP -> DOWN`, on `UNKNOWN -> DOWN` (a target already
  failing at startup with no open incident), or on `SUPPRESSED -> DOWN` when a
  dependent is still failing after its parent recovers.
- `target.recovered` when a target with an open incident reaches `UP`,
  whatever state it came from.
- `target.flapping` when a target crosses `flap_limit`.
- `collector.started` once, when every configured target/check has produced
  at least one result.
- `test`, sent only by `tail-watch notify-test`.

The type `target.degraded` SHALL be reserved in the payload contract and SHALL
NOT be emitted in phase 1. Transitions into `SUPPRESSED` and `UNKNOWN -> UP`
SHALL NOT emit events.

#### Scenario: Down then recovered

- **WHEN** target `nyc-nas` goes `UP -> DOWN` and later `DOWN -> UP`
- **THEN** tail-watch emits one `target.down` and then one `target.recovered` for
  `nyc-nas`

#### Scenario: Suppressed dependent

- **WHEN** `nyc-router` goes DOWN and its dependent `nyc-nas` moves to
  `SUPPRESSED`
- **THEN** no event is emitted for `nyc-nas`, and the `target.down` for
  `nyc-router` lists `nyc-nas` in `dependents`

#### Scenario: Paged target's parent later fails

- **WHEN** `nyc-nas` is DOWN with an open incident and its parent goes DOWN
- **THEN** `nyc-nas` remains DOWN and emits exactly one recovery when its own
  checks meet the recovery threshold

#### Scenario: Down at startup

- **WHEN** the collector starts, `nyc-nas` has no open incident, and its checks
  fail `fail_threshold` times
- **THEN** tail-watch emits one `target.down` for `nyc-nas`

#### Scenario: Silent startup

- **WHEN** the collector starts and every target goes `UNKNOWN -> UP`
- **THEN** no `target.recovered` events are emitted and one
  `collector.started` event is emitted after the first full round

### Requirement: Event severity

Each event SHALL carry a `severity` of `info`, `warn` or `page`. `target.down`
SHALL use the severity of the target's `route` when it names one, and otherwise the
target's configured `severity` (default `page`).
`target.recovered` SHALL use the severity of the `target.down` it resolves.
`target.flapping` SHALL be `warn`. `collector.started` and `test` SHALL be
`info`.

#### Scenario: Warn-only target

- **WHEN** target `nyc-printer` has `severity: warn` and goes DOWN
- **THEN** its `target.down` event has `"severity": "warn"`

#### Scenario: Severity from the route

- **WHEN** target `lab-camera` has `route: cameras` and `cameras` has `severity: warn`
- **THEN** its `target.down` event has `"severity": "warn"`

### Requirement: Payload contract

Every event SHALL be serialized as one JSON object with `version` (integer,
`1`), `id`, `type`, `severity`, `time` (RFC 3339 UTC) and `collector`
(`name`, `tailscale_ip`). Target events SHALL add `target` (`name`, `addr`,
`site`, `parent`, and `implicit: true` only for implicit targets), `check`,
`layer`, `error`, `since`, `failures`, `path`
(`kind`, `endpoint`, `derp_region`, `latency_ms`, each present only when
known), and `dependents` (always an array on `target.down` and
`target.recovered`). `target.recovered` SHALL add `duration_seconds` (from the
outage's `since` to recovery) and `down_event_id`, and MAY repeat the down's
`check`, `error`, `since`, `failures` and `path`. Fields that do not apply SHALL be omitted, not sent as null.
`layer` SHALL be one of `unrouted`, `control`, `tunnel`, `lan` or `service`
on `target.down` and `target.flapping`, and SHALL be omitted on all other
events. Payload changes within `version: 1` SHALL only add fields.

#### Scenario: Down payload

- **WHEN** `nyc-nas` goes DOWN on `tcp:445` at layer `service` over a direct
  path
- **THEN** the payload has `"type": "target.down"`, `"check": "tcp:445"`,
  `"layer": "service"`, `"path": {"kind": "direct", ...}`, `"failures": 3` and
  `"dependents": []`

#### Scenario: Recovered references its down

- **WHEN** `nyc-nas` recovers 12 minutes after a down event with id
  `evt_01JAB3K9T6M4X2Q8R7N5W0YZCD`
- **THEN** the recovered payload has
  `"down_event_id": "evt_01JAB3K9T6M4X2Q8R7N5W0YZCD"` and
  `"duration_seconds": 720`

#### Scenario: Unrouted address

- **WHEN** a target goes DOWN because no peer handles its address
- **THEN** the payload has `"layer": "unrouted"`

#### Scenario: Implicit router

- **WHEN** the learned router `nyc-router`, which is not in the config, goes
  DOWN
- **THEN** the payload's `target` object has `"name": "nyc-router"` and
  `"implicit": true`

#### Scenario: No layer on recovery

- **WHEN** a `target.recovered` is emitted
- **THEN** the payload has no `layer` field

### Requirement: Event identifiers

Each event SHALL have an `id` of the form `evt_` followed by 26 Crockford
base32 characters whose leading characters encode the creation time in
milliseconds, so ids sort by creation time. The `id` SHALL stay the same across
every delivery attempt and every sink.

#### Scenario: Retries keep the id

- **WHEN** a webhook delivery of event `evt_X` is retried twice
- **THEN** all three requests carry `id` `evt_X`

#### Scenario: Ids sort by time

- **WHEN** event A is created 1 ms before event B
- **THEN** A's id sorts lexically before B's

### Requirement: Collector identity

The `collector.name` field SHALL be the collector node's Tailscale hostname,
and `collector.tailscale_ip` its first Tailscale IPv4 address (or IPv6 when it
has no IPv4), as reported by tailscaled at startup.

#### Scenario: Collector fields

- **WHEN** the collector node is `monitor-nyc` with IP `100.101.102.103`
- **THEN** every payload has
  `"collector": {"name": "monitor-nyc", "tailscale_ip": "100.101.102.103"}`

### Requirement: Sink routing filters

Each sink SHALL accept `events` (list of event types; default all),
`min_severity` (`info`, `warn` or `page`, ordered `info < warn < page`; default
`info`), and `match` with optional `sites`, `targets` (shell-style globs) and
`tags`. An event SHALL reach a sink only when the sink is a candidate for it (see
`notification-routes`: the route's sinks for a routed target, every sink otherwise),
its type is listed, its severity is at least `min_severity`, and it satisfies every
`match` key given (any listed value may match). `tags` SHALL match the tags of the
Tailscale node the target resolves to; a target with no node tags never matches a
`tags` filter. Events without a target (`collector.started`) SHALL ignore `match` and
routes. `test` events SHALL bypass all filters and routes.

#### Scenario: Severity floor

- **WHEN** a sink has `min_severity: page` and a `target.flapping` (`warn`)
  event is emitted
- **THEN** that sink does not receive it

#### Scenario: Site match

- **WHEN** a sink has `match: {sites: [nyc, sfo]}` and a target in site `lon`
  goes DOWN
- **THEN** that sink does not receive the event

#### Scenario: Target glob

- **WHEN** a sink has `match: {targets: ["nyc-*"]}` and `nyc-nas` goes DOWN
- **THEN** that sink receives the event

#### Scenario: Sink outside the route

- **WHEN** a sink accepts every event but is not in the route of target `pg-primary`
- **THEN** that sink does not receive `pg-primary`'s `target.down`

#### Scenario: Collector start ignores routes

- **WHEN** routes are defined and the collector emits `collector.started`
- **THEN** every sink whose `events` and `min_severity` accept it receives it

### Requirement: Recoveries follow their pages

A recovery SHALL bypass current routing filters for sinks selected for its
matching down, subject to the per-sink delivery-state rules in
`notification-delivery`. It SHALL NOT go to sinks filtered out of the down or
whose down was dropped before any attempt. If an attempted down's acknowledgment
is uncertain, recovery SHALL still be eligible; receivers MUST tolerate resolves
for unknown down IDs. Pending downs SHALL retain recovery intents until delivery
or dropping determines eligibility, except that an unattempted down and its
recovery may share one ordered batch/group with atomic attempt state.

#### Scenario: Filter would drop the recovery

- **WHEN** a sink with `events: [target.down]` acknowledged a down for `nyc-nas`
- **THEN** it also receives attempts for the matching recovery

#### Scenario: Sink skipped the down

- **WHEN** a sink filtered out the down because of `match.sites`
- **THEN** it receives no matching recovery
