# Spec Delta

## MODIFIED Requirements

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
