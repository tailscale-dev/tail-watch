# Spec Delta

## Purpose

Defines how tail-watch opens and resolves PagerDuty alerts through the Events
API v2: sink configuration, request format, dedup and resolve behavior,
severity mapping, and the same delivery policy as the other sinks.

## ADDED Requirements

### Requirement: PagerDuty sink configuration

A sink with `type: pagerduty` SHALL require exactly one of `routing_key_env`
or `routing_key_file` naming the Events API v2 integration key, and SHALL
accept `region` (`us` or `eu`, default `us`), `status_url`, `events`, and the
shared routing and queue keys. It SHALL NOT accept `url_env`, `url_file`,
`secret_env`, `secret_file`, `headers_env`, `batch`, `max_batch`,
`group_wait` or `mention_on_page`. `events`, when set, SHALL list only
`target.down` and `target.recovered`; when unset it SHALL default to those
two.

#### Scenario: Both key sources

- **WHEN** a PagerDuty sink sets both `routing_key_env` and `routing_key_file`
- **THEN** config loading fails with an error naming the sink

#### Scenario: No key source

- **WHEN** a PagerDuty sink sets neither `routing_key_env` nor `routing_key_file`
- **THEN** config loading fails with an error naming the sink

#### Scenario: Webhook URL on PagerDuty

- **WHEN** a PagerDuty sink sets `url_env`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Flapping on PagerDuty

- **WHEN** a PagerDuty sink sets `events: [target.down, target.flapping]`
- **THEN** config loading fails with an error naming the sink and
  `target.flapping`

#### Scenario: Unknown region

- **WHEN** a PagerDuty sink sets `region: apac`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Default events

- **WHEN** a PagerDuty sink sets no `events` and `target.flapping` and
  `collector.started` events are emitted
- **THEN** the sink receives neither

### Requirement: PagerDuty request format

Each delivery SHALL be an HTTP `POST` of one JSON object to
`https://events.pagerduty.com/v2/enqueue` (`https://events.eu.pagerduty.com/v2/enqueue`
for `region: eu`) with `Content-Type: application/json` and
`User-Agent: tail-watch/<version>`. The body SHALL carry `routing_key`,
`event_action`, `dedup_key`, and, for a `trigger`, a `payload` with `summary`,
`source`, `severity`, `timestamp`, `component`, `group`, `class` and
`custom_details`. The request SHALL NOT carry `Tail-Watch-Signature`. It SHALL
NOT follow redirects. Each request SHALL time out after 10s.

#### Scenario: Down becomes a trigger

- **WHEN** a `target.down` for `nyc-nas` on check `tcp:445` is delivered
- **THEN** the body has `event_action` `trigger`, `dedup_key` `nyc-nas/tcp:445`,
  and a `payload.summary` that includes `nyc-nas`, `tcp:445` and the error

#### Scenario: Recovery becomes a resolve

- **WHEN** the `target.recovered` for that down is delivered
- **THEN** the body has `event_action` `resolve` and `dedup_key`
  `nyc-nas/tcp:445`

#### Scenario: No signature

- **WHEN** a PagerDuty sink delivers an event
- **THEN** the request has no `Tail-Watch-Signature` header

#### Scenario: EU region

- **WHEN** a PagerDuty sink sets `region: eu`
- **THEN** requests go to `events.eu.pagerduty.com`

### Requirement: Dedup key

The `dedup_key` of every request for a target and check SHALL be
`<target name>/<check>`. When that string is longer than 255 bytes, the check
part SHALL be replaced by the first 16 hex characters of the SHA-256 of the
check, so the key stays stable and under the PagerDuty limit. A `trigger` and
the `resolve` for the same incident SHALL carry the same `dedup_key`,
including a `resolve` with `reason: removed`.

#### Scenario: Long check

- **WHEN** the check string makes `<target>/<check>` longer than 255 bytes
- **THEN** the `dedup_key` is `<target>/` followed by 16 hex characters, and
  the recovery for the same check carries the same value

#### Scenario: Target removed

- **WHEN** a configured target with an open incident is deleted from config
- **THEN** the sink sends a `resolve` with the dedup key of its down

### Requirement: Severity and details

A `trigger` SHALL set `payload.severity` to `critical` for an event of
severity `page`, `warning` for `warn`, and `info` for `info`. It SHALL set
`payload.source` to the target name, `payload.component` to the check,
`payload.group` to the target's site when it has one, `payload.class` to the
payload `layer`, and `payload.timestamp` to the event time. `custom_details`
SHALL include the address, parent, path kind, latency, error, down-since time,
failure count, dependents, event id and collector name when the event has
them. When `status_url` is set, `links` SHALL contain one entry whose `href` is
`<status_url>/#<target>`, with trailing slashes trimmed from `status_url`. An
error longer than 300 bytes SHALL be cut to 300 bytes and followed by `…`.

#### Scenario: Page severity

- **WHEN** a `target.down` with severity `page` is delivered
- **THEN** `payload.severity` is `critical`

#### Scenario: Warn severity

- **WHEN** a `target.down` with severity `warn` is delivered
- **THEN** `payload.severity` is `warning`

#### Scenario: Router with dependents

- **WHEN** `nyc-router` goes DOWN with 14 dependents
- **THEN** `custom_details` lists the 14 dependent names

#### Scenario: Status link

- **WHEN** `status_url` is `https://monitor-nyc.example.ts.net:9109/`
- **THEN** `links` has one entry with `href`
  `https://monitor-nyc.example.ts.net:9109/#nyc-nas`

#### Scenario: Long error

- **WHEN** the error is longer than 300 bytes
- **THEN** `custom_details.error` is the first 300 bytes followed by `…`

### Requirement: Routing key stays out of queues

The routing key SHALL be read from its `*_env` or `*_file` source when a
request is sent and SHALL be added to the body at that moment. A queued or
persisted body, `notification-state.json`, logs and errors SHALL NOT contain
the key.

#### Scenario: Disk queue

- **WHEN** a PagerDuty sink uses `queue: disk` and an event is queued
- **THEN** `notification-state.json` does not contain the routing key

#### Scenario: Rotated key

- **WHEN** the routing key file changes between two attempts of the same event
- **THEN** the second attempt sends the new key

#### Scenario: Key missing at send time

- **WHEN** the variable named by `routing_key_env` is empty at send time
- **THEN** the attempt is retried as a transient failure and the error
  names the sink and the variable, not its value

### Requirement: PagerDuty delivery policy

The PagerDuty sink SHALL apply the same retry, `Retry-After`, permanent-failure
and `max_age` rules as the webhook sink, and SHALL count `2xx` (PagerDuty
answers `202`) as delivered. A `400` SHALL be permanent. A delivery error
SHALL NOT include the routing key.

#### Scenario: Accepted

- **WHEN** PagerDuty answers `202`
- **THEN** the event is delivered and counted in `tailwatch_notify_sent_total`

#### Scenario: Rate limit

- **WHEN** PagerDuty answers `429`
- **THEN** the sink retries the same body after the backoff, honoring
  `Retry-After` when present

#### Scenario: Bad request

- **WHEN** PagerDuty answers `400`
- **THEN** tail-watch does not retry and increments
  `tailwatch_notify_dropped_total{reason="permanent"}`

#### Scenario: Server error

- **WHEN** PagerDuty answers `503`
- **THEN** the sink retries the same body

#### Scenario: Key stays out of the error

- **WHEN** a request fails with a transport error
- **THEN** the logged error contains the scheme and host and not the routing key

### Requirement: Notify-test opens and resolves an alert

`notify-test` against a PagerDuty sink SHALL send a `trigger` with
`payload.severity` `info`, `dedup_key` `tail-watch-test/<collector name>`
and a summary that says it is a tail-watch test, and, once that is accepted,
SHALL send a `resolve` for the same `dedup_key`. It SHALL report success only
when both requests answer `2xx`.

#### Scenario: Both accepted

- **WHEN** PagerDuty answers `202` to both requests
- **THEN** notify-test prints `202` for the sink and exits 0

#### Scenario: Trigger rejected

- **WHEN** PagerDuty answers `400` to the trigger
- **THEN** notify-test prints `400` for the sink, sends no resolve, and exits 1

#### Scenario: Resolve fails

- **WHEN** the trigger is accepted and the resolve is rejected
- **THEN** notify-test prints an error naming the resolve and the alert key
  `tail-watch-test/<collector name>`, and exits 1
