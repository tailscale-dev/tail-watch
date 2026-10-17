# notification-delivery Specification

## Purpose

Defines how events move from the alert state machine to each sink without
blocking checks: per-sink queues and ordering, memory and crash-safe disk
queues, open-incident tracking across restarts, delayed-delivery marking,
secret handling, and the metrics that reveal lost alerts.

## Requirements

### Requirement: Non-blocking dispatch

Accepting a state transition from the alert state machine SHALL be a bounded,
non-blocking enqueue: it SHALL NOT perform network I/O, disk I/O, or wait on
any sink. Mapping, incident updates, routing and persistence SHALL happen off
that call path. If the intake buffer is full, the transition SHALL be dropped,
its target marked dirty for snapshot reconciliation, logged, and counted in
`tailwatch_notify_dropped_total{sink="intake",reason="queue_full"}`. Each sink SHALL have its own worker and its own queue. Network I/O of one sink
SHALL NOT block another sink or any check. Snapshot commits are serialized and
can delay all durable enqueues and workers awaiting attempt-state commit; this
limitation SHALL be
measured and documented. The notifier SHALL reconcile dirty targets within
one second after intake/commit capacity becomes available to begin reconciliation,
including when no
new transitions arrive. An open incident whose notification-visible state is
UP SHALL get a recovery intent. A current DOWN without an open incident SHALL
get a down intent. Intermediate unreported blips may be coalesced. Dirty marks
SHALL be acknowledged only through the reconciled sequence, preserving newer
updates. Queued transitions at or below a target's reconciled sequence SHALL
be ignored. Target removal SHALL retain a terminal snapshot until reconciled.

#### Scenario: Slow disk

- **WHEN** `state_dir` is on a disk where each fsync takes 2 seconds
- **THEN** accepting a transition still returns without waiting for any
  fsync

#### Scenario: Lost recovery reconciled

- **WHEN** intake overflows on recovery and the target stays UP permanently
- **THEN** after capacity returns, the notifier reconciles its dirty snapshot
  and commits a recovery intent without requiring another state change

#### Scenario: Slow webhook

- **WHEN** a webhook receiver takes 10s per request and a Slack sink is also
  configured
- **THEN** Slack messages are delivered without waiting for the webhook

### Requirement: In-order delivery per sink

Within one sink, delivery attempts SHALL preserve event order. A recovery SHALL
wait until its down is acknowledged or terminally dropped,
then follow the per-sink incident delivery-state rules. An unattempted down and
its recovery MAY share one batch/group, with down first and their attempt and
acknowledgment states committed together. Network uncertainty
means receiver observation cannot be guaranteed; receivers must handle unknown
resolve IDs.

#### Scenario: Down retried, recovery queued

- **WHEN** a `target.down` is being retried and its `target.recovered` is
  emitted
- **THEN** recovery waits for the down to become terminal and is sent only if
  its per-sink delivery state makes recovery eligible

### Requirement: Bounded queues

Each sink's queue SHALL hold at most `queue_size` events (default 1000). When
full, the oldest queued event SHALL be dropped and counted with reason
`queue_full`.

#### Scenario: Overflow

- **WHEN** a sink's queue holds 1000 events and another arrives
- **THEN** the oldest event is dropped and
  `tailwatch_notify_dropped_total{reason="queue_full"}` increases by 1

### Requirement: Late delivery flag

An event first attempted more than 1 minute after its `time` SHALL carry
`"late": true`, keep its original `time`, and keep that body for all later
attempts.

#### Scenario: Uplink outage

- **WHEN** the collector's uplink is down for 5 minutes and events queue up
- **THEN** they are delivered after the uplink returns with their original
  `time` and `"late": true`

### Requirement: Queue modes

Each sink SHALL accept `queue: memory` (default) or `queue: disk`. `queue: disk`
without `collector.state_dir` SHALL be a config error. A memory queue SHALL
lose pending events on restart.

#### Scenario: Disk without state_dir

- **WHEN** a sink sets `queue: disk` and `collector.state_dir` is unset
- **THEN** config loading fails with an error naming the sink

### Requirement: Atomic notification durability

With `state_dir`, one coordinator SHALL own a versioned
`<state_dir>/notification-state.json` snapshot containing incident records,
per-sink delivery states, sequence allocation, and pending disk-queue events.
An incident mutation and the corresponding disk-sink enqueue intents SHALL be
committed in the same snapshot. The coordinator SHALL write a temporary file,
fsync it, rename it over the snapshot, then fsync the directory before exposing
new disk-backed deliveries to workers. No independent `incidents.json` or
per-event file SHALL be authoritative.

A disk-backed incident SHALL retain its closing event and original down ID
until all associated durable delivery intents have been acknowledged or
explicitly dropped. Worker results SHALL update the snapshot through the same
coordinator. Startup SHALL replay pending intents in per-sink sequence order,
applying expiry and queue limits with their associated delivery-state updates
in the same commit. Memory-sink pending payloads SHALL NOT be restored, but
recorded acknowledgment/uncertainty and incident identity SHALL survive when
`state_dir` is configured. Abandoned temporary files SHALL be removed.

Delivery SHALL be at least once after a successful durable commit, subject to
explicit expiry, overflow and permanent-failure policies. A crash before commit
SHALL leave the old snapshot intact; a crash after commit SHALL replay the new
snapshot. Snapshot parse failure SHALL stop startup with an actionable error,
not silently discard open incidents.

#### Scenario: Crash between incident change and queue distribution

- **WHEN** a recovery is committed and tail-watch crashes before any worker sees it
- **THEN** replay finds the closing incident and its recovery intents with the
  original down ID and sends them without requiring a new outage

#### Scenario: Corrupt committed snapshot

- **WHEN** the committed notification snapshot cannot be decoded
- **THEN** startup fails with an actionable error and preserves the file for repair

#### Scenario: Crash before commit

- **WHEN** tail-watch crashes after writing a new snapshot's temporary file but before
  rename
- **THEN** startup uses the old snapshot, retains any old open incident, and
  removes the temporary file

#### Scenario: Crash after delivery

- **WHEN** a worker gets `2xx` but tail-watch crashes before its acknowledgment commits
- **THEN** replay may resend the event with the same ID

#### Scenario: Stale event at startup

- **WHEN** replay expires a pending down that was never attempted
- **THEN** that sink's down state and dependent recovery intent are updated in
  the same commit, and no orphan recovery is sent to it

### Requirement: Disk write fallback

If committing the atomic snapshot fails, its event and incident mutations
SHALL stay together in authoritative memory and pending events may be delivered
from memory instead. The coordinator SHALL retry persistence of the full current
state. The failure SHALL be logged once until a write succeeds again, and
`tailwatch_notify_persist_errors_total{sink="state"}` SHALL increase. The sink
name `state` SHALL be reserved. Durability SHALL be explicitly degraded until
a subsequent successful snapshot commit.

#### Scenario: Read-only state_dir

- **WHEN** `state_dir` is read-only and a `target.down` is emitted
- **THEN** the event is still delivered and the persist-error counter
  increases

### Requirement: State directory safety

tail-watch SHALL create `state_dir` and its subdirectories with mode 0700 and write
files with mode 0600. Files SHALL hold event payloads and incident records
only, never URLs, secrets or headers. tail-watch SHALL log a warning when
`state_dir` is readable by group or others.

#### Scenario: Secrets never persisted

- **WHEN** a disk-queued webhook sink has a secret and an auth header
- **THEN** no file under `state_dir` contains the URL, the secret or the
  header value

### Requirement: Open incident tracking

Open incidents SHALL always be indexed in memory by target name, with open and
closing delivery records keyed by incident ID. With
`state_dir`, they SHALL be persisted in the atomic notification snapshot. Each
record SHALL retain target definition and identity (including `implicit`, stable
node ID, address and reachability check for learned routers), tripping check,
event ID, `since`, severity, and per-sink delivery state. An incident SHALL have
an open or closing lifecycle; recovery SHALL atomically move it to closing and
create delivery intents before it may be garbage-collected. A closing incident
SHALL NOT be restored as DOWN or prevent a later outage from opening a new
incident with a new ID. Delivery records SHALL remain keyed by incident ID until
all intents are terminal; only one incident per target may be open.

Before scheduling checks, tail-watch SHALL restore open configured incidents as DOWN
without a second page and recreate recorded implicit reachability targets by
stable node identity before restoring them. Recovery SHALL use the original
`down_event_id`, severity and `since`. A restore refusal SHALL log an error and
retain the incident; further downs SHALL be deduplicated while it is open.

An explicitly removed configured target SHALL close with `reason: removed`.
An unresolved implicit target SHALL be retained until authoritative topology
reconciliation confirms removal; completion of failed discovery attempts SHALL
NOT count as removal. Removed sinks SHALL be skipped and logged. Persistence
errors SHALL retain authoritative in-memory state, be counted and logged, and
be retried; crash durability is not guaranteed while writes fail.

#### Scenario: Restart during an outage

- **WHEN** tail-watch restarts while `nyc-nas` has an open incident and later recovers
- **THEN** no second down is created and its recovery has the original down ID

#### Scenario: Implicit router cannot be rediscovered

- **WHEN** an implicit router has an open incident and first-pass discovery fails
- **THEN** its recorded reachability target and incident remain, without a
  `reason: removed` recovery

#### Scenario: Removed configured target

- **WHEN** a configured target with an open incident is deleted from config
- **THEN** a `reason: removed` recovery is committed for eligible recorded sinks

#### Scenario: New outage while recovery is pending

- **WHEN** a closing incident's recovery is retrying and the same target fails again
- **THEN** a new incident opens with a new down ID, queued after the old recovery

### Requirement: Per-sink incident delivery state

For each incident and selected sink, the coordinator SHALL distinguish
`pending` (queued, never attempted), `attempted` (receiver may have accepted),
`acknowledged` (`2xx`), and `dropped`, retaining whether any attempt occurred.
It SHALL mark a disk event attempted durably before making its request. A
recovery generated while the down is pending or retrying SHALL be retained as
an intent and wait behind it, except when both share an ordered batch/group
whose attempt states are committed together. An acknowledged down SHALL make its recovery
eligible even when filters changed. A never-attempted down that is dropped
SHALL discard its recovery intent. If any attempt occurred but delivery was
not acknowledged, the recovery SHALL remain eligible after the down becomes
terminal: the receiver may have accepted a request whose response was lost.
Receivers SHALL therefore tolerate duplicate events and resolves for unknown
IDs. A sink filtered out of the down SHALL never receive its recovery.

Expiry, overflow and permanent failures SHALL update these states and pending
intents atomically. Guarantees SHALL be qualified by these explicit drop
policies; losses SHALL be logged and counted. On restart, lost memory-sink
pending payloads SHALL be marked dropped and counted with reason `restart`;
never-attempted downs become ineligible while attempted downs retain recovery
eligibility. Routing and delivery acknowledgments
SHALL NOT rely solely on a list of selected sink names.

#### Scenario: Recovery while down is retrying

- **WHEN** a down gets `503`, its recovery is generated, and the down later gets `200`
- **THEN** recovery waits and is subsequently sent to the same sink

#### Scenario: Down dropped before first attempt

- **WHEN** overflow or expiry drops a never-attempted down
- **THEN** its recovery is discarded for that sink and the drop is counted

#### Scenario: Receiver accepted but response was lost

- **WHEN** the down request times out after acceptance and eventually expires
- **THEN** recovery is still attempted with the original down ID

### Requirement: Secret handling

Sink URLs, HMAC secrets, `headers_env` and `headers_setec` values and the
heartbeat URL SHALL come only from `*_env`, `*_file` or `*_setec` keys; `*_file`
and setec values SHALL have surrounding whitespace trimmed. tail-watch SHALL
fail at startup when a referenced variable is unset or empty, a referenced file
is unreadable, or a referenced setec secret is missing or its server
unreachable, and SHALL re-read the value at send time so rotation needs no
restart. Logs and errors SHALL show URLs only as scheme and host, with
everything after the host replaced by `/…`.

#### Scenario: Unset variable

- **WHEN** a sink sets `url_env: SLACK_WEBHOOK_URL` and the variable is unset
- **THEN** tail-watch exits non-zero naming the sink and the variable

#### Scenario: Missing setec secret

- **WHEN** a sink sets `url_setec: slack-url` and the server has no such secret
- **THEN** tail-watch exits non-zero naming the sink and `slack-url`

#### Scenario: Redacted log

- **WHEN** delivery to `https://hooks.slack.com/services/T0/B0/xyz` fails
- **THEN** the log line shows `https://hooks.slack.com/…` and not the path

### Requirement: Notification metrics

tail-watch SHALL export, in the `tailwatch` namespace:

- `tailwatch_notify_sent_total{sink,type}`
- `tailwatch_notify_failed_total{sink,reason}` with reason `timeout`, `http_5xx`,
  `http_429`, `http_4xx_permanent` or `network`
- `tailwatch_notify_dropped_total{sink,reason}` with reason `queue_full`,
  `expired`, `permanent` or `restart`
- `tailwatch_notify_persist_errors_total{sink}`
- `tailwatch_notify_batch_size{sink}` histogram, for batched webhook sinks only
- `tailwatch_notify_queue_len{sink}` gauge
- `tailwatch_notify_latency_seconds{sink}` histogram of time from event creation
  to confirmed delivery
- `tailwatch_open_incidents` gauge, when `state_dir` is set

#### Scenario: Failed attempt counted

- **WHEN** a webhook attempt gets `503`
- **THEN** `tailwatch_notify_failed_total{reason="http_5xx"}` increases by 1 for
  that sink

### Requirement: Durable attempt identity

Disk sinks SHALL persist the first-attempt body and any batch ID/membership before
sending, and replay identical bodies and IDs after restart; only signature time
may change. If any member of an attempted batch expires, the whole batch SHALL
be dropped and every event counted, retaining per-sink attempted state. Newly
arriving events SHALL NOT change an active batch.

#### Scenario: Restart during a batch retry

- **WHEN** a batch fails and the collector restarts before its next attempt
- **THEN** replay sends the same batch ID and body with a fresh signature
