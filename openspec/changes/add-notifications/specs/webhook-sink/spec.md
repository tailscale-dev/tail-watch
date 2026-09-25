# Spec Delta

## Purpose

Defines how tsmon delivers events to a generic HTTP receiver: request format,
HMAC signing compatible with Tailscale's own webhooks, retry and expiry policy,
and the optional batched mode for rate-limited receivers.

## ADDED Requirements

### Requirement: Webhook sink configuration

A sink with `type: webhook` SHALL require exactly one of `url_env` or
`url_file`, and SHALL accept `secret_env` or `secret_file` (HMAC key),
`headers_env` (map of header name to environment variable name), `max_age`
(default `1h`), `batch` (default `false`), `group_wait` (default `10s`) and
`max_batch` (default `100`), plus the shared routing and queue keys. Sink names
SHALL be unique, match `[A-Za-z0-9._-]+`, and not be `incidents`, `intake` or `state`.

#### Scenario: Both URL sources

- **WHEN** a webhook sink sets both `url_env` and `url_file`
- **THEN** config loading fails with an error naming the sink

#### Scenario: No signing secret

- **WHEN** a webhook sink has no `secret_env` or `secret_file`
- **THEN** tsmon starts, logs a warning that the sink is unsigned, and sends
  requests without a `Tsmon-Signature` header

### Requirement: Webhook request format

Each delivery SHALL be an HTTP `POST` to the sink URL with
`Content-Type: application/json`, `User-Agent: tsmon/<version>`, the headers
from `headers_env`, and a body holding exactly one event payload. It SHALL set
`Tsmon-Event-Id: <id>`. It SHALL NOT follow redirects. Each request SHALL time
out after 10s.

#### Scenario: Single event

- **WHEN** a `target.down` is delivered to a non-batched webhook sink
- **THEN** the receiver gets one POST whose body is that event's JSON payload
  and whose `Tsmon-Event-Id` equals the payload `id`

#### Scenario: Extra auth header

- **WHEN** `headers_env: {Authorization: OPS_WEBHOOK_AUTH}` is set and
  `OPS_WEBHOOK_AUTH=Bearer abc`
- **THEN** each request carries `Authorization: Bearer abc`

### Requirement: Request signing

When the sink has a secret, each request SHALL carry
`Tsmon-Signature: t=<unix seconds>,v1=<hex>`, where `v1` is the lowercase hex
HMAC-SHA256, keyed by the secret, of the string `<t>.<body>`. `t` SHALL be the
time of that attempt, so a retry carries a fresh `t` over identical body bytes.

#### Scenario: Receiver verifies

- **WHEN** a receiver computes HMAC-SHA256 with the shared secret over
  `<t>.<body>` from the request
- **THEN** the result equals the `v1` value

#### Scenario: Wrong secret

- **WHEN** a receiver verifies with a different secret
- **THEN** the computed value differs from `v1`

#### Scenario: Independent signing vector

- **WHEN** signing a fixed timestamp and exact body with a fixed key
- **THEN** the signature equals a fixture computed independently of the sender's
  signing helper, and changing either timestamp or body changes the signature

### Requirement: Delivery result handling

A `2xx` response SHALL mark the event delivered. `408`, `429`, `5xx`, network
errors and timeouts SHALL be retried with increasing delay, capped at 5
minutes between attempts. On `429` with a `Retry-After` header (seconds or
HTTP date), the next attempt SHALL wait at least that long, capped at 5
minutes. Any other status, including `3xx` and other `4xx`, SHALL be a
permanent failure: logged once, counted, and not retried. An event older than
the sink's `max_age` at the time of an attempt SHALL be dropped and counted as
expired instead of sent.

#### Scenario: Server error then success

- **WHEN** the receiver answers `500` then `200`
- **THEN** tsmon sends the event twice and marks it delivered

#### Scenario: Rate limited

- **WHEN** the receiver answers `429` with `Retry-After: 30`
- **THEN** the next attempt is sent no sooner than 30 seconds later

#### Scenario: Bad request

- **WHEN** the receiver answers `400`
- **THEN** tsmon does not retry, logs the failure once, and increments
  `tsmon_notify_dropped_total{reason="permanent"}`

#### Scenario: Expired while retrying

- **WHEN** a receiver keeps answering `503` past the event's `max_age`
- **THEN** the event is dropped and counted with reason `expired`

### Requirement: Batched mode

With `batch: true`, the sink SHALL start a window on the first queued event
and send when `group_wait` elapses or `max_batch` events are waiting,
whichever comes first. The body SHALL be
`{"version": 1, "batch_id": "bat_<id>", "events": [ ... ]}` where each element
is exactly a single-event payload, in queue order. The request SHALL carry
`Tsmon-Batch-Id` instead of `Tsmon-Event-Id`, and `Tsmon-Signature` SHALL cover
the whole body. A retry SHALL resend identical body bytes and `batch_id` with a
fresh signature. Events arriving during retries SHALL go into the next batch. A
`target.down` and its `target.recovered` in the same window SHALL both be sent,
in order. A permanent failure SHALL drop the whole batch and count every event
in it.

#### Scenario: Large burst

- **WHEN** 250 events arrive within one `group_wait` window and `max_batch` is
  100
- **THEN** the receiver gets three batches of 100, 100 and 50 events, in
  order

#### Scenario: Batch retry

- **WHEN** a batch POST receives `503` and is retried
- **THEN** the retry has the same body bytes and `Tsmon-Batch-Id`, and a
  `Tsmon-Signature` with a newer `t`

#### Scenario: Blip inside a window

- **WHEN** `nyc-nas` goes down and recovers inside one window
- **THEN** the batch holds both events, down before recovered
