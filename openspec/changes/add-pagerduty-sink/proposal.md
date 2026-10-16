# Proposal

## Why

Operators who page through PagerDuty can reach tail-watch only through the
generic `webhook` sink and a flow they build themselves. PagerDuty does not
accept tail-watch's event JSON. It wants an Events API v2 body with a
`routing_key`, an `event_action` and a `dedup_key`, and it only closes an
incident when a later `resolve` carries the same `dedup_key` as the
`trigger`. Without that, a recovery in tail-watch leaves the incident open in
PagerDuty until a person closes it.

## What Changes

- Add a `type: pagerduty` sink that POSTs one Events API v2 request per
  event to `events.pagerduty.com` (or `events.eu.pagerduty.com` with
  `region: eu`).
- Map `target.down` to `event_action: trigger` and `target.recovered` to
  `event_action: resolve`. Both carry `dedup_key` `<target>/<check>`, so a
  recovery resolves the alert its down opened.
- Map tail-watch severity to PagerDuty severity: `page` to `critical`,
  `warn` to `warning`, `info` to `info`.
- Read the integration (routing) key from `routing_key_env` or
  `routing_key_file`. The key is added to the request at send time and is
  never part of a queued body, so it is not written to
  `notification-state.json`.
- Send one event per request. There is no grouping, so `group_wait`,
  `batch` and `max_batch` do not apply.
- Deliver only `target.down` and `target.recovered`. A `pagerduty` sink
  rejects any other value in `events`, because a `trigger` for a flapping
  target would open an alert that no recovery ever resolves.
- Make `notify-test` open and immediately resolve a test alert, so it
  exercises the real key without leaving an incident behind.
- Carry the target's address, site, parent, layer, path, error, failure
  count and dependents in `custom_details`, and a `status_url` link in
  `links`.
- Document the sink and add an example.

This delivers the ROADMAP.md near-term item "PagerDuty sink". It follows the
delivery pattern in `webhook-sink` and `slack-sink`, and the retry,
`Retry-After` and permanent-failure rules in `notification-delivery`.

Non-goals:

- Phase 2 items that stay open: Slack bot-token mode, emitting
  `target.degraded`, `tsnetEnv`, `discover:` and the TLS expiry check.
- Acknowledging incidents (`event_action: acknowledge`). tail-watch reports
  failures and never acts on them.
- The PagerDuty REST API, incident notes, priorities, escalation changes or
  any API that needs an account token. Only an Events API v2 integration key
  is used.
- Change Events (`/v2/change/enqueue`). See design D5.
- Paging on `target.flapping`. A flapping target that is also down already
  has an alert; one that is up has no recovery event to resolve a new one.
- Sharing alert state across collectors (CONTEXT.md boundary). Two
  collectors that watch the same target and check use the same `dedup_key`
  and so share one PagerDuty alert, which is PagerDuty's behavior, not
  tail-watch state.

## Capabilities

### New Capabilities

- `pagerduty-sink`: PagerDuty sink configuration, Events API v2 request
  format, dedup and resolve behavior, severity mapping, `notify-test`
  behavior, and the shared delivery policy.

### Modified Capabilities

- `notification-delivery`: the "Secret handling" requirement names sink URLs,
  HMAC secrets and header values. It adds the PagerDuty routing key, and says
  that a secret which travels in a request body is injected at send time and
  never queued or persisted.

`notify-test` needs no requirement change: it already sends through each
sink's real delivery code, and the PagerDuty trigger-then-resolve pair is
specified in `pagerduty-sink`.

## Impact

- Code: new `cmd/tail-watch/pagerduty.go` and `pagerduty_test.go`.
  `config_sinks.go` accepts `type: pagerduty`, the `routing_key_env` /
  `routing_key_file` and `region` keys, relaxes the "exactly one of
  `url_env` or `url_file`" rule for this type, and validates `events`.
  `checkSecrets` reads the routing key. `sink.go` constructs the sink.
  `notifytest.go` needs no new branch because the sink performs the resolve.
- Config: `type: pagerduty` plus `routing_key_env` / `routing_key_file`,
  `region`, `status_url`, and the shared routing and queue keys.
- Network: outbound HTTPS to `events.pagerduty.com` or
  `events.eu.pagerduty.com` through the shared sink client (no redirect
  following, 10s timeout).
- Dependencies: none. The public event payload is unchanged.
- Docs: a how-to, the sink tables in the config reference, the docs index,
  the troubleshooting table, the example config, and the `ROADMAP.md` item.
- UI: unchanged. `GET /sinks` already returns the configured `type`.
