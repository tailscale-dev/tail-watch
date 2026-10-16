# Design

## Context

See proposal.md for why. The behavior contract is
`specs/incidentio-sink/spec.md`, plus the "Secret handling" delta in
`specs/notification-delivery/spec.md`.

Webhook, Slack and Teams share one delivery loop (`retryLoop` in
`deliver.go`) behind the `sink` interface: `Render(evs, unitID)` builds a body
when an event is queued, and `Send(ctx, body, meta)` makes one request.
`httpPost` in `sink.go` does the POST (10s timeout, no redirects, `redactURL`
on errors), and `classifyStatus` maps `2xx` to delivered, `408`/`429`/`5xx` to
retry (with `Retry-After`) and other `4xx` to permanent.

### Verified API facts

Checked against the incident.io API reference on 2026-10-16:

- `POST https://api.incident.io/v2/alert_events/http/{alert_source_config_id}`,
  `Content-Type: application/json`. No regional endpoint is documented.
- Auth is the alert source's own secret, not an account API key. It is
  accepted as `Authorization: Bearer <secret>`, as Basic credentials, or as a
  `token` query parameter. The OpenAPI operation declares no security scheme.
- Body: `title` (string, required), `status` (`firing` or `resolved`,
  required), `deduplication_key` (optional in the schema, required for newly
  created HTTP sources), `description` (string, markdown), `metadata` (free
  form object), `source_url` (string).
- Success is `202 Accepted` with `{status, message, deduplication_key}`.
  Errors carry `type`, `status`, `request_id`, `errors`: `400`, `401`, `403`,
  `404`, `413`, `422`, `429`, `5xx`.
- A `resolved` event with the key of an active alert resolves it. Repeated
  `firing` events with one key create one alert while it is active.
- Rate limit: 1200 requests per minute per API key by default, token bucket
  refilling 20 per second, "some endpoints lower"; `429` carries
  `Retry-After` (seconds), plus `X-RateLimit-*` headers.

### Not verified

- This endpoint's own rate limit (the docs give only the default).
- `Retry-After` format. The reference says seconds; the introduction page's
  sample `429` body shows `retry_after` as a UTC date string. See D4.
- What `resolved` does with no active alert, a second `resolved`, or a
  `firing` sent after a `resolved` (new alert versus reopen). The dedup guide
  is silent. The design relies only on the documented pairing and treats
  everything else as unspecified (D5).
- Maximum length of `deduplication_key`, `title` and `description`, and
  whether `title` is required on a `resolved` event (the schema says yes).
- Which status `401` versus `404` incident.io returns for a wrong token versus
  a wrong alert source id.

## Goals / Non-Goals

**Goals:**

- One new renderer on the existing sink interface, client and retry loop.
- A token that never reaches a queued body, the state file or a log.
- A golden request body a reviewer can diff.

**Non-Goals:**

- A live incident.io account in CI. `notify-test` against the operator's
  alert source is the manual check. An opt-in live test is not part of this
  change.
- Changing `GET /sinks` or the monitor UI. `sinkViewOf` already returns the
  type and filters.
- Refactoring `pagerduty.go`. See open question Q1.
- Designing the setec source for `secretRef`.

## Decisions

### D1. Boundaries and the old design doc

No CONTEXT.md boundary is crossed:

- *Reports failures; never acts on them.* An alert reports. Only `firing` and
  `resolved` are sent, and `resolved` mirrors tail-watch's own recovery.
- *Secrets only in `*_env` / `*_file`; never logged, persisted or shown.* The
  token is a `secretRef` set by `token_env` / `token_file`, read at send time
  (D3). Any future `secretRef` source (for example setec) applies unchanged.
- *No shared state between collectors.* The `deduplication_key` makes two
  collectors that watch the same target and check merge into one alert, and
  the first recovery resolves it for both. That is incident.io's dedup, and
  the how-to says so. Use a separate alert source per collector, or a distinct
  target name, when that is not wanted.
- *No credentials for discovery.* The alert source token only writes events.

This does contradict one thing in the current specs: `webhook-sink`,
`slack-sink` and `teams-sink` each say their type requires exactly one of
`url_env` or `url_file`. Those are per-type requirements, so they stay true.
The shared parser's unconditional check becomes per-type (D2), the same
relaxation `add-pagerduty-sink` makes. The generic "Secret handling"
requirement is modified.

Ordering with `add-pagerduty-sink`: both modify "Secret handling" and the
per-type URL check. Whichever lands second rebases its delta; the edits are
additive (one more named secret, one more type in the unknown-type error).

### D2. Config

`sinkIncidentIO = "incidentio"`. `rawSink` gains `AlertSourceID`
(`alert_source_id`), `TokenEnv` (`token_env`) and `TokenFile` (`token_file`).
`SinkConfig` gains `AlertSourceID string` and `Token secretRef`.

- The type switch accepts it, and the unknown-type error lists the new type
  alongside the others.
- The "exactly one of `url_env` or `url_file`" check moves behind a per-type
  rule. For `incidentio` the checks are: `alert_source_id` set, and exactly
  one of `token_env` or `token_file`. `url_env` / `url_file` are rejected. For
  the other types, `alert_source_id`, `token_env` and `token_file` are
  rejected.
- `alert_source_id` must match `[A-Za-z0-9_-]+`, so it cannot alter the URL
  path. It is not a secret and appears in `check-config` output and logs.
- `resolveSinkTypeKeys` rejects `secret_env`, `secret_file`, `headers_env`,
  `batch`, `max_batch`, `group_wait` and `mention_on_page`: one event per
  request, no HMAC (auth is the bearer token), and no mentions.
  `status_url` stays valid and becomes `source_url`.
- There is no `url` key: the host is fixed (a non-goal in the proposal), so an
  arbitrary URL would need the secret-URL handling Slack and Teams have.
  Tests point the sink at an `httptest.Server` through an unexported field.
- `events` defaults to `[target.down, target.recovered]` and rejects any other
  value. `test` events bypass the filter, as for every sink.
- `checkSecrets` resolves `Token` for an incident.io sink, so an unset
  variable stops startup and fails `notify-test` before any request.
  `check-config` does not read secrets, as today, and `describeSink` prints
  `type=incidentio` and the alert source id, never the token.

### D3. Renderer and send-time token (`incidentio.go`)

`newIncidentIOSink` matches `newTeamsSink`: config, collector info and the
shared `http.Client` from `newSinkWithEnv`.

`Render` builds an `iioRequest` struct (`title`, `status`,
`deduplication_key`, `description`, `metadata`, `source_url`) and marshals it
with `marshalJSON`. Unlike the PagerDuty routing key, the token is not in the
body, so the rendered body is exactly what is sent and no round trip is
needed. The queued body holds nothing secret by construction.

`Send` resolves the token on every attempt (`secretUnavailable` on error,
which retries and names the sink and variable), builds the URL from the fixed
base and `alert_source_id`, sets `Authorization: Bearer <token>`,
`Content-Type` and `User-Agent`, and calls `httpPost`. Rotation needs no
restart. `httpPost` errors already redact; the test confirms the header value
never appears in an error. The URL carries the alert source id, which is not
secret, so `redactURL` still reduces it to scheme and host in logs.

`MinInterval` is `0`. The documented default limit (1200 per minute) is far
above tail-watch's rate, and dependency suppression sends one event for a
failed parent. The existing `429` and `Retry-After` handling is the control.
No `mentionRenderer`; `groupsMessages` and `messageSink` stay false, so
`groupCap` is 1.

### D4. Retry-After

`classifyStatus` already parses `Retry-After`. If it accepts only delta
seconds, a date-formatted value is treated as absent and the normal backoff
applies, which is safe. Task 3.1 includes a test with both forms. If the
existing parser rejects the HTTP-date form and a fix is warranted, that is a
change to shared code and is raised with the user rather than made here.

### D5. Event mapping and dedup key

| tail-watch event | `status` | notes |
| --- | --- | --- |
| `target.down` | `firing` | `title`, `description`, `metadata` from the event |
| `target.recovered` | `resolved` | same `deduplication_key`; also `reason: removed` |
| `test` | `firing`, then `resolved` | see D6 |

`deduplication_key` is `<target name>/<check>`, built by the same rule as the
PagerDuty `dedup_key`: when the string is longer than 255 bytes the check part
becomes the first 16 hex characters of its SHA-256. incident.io documents no
key length limit, so 255 is a conservative cap that costs nothing for normal
checks. Both statuses use the same function and a recovered event keeps the
check its down tripped, so keys match without extra state.

Fields:

- `title`: `<target> <check> DOWN (<layer>): <error>` for firing, and
  `<target> <check> recovered` for resolved. `title` is required on both. Cut
  to 500 bytes (a self-imposed bound; no limit is documented).
- `description`: markdown with the address, error (300 bytes plus `…`, the
  same `truncate` Slack uses), down-since time and dependents.
- `metadata`: object with `target`, `check`, `address`, `site`, `parent`,
  `layer`, `severity`, `path`, `failures`, `dependents`, `event_id`,
  `collector` and `implicit`, omitting empty values. Which keys become alert
  attributes is decided by the operator's alert source configuration.
- `source_url`: `<status_url>/#<name>` when `status_url` is set, trailing
  slashes trimmed.

The unspecified cases in "Not verified" are tolerated: a `resolved` for an
alert incident.io never saw, or one sent twice, is at worst ignored or answered
with a `4xx`. A `4xx` is permanent and counted, and does not block later
events.

Severity has no incident.io field here. Routing by tail-watch severity stays
in tail-watch (`routes`, `severity`), and priority mapping is an alert source
setting. It is carried in `metadata.severity`.

### D6. `notify-test`

Same shape as the PagerDuty D5. A `test` event renders as `firing` with
`deduplication_key` `tail-watch-test/<collector name>` and a title that says
it is a tail-watch test. `Send` recognizes the prefix, posts the `firing`
event and, on `2xx`, posts `resolved` with the same key. A non-`2xx` result
for the first request is returned as is and no resolve is sent. A failed
resolve returns its result with an `Err` naming the resolve and the key, so
the operator can close the alert by hand. A retry after a transient resolve
failure repeats the firing event (merged into the same alert) and then the
resolve, so the pair is idempotent.

An operator may see a brief notification from the `firing` event before the
`resolved` arrives. The how-to says to point the test at an alert source with
no escalation, or to warn the on-call person.

### D7. Tests

Golden files in `cmd/tail-watch/testdata/incidentio/`, regenerated with
`go test ./cmd/tail-watch -run 'TestIncidentIOGolden' -update`. Cases: down at
layer `service`, a `warn` down, an implicit router with 14 dependents,
recovered, recovered with `reason: removed`, a 301-byte error, a long check
that triggers the hash form, a `status_url` with a trailing slash, and the
`test` firing event. A golden file holds the body only.

`httptest.Server` tests: the request has `Authorization: Bearer <token>` and
path `/v2/alert_events/http/<id>`, and the queued body does not contain the
token; `202` is delivered; `401` and `422` are permanent and counted; `429`
with `Retry-After: 2` waits at least 2 seconds under `tstest.Clock` and
resends the same body; `503` retries; a rotated token file is picked up; an
unset variable at send time retries with an error that omits the value. A
`queue: disk` test asserts the token is absent from
`notification-state.json`. `notify-test` tests cover firing then resolved, a
rejected firing sending no resolve, and a rejected resolve exiting 1.

Config table rows: both token sources, neither, no `alert_source_id`, a bad
`alert_source_id`, `url_env` on incident.io, `token_env` on a webhook,
`events` with `target.flapping`, `group_wait`, and a valid sink that
`check-config` prints without the token.

## Open Questions

- Q1. Share the firing/resolved pair logic with `pagerduty.go` (dedup key
  helper, fire-then-resolve `notify-test` driver, down/recovered mapping)? It
  is about 60 lines duplicated. This change copies it so neither change blocks
  the other; extract it afterwards if a third alerting sink arrives.
- Q2. Config key names: `alert_source_id` and `token_env` / `token_file`.
  Should the token keys be `routing_key_*`-style for symmetry with PagerDuty?
  The proposed names follow incident.io's vocabulary ("alert source", "token").
- Q3. Is `Retry-After` ever a date? See D4.

## Risks / Trade-offs

- [A dropped recovery leaves an alert open] -> The default `max_age` is 1h.
  The how-to recommends a longer `max_age` and `queue: disk` for this sink;
  expired-recovery drops are counted in `tailwatch_notify_dropped_total`.
- [Two collectors share one alert] -> Same key; documented in the how-to.
- [Unspecified resolve semantics] -> Listed above; `notify-test` against a
  real alert source is the check, and the how-to states what was observed.
- [The token leaks] -> Never rendered, read at send time, absent from the
  state file, logs and errors (tested).
- [Rollback of the binary] -> An old binary rejects `type: incidentio` at
  load. Remove the sink first. The sink is opt-in.

## Migration Plan

1. Ship the sink. Existing configs load as before.
2. An operator creates an HTTP alert source in incident.io, copies its id and
   token, stores the token in `token_env` or `token_file`, adds
   `type: incidentio`, and runs `notify-test`.
3. Rollback of the config is deleting that sink. Rollback of the binary
   requires that deletion first. Open alerts close only when a person
   resolves them.
