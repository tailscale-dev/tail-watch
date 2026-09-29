# Metrics reference

The collector serves Prometheus text at `/metrics` on the listen address. Every
series starts with `tailwatch_`. The prefix has no hyphen because Prometheus metric
names cannot contain one. The default Go and process collectors are also exposed
and are not listed here.

## Check series

One set per target and check pair, registered by the scheduler when the check is
added and removed when the target is removed. Labels on every series:

| Label | Value |
| --- | --- |
| `name` | `<target>/<check>`, such as `nyc-nas/tcp:445`. |
| `class` | Check kind: `icmp`, `tcp`, `http`, or `tsping`. |
| `target` | Target name. |
| `check` | Check string as configured, such as `tcp:445`. |
| `site` | Target `site`, empty when unset. |

| Metric | Type | Extra labels | Meaning |
| --- | --- | --- | --- |
| `tailwatch_interval_secs` | gauge | | Probe interval in seconds. |
| `tailwatch_start_secs` | gauge | | Start time of the latest run, seconds since the epoch. |
| `tailwatch_end_secs` | gauge | | End time of the latest run, seconds since the epoch. |
| `tailwatch_latency_millis` | gauge | | Latency of the latest run in milliseconds. Absent when the latest run reported none. |
| `tailwatch_result` | gauge | | `1` if the latest run succeeded, `0` if it failed. |
| `tailwatch_in_flight` | gauge | | Runs in progress. |
| `tailwatch_attempts_total` | counter | `status` | Runs, by `status` `ok` or `fail`. |
| `tailwatch_seconds_total` | counter | `status` | Time spent running, in seconds, by `status`. |

`tailwatch_result`, `tailwatch_end_secs`, and the two counters appear after the
first run finishes. These series come from the Tailscale `prober` package.

## Classification and state series

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `tailwatch_failure_layer_total` | counter | `target`, `layer` | Failed check results by assigned failure layer. Increments once per failed result. `layer` is `unrouted`, `control`, `tunnel`, `lan`, or `service`. |
| `tailwatch_path` | gauge | `target`, `kind` | Current `tsping` path kind. One series per `kind` (`direct`, `derp`, `peer_relay`); the active kind is `1` and the others are `0`. |
| `tailwatch_target_parent` | gauge | `target`, `parent` | `1` while `parent` is the target's current parent, configured or learned. Removed when the parent changes. |
| `tailwatch_target_state` | gauge | `target` | Alert state: `0` unknown, `1` up, `2` down, `3` suppressed, `4` flapping. |

See [Failure layers](../explanation/failure-layers.md) and
[Alert state](../explanation/alert-state.md).

## Notification series

Registered only when at least one sink is configured, except
`tailwatch_heartbeat_failures_total`. The `sink` label is the sink `name`.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `tailwatch_notify_sent_total` | counter | `sink`, `type` | Notifications delivered, by event type. |
| `tailwatch_notify_failed_total` | counter | `sink`, `reason` | Failed delivery attempts. `reason` is `timeout`, `http_5xx`, `http_429`, `http_4xx_permanent`, or `network`. |
| `tailwatch_notify_dropped_total` | counter | `sink`, `reason` | Notifications dropped without delivery. `reason` is `queue_full`, `expired`, `permanent`, or `restart`. |
| `tailwatch_notify_persist_errors_total` | counter | `sink` | Failed writes of notification state. `sink` is always `state`. |
| `tailwatch_notify_queue_len` | gauge | `sink` | Events waiting in the sink's queue. |
| `tailwatch_notify_latency_seconds` | histogram | `sink` | Seconds from event creation to confirmed delivery. Buckets: 0.1, 0.5, 1, 5, 10, 30, 60, 300, 900, 3600. |
| `tailwatch_notify_batch_size` | histogram | `sink` | Events per request for batched webhook sinks. Buckets: 1, 2, 5, 10, 25, 50, 100, 250, 500. |
| `tailwatch_open_incidents` | gauge | | Incidents opened and not yet closed. Registered only when `collector.state_dir` is set. |

`tailwatch_notify_dropped_total` also uses `sink="intake"` with `reason="queue_full"`
when the 4096-slot intake channel is full. The alerter's next reconcile, within a
second, repairs the lost transition.

Drop reasons:

| Reason | Cause |
| --- | --- |
| `queue_full` | The sink queue or the intake channel was full. |
| `expired` | The event was older than the sink's `max_age` when an attempt was due. |
| `permanent` | The receiver returned a non-retryable status, such as `404`. |
| `restart` | The event sat in a `memory` queue when the collector restarted. |

See [Incidents and queues](../explanation/incidents-and-queues.md).

## Heartbeat series

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `tailwatch_heartbeat_failures_total` | counter | | Heartbeat pings that failed. Registered only when `heartbeat` is configured. |

## Suggested alerts

```yaml
- alert: TailWatchNotificationLoss
  expr: increase(tailwatch_notify_dropped_total[15m]) > 0
- alert: TailWatchQueueStuck
  expr: min_over_time(tailwatch_notify_queue_len[15m]) > 0
- alert: TargetDownQuorum
  expr: count by (target) (tailwatch_target_state == 2) >= 2
  for: 1m
```

The first two mean an alert did not get out. The third pages only when two
collectors agree that a target is down.
