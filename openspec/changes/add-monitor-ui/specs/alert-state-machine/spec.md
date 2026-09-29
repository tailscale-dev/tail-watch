# Spec Delta

## MODIFIED Requirements

### Requirement: Runtime target registration

The state machine SHALL accept targets registered after startup, such as
implicit targets for learned parents that are not in the config, and dynamic
targets created through the monitor API. A registered
target SHALL start in `DOWN` when a restore is pending for its name (see
"Restore of open incidents"). Otherwise it SHALL start in `SUPPRESSED` when
its parent is already down, and in `UNKNOWN` in every other case. It SHALL follow every rule that applies to configured targets:
thresholds, flap damping, suppression, grace hold and transitions. Registering
a name that is already registered SHALL return an error.

The state machine SHALL allow removing a target whose last emitted state is
not `DOWN`, and SHALL refuse removal with an error when a configured target is
`DOWN`, so an open incident always gets its recovery. An implicit or dynamic
target MAY be removed while `DOWN`. For an implicit target, the learner asks only
after authoritative topology reconciliation; for a dynamic target, the operator
asks through the monitor API and accepts that the monitor is gone. In both cases
the removal leaves a terminal dirty snapshot from which the
notifier closes the incident with `reason: removed`. A refused caller retries
later. Removal
SHALL cancel the target's timers and drop its metric series. Any dependents
that were `SUPPRESSED` because of it SHALL be released, as when a parent
recovers.

#### Scenario: Implicit parent is registered

- **WHEN** an implicit target `nyc-router` is registered and its `tsping`
  fails `fail_threshold` times
- **THEN** it emits `UNKNOWN -> DOWN` and suppresses the targets that have it
  as parent

#### Scenario: Remove a down target

- **WHEN** removal is requested for a configured target whose last emitted
  state is `DOWN`
- **THEN** the removal returns an error and the target stays registered

#### Scenario: Remove a down implicit target

- **WHEN** removal is requested for an implicit target whose last emitted state
  is `DOWN`
- **THEN** it is removed, emits no transition, and its snapshot is a removed,
  dirty view that the notifier reconciles into a `reason: removed` recovery

#### Scenario: Remove a down dynamic target

- **WHEN** removal is requested for a dynamic target whose last emitted state
  is `DOWN`
- **THEN** it is removed, emits no transition, and its snapshot is a removed,
  dirty view that the notifier reconciles into a `reason: removed` recovery

#### Scenario: Remove a suppressed target

- **WHEN** removal is requested for a `SUPPRESSED` target whose last emitted
  state is `UP`
- **THEN** it is removed and emits no transition

#### Scenario: Remove an up target

- **WHEN** removal is requested for an `UP` target
- **THEN** it is removed, emits no transition, and `tailwatch_target_state` no
  longer has a series for it
