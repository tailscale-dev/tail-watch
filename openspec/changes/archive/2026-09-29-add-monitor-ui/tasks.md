# Tasks

## 1. Config key and flag

- [x] 1.1 Add `UI *bool` to `rawCollector`, `UI bool` to `CollectorConfig` and the `--ui` tri-state flag in `cmd/tail-watch/config.go` and `cmd/tail-watch/main.go`; verify `TestUIFlagOverridesConfig` covers unset, `collector.ui: true`, `--ui` and `--ui=false`
- [x] 1.2 Fail startup with an error naming `collector.state_dir` when the UI is on and it is unset, in `cmd/tail-watch/main.go`; verify a test in `main_test.go` exits non-zero before preflight
- [x] 1.3 Ignore the UI setting under `--once`; verify a test in `main_test.go` runs the YAML checks and starts no server
- [x] 1.4 Update `examples/tail-watch.yaml` with a commented `collector.ui` line; verify `tail-watch check-config --config examples/tail-watch.yaml` passes

## 2. Shared validation

- [x] 2.1 Split the per-target and parent/cycle validation out of `resolveConfig` in `cmd/tail-watch/config.go` so a raw target list plus `defaults` timing can be resolved on its own; verify the existing `config_test.go` cases pass unchanged
- [x] 2.2 Keep raw YAML targets and base timing on `Config`, add `validateCandidate(existing, candidate)` returning errors located at the candidate, in `cmd/tail-watch/config.go`; verify table tests in `config_test.go` cover bad check, timeout not below interval, duplicate name, unknown parent, non-reachability parent and parent cycle

## 3. Dynamic store and startup merge

- [x] 3.1 Add `Target.Dynamic` in `cmd/tail-watch/config.go`, and add `cmd/tail-watch/dynamic.go` with `targets.json` load and atomic save (mode 0600); verify `dynamic_test.go` covers round trip, missing file, corrupt file, and file permissions
- [x] 3.2 Merge dynamic entries after YAML targets in `startCollectors` in `cmd/tail-watch/main.go`, skipping clashing or invalid entries with a warning and leaving them in the file; verify `dynamic_test.go` covers a name clash and an invalid entry
- [x] 3.3 Include dynamic names in the `configured` set given to `restoreOpen`; verify a test in `restore_test.go` restores an open incident for a dynamic target without a second page
- [x] 3.4 Warn at startup when `targets.json` exists and the UI is off, with the count of inactive monitors; verify a test in `dynamic_test.go` asserts the log line and that the file is not modified

## 4. Removal rule and live registration

- [x] 4.1 Allow removing a `DOWN` dynamic target in `removeLocked` in `cmd/tail-watch/alert_target.go`; verify a test in `alert_test.go` removes a `DOWN` dynamic target, emits no transition, and produces a removed dirty snapshot
- [x] 4.2 Verify the notifier closes it with `reason: removed`; covered by `TestDynamicTargetRemovedWhileDownClosesRemoved` in `restore_test.go`, which drives `reconcile()` with `countingClock`
- [x] 4.3 Keep the refusal for a `DOWN` configured target; verify the existing "Remove a down target" test still passes

- [x] 4.4 Add `classifier.AddConfigured`/`RemoveConfigured` in `cmd/tail-watch/classify.go` that update `configured`, `byName` and parent metrics under `c.mu`, and republish the parent graph through `publishParents`; verify `classify_test.go` adds a configured target with a parent, sees the edge in `proposedParents`, and removes it
- [x] 4.5 Call them from `runtimeRegistry.AddTarget`/`RemoveTarget` for non-implicit targets so the parent edge reaches the alerter before the first check; verify a `main_test.go` test that a dynamic child of a `DOWN` reachability parent starts `SUPPRESSED` and is released when the parent is removed
- [x] 4.6 Verify a learn round that changes the graph keeps the dynamic target's parent edge; verify a test in `learn_test.go` (or `classify_test.go`) running `finishRound` after a dynamic add with a learned router appearing
- [x] 4.7 Verify a failing check on a dynamic target is classified into a layer as for a YAML target; add the assertion to `classify_dynamic_test.go`

## 5. Monitor API

- [x] 5.1 Add `cmd/tail-watch/monitor_api.go` with the list handler joining targets to `alerter.Snapshots()`; verify `monitor_api_test.go` (httptest) checks `source`, `readonly` and `state` for YAML and dynamic entries
- [x] 5.2 Add create, replace, delete and validate handlers with the status codes in the spec, applying persist-then-apply under one mutex through `runtimeRegistry`; verify tests for 201, 200, 204, 404, 409 (duplicate, YAML target, dependents), 422 with several errors, and body-name mismatch
- [x] 5.3 Implement edit as remove then add with restore of the old definition on failure; verify tests for editing a `DOWN` monitor, editing a parent that has dependents, and a forced add failure that restores the old one
- [x] 5.4 Persist failure applies nothing; verify a test with an unwritable `state_dir` returns 500 and leaves no monitor running
- [x] 5.5 Add the middleware chain: JSON content type (415), Origin versus Host (403), then `authorize(r)` (403 on error), and the write audit log with remote address; verify tests for each rejection changing nothing, a denying `authorize`, and the audit line
- [x] 5.6 Register the routes in `runServer` in `cmd/tail-watch/main.go`, with explicit 404 handlers for `/ui/` and `/api/v1/` when the UI is off, and the startup log and non-loopback warning; verify tests that `/` still serves the status page in both modes and the log lines appear

## 6. SPA

- [x] 6.1 Add `cmd/tail-watch/ui/index.html`, `app.js`, `style.css` and the `go:embed` server with the CSP header; verify a test in `monitor_api_test.go` fetches `/ui/` and checks the header and that no asset references another origin
- [x] 6.2 Implement the list with 5s polling, source and state badges, and no controls on read-only rows; verify with Playwright against `scripts/live-stack.sh`: state changes without a reload, and a screenshot at desktop and phone width shows the list laid out cleanly
- [x] 6.3 Implement the add/edit dialog with per-field server errors, read-only name on edit, and the parent selector filtered to `icmp`/`tsping` monitors; verify with Playwright that `tcp:70000` shows the error at the checks field and creates nothing, and screenshot the dialog in its empty, filled and error states
- [x] 6.4 Implement delete confirmation, with the incident-closing message for a `DOWN` monitor, and the warning when editing a monitor that has dependents; verify with Playwright against a stopped target, including the confirmation text
- [x] 6.5 Style pass in a real browser with Playwright: desktop and phone widths, light and dark (`prefers-color-scheme`), long names and many rows, no horizontal page scroll, keyboard focus visible and the dialog closable with Escape; verify by reviewing the screenshots and fixing what looks wrong in `style.css` until it reads cleanly

## 7. Docs

- [x] 7.1 Add a UI section to `README.md` (or `docs/how-to/monitor-ui.md` if `add-user-documentation` has landed): enabling `--ui`, `state_dir`, the ACL grant for `tag:monitor`, `listen` guidance, the Compose shared-namespace caveat, and the risks in design.md; include Playwright screenshots of the list, the form with a validation error and the delete confirmation under `docs/images/`; verify `markdownlint` passes and the ACL snippet is valid HuJSON
- [x] 7.2 Add a `CLAUDE.md` layout entry for `dynamic.go`, `monitor_api.go` and `ui/`; verify the file names match the tree
- [x] 7.3 Add a `CHANGELOG.md` entry if the release process lists it; verify against `release-please-config.json`

## 8. Validation

- [x] 8.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test ./...`; verify all pass
- [x] 8.2 Run `mise run lint` (golangci-lint) and `govulncheck ./...`; verify no findings
- [x] 8.3 Run `markdownlint` over changed Markdown; verify zero findings
- [x] 8.4 Run the live stack with `--ui`, add a monitor that fails, confirm the notification, delete it while `DOWN`, and confirm the `removed` recovery arrives; verify the sequence in the sink output
- [x] 8.5 Run `mise exec -- openspec validate add-monitor-ui --strict`; verify it passes
