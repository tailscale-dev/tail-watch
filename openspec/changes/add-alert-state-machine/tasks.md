# Tasks

## 1. Types and core transitions (`cmd/tail-watch/alert.go`)

- [ ] 1.1 Define State, immutable sequenced Transition, alerter with owned validated parent graph, check counters and recheck hooks in `alert.go`; verify `go build ./cmd/tail-watch` and snapshot copies do not alias mutable target slices.
- [ ] 1.2 Implement `ObserveClassified` per-check counters and the `UNKNOWN -> UP` (silent, one success per check) and `UNKNOWN -> DOWN` (emits) rules; verify table tests in `alert_test.go` driven by synthetic `Classified` values and `tstest.NewClock`
- [ ] 1.3 Implement `UP -> DOWN` at `fail_threshold` with trip details (check, layer, err, `Since` = first failure of the run, failures, path) and counter reset on success; verify table tests for threshold reached, intermittent failure, and two checks where the first to trip is recorded
- [ ] 1.4 Implement `DOWN -> UP` (every `failedSinceDown` check has `recover_threshold` successes, others latest-success) with `DownSince` and the resolved trip's check/layer; verify table tests for full recovery, partial recovery, and a second check failing while down
- [ ] 1.5 Implement sequence allocation plus one bounded FIFO under `mu` in `alert.go`, drained outside the lock by one goroutine; verify forced concurrent producer scheduling still delivers DOWN before UP and callbacks can query state without deadlock.

## 2. Flap damping (`cmd/tail-watch/alert.go`)

- [ ] 2.1 Track `UP`/`DOWN` change times per target in a ring buffer of `flap_limit+1`, set the flapping mark and emit one `Flapping` transition when the count exceeds `flap_limit` within `flap_window`; verify a `tstest.Clock` test where the fifth change in 15m emits exactly one flapping transition and later changes emit none
- [ ] 2.2 Arm a `clock.AfterFunc(flap_window)` settle timer reset on each change; on firing clear the mark and emit a normal transition only when `state != emitted`; verify tests for settling after an unresolved `DOWN` (emits `DOWN -> UP` with original `DownSince`) and settling in the emitted state (emits nothing)

## 3. Dependency suppression (`cmd/tail-watch/alert.go`)

- [ ] 3.1 Use the owned parent graph updated by `UpdateParents` in `alert.go`, treating unregistered names as no parent and walking down ancestry with a visited set; verify override, learned, implicit, no-parent and transitive cases plus a rejected mixed-edge cycle.
- [ ] 3.2 On a parent's `-> DOWN`, move `UNKNOWN`/`UP`/held descendants to `SUPPRESSED` silently and fill sorted `Dependents`; redirect any would-be `DOWN` of a target with a down parent to `SUPPRESSED`; verify tests for "router dies with 14 dependents" and "already-down dependent stays DOWN and is not listed"
- [ ] 3.3 Implement the grace hold for `control`/`tunnel` trips (duration = parent interval × fail_threshold + timeout, `clock.AfterFunc`), resolving to `SUPPRESSED` if the parent goes down, else to `DOWN` if still failing; verify tests for device-trips-first, parent-never-goes-down (emits after 50s with original layer), check-recovered-during-hold (no transition), and `unrouted`/`lan`/`service` trips not held
- [ ] 3.4 Implement release in `alert.go` on recovery, removal or topology update when no down ancestor remains; reset counters/generation, enqueue rechecks within one second, and emit child DOWN only after fresh failures; verify HA A-to-B failover while A remains down and an independently failed child eventually pages.

## 4. Restore and first round (`cmd/tail-watch/alert.go`)

- [ ] 4.1 Implement `Restore(target, since) error` (registered and not yet observed: sets state and emitted to `DOWN`, marks all checks `failedSinceDown`; registered and observed: error; unregistered: record pending); verify `tstest.Clock` tests for restart-during-outage (no `DOWN`, one `DOWN -> UP` with the restored `DownSince`), target already observed (error, state unchanged), and unregistered name (nil, recorded pending)
- [ ] 4.2 Implement `FirstRoundDone() <-chan struct{}` closed via `sync.Once` when every target/check pair present at construction has at least one result (suppressed and timeouts included), closed at construction for zero targets; verify tests for the slow-fifth-check case, the zero-target case, and an implicit target added mid-round that does not delay the signal
- [ ] 4.3 Implement non-destructive `PendingRestores` and targeted `DiscardPendingRestore(name)` in `alert.go`; verify discarding one authoritatively removed target preserves another unresolved restore and first-pass completion never clears it.

## 5. Runtime target registration (`cmd/tail-watch/alert.go`)

- [ ] 5.1 Implement `AddTarget(*Target) error` (under `mu`: apply a pending restore → `DOWN` silently; else `SUPPRESSED` under a down parent; else `UNKNOWN`; error on duplicate name); verify `tstest.Clock` tests for restore-then-add (starts `DOWN`, no transition, recovers with the restored `DownSince`), add-without-restore (`UNKNOWN`), an implicit parent that trips and suppresses its dependents, registration under an already-down parent, and a duplicate name
- [ ] 5.2 Implement `IsDown(name) bool` (true for state `DOWN`/`SUPPRESSED` or emitted `DOWN`; false for `UNKNOWN`, `UP` and unregistered names); verify a table test covering each case, including flapping-after-unresolved-down
- [ ] 5.3 Implement `RemoveTarget(name) error` (refused while emitted `DOWN`; otherwise stop timers, delete the gauge series, release suppressed dependents with `recheck`); verify tests for remove-down (error, still registered), remove-suppressed, remove-up (no transition, series gone), and a property test that `!IsDown(n)` implies `RemoveTarget(n) == nil` over random state sequences

## 6. Metric and runtime wiring (`cmd/tail-watch/alert.go`, `cmd/tail-watch/main.go`)

- [ ] 6.1 Register gauge `tailwatch_target_state{target}` (0–4, flapping precedence) on the default registry, updated under `mu`; verify a test reading the gauge via `prometheus/testutil.ToFloat64` for up, suppressed and flapping (register on a test-local registry to keep tests independent)
- [ ] 6.2 Add FIFO rechecks in the runtime with eight workers, serialized per target/check and release-generation filtering; verify more than eight slow targets enqueue within one second without exceeding concurrency, and old in-flight timeouts do not affect new counters.
- [ ] 6.3 Wire the alerter into `main.go` with immutable initial parent graph, atomic topology updates and recheck hook; verify classified failures drive transitions and topology-only updates release affected targets without a new check.
- [ ] 6.4 Document states, thresholds, flapping, suppression/grace and the Alertmanager quorum rule in `README.md`; verify markdownlint passes

## 7. Delivery reconciliation

- [ ] 7.1 In `alert.go`, retain immutable latest notification-visible snapshots and dirty sequences on emission/intake overflow, including terminal removed-target snapshots; verify a newer update is not cleared by acknowledgment of an older generation.
- [ ] 7.2 In `alert.go`, expose atomic `UpdateParents` and targeted pending-restore discard after authoritative removal; verify dependency changes release or suppress immediately and failed discovery cannot discard restores.

## 8. Validation

- [ ] 8.1 Run `go build ./...`, `go vet ./...`, `go test -race ./...`; all pass and no test constructs a `prober.Prober`
- [ ] 8.2 Run `golangci-lint run ./...` and `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`; zero issues and no reachable vulnerabilities
- [ ] 8.3 Run markdownlint on changed Markdown and `openspec validate add-alert-state-machine --strict`; both pass
- [ ] 8.4 On a real tailnet, stop tailscaled on a monitored subnet router and confirm one router `DOWN` transition listing its dependents and no dependent transitions; restart it and confirm one recovery and silent dependent `UP`s
