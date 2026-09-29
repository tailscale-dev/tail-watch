# Event payload reference

Every notification is a JSON event. This page describes the version 1 payload, the
batch envelope, the webhook request, and the signature. For the Slack rendering of
the same events, see [Send alerts to Slack](../how-to/slack-sink.md). For how events
are produced and queued, see
[Incidents and queues](../explanation/incidents-and-queues.md).

Within `version: 1`, changes only add fields. Receivers must ignore fields they do
not know. Fields that do not apply are omitted, never sent as `null`.

## Event types

| `type` | `severity` | Sent when |
| --- | --- | --- |
| `target.down` | The target's `severity`: `page` or `warn` | A target goes `DOWN` and has no open incident. |
| `target.recovered` | The severity of the down it resolves | A target with an open incident returns to `UP`, or the target is removed. |
| `target.flapping` | `warn` | A target crosses `flap_limit` state changes within `flap_window`. |
| `collector.started` | `info` | Once, after every target and check has a first result. |
| `test` | `info` | Only from `tail-watch notify-test`. |
| `target.degraded` | | Reserved. Never sent in this version. |

Severity ranks `info` < `warn` < `page`. A sink's `min_severity` compares against it.
A start with every target `UP` sends only `collector.started`. A target already
failing at startup produces one `target.down`.

## Fields

| Field | Type | Present on | Meaning |
| --- | --- | --- | --- |
| `version` | integer | all | Always `1`. |
| `id` | string | all | Event id, see [Ids](#ids). |
| `type` | string | all | One of the types above. |
| `severity` | string | all | `info`, `warn`, or `page`. |
| `time` | string | all | Creation time, RFC 3339 in UTC with second precision. |
| `late` | boolean | any, when true | The first attempt happened more than one minute after `time`. The original `time` is kept. |
| `collector` | object | all | The sending collector. |
| `collector.name` | string | all | The node's hostname. |
| `collector.tailscale_ip` | string | when known | The node's first Tailscale IPv4 address, or IPv6 when it has none. |
| `target` | object | target events | The affected target. |
| `target.name` | string | target events | Target name. |
| `target.addr` | string | target events | Target address. |
| `target.site` | string | when set | Target site. |
| `target.parent` | string | when set | Parent target name. |
| `target.implicit` | boolean | when true | The target is a router tail-watch learned, not one from the config. |
| `check` | string | target events | The check that failed, such as `tcp:445`. |
| `layer` | string | down, flapping | Failure layer: `unrouted`, `control`, `tunnel`, `lan`, or `service`. |
| `error` | string | target events | The error from the failing check. |
| `since` | string | target events | When the target's outage began, RFC 3339 UTC. |
| `failures` | integer | target events | Consecutive failures that opened the incident. |
| `path` | object | when known | How the latest probe reached the target. |
| `path.kind` | string | when known | `direct`, `derp`, or `peer_relay`. |
| `path.endpoint` | string | direct paths | Remote UDP endpoint, `ip:port`. |
| `path.derp_region` | string | derp paths | DERP region code. |
| `path.latency_ms` | integer | when measured | Latency in milliseconds. |
| `dependents` | array of strings | down, recovered | Targets suppressed behind this one. Empty array when none. |
| `duration_seconds` | integer | recovered | Seconds from `since` to the recovery. |
| `down_event_id` | string | recovered | The `id` of the `target.down` it resolves. |
| `reason` | string | recovered, when set | `removed` when the target was deleted or edited while `DOWN`, rather than recovered. |

`collector.started` and `test` carry only `version`, `id`, `type`, `severity`,
`time`, `collector`, and `late` when applicable. `target.recovered` has no `layer`.
`target.flapping` has no `dependents`.

## Examples

Full examples for each shape are in `cmd/tail-watch/testdata/payload/` in the
repository.

### `target.down`

```json
{
  "version": 1,
  "id": "evt_01JAB3K9T6M4X2Q8R7N5W0YZCD",
  "type": "target.down",
  "severity": "page",
  "time": "2026-04-02T14:02:11Z",
  "collector": {
    "name": "monitor-nyc",
    "tailscale_ip": "100.101.102.103"
  },
  "target": {
    "name": "nyc-nas",
    "addr": "10.20.0.50",
    "site": "nyc",
    "parent": "nyc-router"
  },
  "check": "tcp:445",
  "layer": "service",
  "error": "connection refused",
  "since": "2026-04-02T14:00:41Z",
  "failures": 3,
  "path": {
    "kind": "direct",
    "endpoint": "203.0.113.5:41641",
    "latency_ms": 12
  },
  "dependents": []
}
```

A learned router adds `"implicit": true` inside `target`.

### `target.recovered`

```json
{
  "version": 1,
  "id": "evt_01JAB3K9T6M4X2Q8R7N5W0YZCG",
  "type": "target.recovered",
  "severity": "page",
  "time": "2026-04-02T14:14:11Z",
  "collector": {
    "name": "monitor-nyc",
    "tailscale_ip": "100.101.102.103"
  },
  "target": {
    "name": "nyc-nas",
    "addr": "10.20.0.50",
    "site": "nyc",
    "parent": "nyc-router"
  },
  "check": "tcp:445",
  "error": "connection refused",
  "since": "2026-04-02T14:00:41Z",
  "failures": 3,
  "path": {
    "kind": "direct",
    "endpoint": "203.0.113.5:41641",
    "latency_ms": 12
  },
  "dependents": [
    "nyc-cam"
  ],
  "duration_seconds": 810,
  "down_event_id": "evt_01JAB3K9T6M4X2Q8R7N5W0YZCD"
}
```

When the target was removed instead of recovering, the event also carries
`"reason": "removed"`.

### `collector.started` and `test`

```json
{
  "version": 1,
  "id": "evt_01JAB3K9T6M4X2Q8R7N5W0YZCK",
  "type": "collector.started",
  "severity": "info",
  "time": "2026-04-02T14:02:11Z",
  "collector": {
    "name": "monitor-nyc",
    "tailscale_ip": "100.101.102.103"
  }
}
```

## Ids

An event `id` is `evt_` followed by 26 Crockford base32 characters
(`0123456789ABCDEFGHJKMNPQRSTVWXYZ`): 48 bits of Unix milliseconds followed by 80
random bits. Ids sort by creation time across milliseconds. The id is the same
across every retry and every sink.

A batch id is `bat_` followed by 26 characters of the same form.

Delivery is at least once. Receivers must deduplicate on `id`, and must accept a
`target.recovered` whose `down_event_id` they never saw.

## Webhook request

Each request is a `POST` with these headers:

| Header | Value |
| --- | --- |
| `Content-Type` | `application/json` |
| `User-Agent` | `tail-watch/<version>` |
| `Tail-Watch-Event-Id` | The event `id`. Sent when `batch` is `false`. |
| `Tail-Watch-Batch-Id` | The `batch_id`. Sent when `batch` is `true`. |
| `Tail-Watch-Signature` | The signature. Sent when the sink has a secret. |
| any `headers_env` name | The value of the environment variable it names. |

Redirects are not followed and each request times out after 10 seconds.

| Response | Result |
| --- | --- |
| `2xx` | Delivered. |
| `408`, `5xx`, timeout, network error | Retried. |
| `429` | Retried, waiting at least `Retry-After` (seconds or an HTTP date). |
| Anything else, including `3xx` and other `4xx` | Dropped, logged once, counted as `permanent`. |

The retry delay is `min(2^(attempt-1) seconds, 5 minutes)` scaled by 0.5 to 1.5
times jitter, raised to `Retry-After` when longer, and never more than 5 minutes.
An event older than the sink's `max_age` when an attempt is due is dropped and
counted as `expired`.

### Batch envelope

With `batch: true`, the request body is:

```json
{
  "version": 1,
  "batch_id": "bat_01JAB3K9T6M4X2Q8R7N5W0YZCP",
  "events": []
}
```

`events` holds full event objects, in queue order. The sink waits `group_wait` after
the first queued event, or until `max_batch` events are waiting. A retry resends
identical body bytes and the same `batch_id`. Events that arrive meanwhile go into
the next batch. A permanent failure or expiry drops the whole batch, and every event
in it is counted.

## Signature

With `secret_env` or `secret_file`, each request carries:

```text
Tail-Watch-Signature: t=<unix seconds>,v1=<hex>
```

`v1` is the lowercase hex HMAC-SHA256, keyed with the secret, of the ASCII string
`<t>.` followed by the exact request body bytes. For a batch the body is the whole
envelope. A retry sends the same body bytes with a fresh `t`.

To verify, parse `t` and `v1`, reject a `t` outside your tolerance, recompute the
HMAC over `<t>.<raw body>`, and compare in constant time. Verify against the raw
bytes, not a re-serialized copy.

```bash
{ printf '%s.' "$T"; cat body.json; } | openssl dgst -sha256 -hmac "$SECRET" -hex
```

## `late`

The body of the first attempt is fixed when the request is first formed. If that
moment is more than one minute after the event's `time`, the event carries
`"late": true`, for example after a receiver outage or a collector restart. The
original `time` is never changed.
