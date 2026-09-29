# Spec Delta

## Purpose

Turns a stream of classified check results into per-target states (UNKNOWN,
UP, DOWN, SUPPRESSED, flapping) and emits a transition only when a target's
alertable state changes, so one lost packet never pages and one outage pages
once.

## ADDED Requirements

### Requirement: Per-target state

The collector SHALL keep exactly one state per target: `UNKNOWN`, `UP`, `DOWN`
or `SUPPRESSED`. It SHALL keep consecutive-failure and consecutive-success
counters per target/check pair. Every target SHALL start in `UNKNOWN` when the
process starts, unless it is restored as described below. State SHALL be held
in memory only.

#### Scenario: Fresh start

- **WHEN** tail-watch starts with three targets and no restored incidents
- **THEN** all three targets are `UNKNOWN` and `tailwatch_target_state` is 0 for
  each

### Requirement: Leaving UNKNOWN

A target in `UNKNOWN` SHALL move to `UP` once every one of its checks has
reported at least one success since start, and this move SHALL emit no
transition. A target in `UNKNOWN` SHALL move to `DOWN` when any one of its
checks reaches `fail_threshold` consecutive failures, and this move SHALL emit
a transition from `UNKNOWN` to `DOWN`.

#### Scenario: Silent start

- **WHEN** a target with checks `icmp` and `tcp:445` gets one success from each
  after a restart
- **THEN** it becomes `UP` and no transition is emitted

#### Scenario: Down at start

- **WHEN** a target's `tcp:445` check fails 3 times in a row after a restart
  with `fail_threshold: 3`
- **THEN** a transition `UNKNOWN -> DOWN` is emitted for that target

### Requirement: Going down

A target in `UP` SHALL move to `DOWN` when any one of its checks reaches
`fail_threshold` consecutive failures. The emitted transition SHALL identify
the tripping check, its failure layer and last error, the consecutive failure
count, the time of the first failure in that run (`Since`), and the latest
path details. Failures below the threshold SHALL emit nothing. A success
SHALL reset that check's failure counter. Silent promotion to `UP` at startup
or after release SHALL preserve other checks' consecutive-failure runs.

#### Scenario: Threshold reached

- **WHEN** target `nyc-nas` is `UP`, `fail_threshold` is 3, and `tcp:445` fails
  at 14:02:11, 14:02:41 and 14:03:11
- **THEN** a transition `UP -> DOWN` is emitted with check `tcp:445`,
  failures 3 and `Since` 14:02:11

#### Scenario: Intermittent failure

- **WHEN** `tcp:445` fails twice, succeeds once, then fails twice
- **THEN** no transition is emitted and the target stays `UP`

#### Scenario: Failure run spans silent promotion

- **WHEN** `tcp:445` succeeds once and then fails twice, `icmp` reports its
  first success and silently promotes the target to `UP`, and `tcp:445` fails
  a third time with `fail_threshold: 3`
- **THEN** the target emits `UP -> DOWN` with failures 3 and `Since` equal to
  the first TCP failure, both at startup and following a suppression release

### Requirement: Recovering

A target in `DOWN` SHALL move to `UP` only when every check that failed since
the target went `DOWN` has `recover_threshold` consecutive successes, and no
other check has a failure as its latest result. The emitted transition SHALL
carry the tripping check and layer of the `DOWN` it resolves and the time the
target went down (`DownSince`), so that the outage length can be computed.
Releasing a suppressed target resets its counters and starts a fresh run, so a
recovery owed for an incident emitted before suppression SHALL be emitted after
one success on each of the target's checks instead of `recover_threshold`
successes (see the `dependency-suppression` capability).

#### Scenario: Recovery after two successes

- **WHEN** a `DOWN` target with `recover_threshold: 2` gets two consecutive
  successes on its failing check and its other checks are passing
- **THEN** a transition `DOWN -> UP` is emitted with `DownSince` equal to the
  original `Since`

#### Scenario: Partial recovery

- **WHEN** a target went `DOWN` on `tcp:445` and later `icmp` also failed, and
  now `tcp:445` has two successes but `icmp` has only one
- **THEN** the target stays `DOWN`

### Requirement: Flap damping

The collector SHALL count a target's `UP`/`DOWN` changes within a sliding
`flap_window`. When a change would make the count exceed `flap_limit`, the
target SHALL be marked flapping. Instead of that change's normal transition, a
single transition with `Flapping` set SHALL be emitted. While a target is
flapping, the collector SHALL keep tracking its state but SHALL emit no
transitions for it. The flapping mark SHALL clear once the target's state has
not changed for `flap_window`. When it clears, the collector SHALL emit a
normal transition if the current state differs from the last state it emitted
for that target, and nothing otherwise. Settling in `SUPPRESSED` SHALL remain
silent and SHALL preserve any outstanding emitted `DOWN`, its trip details and
`DownSince` until recovery. Notification-visible snapshots SHALL preserve that
open incident even after the flapping mark clears.

#### Scenario: Becoming flapping

- **WHEN** `flap_limit` is 4, `flap_window` is 15m, and a target changes state
  for the fifth time within 15 minutes
- **THEN** exactly one transition with `Flapping` set is emitted, and
  `tailwatch_target_state` for that target is 4

#### Scenario: Settling down after an unresolved down

- **WHEN** the last transition emitted before flapping was `UP -> DOWN`, and
  the target then stays `UP` for 15 minutes
- **THEN** the flapping mark clears and a `DOWN -> UP` transition is emitted

#### Scenario: Settling in the already-reported state

- **WHEN** the last emitted state was `UP` and the target settles `UP` for
  `flap_window`
- **THEN** the flapping mark clears and no transition is emitted

#### Scenario: Settling while suppressed with an unresolved down

- **WHEN** a flapping target is internally `UP` with its last emitted state
  `DOWN`, its parent goes down, and it remains `SUPPRESSED` for `flap_window`
- **THEN** the flapping mark clears without emitting a suppression transition,
  its snapshot still reports the original open `DOWN`, and removal remains
  refused until the incident receives its recovery

### Requirement: Restore of open incidents

The state machine SHALL accept, before the first check result, a restore of a
target as `DOWN` with a given start time. A restored target SHALL start in
`DOWN` with `DownSince` set to that time, SHALL NOT emit a `DOWN` transition,
and SHALL emit its eventual recovery exactly as if it had gone down in this
process. Restoring a registered target that has already observed a result
SHALL return an error. Restoring a name that is not registered yet SHALL
record a pending restore instead of failing. When a target with that name is
registered later, the pending restore SHALL be applied atomically as part of
registration, before any result for that target can be observed. The target
then starts in `DOWN` with the recorded start time, without a transition. The
state machine SHALL expose pending restore names without clearing them and
provide a targeted discard operation for an authoritatively removed target.
First-pass completion SHALL NOT discard pending restores.

#### Scenario: Restart during an outage

- **WHEN** `nyc-nas` is restored as `DOWN` since 14:02:11 and later recovers
- **THEN** no `DOWN` transition is emitted, and one `DOWN -> UP` transition is
  emitted with `DownSince` 14:02:11

#### Scenario: Restore then add

- **WHEN** a restore names `nyc-router` before it is registered, and the
  implicit target `nyc-router` is registered later
- **THEN** `nyc-router` starts in `DOWN` with the restored start time, no
  transition is emitted, and its first results count toward recovery

#### Scenario: Add without restore

- **WHEN** an implicit target is registered and no restore is pending for its
  name
- **THEN** it starts in `UNKNOWN`

#### Scenario: Discard one confirmed removal

- **WHEN** restores are pending for `old-router` and `gone-nas`, and only
  `gone-nas` is authoritatively removed
- **THEN** discarding `gone-nas` leaves `old-router` pending, and a later
  registration of `old-router` still starts DOWN with its original start time

#### Scenario: Target already reported

- **WHEN** a restore names a registered target that has already observed a
  result
- **THEN** the restore returns an error and the target's state is unchanged

### Requirement: First round complete

The state machine SHALL signal, exactly once, when every target/check pair
present when the state machine was constructed (the configured targets) has
delivered at least one result, whether success or failure, including results
for suppressed targets. Targets registered later SHALL NOT delay the signal.
With zero configured targets, it SHALL signal immediately.

#### Scenario: Slow check

- **WHEN** four of five checks have reported and the fifth reports a timeout
  failure
- **THEN** the first-round signal fires after the fifth result, not before

#### Scenario: Implicit target added before the round completes

- **WHEN** an implicit target is registered while configured checks are still
  pending, and every configured check then reports
- **THEN** the signal fires without waiting for the implicit target's check

### Requirement: Runtime target registration

The state machine SHALL accept targets registered after startup, such as
implicit targets for learned parents that are not in the config. A registered
target SHALL start in `DOWN` when a restore is pending for its name (see
"Restore of open incidents"). Otherwise it SHALL start in `SUPPRESSED` when
its parent is already down, and in `UNKNOWN` in every other case. It SHALL follow every rule that applies to configured targets:
thresholds, flap damping, suppression, grace hold and transitions. Registering
a name that is already registered SHALL return an error.

The state machine SHALL allow removing a target whose last emitted state is
not `DOWN`, and SHALL refuse removal with an error when it is `DOWN`, so an
open incident always gets its recovery. The caller retries later. Removal
SHALL cancel the target's timers and drop its metric series. Any dependents
that were `SUPPRESSED` because of it SHALL be released, as when a parent
recovers.

#### Scenario: Implicit parent is registered

- **WHEN** an implicit target `nyc-router` is registered and its `tsping`
  fails `fail_threshold` times
- **THEN** it emits `UNKNOWN -> DOWN` and suppresses the targets that have it
  as parent

#### Scenario: Remove a down target

- **WHEN** removal is requested for a target whose last emitted state is
  `DOWN`
- **THEN** the removal returns an error and the target stays registered

#### Scenario: Remove a suppressed target

- **WHEN** removal is requested for a `SUPPRESSED` target whose last emitted
  state is `UP`
- **THEN** it is removed and emits no transition

#### Scenario: Remove an up target

- **WHEN** removal is requested for an `UP` target
- **THEN** it is removed, emits no transition, and `tailwatch_target_state` no
  longer has a series for it

### Requirement: Down query

The state machine SHALL answer whether a target is down. The answer SHALL be
true when the target's current state is `DOWN` or `SUPPRESSED`, or when its
last emitted state is `DOWN` (for example while flapping). It SHALL be false
for `UNKNOWN` and `UP` targets whose last emitted state is not `DOWN`, and for
names that are not registered. Whenever the answer is false, removing that
target SHALL succeed.

#### Scenario: Down query on a suppressed target

- **WHEN** the query names a `SUPPRESSED` target
- **THEN** it returns true

#### Scenario: Flapping after an unresolved down

- **WHEN** a flapping target is currently `UP` but its last emitted state is
  `DOWN`
- **THEN** the query returns true

#### Scenario: Unknown name

- **WHEN** the query names a target that is not registered
- **THEN** it returns false

### Requirement: Transition record

Every emitted transition SHALL contain the target, the previous and new state,
the flapping flag, the check name, the failure layer, the error, `Since`, the
emission time `At`, the consecutive failure count, the latest path details,
the names of suppressed dependents (see the `dependency-suppression`
capability), and for recoveries `DownSince`. Transitions SHALL receive a monotonically increasing process-local sequence
under the state mutex and enter one shared FIFO under that same mutex. A single
drainer SHALL deliver immutable transitions outside the mutex, in sequence
order, to every registered consumer. The FIFO SHALL be bounded; overflow SHALL
mark affected targets dirty for reconciliation instead of blocking checks.
The alerter SHALL expose an immutable snapshot of the latest notification-visible
state (respecting flap damping), trip details and sequence for every target.
A dirty mark SHALL clear only after a consumer has reconciled through its
sequence; a concurrent newer update SHALL remain dirty.

#### Scenario: Ordered delivery

- **WHEN** a target goes `DOWN` and then `UP`
- **THEN** every consumer receives the `DOWN` transition before the `UP`
  transition

### Requirement: Target state metric

The collector SHALL export the gauge `tailwatch_target_state{target}` with values
0=unknown, 1=up, 2=down, 3=suppressed and 4=flapping. The flapping value SHALL
take precedence over the underlying state while the mark is set.

#### Scenario: Suppressed target

- **WHEN** target `nyc-core-switch` is `SUPPRESSED`
- **THEN** `tailwatch_target_state{target="nyc-core-switch"}` is 3

#### Scenario: Concurrent emission

- **WHEN** DOWN is assigned sequence 10 and UP sequence 11, and the thread that
  produced DOWN is paused immediately after releasing the state mutex
- **THEN** consumers still receive 10 before 11, and may query state in callbacks
  without deadlocking

#### Scenario: Overflow does not strand an incident

- **WHEN** the bounded emission FIFO drops a recovery notification and the target
  stays UP without further transitions
- **THEN** its dirty snapshot remains available until the notifier reconciles it
  and schedules recovery for any open incident

### Requirement: Release generations

Releasing a suppressed target SHALL advance its result generation. Results from
runs started before release SHALL NOT affect reset counters. Scheduled runs and
rechecks of one target/check SHALL execute serially, and rechecks SHALL start
a new run in the release generation.

#### Scenario: Old timeout after release

- **WHEN** a pre-release TCP attempt times out after its target was released
- **THEN** it does not count toward the new failure threshold
