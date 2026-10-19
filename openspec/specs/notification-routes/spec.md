# notification-routes Specification

## Purpose

Lets an operator name where a monitor's alerts go: a route pairs a severity with a list
of sinks, monitors pick a route, and the collector can show, before saving, which sinks
each of a monitor's events would reach and why any sink would drop it.

## Requirements

### Requirement: Route definition

The config SHALL accept a top-level `routes` list. Each route SHALL have a `name`
matching `[A-Za-z0-9._-]+` and a non-empty `sinks` list naming sinks defined in `sinks`,
and MAY set `severity` (`page` or `warn`, default `page`) and `mention` (a string). The
config loader SHALL reject unknown route keys, duplicate route names, an empty `sinks`
list, a sink name listed twice in one route, and a sink name that no sink defines,
reporting each problem with its location (for example `routes[1].sinks[0]`) in the same
error as every other config problem.

#### Scenario: Valid route

- **WHEN** the config defines sinks `pagerduty` and `ops-slack` and a route
  `{name: db-team, severity: page, sinks: [pagerduty, ops-slack]}`
- **THEN** the config loads and `db-team` sends page-severity events to both sinks

#### Scenario: Unknown sink

- **WHEN** a route lists `sinks: [pagerdutty]` and no sink has that name
- **THEN** loading fails with an error at `routes[0].sinks[0]` naming `pagerdutty`

#### Scenario: Default severity

- **WHEN** a route omits `severity`
- **THEN** its severity is `page`

### Requirement: Route resolution

When a target names a `route`, its `target.down` events SHALL take the route's
`severity`, and the route's `sinks` SHALL be the only candidate sinks for its
`target.down` and `target.flapping` events. When a target names no route, every sink
SHALL be a candidate, as before routes existed. A candidate sink SHALL still apply its
own `events`, `min_severity` and `match` filters, so an event reaches a sink only when the
sink is a candidate and its filters accept the event. A route SHALL be resolved when the
event is created, so editing a route affects later events and never an event already
created. Events without a target (`collector.started`, `test`) SHALL NOT be routed.

#### Scenario: Route narrows the sinks

- **WHEN** sinks `pagerduty`, `ops-slack` and `db-hook` accept every event and target
  `pg-primary` has `route: db-team` whose sinks are `[db-hook]`
- **THEN** a `target.down` for `pg-primary` reaches only `db-hook`

#### Scenario: Sink filter still applies

- **WHEN** route `db-team` lists `pagerduty`, `pagerduty` has `match: {sites: [nyc]}`,
  and `pg-primary` in site `lab` goes DOWN
- **THEN** `pagerduty` does not receive the event

#### Scenario: Unrouted target

- **WHEN** a target sets no `route`
- **THEN** its events are offered to every sink, filtered exactly as before routes
  existed

#### Scenario: Flapping follows the route

- **WHEN** a target with `route: db-team` crosses `flap_limit`
- **THEN** the `target.flapping` event, at severity `warn`, is offered only to the
  route's sinks

### Requirement: References between targets and routes

A target SHALL NOT set both `route` and `severity`. A YAML target's `route` SHALL name a
route from the YAML file. A dynamic monitor's `route` MAY name a YAML route or a UI route.
A `route` naming no known route SHALL be a validation error at the target's `route`
field.

#### Scenario: Both keys set

- **WHEN** a target sets `route: db-team` and `severity: warn`
- **THEN** loading fails with an error at that target's `route` saying the two keys are
  mutually exclusive

#### Scenario: YAML target names a UI route

- **WHEN** a YAML target names a route that exists only in `routes.json`
- **THEN** loading fails with an error naming the route

### Requirement: UI route store

When the UI is on, the collector SHALL persist routes created through the API in
`routes.json` inside `collector.state_dir`, using the same field names as a YAML route.
The file SHALL be replaced atomically on every change and SHALL NOT be readable by group
or others. At startup UI routes SHALL be loaded after the YAML file and before
`targets.json`, so dynamic monitors can name them. A missing file SHALL mean no UI
routes. A file that cannot be parsed SHALL stop startup with an error naming the file. An
entry whose name matches a YAML route, or that fails validation for any reason other than
a missing sink, SHALL be skipped with a logged warning and left in the file. An entry
naming a sink the config no longer defines SHALL still load: the missing sink SHALL be
ignored for routing, logged as a warning at startup, and reported in the route's
`missing_sinks`, so the monitors that use it keep running. When the UI is off, the
collector SHALL NOT read or modify `routes.json` and SHALL log a warning if it exists.

#### Scenario: UI route survives a restart

- **WHEN** route `db-team` is created through the API and the collector restarts
- **THEN** `db-team` exists again and dynamic monitors naming it load

#### Scenario: Sink removed from the config

- **WHEN** UI route `db-team` lists `db-hook` and `ops-slack`, and `db-hook` is removed
  from the config before a restart
- **THEN** `db-team` loads, routes to `ops-slack` only, a startup warning names
  `db-hook`, and `GET /api/v1/routes` reports `missing_sinks: ["db-hook"]` for it

#### Scenario: Name clash with a YAML route

- **WHEN** `routes.json` holds `db-team` and the YAML file also defines `db-team`
- **THEN** the YAML route is used and a warning names the skipped entry

### Requirement: Route API

When the UI is on, the collector SHALL serve `GET /api/v1/routes` returning every route,
YAML and UI; `POST /api/v1/routes` creating a UI route; `PUT /api/v1/routes/{name}`
replacing one; and `DELETE /api/v1/routes/{name}` removing one. Each entry SHALL carry
`name`, `severity`, `sinks`, `mention` when set, `source` (`config` or `ui`), `readonly`
(true for `config`), `used_by` (names of monitors that name the route), and
`missing_sinks` (always a list). Status codes, body limits, unknown-field handling, 422
error lists, and the 409 for changing a YAML entry SHALL follow the monitor API. A name
that already exists SHALL be refused on create with 409. A delete SHALL be refused with
409, listing `used_by`, while any monitor names the route. A change SHALL be persisted
before it applies, and SHALL apply to events created after it without a restart.

#### Scenario: Create and use a route

- **WHEN** `POST /api/v1/routes` creates `db-team` and a monitor is then created with
  `route: db-team`
- **THEN** both return 201 and the monitor's next `target.down` uses `db-team`

#### Scenario: Delete a route in use

- **WHEN** monitor `pg-primary` names `db-team` and `DELETE /api/v1/routes/db-team` is
  sent
- **THEN** the response is 409 naming `pg-primary` and the route is unchanged

#### Scenario: Edit during an open incident

- **WHEN** `pg-primary` is DOWN, its down went to `db-hook`, and `db-team` is replaced
  to list only `ops-slack`
- **THEN** the recovery for `pg-primary` still goes to `db-hook`, and its next down goes
  to `ops-slack`

### Requirement: Sink list

When the UI is on, the collector SHALL serve `GET /api/v1/sinks` returning every
configured sink with `name`, `type`, `events`, `min_severity`, `match`, and for Slack
sinks `mention_on_page` when set. The response SHALL NOT contain any URL, secret,
header value, or the name of the environment variable or file that holds one. Sinks
SHALL NOT be created, changed or deleted through the API.

#### Scenario: Secrets stay out

- **WHEN** a webhook sink has `url_env: OPS_WEBHOOK_URL` and `secret_env`
- **THEN** its entry has its name, type and filters, and neither the URL, the secret,
  nor the variable names

### Requirement: Routing preview

When the UI is on, the collector SHALL serve `POST /api/v1/route-preview`, accepting the
same body as `POST /api/v1/targets`. It SHALL validate the definition as
`POST /api/v1/validate` does and return 422 with the same `errors` on failure. For a
valid definition it SHALL return 200 with the resolved `severity`, the `route` (or none),
and, for each of `target.down` and `target.flapping`, the event's severity and one entry
per configured sink with `sink`, `outcome` (`sent`, `dropped` or `unknown`) and, unless
`sent`, `reason`. A `reason` SHALL be one of `not_in_route`, `events`, `min_severity`,
`match.sites`, `match.targets`, `match.tags` or `missing_sink`. A `match.tags` filter
SHALL be decided from the tags of the peer the address resolves to in the collector's
peer view; when that cannot be decided, the outcome SHALL be `unknown` with reason
`match.tags`. The response SHALL state that a recovery goes to the sinks that received
its down. The preview SHALL change nothing, run no check, and apply the same
`Content-Type` and `Origin` checks as `POST /api/v1/validate`, without an audit entry.

#### Scenario: Preview shows a filtered sink

- **WHEN** the preview is asked for a monitor in site `lab` with `route: db-team`
  (`[pagerduty, ops-slack]`), and `pagerduty` has `match: {sites: [nyc]}`
- **THEN** `target.down` lists `pagerduty` as `dropped` with reason `match.sites`,
  `ops-slack` as `sent`, and every other sink as `dropped` with reason `not_in_route`

#### Scenario: Flapping below a page-only sink

- **WHEN** the route's only sink has `min_severity: page`
- **THEN** `target.flapping` lists that sink as `dropped` with reason `min_severity`

#### Scenario: Tags not yet known

- **WHEN** a candidate sink has `match: {tags: [tag:db]}` and the address resolves to
  no known peer
- **THEN** that sink's outcome is `unknown` with reason `match.tags`

### Requirement: Route write authorization and audit

Every request that changes a route SHALL pass the same `Content-Type`, `Origin` and
authorization decision as a monitor write, before any change. The collector SHALL log
every route create, replace and delete, accepted or refused, at info level with the
operation, the route name, the outcome, the caller's remote address and the verified
identity fields defined by `ui-authorization`.

#### Scenario: Route write is logged

- **WHEN** route `db-team` is deleted from `100.64.0.7`
- **THEN** a log line records the delete, `db-team`, the outcome and `100.64.0.7`

#### Scenario: Cross-site route write

- **WHEN** a `POST /api/v1/routes` carries `Content-Type: text/plain`
- **THEN** the response is 415 and no route changes

#### Scenario: Route write by a viewer

- **WHEN** authorization is on and a `viewer` sends `POST /api/v1/routes`
- **THEN** the response is 403 and no route changes
