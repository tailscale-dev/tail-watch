# Incidents and queues

A notification system has two promises that pull against each other. An alert
must not be lost, and a slow or dead receiver must never stop the monitoring
that produces alerts. This page explains how tail-watch keeps both promises,
what it does when it cannot, and the limits it tells you about instead of
hiding.

The sink keys mentioned here are in the
[configuration reference](../reference/config.md). The metrics are in the
[metrics reference](../reference/metrics.md), and the fields on the wire are
in the [payload reference](../reference/payload.md). Setup instructions are in
the [webhook](../how-to/webhook-sink.md) and [Slack](../how-to/slack-sink.md)
guides.

## Events and incidents

The alert state machine emits transitions. The notifier turns them into events
(`target.down`, `target.recovered`, `target.flapping`, and the rest) and
groups the down and recovery of one outage into an incident.

An incident is the memory that pairs a recovery with the down it resolves. It
records the tripping check, the severity, the start time and the id of the
`target.down` event. A recovery carries that id as `down_event_id`, which lets
a receiver close the exact alert it opened, for example an incident in a paging tool,
instead of guessing by target name.

There is at most one open incident per target. If a target fails again while
its recovery is still being delivered, a new incident opens with a new id
queued behind the old recovery. Incidents exist even without `state_dir`,
which keeps duplicate downs from being sent while one is open.

## The path from a check to a receiver

The design goal is that nothing on the way from a probe to a network socket
can make a probe wait.

1. The alert state machine hands each transition to the notifier through an
   intake with 4096 slots. That call only writes to the channel. It does no
   disk or network work, so a check can never block on notification.
2. The notifier maps the transition to an event, updates the incident, decides
   which sinks should receive it, and commits the result.
3. Each sink has its own worker and its own bounded queue. A receiver that
   takes ten seconds per request delays only its own sink, not another sink
   and not a check.

Delivery within one sink is strictly ordered. A recovery waits until its down
has been acknowledged or has been dropped for good. The alternative, sending in
parallel for throughput, would allow a recovery to arrive first and a receiver
to resolve something it has not yet opened. Ordering costs some throughput
under a backlog, which is a good trade for an alerting channel.

## At-least-once delivery

Exactly-once delivery across a network is not achievable, so tail-watch picks
the failure mode that is safe for alerts: an event may be delivered more than
once, but a committed event is not silently forgotten.

Every event has an id that stays the same across every retry and every sink.
That gives receivers what they need to deduplicate. The contract has a second
half that receivers must accept: a `target.recovered` can arrive for a down the
receiver never saw. That happens when a request reached the receiver but the
response was lost, when the down was dropped, or when the receiver was added
later. Treat an unknown `down_event_id` as a no-op, not as an error.

The same reasoning explains an apparent oddity. When a down was never
attempted, for example it was dropped from a full queue, its recovery is
discarded too, because sending a resolve for something never sent is only
noise. When a down was attempted but not acknowledged, its recovery is still
sent. The receiver may have accepted a request whose response was lost, and
leaving that alert open forever would be worse than a stray resolve.

Recoveries also ignore filters that changed since the down. A sink that
received a down always gets the matching recovery, even if its `events` list
would now exclude it, and a sink that never saw the down never sees a recovery.

An event first attempted more than a minute after its creation time is marked
`late` and keeps its original timestamp, so a receiver can tell a delayed
delivery from a fresh one.

## Queues: memory or disk

Each sink chooses where pending events live.

| Mode | Behavior | Suits |
| --- | --- | --- |
| `queue: memory` | Fast and default; pending events vanish on restart. | Chat sinks where a late message is worth little. |
| `queue: disk` | Pending events survive restart; needs `collector.state_dir`. | Sinks that feed paging or automation. |

The trade-off is between complexity and safety. A memory queue has no failure
mode beyond process death. A disk queue requires a directory that the
collector can write, and a slow disk can add latency to every durable enqueue.
It never delays a check, but it can delay delivery.

Queues are bounded by `queue_size` (default 1000). When one is full, the
oldest event is dropped, not the newest. A backlog is most often the sign of a
receiver that has been away for a while, and by then the newest events describe
the current state of the world while the oldest describe history that has
probably resolved. The events also expire after `max_age` (default 1 hour), for
the same reason: a page for a target that recovered hours ago is worse than no
page.

## State on disk

With `collector.state_dir`, tail-watch keeps one file,
`notification-state.json`. It holds open incidents, the delivery state of every
incident on every sink, and the pending events of disk sinks. A queued event
may carry `mention`, the route text resolved when that event was created. The
field is omitted when empty, it is not part of the event payload, and a file
written before routes existed still loads. The file deliberately does not hold
URLs, secrets or header values, so a copied or backed up state directory is
not a credential leak.

Two decisions explain how it is written.

**One file, one commit.** An incident change and the queue intents that follow
from it are written in the same snapshot. Splitting them across files would
create a window where a crash leaves an incident closed with its recovery never
queued, or a recovery queued for an incident that does not exist. The snapshot
is written to a temporary file, synced, renamed over the old one, and the
directory is synced. Only then do workers see the new work. A crash before the
rename leaves the old snapshot intact and a crash after it replays the new one.
That is why delivery is at least once *after a successful commit*, and not
before it.

**Attempts are recorded before they are made.** A disk sink marks an event
"attempted" durably before sending it. If the process dies between the send
and the acknowledgment, replay knows a request may have gone out. It resends
the identical body and id, and it keeps the recovery eligible. The cost is a
possible duplicate.

A snapshot that cannot be decoded stops startup with an error that names the
file. The alternative, starting with empty state, would forget every open
incident and page again, or worse, never send the recoveries. Refusing to start
is loud, and the operator decides what to do with the file.

If a write fails, for example the disk is read-only or full, the collector does
not stop notifying. It keeps the authoritative state in memory, delivers from
memory, logs the loss of durability once, increments
`tailwatch_notify_persist_errors_total`, and retries persisting the full state
every second. Durability is degraded until a write succeeds, and that is
something you should alert on, because a crash in that window loses queued
events.

## Restarts and open incidents

At startup, before any check is scheduled, the notifier restores open
incidents into the alert state machine as `DOWN` with their original start
time. Restoring an incident is not a new alert. No second `target.down` is
sent, and the eventual recovery carries the original `down_event_id` and
duration. Without this, every restart during an outage would either re-page or
strand the original alert without a resolution.

Learned routers add a wrinkle. An implicit target exists only because
dependency learning created it, so at restart it might not be rediscoverable
yet. The notifier records enough to recreate it and does not treat a failed
discovery pass as evidence that the router is gone. It closes such an incident
with `reason: removed` only after a reconciliation that saw the full topology.
A target that was deleted from the configuration, or a monitor deleted in the
UI, closes its incident the same way, so a receiver is not left with an alert
for something that no longer exists.

## Intake overflow and reconciliation

The intake is bounded, and a full intake drops the transition instead of
blocking. That looks alarming until you consider the alternative: a blocked
intake would stall the state machine, which would stall probing. The design
keeps a second, independent source of truth instead. The alert state machine
holds a snapshot of the latest notification-visible state of every target and
marks a target dirty when its transition could not be delivered.

Once per second, the notifier reconciles dirty targets against their
snapshots. An open incident whose target is now `UP` gets a recovery. A target
that is `DOWN` with no incident gets a down. A recovery lost to overflow is
therefore still sent, without waiting for another state change. The price is
coalescing: if a target blipped down and up while the notifier was behind, the
receiver may never hear about the blip. That is acceptable, because the blip
was, by definition, not what the receiver would have wanted a page for.

Removal follows the same route. A target removed from the collector reaches the
notifier only through a terminal snapshot, so the incident close is not lost if
the transition was.

## When alerts are lost

tail-watch does not promise that no alert is ever lost. It promises that a loss
has a named reason and increments a counter. Every drop increments
`tailwatch_notify_dropped_total` with one reason:

| Reason | What happened |
| --- | --- |
| `queue_full` | The sink queue, or the intake with `sink="intake"`, was full. |
| `expired` | The event was older than `max_age` when an attempt was due. |
| `permanent` | The receiver answered with a status that will never succeed. |
| `restart` | A memory queue lost the event when the process restarted. |

Failed attempts that will be retried are counted separately in
`tailwatch_notify_failed_total`. The recommended alerts are on any increase of
the dropped counter and on a queue length that stays above zero, since both
mean an alert did not get out.

Notification also has an obvious blind spot: a collector that is dead cannot
say so. The [heartbeat how-to](../how-to/heartbeat.md) covers pointing the
collector at a dead-man's-switch service that alarms on silence.

## Related pages

- [Alert state](alert-state.md) for how transitions are produced.
- [Event payload](../reference/payload.md) for the fields receivers depend on.
- [Troubleshooting](../how-to/troubleshoot.md) for missing notifications.
