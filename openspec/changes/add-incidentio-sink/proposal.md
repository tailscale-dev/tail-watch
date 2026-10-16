# Proposal

## Why

Operators who page through incident.io can reach tail-watch only through the
generic `webhook` sink and a flow they build themselves. incident.io does not
accept tail-watch's event JSON. Its HTTP alert source wants a `title`, a
`status` of `firing` or `resolved`, and a `deduplication_key`, and it only
closes an alert when a later `resolved` event carries the same
`deduplication_key` as the `firing` one. Without that, a recovery in
tail-watch leaves the alert open in incident.io until a person closes it.

## What Changes

- Add a `type: incidentio` sink that POSTs one event per request to
  `https://api.incident.io/v2/alert_events/http/<alert_source_id>`.
- Map `target.down` to `status: firing` and `target.recovered` to
  `status: resolved`. Both carry `deduplication_key` `<target>/<check>`, so a
  recovery resolves the alert its down opened.
- Send the alert source token in an `Authorization: Bearer` header. Read it
  from `token_env` or `token_file`. It is never part of a queued body, so it
  is not written to `notification-state.json`.
- Take the alert source config id from `alert_source_id`. It is not secret; it
  is part of the URL path.
- Carry the target's address, site, parent, layer, path, error, failure count
  and dependents in `metadata`, a markdown `description`, and a `source_url`
  link built from `status_url`.
- Send one event per request. There is no grouping, so `group_wait`, `batch`
  and `max_batch` do not apply.
- Deliver only `target.down` and `target.recovered`. An `incidentio` sink
  rejects any other value in `events`, because a `firing` event for a flapping
  target would open an alert that no recovery ever resolves.
- Make `notify-test` fire and immediately resolve a test alert, so it
  exercises the real token and alert source without leaving an alert behind.
- Document the sink and add an example.

The token is a `secretRef` (`cmd/tail-watch/config_sinks.go`), so it inherits
every source `secretRef` supports. This change does not add one. It is
independent of `add-setec-secrets`, which extends `secretRef` with a setec
source: this change can land before or after it, and needs no edit either way.

This adds a ROADMAP.md near-term item, "incident.io sink", and ticks it. It
follows the delivery pattern in `webhook-sink` and `slack-sink`, the retry,
`Retry-After` and permanent-failure rules in `notification-delivery`, and the
firing/resolved lifecycle reasoning of the PagerDuty change
(`add-pagerduty-sink`).

Non-goals:

- Phase 2 items that stay open: Slack bot-token mode, emitting
  `target.degraded`, `tsnetEnv`, `discover:` and the TLS expiry check.
- Refactoring the PagerDuty sink. The firing/resolved pair logic (dedup key
  helper, `notify-test` fire-then-resolve, down/recovered mapping) is a
  candidate to share between the two sinks. This change copies the small
  amount it needs and flags the extraction as an open question in design.md.
- The incident.io REST API for incidents, schedules, escalations or any call
  that needs an account API key. Only the HTTP alert source endpoint is used,
  with that alert source's own token.
- Passing the token as the `token` query parameter, or using Basic auth.
  Query strings are logged by proxies; the header is the only supported form.
- Alert source attributes, priorities, teams or escalation paths. Those are
  configured in incident.io against the alert source.
- A regional or self-hosted endpoint. incident.io documents a single base URL.
- The setec secret source itself (`add-setec-secrets`).
- Sharing alert state across collectors (CONTEXT.md boundary). Two collectors
  that watch the same target and check use the same `deduplication_key` and so
  share one alert, which is incident.io's behavior, not tail-watch state.

## Capabilities

### New Capabilities

- `incidentio-sink`: incident.io sink configuration, HTTP alert event request
  format, dedup and resolve behavior, `notify-test` behavior, and the shared
  delivery policy.

### Modified Capabilities

- `notification-delivery`: the "Secret handling" requirement names sink URLs,
  HMAC secrets and header values. It adds the incident.io token, which
  travels in an `Authorization` header and is therefore read at send time and
  never queued or persisted.

`notify-test` needs no requirement change: it already sends through each
sink's real delivery code, and the fire-then-resolve pair is specified in
`incidentio-sink`.

## Impact

- Code: new `cmd/tail-watch/incidentio.go` and `incidentio_test.go`.
  `config_sinks.go` accepts `type: incidentio`, the `alert_source_id`,
  `token_env` and `token_file` keys, relaxes the "exactly one of `url_env` or
  `url_file`" rule for this type, and validates `events`. `checkSecrets` reads
  the token. `sink.go` constructs the sink. `notifytest.go` needs no new
  branch because the sink performs the resolve.
- Config: `type: incidentio` plus `alert_source_id`, `token_env` /
  `token_file`, `status_url`, and the shared routing and queue keys.
- Network: outbound HTTPS to `api.incident.io` through the shared sink client
  (no redirect following, 10s timeout).
- Dependencies: none. The public event payload is unchanged. Ordering with
  `add-setec-secrets`: none; either may land first.
- Docs: a how-to, the sink tables in the config reference, the docs index, the
  troubleshooting table, the example config, `CLAUDE.md` file list, and a
  `ROADMAP.md` item.
- UI: unchanged. `GET /sinks` already returns the configured `type`.
