# Design

## Context

This change builds on two seams from the earlier changes:

- **From `add-tsmon-collector`:** every check run becomes a `Result`, which a
  per-target mutex delivers in completion order (`publish`, design D6). The
  prober owns the schedule. `prober.New()` may be called only once per process,
  so no unit test here constructs a `Prober`.
- **From `add-failure-classification`:** each `Result` arrives wrapped as
  `Classified{Result; Layer layer; Parent string; Path pathInfo}` through
  `classifiedConsumer.ObserveClassified`. A success has `Layer == ""`. The
  current parent of a target is available from `classifier.Parent(target)`.
  It returns a configured or *implicit* target name, or `""` when no parent is
  known. Implicit targets are created at runtime for learned parents that are
  not in the config (`Target.Implicit == true`, a single `tsping` check). The
  learner registers them through `AddTarget`, and it removes them through
  `RemoveTarget` once it has checked `IsDown` (D12). The failure layers are
  `unrouted`, `control`, `tunnel`, `lan` and `service`.

Two facts about `tailscale.com` v1.102.5 were checked in the module cache:

- **Clocks:** `tstime.Clock` offers `Now`, `NewTimer`, `NewTicker`,
  `AfterFunc` and `Since`. `tstest.NewClock(tstest.ClockOpts{Start: …})` gives
  a manual clock with `Advance` and `AdvanceTo`, whose `AfterFunc` callbacks
  fire on advance.
- **Running a probe on demand:** `prober` has no exported way to do this.
  `Prober.RunHandler` is an HTTP handler, it refuses continuous probes, and it
  calls the unexported `Probe.run`.

## Goals / Non-Goals

**Goals:**

- A deterministic state machine that tests can drive entirely with synthetic
  `Classified` values and a `tstest.Clock`.
- The exact seams `add-notifications` needs: `Transition`, `Restore`,
  `FirstRoundDone` and the consumer interface.

**Non-Goals:**

- Event types, severity, sink filtering and payloads, which belong to
  `add-notifications`.
- Persisting alert counters. `add-notifications` owns persistence of incidents,
  delivery state and pending durable event intents.
- Quorum across collectors (see D9).

## Decisions

### D1. One `alerter` value guarded by one mutex

```go
type State int // stateUnknown, stateUp, stateDown, stateSuppressed

type alerter struct {
    mu       sync.Mutex
    clock    tstime.Clock
    parents  map[string]string // validated graph, updated under mu
    recheck  func(target string)        // runtime hook, see D7
    targets  map[string]*targetState
    out      []transitionConsumer
    first    chan struct{} // closed once, see D8
}
```

- **Why one mutex:** suppression reads and writes several targets at once
  (a parent and its whole subtree). Per-target locks would need a lock order
  across the dependency graph. The work done under the lock is only a map
  update, which costs nothing next to a network check.
- **Where timers run:** grace holds and flap-settle timers use
  `clock.AfterFunc`, and their callbacks take the same mutex.

### D2. Counters per check, state per target

- **Counters:** `targetState` keeps `map[string]*checkState` holding
  `consecFail`, `consecOK`, `runStart` (the time of the first failure in the
  current run) and `failedSinceDown bool`.
- **Going down:** the first check to reach `fail_threshold` becomes the
  target's `trip`. The `trip` records the check, layer, error, `Since`,
  failure count and path.
- **Recovering:** recovery follows the spec rule. Every check with
  `failedSinceDown` needs `consecOK >= recover_threshold`, and every other
  check must have a success as its latest result.
- **Alternative rejected: counters per target.** One check that keeps
  succeeding (such as `icmp`) would keep resetting a target whose service
  (`tcp:445`) is dead.

### D3. Emitted state versus actual state

`targetState` keeps both `state` (actual) and `emitted` (the last state sent
to consumers).

- **Flap damping:** it compares these two fields when a target settles
  (spec "Flap damping"), which guarantees every `DOWN` reported to
  `add-notifications` eventually gets its recovery.
- **Flap counting:** only real `UP`↔`DOWN` changes count. Moves into and out
  of `SUPPRESSED`, and `UNKNOWN -> UP`, do not.
- **Sliding window:** the change times are stored in a ring buffer holding at
  most `flap_limit + 1` entries.
- **Settle timer:** each change resets a `clock.AfterFunc(flap_window)`.

### D4. A `control` or `tunnel` failure waits in a grace hold

**Question:** should a dependent whose failure is caused by its parent (layer
`control` or `tunnel`) be suppressed before the parent itself is `DOWN`?

**Decision: hold it, don't suppress it right away.** When such a failure hits
`fail_threshold` and the parent is not down yet, the target keeps its state
and arms a grace timer for `parent.interval × parent.fail_threshold +
parent.timeout`. That is the longest the parent can take to reach `DOWN`
from its first failure.

- **If the parent goes down first:** the hold resolves to `SUPPRESSED`.
- **If the timer fires first:** the collector checks that the tripping check
  is still failing, then emits `DOWN`.

**Why:**

- **Without a hold, the page goes to the wrong target.** Devices are often
  polled more often, or with a lower threshold, than their router, so their
  checks would page first. That breaks the design doc's goal of "page once
  for the router".
- **Suppressing on the layer alone is unsafe.** Presence can lag, and a
  learned parent can be stale. In those cases a real outage would never
  page.

**Scope limits:**

- The hold applies only when `Parent()` names a registered target, whether
  configured or implicit. With `""`, nothing will ever carry the dependent
  list.
- `unrouted`, `lan` and `service` failures are never held. With `unrouted`,
  no node handles the address, so there is no parent to wait for. With `lan`
  and `service`, the tunnel to the parent works, so the parent is not the
  cause.

### D5. Reachability suppression propagates through the whole subtree

- **What counts as a down parent:** a target whose parent is `DOWN` or
  `SUPPRESSED`. That makes suppression transitive, for example a switch
  behind a router with devices behind the switch.
- **When the parent's `UP -> DOWN` fires:** the alerter walks the reverse
  parent map held in the alerter. Only dedicated reachability targets can
  be parents, so a failed router HTTP service cannot suppress its LAN. It moves
  every `UNKNOWN` or `UP` descendant, and every descendant in a grace hold,
  to `SUPPRESSED`.
- **What the transition lists:** `Transition.Dependents` holds all of those
  names, sorted, direct and transitive together.
- **Cycles:** validate the full configured-plus-learned graph before every
  update. Mixed edges can form cycles even when either set alone is acyclic.
  Reject cyclic updates; keep a visited set as defense in depth.

### D6. Release on the parent's recovery

- **When the parent recovers or the graph changes:** release only suppressed
  targets that have no down ancestor in the new graph. Reset their counters
  and mark them `releasing`. `UpdateParents(graph)` also suppresses newly
  blocked targets under the same state lock, then requests rechecks outside
  it. Parent changes are processed without waiting for check results.
- **Recording the dependents:** their names go into the parent's recovery
  transition.
- **Triggering rechecks:** enqueue each released target within one second,
  after unlocking. Eight workers drain the FIFO; no promise is made that all
  checks start within one second when earlier checks are slow.
- **What a released target can do:** it goes `UP` silently after one success
  per check. It goes `DOWN`, with a transition from `SUPPRESSED`, after
  `fail_threshold` failures counted from the release.
- **Why one success is enough:** the target was never reported `DOWN`, so
  nothing needs a recovery, and waiting for `recover_threshold` would only
  delay its `UP` state and metric.

### D7. The recheck hook runs checks outside prober

prober cannot run a probe on demand from code (see Context). So the runtime
from `add-tsmon-collector` gains `recheck(target string)`. The call runs every
check of that target once, concurrently, through the same `runCheck` →
`publish` path the prober closure uses.

- **Limits:** it is bounded by a semaphore of 8 in-flight rechecks and uses
  each target's timeout.
- **Overlap with scheduled runs:** serialize execution per target/check.
  Assign a release generation and ignore pre-release results for the reset
  alert counters; schedule the new recheck after any existing run finishes.
  This prevents an old timeout from counting as a post-release failure.
- **Alternative rejected:** driving `Prober.RunHandler` over loopback HTTP.
  It refuses continuous probes, needs the debug mux, and adds a network hop
  just to call our own function.

### D8. First-round signal

`FirstRoundDone() <-chan struct{}` is closed by `sync.Once`. It fires when
the set of target/check pairs that have produced at least one result covers
every pair present at construction, which are the configured targets.
Implicit targets added later through `AddTarget` never enter the required
set, so they cannot delay `collector.started`. Their results are still
processed normally.

- **Results that count:** suppressed and held targets count, and so do
  timeouts, because every run produces a `Result`.
- **Zero targets:** the channel closes in the constructor.
- **Where the set lives:** the set of pairs seen so far is kept on the
  `alerter`, so that it is not tied to prober internals.

### D9. Quorum stays in Alertmanager

Collectors keep no shared state in phase 1. Each collector exports
`tsmon_target_state`, and quorum is expressed as a Prometheus rule:

```yaml
- alert: TargetDownQuorum
  expr: count by (target) (tsmon_target_state == 2) >= 2
  for: 1m
```

With two collectors in different regions, this pages only when both agree.
The phase 3 work, collectors sharing state, is out of scope.

### D10. Ordered transitions and reconciliation

Assign each immutable transition a process-local sequence and append it to one
shared bounded FIFO while holding `mu`. One drainer delivers FIFO entries to
consumers outside `mu`. Producers never independently race to acquire an
`emitMu`; callback scheduling cannot reorder transitions. Consumers may query
alert state without deadlocking and must only perform non-blocking intake.

Maintain the latest notification-visible state, trip details and sequence per
target under `mu`, respecting flap damping. On FIFO or notifier intake overflow,
mark that target dirty; retain terminal snapshots for removed targets until
acknowledged. The notifier reads immutable dirty snapshots and reconciles open
incidents even without new transitions. Acknowledging sequence N clears only
updates through N, never a newer dirty generation. Intermediate unreported
blips can coalesce; an already-open incident must not lose its resolve merely
because its recovery transition overflowed. FIFO events at or below a target's
reconciled sequence are ignored to prevent reopening an obsolete incident.

### D11. Restore and pending restores

`Restore(target string, since time.Time) error` takes `mu`, then:

- **Registered target, no result observed yet** (tracked by a per-target
  `observed` flag set in `ObserveClassified`): it sets `state` and `emitted`
  to `DOWN` with `downSince = since`, and marks every check
  `failedSinceDown`, so recovery needs `recover_threshold` successes on each
  check.
- **Registered target that has observed a result:** it returns an error.
- **Name not registered:** it records `pending[name] = since` and returns nil.

`AddTarget` checks `pending` under the same `mu` that creates the
`targetState`. Registration and restore therefore happen as one step, before
any `ObserveClassified` call for that target can take the lock. This closes
the race where an implicit router is registered and reports results before
change 4 gets to call `Restore`.

`PendingRestores() []string` returns pending names without mutation.
`DiscardPendingRestore(name string)` clears only one confirmed removal under
`mu`. It is never called merely because `FirstPassDone` fired.

**Ordering with change 4:** load the notification snapshot, recreate recorded
implicit targets and restore open incidents before checks start. Retain unresolved
implicit incidents during failed discovery; close them only after authoritative
reconciliation. Explicit removal of a configured target needs no discovery.

### D12. Registering and removing targets at runtime

- **`AddTarget(*Target) error`:** takes `mu` and creates a `targetState`. It
  applies a pending restore if there is one (D11). Otherwise the target starts
  in `SUPPRESSED` if its parent is already down, and in `UNKNOWN` if not. It fails on a
  duplicate name. The runtime from change 1 must also schedule the new
  target's checks, which the learner does through the collector. The
  alerter only tracks state.
- **`RemoveTarget(name string) error`:** refused while `emitted == DOWN`,
  because that open incident still needs its recovery. Otherwise it stops
  the grace and flap timers, deletes the gauge series, and releases any
  suppressed dependents as in D6.
- **`IsDown(name string) bool`:** returns true for current state `DOWN` or
  `SUPPRESSED`, or when `emitted == DOWN`. The learner defers removal while
  it is true. The invariant is `!IsDown(n)` ⇒ `RemoveTarget(n)` succeeds.
  Refusing in `RemoveTarget` is only a safety net.
- **Why refuse rather than queue:** a deferred removal inside the alerter
  would duplicate the learner's own retry, and it would hide config drift in
  state that nothing else can see.

## Deviations from the design doc

- **Implicit targets (D12).** The design doc assumes that every parent is a
  configured target. When a learned router is not in the config, the
  collector now monitors it implicitly, so suppression still works.

- **Grace hold (D4).** The design doc says a target moves to `SUPPRESSED`
  when its parent goes `DOWN`, but does not say what happens when a
  dependent trips first. The grace hold fills that gap. Without it, the
  "page once for the router" goal only works when the router trips first.
- **Suppression is transitive (D5).** The doc only talks about a parent and
  its direct dependents.
- **Released dependents return `UP` after one success (D6),** not
  `recover_threshold`. The doc says dependents "re-check at once and return
  to UP or DOWN".
- **`UNKNOWN -> DOWN` emits a transition.** The doc says only that restarts
  reach `UP` silently. A target that is down at startup still needs to page,
  unless `Restore` covers it.
- **Flapping is a mark on top of the state, not a fifth state.** The
  underlying state is still needed to decide what to emit when the target
  settles. `tsmon_target_state` still reports 4 while the mark is set.

## Risks / Trade-offs

- **[Risk] A grace hold delays a real device outage** by up to one parent
  detection window (about 50s with the example config) when the classifier
  wrongly blames the parent. → The hold applies only to `control` and
  `tunnel` layers, and it is bounded.
- **[Risk] A learned parent changes between results,** for example when an
  HA router fails over. → The parent is re-evaluated on every result. A
  graph update cancels stale grace timers and re-evaluates held targets against
  current ancestry; a healthy replacement releases suppression. The original
  hold deadline is not extended indefinitely by topology changes.
- **[Trade-off] Rechecks may wait behind a running check.** → Serialize by
  check and exclude pre-release generations from reset counters; the start
  latency is bounded by worker backlog and configured check timeouts.
- **[Risk] A consumer that blocks delivery stalls result processing.**
  One blocked consumer stalls the shared drainer. → The
  contract says consumers must not block, and the only production consumer
  (`add-notifications`' dispatcher) does a bounded, non-blocking enqueue.
  Delivery runs outside `mu`, so timers and `Restore` never wait on a
  consumer. Bounded overflow retains dirty snapshots for reconciliation.
