# Tasks

## 1. Config schema (`cmd/tsmon/config.go`)

- [ ] 1.1 Add `SinkConfig`, `MatchConfig`, `HeartbeatConfig`, `secretRef` and the `sinks`/`heartbeat` fields to `Config`; verify decoding the design doc's full `sinks`/`heartbeat` example succeeds with `KnownFields(true)`
- [ ] 1.2 Add sink and heartbeat defaults (`min_severity: info`, all events, `max_age: 1h`, `queue: memory`, `queue_size: 1000`, `group_wait: 10s`, `max_batch: 100`, `heartbeat.interval: 1m`); verify a table test of resolved values
- [ ] 1.3 Add validation rules from design D12 to the error-collecting pass; verify one table case per rule plus a multi-error case, including `queue: disk` without `state_dir` and the reserved sink names `incidents`, `intake` and `state`
- [ ] 1.4 Implement `secretRef` resolution (env, file with whitespace trim, unset/empty/unreadable errors) and `redactURL`; verify tests with `t.Setenv` and temp files, and that `redactURL("https://hooks.slack.com/services/T0/B0/x")` returns `https://hooks.slack.com/…`
- [ ] 1.5 Extend `check-config` to print sinks (name, type, filters, queue mode) without secret values; verify a test asserting a set secret value never appears in the output

## 2. Events and routing (`cmd/tsmon/notify.go`)

- [ ] 2.1 Implement the `evt_`/`bat_` id generator (D2); verify tests for length 26 after prefix, Crockford alphabet, and lexical order across milliseconds using `tstest.Clock`
- [ ] 2.2 Define `Event` with JSON tags per the payload contract; verify golden-file tests (`testdata/payload/*.json`) for `target.down`, `target.recovered` (with and without `"reason": "removed"`), `target.flapping`, `collector.started` and `test`
- [ ] 2.3 Implement the Transition→Event mapping table from D1, severity rules and collector identity; verify table tests over synthetic `Transition`s including `UNKNOWN -> DOWN` (pages), `SUPPRESSED -> DOWN`, `SUPPRESSED -> UP` with an open incident, `UNKNOWN -> UP` (no event), `* -> SUPPRESSED` (no event), each layer value including `unrouted`, `layer` omitted on recovered, and `"implicit": true` for an implicit target (golden file)
- [ ] 2.4 Implement routing filters (`events`, `min_severity`, `match.sites`, `match.targets` globs, `match.tags` via `presence.TagsFor`) and the `test` bypass; verify table tests for each filter and a collector.started event ignoring `match`
- [ ] 2.5 Emit `collector.started` on `FirstRoundDone()`; verify with a fake signal that exactly one event is emitted

## 3. Queues (`cmd/tsmon/queue.go`)

- [ ] 3.1 Implement `memQueue` (bounded ring, drop-oldest, `Peek`/`Ack`/`Len`); verify tests for ordering and overflow counting `queue_full`
- [ ] 3.2 Implement the versioned `notification-state.json` coordinator in `queue.go` and `incidents.go`: atomic incident+delivery-intent snapshot, temp/fsync/rename/directory-fsync, permissions 0700/0600; verify workers cannot send disk intents before commit and all selected disk sinks commit together.
- [ ] 3.3 Implement replay of the atomic snapshot in `queue.go`: ordered pending intents, committed attempt/batch identity, expiry/overflow plus eligibility updates; verify crashes before rename, after commit before worker distribution, and after `2xx` before acknowledgment commit, for both down and recovery; corrupt committed state must fail startup.
- [ ] 3.4 Implement failed-commit fallback in `queue.go` and `incidents.go`: retain event and incident mutations together in memory, count `sink="state"`, retry full-state persistence and log degradation; verify delivery during failed writes and restoration of durability after writes recover.
- [ ] 3.5 Warn when `state_dir` is group- or world-readable; verify a test with a 0755 directory captures the warning
- [ ] 3.6 Verify no secret reaches disk: test that after queueing to a signed webhook sink with an auth header, no file under `state_dir` contains the URL, secret or header value

## 4. Dispatcher and delivery loop (`cmd/tsmon/notify.go`)

- [ ] 4.0 Implement non-blocking 4096-slot intake in `notify.go`, marking dirty sequences on overflow; reconcile snapshots after capacity returns even without new transitions and ignore stale queued sequences; verify dropped recovery plus permanently UP target eventually gets its resolve, and concurrent newer dirty state remains pending.
- [ ] 4.1 Implement per-sink workers and the shared retry loop (D5 schedule, `Retry-After`, `max_age` expiry, late flag fixed at first attempt); verify under `tstest.Clock` that delays follow 1s, 2s, 4s… capped at 5m and that `Retry-After: 30` delays at least 30s
- [ ] 4.2 Verify worker network isolation in `notify_test.go` with a blocked receiver and another delivering sink; separately test/document serialized snapshot commits delay all durable enqueues while checks and non-blocking intake continue.
- [ ] 4.3 Verify per-sink ordering: a test where `down` is retried and `recovered` is queued delivers `down` first
- [ ] 4.4 Register `tsmon_notify_sent_total`, `tsmon_notify_failed_total`, `tsmon_notify_dropped_total`, `tsmon_notify_queue_len`, `tsmon_notify_latency_seconds`, `tsmon_notify_batch_size`, `tsmon_notify_persist_errors_total`; verify with `prometheus/testutil` counts after a scripted delivery run

## 5. Incidents (`cmd/tsmon/incidents.go`)

- [ ] 5.1 Implement open/closing incidents and delivery records keyed by event ID in `incidents.go`, with at most one open incident per target; commit closing state and recovery intents together; verify a new outage can open while old recovery retries, and closing incidents are never restored as DOWN.
- [ ] 5.2 Implement per-sink pending/attempted/acknowledged/dropped state and recovery eligibility in `notify.go`/`incidents.go`; verify recovery during down retries, filtered sink exclusion, never-attempted down drops, permanent failure after attempt, and acceptance with lost HTTP response.
- [ ] 5.3 Implement startup restoration in `incidents.go`/`tsmon.go` using persisted implicit target definition and stable node identity before scheduling; retain implicit incidents during failed discovery, close them only on authoritative removal, and close explicitly removed configured targets; verify restart with unavailable presence, later rediscovery without duplicate down, actual removal and restore-refusal deduplication.

## 6. Webhook sink (`cmd/tsmon/webhook.go`)

- [ ] 6.1 Implement signed POST in `webhook.go` by writing timestamp, dot and exact body to HMAC then hex-encoding `Sum(nil)`; verify an independently computed fixed signature vector, changed timestamp/body, wrong secret, stale timestamp and real receiver validation.
- [ ] 6.2 Implement result classification; verify the retry matrix against `httptest.Server`: `500` then `200` retries, `429` with `Retry-After` retries after the delay, timeout retries, `408` retries as `timeout`, `400` and `302` do not retry and count `permanent`
- [ ] 6.3 Implement batched mode in `webhook.go` with stable retry bytes/ID persisted before first disk attempt; verify 250 events split 100/100/50, restart during retry preserves body and ID, arrivals form later batches, and expiry of any attempted member drops/counts the whole batch.
- [ ] 6.4 Document the webhook sink, payload, signature verification (with a Go snippet) and batched mode in `README.md`, and add a webhook sink to `examples/tsmon.yaml`; verify markdownlint passes and `tsmon check-config --config examples/tsmon.yaml` exits 0

## 7. Slack sink (`cmd/tsmon/slack.go`)

- [ ] 7.1 Implement Block Kit rendering with text fallback, layer label, `(learned)` suffix for implicit targets, dependents line (up to 10 names), `status_url` links and `mention_on_page` only on page downs; verify golden-file tests (`testdata/slack/*.json`) for down, recovered, router with 14 dependents, warn target with mention configured
- [ ] 7.2 Implement grouping (10 lines + "and N more") and the 1 msg/s limiter on the shared retry loop; verify under `tstest.Clock` that 25 events inside `group_wait` become one message with 10 lines and "and 15 more"
- [ ] 7.3 Document the Slack sink in `README.md` and add one to `examples/tsmon.yaml`; verify markdownlint and `check-config` pass

## 8. Heartbeat and notify-test (`cmd/tsmon/heartbeat.go`, `cmd/tsmon/notifytest.go`)

- [ ] 8.1 Implement the heartbeat ticker (starts on `FirstRoundDone()`, `GET`, 10s timeout, `tsmon_heartbeat_failures_total`, stops on shutdown); verify under `tstest.Clock` with an `httptest.Server` that 5 intervals yield 5 requests and a failing receiver increments the counter
- [ ] 8.2 Implement `tsmon notify-test [--sink] [--config] [--timeout]` using real sinks and the shared retry loop, no queues or `state_dir`, OS-hostname fallback when tailscaled is unreachable; verify tests: two `200` sinks exit 0; one `401` sink exits 1; unknown `--sink` lists sink names
- [ ] 8.3 Document heartbeat, notify-test, queue modes, `state_dir`, open incidents, alerting on `tsmon_notify_dropped_total`/`tsmon_notify_queue_len`, and the egress guidance (no exit node; tailnet receivers need a `tag:monitor` grant) in `README.md`; verify markdownlint passes

## 9. Wiring (`cmd/tsmon/tsmon.go`)

- [ ] 9.1 Construct the incident store and notifier before the prober, register the notifier as the alert machine's `transitionConsumer`, start the heartbeat, and stop workers on shutdown; verify `tsmon --once` still works with sinks configured and emits no notifications
- [ ] 9.2 Add `notify-test` to subcommand dispatch and `--help`; verify `go run ./cmd/tsmon notify-test --help` prints its flags

## 10. Validation

- [ ] 10.1 Run `go build ./...`, `go vet ./...`, `go test -race ./...`; all pass
- [ ] 10.2 Run `golangci-lint run ./...` and `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`; zero issues and no reachable vulnerabilities
- [ ] 10.3 Run markdownlint on changed Markdown and `openspec validate add-notifications --strict`; both pass
- [ ] 10.4 On a real tailnet, run `tsmon notify-test` against a real Slack channel and a webhook receiver that verifies signatures, then stop a monitored service and confirm one down and one recovery in both, and one recovery after restarting tsmon mid-outage with `queue: disk`; record results in the PR description
