# Proposal

## Why

After `add-alert-state-machine`, tail-watch knows when a target goes down, recovers
or flaps, but nobody hears about it. Operators need those state changes
delivered to the places they already watch (ticketing webhooks, n8n or Zapier
flows, Slack) without false pages, lost resolves, or silent failures when the
collector itself restarts or loses its uplink.

This is the fourth and last change of phase 1 (MVP). It depends on
`add-tsmon-collector` (config, runtime, metrics endpoint) and
`add-alert-state-machine` (state transitions). It uses target tags from
`add-failure-classification` for sink filters.

## What Changes

- Turn every alert state transition into a versioned JSON event
  (`target.down`, `target.recovered`, `target.flapping`, `collector.started`,
  `test`; `target.degraded` is reserved for phase 2).
- Add a generic webhook sink: HMAC-SHA256 `Tail-Watch-Signature`, `headers_env`,
  retry policy by status class, `max_age`, 10s request timeout, and an optional
  batched mode.
- Add a Slack incoming-webhook sink: Block Kit with a plain-text fallback,
  `mention_on_page`, `group_wait` grouping, and `status_url` links.
- Add a dispatcher with one goroutine and one bounded queue per sink, in-order
  delivery, drop-oldest overflow, and a `late` flag for delayed delivery.
- Add `queue: memory | disk` per sink, with a crash-safe on-disk layout under
  `collector.state_dir` and at-least-once delivery.
- Commit incidents, per-sink delivery state and disk-delivery intents together
  in `state_dir/notification-state.json`; replay committed work after restart
  with stable IDs, subject to explicit expiry/overflow/failure policies.
- Reconcile overflowed target state and retain unresolved implicit incidents
  until authoritative topology discovery confirms removal.
- Add an optional `heartbeat` that pings a dead-man's-switch URL.
- Add the `tail-watch notify-test [--sink NAME]` subcommand.
- Accept the `sinks` and `heartbeat` config keys; secrets only via `*_env` or
  `*_file` keys.
- Export the `tailwatch_notify_*` metrics and `tailwatch_open_incidents`.

Implements design-doc section "Notifications" in full (Events, Payload,
Generic webhook, Batched mode, Slack, Delivery, Queue modes, Open incidents,
Egress path, Watching the watcher, Trying it out), the `sinks` and `heartbeat`
parts of "Config", and the notification series of "Metrics".

Non-goals (later phases):

- Phase 2: PagerDuty (Events API v2), Slack bot-token mode with threaded
  recoveries and edited originals, emitting `target.degraded`.
- Phase 3: sharing incident state across collectors for quorum.
- Any sink type other than `webhook` and `slack`.

## Capabilities

### New Capabilities

- `notification-events`: event types, severities, the JSON payload contract,
  and per-sink routing filters (`events`, `min_severity`, `match`).
- `webhook-sink`: generic webhook delivery, signing, extra headers, retry and
  expiry policy, and batched mode.
- `slack-sink`: Slack incoming-webhook rendering, mentions, grouping and status
  links.
- `notification-delivery`: dispatcher, per-sink queues and ordering, memory and
  disk queue modes, open-incident tracking, the `late` flag, secret handling,
  and the notification metrics.
- `collector-heartbeat`: the optional dead-man's-switch ping.
- `notify-test`: the `tail-watch notify-test` subcommand.

### Modified Capabilities

None. `collector-config` still lives only in the unarchived
`add-tsmon-collector` change, so sink and heartbeat config requirements are
specified in this change's own capabilities.

## Impact

- Code: new `cmd/tail-watch/notify.go`, `queue.go`, `incidents.go`, `webhook.go`,
  `slack.go`, `notifytest.go`, `heartbeat.go` and tests; `config.go` gains the
  `sinks` and `heartbeat` schema; `main.go` wires the dispatcher, heartbeat
  and the new subcommand.
- Config: new top-level keys `sinks` and `heartbeat`; `collector.state_dir`
  becomes meaningful.
- Disk: when `state_dir` is set, tail-watch writes one atomic notification snapshot
  there (directory mode 0700, file mode 0600). It contains internal hostnames and
  addresses, never secrets.
- Network: outbound HTTPS to Slack and webhook receivers following the host
  routing table.
- Dependencies: none new. Event IDs use a small in-repo ULID-style generator.
- Public contract: the webhook JSON payload (`version: 1`) and the
  `Tail-Watch-Signature` scheme become a compatibility promise.
