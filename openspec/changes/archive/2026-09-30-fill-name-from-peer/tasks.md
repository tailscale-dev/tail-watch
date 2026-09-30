## 1. Fill the name

- [x] 1.1 In `cmd/tail-watch/ui/app.js`, fill Name on an Address `input` that equals a suggestion, only when Name is empty or still the picker's value, and never while editing; verify in the Playwright test
- [x] 1.2 Reset the picker-filled marker when the form opens; verify a reopened form does not treat an old name as filled

## 2. Playwright UI tests

- [x] 2.1 Add `github.com/playwright-community/playwright-go` to `go.mod`; verify `go mod tidy` and the build pass
- [x] 2.2 Add `cmd/tail-watch/ui_browser_test.go`: mount `uiHandler` and a `monitorManager` with a fake `peerSource`, skip when the driver is missing; verify it skips cleanly without one
- [x] 2.3 Cover pick fills both fields, typed name kept, second pick replaces, edit keeps name, routing hint, unseeded and failed fetch still submit, and a hostile peer name renders as text; verify each passes in Chromium
- [x] 2.4 Document the driver install in `docs/contributing/`; verify markdownlint

## 3. Documentation and checks

- [x] 3.1 Update "Pick a node from the list" in `docs/how-to/monitor-ui.md`; verify against the UI
- [x] 3.2 Run `mise run check` and `openspec validate fill-name-from-peer --strict`; verify both pass
