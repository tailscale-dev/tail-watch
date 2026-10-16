# Spec Delta

## Purpose

Defines how tail-watch opens and resolves incident.io alerts through the HTTP
alert source API: sink configuration, request format, dedup and resolve
behavior, and the same delivery policy as the other sinks.

## ADDED Requirements

### Requirement: incident.io sink configuration

A sink with `type: incidentio` SHALL require `alert_source_id`, matching
`[A-Za-z0-9_-]+`, and exactly one of `token_env` or `token_file` naming the
alert source token. The token SHALL be a `secretRef`, so it accepts every
source that type supports. The sink SHALL accept `status_url`, `events`, and
the shared routing and queue keys. It SHALL NOT accept `url_env`, `url_file`,
`secret_env`, `secret_file`, `headers_env`, `batch`, `max_batch`,
`group_wait` or `mention_on_page`. `events`, when set, SHALL list only
`target.down` and `target.recovered`; when unset it SHALL default to those
two.

#### Scenario: Both token sources

- **WHEN** an incident.io sink sets both `token_env` and `token_file`
- **THEN** config loading fails with an error naming the sink

#### Scenario: No token source

- **WHEN** an incident.io sink sets neither `token_env` nor `token_file`
- **THEN** config loading fails with an error naming the sink

#### Scenario: Missing alert source id

- **WHEN** an incident.io sink sets no `alert_source_id`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Unsafe alert source id

- **WHEN** an incident.io sink sets `alert_source_id: "../x"`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Webhook URL on incident.io

- **WHEN** an incident.io sink sets `url_env`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Token key on another type

- **WHEN** a webhook sink sets `token_env`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Flapping on incident.io

- **WHEN** an incident.io sink sets `events: [target.down, target.flapping]`
- **THEN** config loading fails with an error naming the sink and
  `target.flapping`

#### Scenario: Default events

- **WHEN** an incident.io sink sets no `events` and `target.flapping` and
  `collector.started` events are emitted
- **THEN** the sink receives neither

### Requirement: incident.io request format

Each delivery SHALL be an HTTP `POST` of one JSON object to
`https://api.incident.io/v2/alert_events/http/<alert_source_id>` with
`Authorization: Bearer <token>`, `Content-Type: application/json` and
`User-Agent: tail-watch/<version>`. The body SHALL carry `title`, `status`,
`deduplication_key`, and, when available, `description`, `metadata` and
`source_url`. The request SHALL NOT carry `Tail-Watch-Signature` and SHALL NOT
put the token in the URL or the body. It SHALL NOT follow redirects. Each
request SHALL time out after 10s.

#### Scenario: Down becomes firing

- **WHEN** a `target.down` for `nyc-nas` on check `tcp:445` is delivered
- **THEN** the body has `status` `firing`, `deduplication_key`
  `nyc-nas/tcp:445`, and a `title` that includes `nyc-nas`, `tcp:445` and the
  error

#### Scenario: Recovery becomes resolved

- **WHEN** the `target.recovered` for that down is delivered
- **THEN** the body has `status` `resolved`, a non-empty `title`, and
  `deduplication_key` `nyc-nas/tcp:445`

#### Scenario: Bearer header

- **WHEN** an incident.io sink with alert source id `01ABC` delivers an event
- **THEN** the request path is `/v2/alert_events/http/01ABC`, the
  `Authorization` header is `Bearer <token>`, and the URL
  query is empty

#### Scenario: No signature

- **WHEN** an incident.io sink delivers an event
- **THEN** the request has no `Tail-Watch-Signature` header

### Requirement: Dedup key

The `deduplication_key` of every request for a target and check SHALL be
`<target name>/<check>`. When that string is longer than 255 bytes, the check
part SHALL be replaced by the first 16 hex characters of the SHA-256 of the
check. A `firing` event and the `resolved` event for the same incident SHALL
carry the same `deduplication_key`, including a `resolved` with
`reason: removed`.

#### Scenario: Long check

- **WHEN** the check string makes `<target>/<check>` longer than 255 bytes
- **THEN** the `deduplication_key` is `<target>/` followed by 16 hex
  characters, and the recovery for the same check carries the same value

#### Scenario: Target removed

- **WHEN** a configured target with an open incident is deleted from config
- **THEN** the sink sends a `resolved` event with the key of its down

### Requirement: Alert content

A `firing` event SHALL set `description` to markdown that includes the address,
error and down-since time, and `metadata` to an object with the target, check,
address, site, parent, layer, severity, path kind, latency, failure count,
dependents, event id and collector name when the event has them. When
`status_url` is set, `source_url` SHALL be `<status_url>/#<target>`, with
trailing slashes trimmed from `status_url`. An error longer than 300 bytes
SHALL be cut to 300 bytes and followed by `…`.

#### Scenario: Router with dependents

- **WHEN** `nyc-router` goes DOWN with 14 dependents
- **THEN** `metadata.dependents` lists the 14 dependent names

#### Scenario: Source link

- **WHEN** `status_url` is `https://monitor-nyc.example.ts.net:9109/`
- **THEN** `source_url` is `https://monitor-nyc.example.ts.net:9109/#nyc-nas`

#### Scenario: Long error

- **WHEN** the error is longer than 300 bytes
- **THEN** the error in `description` and `metadata` is the first 300 bytes
  followed by `…`

### Requirement: Token stays out of queues

The token SHALL be read from its `secretRef` source when a request is sent
and SHALL be set only in the `Authorization` header at that moment. A queued
or persisted body, `notification-state.json`, logs and errors SHALL NOT
contain the token.

#### Scenario: Disk queue

- **WHEN** an incident.io sink uses `queue: disk` and an event is queued
- **THEN** `notification-state.json` does not contain the token

#### Scenario: Rotated token

- **WHEN** the token file changes between two attempts of the same event
- **THEN** the second attempt sends the new token

#### Scenario: Token missing at send time

- **WHEN** the variable named by `token_env` is empty at send time
- **THEN** the attempt is retried as a transient failure and the error names
  the sink and the variable, not its value

### Requirement: incident.io delivery policy

The incident.io sink SHALL apply the same retry, `Retry-After`,
permanent-failure and `max_age` rules as the webhook sink, and SHALL count
`2xx` (incident.io answers `202`) as delivered. A `4xx` other than `408` and
`429` SHALL be permanent. A delivery error SHALL NOT include the token.

#### Scenario: Accepted

- **WHEN** incident.io answers `202`
- **THEN** the event is delivered and counted in `tailwatch_notify_sent_total`

#### Scenario: Rate limit

- **WHEN** incident.io answers `429` with `Retry-After: 2`
- **THEN** the sink retries the same body after at least 2 seconds

#### Scenario: Rate limit without a usable hint

- **WHEN** incident.io answers `429` with a `Retry-After` value that is not
  a number of seconds
- **THEN** the sink retries the same body with its normal backoff

#### Scenario: Rejected token

- **WHEN** incident.io answers `401`
- **THEN** tail-watch does not retry and increments
  `tailwatch_notify_dropped_total{reason="permanent"}`

#### Scenario: Server error

- **WHEN** incident.io answers `503`
- **THEN** the sink retries the same body

#### Scenario: Token stays out of the error

- **WHEN** a request fails with a transport error
- **THEN** the logged error contains the scheme and host and not the token

### Requirement: Notify-test fires and resolves an alert

`notify-test` against an incident.io sink SHALL send a `firing` event with
`deduplication_key` `tail-watch-test/<collector name>` and a title that says it
is a tail-watch test, and, once that is accepted, SHALL send a `resolved`
event with the same key. It SHALL report success only when both requests
answer `2xx`.

#### Scenario: Both accepted

- **WHEN** incident.io answers `202` to both requests
- **THEN** notify-test prints `202` for the sink and exits 0

#### Scenario: Firing rejected

- **WHEN** incident.io answers `401` to the firing event
- **THEN** notify-test prints `401` for the sink, sends no resolved event,
  and exits 1

#### Scenario: Resolve fails

- **WHEN** the firing event is accepted and the resolved event is rejected
- **THEN** notify-test prints an error naming the resolve and the alert key
  `tail-watch-test/<collector name>`, and exits 1
