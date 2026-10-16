# Design

## Context

See proposal.md for why. The behavior contract is
`specs/pagerduty-sink/spec.md`, plus the "Secret handling" delta in
`specs/notification-delivery/spec.md`.

Webhook, Slack and Teams share one delivery loop (`retryLoop` in
`deliver.go`) behind the `sink` interface in `deliver.go`: `Render(evs,
unitID)` builds a body when an event is queued, and `Send(ctx, body, meta)`
makes one request. `httpPost` in `sink.go` does the POST (10s timeout, no
redirects, `redactURL` on errors), and `classifyStatus` already maps `2xx` to
delivered, `408`/`429`/`5xx` to retry (with `Retry-After`) and other `4xx` to
permanent. PagerDuty answers `202` on success, `429` on rate limit, `400` on
a bad body, and `5xx` on its own faults, so no new classification is needed.

Three facts about the current code shape this design:

- **Bodies are queued.** `worker` renders a body when an event is taken from
  the queue and `beginUnit` stores it, so with `queue: disk` the body lands in
  `notification-state.json`. Slack and Teams keep their URL out of the body
  and read it in `Send`. The PagerDuty routing key has to be in the JSON body,
  so it cannot be rendered in.
- **URL is mandatory.** `resolveSink` requires exactly one of `url_env` or
  `url_file` for every type, and `SinkConfig.checkSecrets` resolves that
  reference at startup. PagerDuty has no per-operator URL.
- **Recoveries already follow their pages.** `notification-events` and
  `notification-delivery` make a recovery eligible whenever its down was
  attempted, and tell receivers to tolerate a resolve for an unknown id. A
  `resolve` for a `dedup_key` PagerDuty never saw is accepted and ignored, so
  that contract fits.

## Goals / Non-Goals

**Goals:**

- One new renderer on the existing sink interface, client and retry loop.
- A routing key that never reaches a queued body, the state file or a log.
- A golden Events API body a reviewer can diff.

**Non-Goals:**

- A live PagerDuty account in CI. `notify-test` against the operator's key is
  the manual check. An opt-in `TestLivePagerDuty` is not part of this change.
- Changing `GET /sinks` or the monitor UI. `sinkViewOf` already returns the
  type and filters.
- Any per-event PagerDuty feature beyond `trigger` and `resolve`: no
  `acknowledge`, no images, no priorities.

## Decisions

### D1. Boundaries and the old design doc

The design doc named PagerDuty as phase 2 and was deleted on 2026-10-04;
`ROADMAP.md` carries the item. No CONTEXT.md boundary is crossed:

- *Reports failures; never acts on them.* A page reports. Only `trigger` and
  `resolve` are sent, and `resolve` mirrors tail-watch's own recovery.
- *Secrets only in `*_env` / `*_file`; never logged, persisted or shown.* The
  routing key is a `*_env` / `*_file` reference, injected at send time (D3).
- *No shared state between collectors.* The `dedup_key` makes two collectors
  that watch the same target and check merge into one PagerDuty alert, and
  the first recovery resolves it for both. That is PagerDuty's dedup, and the
  how-to says so. Use a different `routing_key` per collector, or a distinct
  target name, when that is not wanted.
- *No credentials for discovery.* An integration key only writes events.

This does contradict one thing in the current specs: `webhook-sink`,
`slack-sink` and `teams-sink` each say their type requires exactly one of
`url_env` or `url_file`. Those are per-type requirements, so they stay true.
The shared parser's unconditional check becomes per-type (D2). The generic
"Secret handling" requirement names URLs, HMAC secrets and headers, so it is
modified.

### D2. Config

`sinkPagerDuty = "pagerduty"`. `rawSink` gains `RoutingKeyEnv`
(`routing_key_env`), `RoutingKeyFile` (`routing_key_file`) and `Region`
(`region`). `SinkConfig` gains `RoutingKey secretRef` and `Region string`.

- The type switch accepts it, and the unknown-type error becomes `must be
  webhook, slack, teams or pagerduty`.
- The "exactly one of `url_env` or `url_file`" check moves behind `Type !=
  pagerduty`. For `pagerduty` the check is exactly one of `routing_key_env` or
  `routing_key_file`, and `url_env` / `url_file` are rejected. For the other
  three types, `routing_key_*` and `region` are rejected.
- `resolveSinkTypeKeys` rejects `secret_env`, `secret_file`, `headers_env`,
  `batch`, `max_batch`, `group_wait` and `mention_on_page` for PagerDuty. This
  is the Teams rejection list plus `group_wait`: PagerDuty sends one event per
  request, so a grouping window means nothing. `status_url` stays valid and
  becomes a `links` entry.
- `region` is `us` (default) or `eu`; anything else fails. A fixed pair
  instead of a free `url` key: an arbitrary URL would need the secret-URL
  handling the Slack and Teams sinks have, and the two regions are the only
  Events API hosts. A self-hosted or proxied endpoint is a non-goal.
- `events` defaults to `[target.down, target.recovered]` and rejects any other
  value. The filter in `explain` is unchanged. `test` events bypass it, as for
  every sink.
- `checkSecrets` resolves `RoutingKey` for a PagerDuty sink and the URL for
  the others, so an unset variable stops startup and fails `notify-test`
  before any request. `check-config` does not read secrets, as today, and
  `describeSink` prints `type=pagerduty` and never the key.
- The warning in `main.go` about a missing HMAC secret is already limited to
  `type: webhook`.

### D3. Renderer and send-time key (`pagerduty.go`)

`newPagerDutySink` matches `newTeamsSink`: config, collector info and the
shared `http.Client` from `newSinkWithEnv`. The endpoint comes from a small
`pagerDutyEndpoints` map keyed by region. The struct holds the resolved
endpoint so a test can point it at an `httptest.Server`; no config key
exposes that.

`Render` builds an `pdRequest` struct and marshals it with `marshalJSON`
(no HTML escaping, as for the webhook). `pdRequest.RoutingKey` is
`json:"routing_key,omitempty"` and `Render` leaves it empty, so the queued
body has no key.

`Send` resolves the key (`secretUnavailable` on error, which retries and
names the sink and variable), unmarshals the queued body into `pdRequest`,
sets `RoutingKey`, marshals again and calls `httpPost` with
`Content-Type` and `User-Agent`. The round trip is one small struct and keeps
the "body is what was rendered" property: everything but the key is the
queued JSON. The key is read on every attempt, so rotation needs no restart,
as with URLs.

Alternative considered: render the key into the body and redact it in the
state file. That makes `queue.go` aware of one sink's body shape and still
leaves the key in memory-queue dumps and any future log of a body.

`MinInterval` is `0`. PagerDuty documents a per-key rate limit and answers
`429`; the existing retry and `Retry-After` handling is the control. A mass
outage is already shrunk by dependency suppression, which sends one event for
the parent.

The sink does not implement `mentionRenderer`, and `groupsMessages` and
`messageSink` stay false, so `worker.grouped` is false and `groupCap` is 1
(one event per request, in queue order).

### D4. Event mapping and dedup key

| tail-watch event | `event_action` | notes |
| --- | --- | --- |
| `target.down` | `trigger` | `payload` built from the event |
| `target.recovered` | `resolve` | no `payload`; `reason: removed` too |
| `test` | `trigger`, then `resolve` | see D5 |

`dedup_key` is `<target name>/<check>`, as ROADMAP.md specifies. Target names
match `[A-Za-z0-9._-]`; a check string can be long (an `http:` check with a
URL and status list) and PagerDuty caps `dedup_key` at 255 characters. When
the string is longer, the check part becomes the first 16 hex characters of
its SHA-256. Both `trigger` and `resolve` run the same function, and a
recovered event keeps the check its down tripped (`newRecoveredEvent` copies
`down.Check`), so the keys match without extra state.

`trigger` payload:

- `summary`: `<target> <check> DOWN (<layer>): <error>`, cut to 1024 bytes,
  PagerDuty's limit.
- `source`: target name. `component`: check. `group`: site, omitted when empty.
  `class`: layer.
- `severity`: `critical`, `warning` or `info` from `page`, `warn` or `info`.
- `timestamp`: the event time, RFC 3339 UTC. A late event (`late: true`) keeps
  its original time, so PagerDuty shows when the outage began.
- `custom_details`: a JSON object with address, parent, path (kind, endpoint,
  latency), error (300 bytes plus `…`, the same `truncate` Slack uses),
  `since`, `failures`, `dependents`, `event_id`, `collector`, and
  `implicit` for a learned target.
- `links`: `[{href: "<status_url>/#<name>", text: "tail-watch monitor"}]` when
  `status_url` is set, trailing slashes trimmed.

Flapping is not mapped. A `trigger` for a flapping target has no recovery
event, so the alert would stay open until a person closed it. The `events`
allow-list (D2) turns that into a config error rather than a silent skip.

### D5. `notify-test`

A `test` event cannot be a no-op: the point is to prove the key reaches a real
integration. The PagerDuty sink renders a `test` event as a `trigger` with
`severity: info`, `dedup_key` `tail-watch-test/<collector name>` and a
summary that says it is a tail-watch test. `Send` recognizes the
`tail-watch-test/` prefix, posts the trigger and, on `2xx`, posts a `resolve`
with the same key. A non-`2xx` trigger result is returned as is and no resolve
is sent. A failed resolve returns its result with an `Err` that names the
resolve and the key, so the operator can close the alert by hand.

The retry loop may run `Send` again after a transient resolve failure. The
repeated trigger merges into the same alert and the resolve is sent again, so
the pair stays idempotent.

An operator may see a brief page: PagerDuty can notify on the `trigger`
before the `resolve` arrives. The how-to says to run `notify-test` against a
service with no escalation policy, or at a time the on-call person expects it.

Alternative considered: the Change Events API (`/v2/change/enqueue`). It never
pages, but it is a different endpoint and body, and it does not prove the
Events API trigger path the sink uses in production. A `400` for a bad
`severity` or `dedup_key` would pass the test and fail in an outage.

### D6. Tests

Golden files in `cmd/tail-watch/testdata/pagerduty/`, regenerated with
`go test ./cmd/tail-watch -run 'TestPagerDutyGolden' -update`. Cases: down at
layer `service`, down at `warn` severity, an implicit router with 14
dependents, recovered, recovered with `reason: removed`, a 301-byte error, a
long check that triggers the hash form, a `status_url` link with a trailing
slash, and the `test` trigger. The golden body has no `routing_key`.

`httptest.Server` tests: the request has the key (and the queued body does
not), `202` is delivered, `400` is permanent and counted, `429` with
`Retry-After: 2` waits at least 2 seconds under `tstest.Clock` and resends the
same body, `503` retries, a rotated key file is picked up, and an unset
variable at send time retries with an error that omits the value. A
`queue: disk` test asserts the key is absent from `notification-state.json`.
`notify-test` tests cover trigger then resolve, a rejected trigger sending no
resolve, and a rejected resolve exiting 1.

Config table rows: both key sources, neither, `url_env` on PagerDuty,
`routing_key_env` on webhook, `events` with `target.flapping`, `region: apac`,
`group_wait`, and a valid sink that `check-config` prints as `type=pagerduty`
without the key.

## Risks / Trade-offs

- [A dropped recovery leaves an alert open] → The default `max_age` is 1h. A
  recovery that expires, or a collector that is off for longer than the
  window, leaves the PagerDuty alert open until a person resolves it. The
  how-to recommends a longer `max_age` and `queue: disk` for this sink, and
  the expired-recovery drop is already counted in
  `tailwatch_notify_dropped_total`.
- [Two collectors share one alert] → Same `dedup_key`; the first recovery
  resolves it. Documented in the how-to; use a separate routing key per
  collector when this matters.
- [A mass outage hits the PagerDuty rate limit] → `429` is retried with
  backoff and `Retry-After`. Dependency suppression sends one event for a
  failed parent.
- [`notify-test` pages someone] → See D5; documented. The trigger is `info`
  severity and resolved within one request round trip.
- [The routing key leaks] → Never rendered, injected at send time, absent from
  the state file, logs and errors (tested). The `*_env` variable still holds
  it in the process environment, as for every other secret.
- [Rollback of the binary] → An old binary rejects `type: pagerduty` at load.
  Remove the sink before rolling back. Adding the sink is opt-in, so existing
  configs are unaffected.

## Migration Plan

1. Ship the sink. Existing configs load as before.
2. An operator creates an Events API v2 integration on a PagerDuty service,
   stores its integration key in `routing_key_env` or `routing_key_file`, adds
   `type: pagerduty`, and runs `notify-test`.
3. Rollback of the config is deleting that sink. Rollback of the binary
   requires that deletion first. Open PagerDuty alerts from before the
   rollback close only when a person resolves them.
