# Tasks

## 1. Probe endpoint

- [x] 1.1 Add `handleProbe` to `cmd/tail-watch/monitor_api.go`: decode the same body as
  create, validate through the same path as `check()` (422 with the shared `errors`
  shape, 409 for a YAML-target name), then run each check's `Run` once against the
  app's `netEnv`, concurrently, each bounded by the definition's `timeout` and the
  whole call by a 15 s context cap; answer 200 with `results`, one entry per check in
  definition order (`check`, `ok`, `latency_ms`, `error` when failed); verify table-driven
  tests in `monitor_api_test.go` cover: all pass, connection refused, non-200 HTTP
  status (httptest server), invalid definition runs no check, slow check bounded, and
  the list is unchanged afterwards
- [x] 1.2 Register the route in `cmd/tail-watch/monitor_guard.go` as
  `POST /api/v1/probe` wrapped with `guard(..., true)` so it is content-type checked,
  origin checked, authorized and audited with op `probe`; verify tests cover: 415 for
  a non-JSON content type, 403 for a mismatched Origin, 403 and no check run when the
  authorization decision denies, an audit log line with the caller's remote address,
  and 404 when the UI is off
- [x] 1.3 Verify the probe holds `monitorManager.mu` only for validation (checks run
  after the lock is released) with a test that a slow probe does not block a concurrent
  create

## 2. UI save gate

- [x] 2.1 In `cmd/tail-watch/ui/index.html` add a confirmation dialog modeled on the
  delete one: a list of failing checks with their errors, "Save anyway" and "No, let me
  edit"; verify the page loads with no console errors (browser test)
- [x] 2.2 In `cmd/tail-watch/ui/app.js` change `save()` to POST the definition to
  `/api/v1/probe` first: 422 goes through the existing `showErrors`, all-`ok` proceeds
  to the unchanged create/replace call, failures open the dialog from 2.1 — "Save
  anyway" proceeds, "No" closes it and marks the failing check rows with the existing
  per-row error display; a failed probe request shows the error and saves nothing;
  verify the browser tests (playwright, as in `ui_checks_browser_test.go`) cover: pass
  saves with no dialog, fail asks and confirms, fail asks and declines leaving the form
  intact with the row marked, and edit is gated the same way

## 3. Check control

- [x] 3.1 Switch the form's check control in `cmd/tail-watch/ui/app.js` from
  `/api/v1/validate` to `/api/v1/probe` and render per-check results (outcome and
  latency) in the result box instead of only "Looks valid."; verify the browser test
  shows one passing check with its latency and one failing check with its error

## 4. Validation

- [x] 4.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test ./...`; verify
  all pass
- [x] 4.2 Run `mise run lint` (golangci-lint) and `govulncheck ./...`; verify no
  findings
- [x] 4.3 Run `markdownlint-cli2` over `openspec/changes/add-monitor-probe-gate/`; verify
  zero findings
- [x] 4.4 Run `mise exec -- openspec validate add-monitor-probe-gate --strict`; verify
  it passes
- [x] 4.5 Manually against a live collector with the UI on: probe a definition with one
  passing and one failing check; verify the dialog lists the failing check, "Save
  anyway" creates the monitor, and "No, let me edit" returns to the intact form
