# Design

## Context

See proposal.md for motivation. This change consumes two seams from earlier
changes and adds none to them beyond what is listed under "Seams".

- From `add-tsmon-collector`: `Config` loaded by `yaml.v3` with
  `KnownFields(true)`, `Target`, `tstime.Clock` injection, the default
  Prometheus registry behind `/metrics`, and the `check-config` loader.
- From `add-alert-state-machine`: `transitionConsumer` and `Transition`:

  ```go
  type transitionConsumer interface{ ObserveTransition(Transition) }
  type Transition struct {
      Sequence    uint64
      Target      Target // immutable copy, including owned slices
      From, To    State
      Flapping    bool
      Check       string
      Layer       string
      Err         error
      Since, At   time.Time
      Failures    int
      Path        pathInfo // kind, endpoint, DERP region, latency
      Dependents  []string
      DownSince   time.Time
  }
  ```

  plus ordered immutable transitions, dirty notification-state snapshots and
  generation-aware acknowledgment, `alerts.UpdateParents(graph)`, and
  `alerts.Restore(target string, since time.Time) error` (seeds DOWN
  silently; for a name not yet registered, records a pending restore applied
  atomically when learning adds that implicit target; errors only for a
  registered target that already observed results),
  `alerts.PendingRestores()`, `alerts.DiscardPendingRestore(name)` and
  `FirstRoundDone()`, which fires when every configured target/check has one
  result. Change 3 invokes consumers through one ordered drainer outside its
  state lock and emits `UNKNOWN -> DOWN` transitions.
- From `add-failure-classification`: `Target.Implicit` (a router created at
  runtime by dependency learning, named by its short hostname), the layer
  values `unrouted`, `control`, `tunnel`, `lan`, `service`, and
  `learner.FirstPassDone()` (attempt completion only), plus authoritative
  topology reconciliation notifications.

Checked against `tailscale.com@v1.102.5`:

- `util/backoff.Backoff.BackOff(ctx, err)` sleeps `min(n²·10ms, max)` with
  0.5–1.5× jitter via `Backoff.Clock`. It has no notion of `Retry-After` and
  its first retries are 10ms, 40ms, 90ms, … (about 30 attempts in the first
  10s).
- `tstest.NewClock(tstest.ClockOpts{Start, Step, FollowRealTime})` supports
  `Advance` and timers, so grouping windows and retries can be tested without
  sleeping.
- No ULID library exists in the module graph (`go mod graph | grep -i ulid`
  is empty).

## Goals / Non-Goals

**Goals:**

- A probe never blocks on notification I/O, and `ObserveTransition` never
  blocks change 3's lock.
- Durably committed incidents retain their resolve intents across restarts,
  sink filter changes and target removal. Expiry, overflow, permanent failures
  and persistence failure remain explicit, measured limits on delivery.
- Test every timing rule (grouping, retries, expiry, late flag) with
  `tstest.Clock` and `httptest.Server`, without sleeps.

**Non-Goals:**

- Exactly-once delivery. Receivers dedupe on `id`.
- Cross-collector incident sharing (phase 3).

## Decisions

### D1. Event model and emitter (`notify.go`)

`ObserveTransition` copies an immutable transition into a bounded intake channel
(capacity 4096) without I/O or waiting. Overflow counts and logs a drop and marks
its target dirty in the alerter's generation-aware snapshot mechanism. The
alerter calls consumers outside its lock through one FIFO drainer.

One coordinator owns incident state, routing, sequence allocation, delivery
intents and persistence. It processes intake in order and reconciles dirty
snapshots within one second once commit/intake capacity returns, even without
new transitions. It ignores queued transitions at or below a target's reconciled
sequence. This may coalesce intermediate unreported blips, but a stable UP with
an open incident always creates a recovery intent. A stable DOWN without one
creates a down. Flap-damped visible state controls this reconciliation.

Normal mapping is `* -> DOWN` to a new down only without an open incident,
`* -> UP` to recovery only with an open incident, and `Flapping` to a warning.
Suppression and silent startup produce no events. Recovery uses the original
incident metadata; its JSON omits `layer`. Optional fields use `omitempty`,
and down/recovered `dependents` is always a non-nil array.

A state mutation and all durable enqueue intents are one snapshot transaction.
Workers may observe new durable events only after commit. This deliberately
serializes disk commits; a slow disk can delay every durable enqueue and any
worker waiting to commit attempt state, but cannot block check execution.

### D2. Event ids without a dependency

A 30-line in-repo generator: 48-bit Unix milliseconds plus 80 bits from
`crypto/rand`, Crockford base32, 26 characters, prefixed `evt_` or `bat_`.
Within one millisecond, ids are not monotonic; ordering is by
`(time, sequence)` inside the queue, so lexical order only matters across
milliseconds. Alternative: `github.com/oklog/ulid/v2`. Rejected to avoid a new
dependency for 30 lines.

### D3. Routing and per-sink delivery state

Apply filters only when selecting recipients of a new down. Retain per-incident,
per-sink `pending`, `attempted`, `acknowledged` and `dropped` state, including
whether an attempt ever occurred. Persist attempted state before disk-backed
requests, and acknowledgment/drop results through the coordinator. Recoveries
bypass current filters for eligible recorded sinks.

A recovery created while its down is pending or retrying becomes a waiting
intent. For separate requests, if the down succeeds, send recovery afterwards.
An unattempted down and its recovery may share the same ordered batch/group;
mark their attempt states together before sending and acknowledge/drop together. If a never-attempted
down is dropped, discard its recovery. If any request was attempted, send the
recovery after the down becomes terminal even without acknowledgment: its
response might have been lost. Receivers must handle unknown resolves and
duplicate IDs. Removed sinks are skipped and logged. Expiry and overflow update
queue and incident-delivery state together. A closing incident stays by event
ID until intents are terminal; a new outage may open a new incident meanwhile.

### D4. Dispatcher and sink workers

One goroutine per sink reads from its `queue`. Sinks implement:

```go
type sink interface {
    Name() string
    // Deliver sends evs (one event, or a batch/group) and classifies the result.
    Deliver(ctx context.Context, evs []queuedEvent) deliveryResult
}
```

The worker owns grouping (`group_wait`, `max_batch` for webhook batches, 10
lines + "and N more" is Slack rendering) and the retry loop, so webhook and
Slack share one retry implementation. The worker body for an attempt is
computed once (including `late`) and reused byte-for-byte on retry; only the
signature header is recomputed. Slack additionally has a 1 msg/s limiter.

### D5. Retry schedule: own, not `util/backoff`

The design doc names `util/backoff`. Its n²·10ms curve fires dozens of
requests at a failing receiver in the first seconds and has no
`Retry-After`. Instead: delay = min(2^(n-1) s, 5m) with 0.5–1.5× jitter, and
on `429` delay = max(that, `Retry-After`) capped at 5m. Timers come from
`tstime.Clock`. Alternative: wrap `util/backoff` and add a pre-sleep for
`Retry-After`. Rejected because the initial burst remains.

### D6. Atomic notification snapshot (`queue.go`, `incidents.go`)

Replace independent per-event files and `incidents.json` with a versioned
`notification-state.json`. One coordinator owns open/closing incidents,
per-sink delivery state, sequence allocation and pending disk-sink payloads.
Each mutation builds a new snapshot, writes a same-directory temporary file,
fsyncs it, renames it and fsyncs the directory. Only then are new durable intents
visible to workers. Queue indices are derived in memory. All worker results,
expiry, overflow and acknowledgment changes return to this coordinator.

The snapshot atomically couples opening/closing an incident with its durable
queue intents. Closing records retain the original down ID and recovery until
all intents are terminal. Committed sends can replay after a crash, using the
same IDs. Memory-sink pending payloads do not survive restart; retain incident
identity and known/uncertain delivery state when persistence is enabled.

Queue size remains bounded per sink with drop-oldest and explicit counters.
Persist active batch identity/body and first-attempt payload bytes for disk
sinks so replay preserves retry bytes and IDs. Acknowledgment atomically removes
the batch intents. A group containing an expired event is dropped as a whole
rather than changing an already-attempted body; each dropped event is counted.

On a snapshot write failure, retain event and incident mutations together in
memory and permit memory delivery, log durability degradation and increment
`tailwatch_notify_persist_errors_total{sink="state"}`. Retry the entire current
snapshot later. No crash guarantee applies until persistence succeeds. Reserve
sink name `state`. A corrupt committed snapshot is a startup error requiring
operator repair, never an excuse to silently forget incidents.

### D7. Restart and topology reconciliation

An incident stores its target definition, `implicit`, stable node ID and address
where applicable, trip/check, down event ID, since, severity and per-sink state.
Before starting any checks, load and replay the snapshot, register recorded
implicit reachability targets, then restore open incidents as DOWN. Closing
incidents only replay pending recovery intents and never restore DOWN.

Close explicitly removed configured targets with `reason: removed`. Keep
unresolved implicit incidents through partial/failed discovery. Only a complete,
authoritative topology reconciliation can establish that a restored implicit
identity is no longer needed and close it as removed. `FirstPassDone` alone is
not evidence. Reconcile again on presence reseed and later learning rounds.

A restore refusal logs an error and retains the open incident; another down is
deduplicated and a later visible UP closes it using original metadata. A new
outage after an incident is closing receives a new ID; its events queue behind
older intents. Garbage-collect closed delivery records only after terminal
acknowledgment or an explicit measured drop.

### D8. Webhook sink (`webhook.go`)

`http.Client` with `Timeout: 10s` and
`CheckRedirect: http.ErrUseLastResponse`; transport dials through the default
dialer (sinks follow the host routing table, including tailnet receivers
reached over MagicDNS). Signature:
write `[]byte(t + ".")` and the exact body bytes to
`mac := hmac.New(sha256.New, secret)`, then use
`hex.EncodeToString(mac.Sum(nil))`. `Sum` does not consume message input.
Test a fixed independently computed signature as well as receiver verification. Result
classification: `2xx` delivered; `408`, timeouts → `timeout`; `429` →
`http_429`; `5xx` → `http_5xx`; dial/TLS errors → `network`; everything else
→ `http_4xx_permanent`, dropped with reason `permanent`.

### D9. Slack sink (`slack.go`)

Renders `{"text": ..., "blocks": [...]}` with one `section` block per line
(mrkdwn), a context block for collector name and time, and the mention as
the first line when any grouped event is a `page` down. Rendering is a pure
function of `[]Event` plus sink options, covered by golden files.

### D10. Secrets

`secretRef{env, file string}` resolves at startup (fail fast) and again at
send time. `redactURL(u)` returns `scheme://host/…`. All sink errors pass
through it before logging. Payloads and queue files never carry secrets; this
is enforced by keeping secrets off the `Event` and `queuedEvent` types.

### D11. Heartbeat (`heartbeat.go`) and notify-test (`notifytest.go`)

The heartbeat is a ticker on `tstime.Clock` that starts on
`FirstRoundDone()` and issues a `GET` with a 10s timeout. notify-test
builds the same sinks as the collector, calls `Deliver` directly through the
shared retry loop under a `--timeout` context, and does not create queues,
incident stores or a `Prober`.

### D12. Config

`Config` gains `Sinks []SinkConfig` and `Heartbeat *HeartbeatConfig`, so
`KnownFields(true)` accepts `sinks` and `heartbeat`. Validation, added to
the existing error-collecting pass, rejects:

- duplicate or invalid sink names, and the reserved names `incidents`,
  `intake` and `state` (used as metric labels);
- unknown `type`;
- zero or two URL sources;
- Slack-only or webhook-only keys on the wrong type;
- `queue: disk` without `collector.state_dir`;
- non-positive `queue_size`, `max_batch`, `group_wait`, `max_age`,
  `heartbeat.interval`;
- unknown event types in `events`;
- invalid globs in `match.targets`.

`check-config` prints each sink's name, type, filters and queue mode, never
secret values.

## Seams

- Ordered immutable transitions and dirty snapshots with sequence acknowledgment
  from alerting; topology graph updates release suppressed targets atomically.
- Target registration before scheduling, and restore before the first result.
- Stable node identity and authoritative topology reconciliation from learning.
  First-pass completion is informational and cannot close incidents.
- Presence tags for routing, with no inherited tailnet tags for local targets.
- Collector identity from startup preflight; test command retains OS fallback.

## Deviations from the original draft

The source design now includes the corrected atomic snapshot, delivery-state
rules and topology reconciliation. Per-event queue files and `incidents.json`
are superseded before implementation. The retry schedule remains exponential
from 1s rather than `util/backoff`; payloads add `target.implicit`, `unrouted`
and `derp_region`. Heartbeats wait for the first configured round. Routing and
incident tracking exist in memory even without `state_dir`.

## Risks / Trade-offs

- Whole-state snapshots are simple but rewrite all pending disk events per commit.
  Bound queues, batch coordinator mutations where possible, and measure commit
  duration and intake drops; do not claim disk sinks are isolated at enqueue.
- Expiry, overflow and permanent failures can still lose deliveries. They are
  explicit observable policies, not an unconditional guarantee of resolution.
- Dirty-state reconciliation may coalesce unreported intermediate blips; it
  preserves eventual incident state once capacity returns, not every transition.
- Memory fallback sacrifices crash durability. Snapshot corruption stops startup
  so the operator can repair state without silently forgetting open tickets.
- A lost HTTP response makes acknowledgment uncertain. Receivers must deduplicate
  IDs and accept recovery for an unknown down; exactly-once delivery is out of scope.
- Sink error strings are redacted before logging; persisted snapshots contain no
  secret references' resolved values.

## Migration Plan

Additive. Configs without `sinks` behave as before (no notifications).
Enabling `queue: disk` on an existing collector needs `collector.state_dir`
to exist or be creatable. Rollback: remove `sinks` from the config;
retain the snapshot until open incidents and pending recovery intents are
resolved; memory-only sinks may still have incident metadata in it.
