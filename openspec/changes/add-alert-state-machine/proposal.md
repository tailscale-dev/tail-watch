# Proposal

## Why

After `add-tsmon-collector` and `add-failure-classification`, the collector
knows every check result and why it failed. It still cannot tell when a target
is actually down, as opposed to dropping one packet. It also cannot keep a dead
subnet router from producing one alert per device behind it. The design doc's
goals of "alert on state transitions only, after N consecutive failures" and
"page once for the router, not 40 times" need a per-target state machine. See
`tailnet-reachability-monitor-design.md`, sections "Alert state machine" and
"Metrics".

This is change 3 of 4 in phase 1 (MVP). It consumes classified results from
`add-failure-classification`. It produces the transitions that
`add-notifications` turns into events.

## What Changes

- Add a state machine for each target. The states are `UNKNOWN`, `UP`, `DOWN`
  and `SUPPRESSED`, and a target can also be marked as flapping.
- A target goes `DOWN` after `fail_threshold` consecutive failures of any one
  check. It goes back `UP` after `recover_threshold` consecutive successes on
  every check that failed.
- Add flap damping driven by `flap_limit` and `flap_window`. A flapping target
  emits one notice and then stays quiet until it has been stable for
  `flap_window`.
- Add dependency suppression:
  - Only reachability targets can suppress dependents. When one goes DOWN,
    its UNKNOWN/UP descendants become SUPPRESSED; already-DOWN children
    retain their own incidents. The parent's transition lists newly suppressed targets.
  - A dependent whose failure points at its parent (layer `control` or
    `tunnel`) waits for a short grace period before it may page.
  - Recovery or a topology change leaving no down ancestor enqueues rechecks
    within one second, executed through a bounded FIFO worker pool.
- On restart, targets start `UNKNOWN` and reach `UP` without emitting
  anything. A restore hook lets `add-notifications` start a target as `DOWN`
  when it has an open incident. A restore for a target that isn't registered
  yet is kept pending and applied when that target is added.
- Allow targets to be added and removed at runtime, for implicit parents
  learned by `add-failure-classification`. Add a query that says whether a
  target is down.
- Add a first-round signal, which fires once every check has reported. It
  drives `collector.started`.
- Add the `tailwatch_target_state{target}` gauge: 0=unknown, 1=up, 2=down,
  3=suppressed, 4=flapping.
- Add a recheck hook to the runtime, so a target's checks can run immediately
  outside the prober schedule.

This change implements the design-doc sections "Alert state machine" and
"Metrics" (`tailwatch_target_state`), plus the restart rule. It is part of phase 1
(MVP).

Non-goals:

- Multi-collector quorum with shared state is phase 3. Until then, quorum runs
  in Alertmanager over each collector's `tailwatch_target_state`; see design.md.
- Turning transitions into events, sinks, payloads and incident persistence
  all belong to `add-notifications`.
- `degraded` path warnings are phase 2. They do not change target state.

## Capabilities

### New Capabilities

- `alert-state-machine`: Per-target states and the rules for moving between
  them. Covers thresholds, flap damping, restart and restore behavior, the
  first-round signal, the transition record, and the `tailwatch_target_state`
  metric.
- `dependency-suppression`: How a parent's state suppresses and releases its
  dependents. Covers the grace hold for parent-caused failures, dependent
  lists on the parent's transitions, and immediate re-checks when the parent
  recovers.

### Modified Capabilities

None. The earlier changes are not archived yet, so this change only adds
capabilities.

## Impact

- **Code:**
  - New `cmd/tail-watch/alert.go` and `cmd/tail-watch/alert_test.go`.
  - `cmd/tail-watch/main.go` gains the wiring, the recheck hook and the metric
    registration.
- **Interfaces:**
  - Consumes `classifiedConsumer`, `Classified` and `classifier.Parent` from
    `add-failure-classification`.
  - Exposes `transitionConsumer`, `Transition`, `State`, `Restore`,
    `PendingRestores`, targeted discard, dirty sequenced snapshots and
    `FirstRoundDone` for `add-notifications`, and
    `AddTarget`, `RemoveTarget`, `UpdateParents` and `IsDown` for the learner in
    `add-failure-classification`. Adds a `recheck` hook to the runtime.
- **Dependencies:** none new. The change uses `tailscale.com/tstime`, and
  `tailscale.com/tstest` in tests.
