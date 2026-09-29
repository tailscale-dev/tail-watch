# Alert state

A check result is a fact about one moment. An alert is a decision that a
human should look. Between them sits a state machine whose job is to turn a
noisy stream of results into as few notifications as possible without hiding
a real outage. This page explains what that machine does and why it is
shaped the way it is, so you can predict its behavior and tune it.

The keys named here are set under `defaults:` or on each target. Their
defaults and syntax are in the [configuration reference](../reference/config.md).
The state values as exported are in the [metrics reference](../reference/metrics.md).

## One state per target

Every target is in exactly one of four states:

| State | Meaning |
| --- | --- |
| `UNKNOWN` | No verdict yet since the process started. |
| `UP` | Every check has been passing. |
| `DOWN` | A check failed `fail_threshold` times in a row. |
| `SUPPRESSED` | A parent that the target depends on is down. |

The states are held in memory only. A restart begins again at `UNKNOWN`, and
the durable part of an outage is the incident record described in
[Incidents and queues](incidents-and-queues.md). That split is deliberate:
the state machine can stay simple and cheap because a restart costs a few
check cycles of "we do not know yet", not a lost incident.

The gauge `tailwatch_target_state` adds a fifth value, 4, for a target that is
flapping. Flapping is a mark on top of the state and not a state of its own,
and it takes precedence in the gauge while it is set.

## Why thresholds, and why they are per check

One lost packet must not page anyone. A target therefore goes `DOWN` only
after `fail_threshold` consecutive failures of a single check (default 3), and
a success resets that check's count. With the default 30 second interval that
is roughly a minute and a half from first failure to page, and the time of the
first failure in the run is what the alert reports as `since`, so the outage
start is not pushed back by the waiting.

Counters are kept per target and check pair, not per target. Consider a target
with `icmp` and `tcp:445`. If the counter were shared, a failing port
interleaved with a passing ping would never accumulate three failures, and a
real fault would be hidden by an unrelated healthy check. Per check counters
mean that any one check reaching the threshold is enough.

Going down is quick and coming back is slow on purpose. A target returns to
`UP` only after `recover_threshold` consecutive successes (default 2) on every
check that failed while it was down, and only when none of its other checks is
currently failing. If `tcp:445` recovers while `icmp` has started failing, the
target stays `DOWN`. The asymmetry favors a recovery message that stays true
over one that is retracted a minute later.

### Starting up

A fresh process has no history, so a target leaves `UNKNOWN` in one of two
ways. If every check succeeds once, the target becomes `UP` and says nothing.
Startup is not news, and a restart during a healthy period would otherwise
produce a burst of "recovered" messages. If a check reaches `fail_threshold`
first, the target goes straight from `UNKNOWN` to `DOWN` and pages once, which
is why a target that is already broken when you start the collector is
reported.

A separate `collector.started` event announces that every target has produced a
first result, so a silent start is still distinguishable from a dead collector.

## Flap damping

A target that alternates between failing and passing can defeat thresholds:
each cycle is long enough to page and short enough to be gone by the time
someone looks. `flap_limit` and `flap_window` (defaults 4 and 15 minutes)
bound the damage.

The machine counts `UP` and `DOWN` changes inside a sliding window. When a
change would push the count past `flap_limit`, it emits one flapping notice in
place of that change's normal notification, and then goes quiet. While the mark
is set the target's state is still tracked, but nothing is emitted. The mark
clears when the target has been stable for a full `flap_window`. If the
settled state differs from the last state that was reported, one ordinary
notification catches the receiver up. If it matches, nothing is sent.

Two consequences follow from that design.

- The receiver's picture stays coherent. A target that flapped and settled
  `UP` after being reported `DOWN` still gets its recovery, so no incident is
  left open on the receiving side.
- Damping trades latency for quiet. A real outage that begins during a flap
  will not page until the target has been stable, or until the flap window
  runs out. If you cannot accept that, raise `flap_limit` or shrink
  `flap_window` for that target rather than turning damping off elsewhere.

## Parent suppression

The most common cause of a wall of alerts on a tailnet is not forty broken
devices. It is one broken subnet router with forty devices behind it. Without
suppression, each device would page separately and the one useful alert, the
router, would be buried among its symptoms.

Each target can have a parent: a target that must work for it to be
reachable. The parent comes from an explicit `parent` key, or from the
dependency learning described in [Failure layers](failure-layers.md). Two
rules keep the mechanism honest.

- **A parent is a reachability target.** It may have only an `icmp` or a
  `tsping` check. A service check on the same node is not a parent, so a
  failed web interface cannot silence the devices behind that router.
- **A down parent means `DOWN` or `SUPPRESSED`.** Suppression is transitive. If
  a switch behind a router is itself suppressed, its own dependents are too.

When a parent goes down, every dependent in `UNKNOWN` or `UP` moves to
`SUPPRESSED` at the same instant, silently. The parent's own notification names
them in `dependents`, so one message carries the whole blast radius. A dependent
that is already `DOWN` keeps its incident, because it was reported on its own
merits and dropping it would leave an alert with no resolution. Checks on
suppressed targets keep running, which is what lets the machine notice the
moment the situation changes.

### The detection window

Suppression has a race. The device's checks and the router's checks run on
independent schedules, so a device can reach its threshold a few seconds before
the router does. Paging on the device then produces exactly the duplicate that
suppression was meant to prevent.

The answer is a short wait. When a target with a parent reaches its threshold
on a `control` or `tunnel` failure and the parent is not yet down, the target
does not go `DOWN` immediately. It waits up to the parent's detection window,
which is the parent's `interval` times its `fail_threshold`, plus its
`timeout`. That is the longest the parent could take to reach its own verdict
if it started failing at the same moment. If the parent goes down in that
time, the target is suppressed. If not, the target goes `DOWN` at the end of
the wait, provided its check is still failing, and the alert keeps the
original layer.

Only `control` and `tunnel` failures wait, because only those layers say "the
path to the parent's side is broken". A `lan`, `service` or `unrouted` failure
pages immediately. By definition the tunnel to the router works for a `lan`
or `service` failure, so the router is not the cause. For `unrouted`, no router
exists to blame. See [Failure layers](failure-layers.md) for how those labels are
chosen.

### Release

When the parent recovers, is removed, or a topology change leaves a target
with no down ancestor, tail-watch does not wait for the next scheduled check.
It re-runs every check of each released target promptly, at most eight at a
time, so a recovered router does not leave forty devices in limbo for a full
interval. A target whose fresh checks pass returns to `UP` silently. One whose
checks keep failing counts `fail_threshold` failures from the release, then
goes `DOWN` with its own alert.

The counters are reset on release, and results from checks that began before
it are ignored. Without that, a timeout that started while the router was
down would be blamed on the device as a fresh failure. A failover in a
high-availability pair works the same way: if learning moves a suppressed
device from a dead router to a live one, the device is released at once, even
though the dead router never recovers.

One subtle case exists for incidents that were already reported. If flap
damping left a `DOWN` unresolved before the target was suppressed, that
incident is preserved through suppression and release. The target sends its
recovery once damping permits, with the original outage start time, and
continued failures do not open a second incident.

## Quorum across collectors

One collector sees the tailnet from one place. If its uplink fails it will
report every target down, and one unhealthy collector cannot tell you whether
the problem is the target or itself. State is not shared between collectors, so
tail-watch does not vote.

Quorum is left to Alertmanager, which already knows how to combine sources.
Each collector exports `tailwatch_target_state`, and a rule can require
agreement:

```yaml
- alert: TargetDownQuorum
  expr: count by (target) (tailwatch_target_state == 2) >= 2
  for: 1m
```

That pages only when two collectors agree the target is `DOWN`. The rejected
alternative was a gossip protocol among collectors. It would add a network
dependency between the very machines you are trying to make trustworthy, to
solve a problem Alertmanager users have already solved.

## Choosing values

The defaults suit a 30 second interval and mostly stable devices. A few
relationships help when you tune them:

- Time to page is about `interval` times `fail_threshold`, plus `timeout` for
  the last attempt. Shortening the interval shortens the detection window of
  every parent that uses it.
- A larger `recover_threshold` makes recovery slower and flapping less likely.
- Raising `flap_limit` makes a target flap less readily. A larger
  `flap_window` keeps it quiet for longer after it settles.
- `severity` on a target controls how loud the resulting events are, not
  whether the state machine reaches `DOWN`. See
  [Incidents and queues](incidents-and-queues.md) for routing.

## Related pages

- [Configuration reference](../reference/config.md) for every key named here.
- [Metrics reference](../reference/metrics.md) for `tailwatch_target_state`.
- [Failure layers](failure-layers.md) for the labels that drive the wait.
