# Tasks

## 1. Config key and flag

- [ ] 1.1 Add `UI *bool` to `rawCollector`, `UI bool` to `CollectorConfig` and the `--ui` tri-state flag in `cmd/tail-watch/config.go` and `cmd/tail-watch/main.go`; verify `TestUIFlagOverridesConfig` covers unset, `collector.ui: true`, `--ui` and `--ui=false`
- [ ] 1.2 Fail startup with an error naming `collector.state_dir` when the UI is on and it is unset, in `cmd/tail-watch/main.go`; verify a test in `main_test.go` exits non-zero before preflight
- [ ] 1.3 Ignore the UI setting under `--once`; verify a test in `main_test.go` runs the YAML checks and starts no server
- [ ] 1.4 Update `examples/tail-watch.yaml` with a commented `collector.ui` line; verify `tail-watch check-config --config examples/tail-watch.yaml` passes

## 2. Shared validation

- [ ] 2.1 Split the per-target and parent/cycle validation out of `resolveConfig` in `cmd/tail-watch/config.go` so a raw target list plus `defaults` timing can be resolved on its own; verify the existing `config_test.go` cases pass unchanged
- [ ] 2.2 Keep raw YAML targets and base timing on `Config`, add `validateCandidate(existing, candidate)` returning errors located at the candidate, in `cmd/tail-watch/config.go`; verify table tests in `config_test.go` cover bad check, timeout not below interval, duplicate name, unknown parent, non-reachability parent and parent cycle

## 3. Dynamic store and startup merge

- [ ] 3.1 Add `Target.Dynamic` in `cmd/tail-watch/config.go`, and add `cmd/tail-watch/dynamic.go` with `targets.json` load and atomic save (mode 0600); verify `dynamic_test.go` covers round trip, missing file, corrupt file, and file permissions
- [ ] 3.2 Merge dynamic entries after YAML targets in `startCollectors` in `cmd/tail-watch/main.go`, skipping clashing or invalid entries with a warning and leaving them in the file; verify `dynamic_test.go` covers a name clash and an invalid entry
- [ ] 3.3 Include dynamic names in the `configured` set given to `restoreOpen`; verify a test in `restore_test.go` restores an open incident for a dynamic target without a second page
- [ ] 3.4 Warn at startup when `targets.json` exists and the UI is off, with the count of inactive monitors; verify a test in `dynamic_test.go` asserts the log line and that the file is not modified

## 4. Removal rule

- [ ] 4.1 Allow removing a `DOWN` dynamic target in `removeLocked` in `cmd/tail-watch/alert_target.go`; verify a test in `alert_test.go` removes a `DOWN` dynamic target, emits no transition, and produces a removed dirty snapshot
- [ ] 4.2 Verify the notifier closes it with `reason: removed`; add the case to `notify_integration_test.go` using `countingClock` and `waitTimers` before `Advance`, per CLAUDE.md
- [ ] 4.3 Keep the refusal for a `DOWN` configured target; verify the existing "Remove a down target" test still passes

## 5. Monitor API

- [ ] 5.1 Add `cmd/tail-watch/monitor_api.go` with the list handler joining targets to `alerter.Snapshots()`; verify `monitor_api_test.go` (httptest) checks `source`, `readonly` and `state` for YAML and dynamic entries
- [ ] 5.2 Add create, replace, delete and validate handlers with the status codes in the spec, applying persist-then-apply under one mutex through `runtimeRegistry`; verify tests for 201, 200, 204, 404, 409 (duplicate, YAML target, dependents), 422 with several errors, and body-name mismatch
- [ ] 5.3 Implement edit as remove then add with restore of the old definition on failure; verify tests for editing a `DOWN` monitor, editing a parent that has dependents, and a forced add failure that restores the old one
- [ ] 5.4 Persist failure applies nothing; verify a test with an unwritable `state_dir` returns 500 and leaves no monitor running
- [ ] 5.5 Add the middleware chain: JSON content type (415), Origin versus Host (403), then `authorize(r)` (403 on error), and the write audit log with remote address; verify tests for each rejection changing nothing, a denying `authorize`, and the audit line
- [ ] 5.6 Register the routes in `runServer` in `cmd/tail-watch/main.go`, with explicit 404 handlers for `/ui/` and `/api/v1/` when the UI is off, and the startup log and non-loopback warning; verify tests that `/` still serves the status page in both modes and the log lines appear

## 6. SPA

- [ ] 6.1 Add `cmd/tail-watch/ui/index.html`, `app.js`, `style.css` and the `go:embed` server with the CSP header; verify a test in `monitor_api_test.go` fetches `/ui/` and checks the header and that no asset references another origin
- [ ] 6.2 Implement the list with 5s polling, source and state badges, and no controls on read-only rows; verify by loading the page against `scripts/live-stack.sh` and seeing state change without a reload
- [ ] 6.3 Implement the add/edit dialog with per-field server errors, read-only name on edit, and the parent selector filtered to `icmp`/`tsping` monitors; verify in the browser that `tcp:70000` shows the error at the checks field and creates nothing
- [ ] 6.4 Implement delete confirmation, with the incident-closing message for a `DOWN` monitor, and the warning when editing a monitor that has dependents; verify in the browser against a stopped target

## 7. Docs

- [ ] 7.1 Add a UI section to `README.md` (or `docs/how-to/monitor-ui.md` if `add-user-documentation` has landed): enabling `--ui`, `state_dir`, the ACL grant for `tag:monitor`, `listen` guidance, the Compose shared-namespace caveat, and the risks in design.md; verify `markdownlint` passes and the ACL snippet is valid HuJSON
- [ ] 7.2 Add a `CLAUDE.md` layout entry for `dynamic.go`, `monitor_api.go` and `ui/`; verify the file names match the tree
- [ ] 7.3 Add a `CHANGELOG.md` entry if the release process lists it; verify against `release-please-config.json`

## 8. Validation

- [ ] 8.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test ./...`; verify all pass
- [ ] 8.2 Run `mise run lint` (golangci-lint) and `govulncheck ./...`; verify no findings
- [ ] 8.3 Run `markdownlint` over changed Markdown; verify zero findings
- [ ] 8.4 Run the live stack with `--ui`, add a monitor that fails, confirm the notification, delete it while `DOWN`, and confirm the `removed` recovery arrives; verify the sequence in the sink output
- [ ] 8.5 Run `mise exec -- openspec validate add-monitor-ui --strict`; verify it passes
