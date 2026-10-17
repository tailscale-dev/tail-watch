# collector-heartbeat Specification

## Purpose

Lets an external dead-man's-switch service page when the collector, its host
or its uplink disappears, since a dead collector cannot report itself.

## Requirements

### Requirement: Heartbeat configuration

The optional top-level `heartbeat` section SHALL require exactly one of
`url_env`, `url_file` or `url_setec` and SHALL accept `interval` (default `1m`). The
heartbeat URL SHALL follow the same secret-handling rules as sink URLs.

#### Scenario: No heartbeat

- **WHEN** the config has no `heartbeat` section
- **THEN** tail-watch sends no heartbeat requests

#### Scenario: Heartbeat URL from setec

- **WHEN** `heartbeat.url_setec: hb-url` is set
- **THEN** pings go to the URL stored in setec under `hb-url`

### Requirement: Heartbeat pings

When configured, tail-watch SHALL send an HTTP `GET` to the heartbeat URL every
`interval`, starting once every configured target/check has produced at least
one result, with a 10s request
timeout. A failed ping SHALL be logged and counted in
`tailwatch_heartbeat_failures_total` and SHALL NOT be retried before the next
interval. Heartbeats SHALL stop when tail-watch shuts down.

#### Scenario: Regular pings

- **WHEN** `interval` is `1m` and the collector runs for 5 minutes after every
  check has produced a result
- **THEN** the heartbeat URL receives 5 requests, give or take one

#### Scenario: Collector stops

- **WHEN** the tail-watch process exits
- **THEN** no further heartbeat requests are sent, so the external service
  alerts
