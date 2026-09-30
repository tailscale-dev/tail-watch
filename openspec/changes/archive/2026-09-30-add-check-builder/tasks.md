# Tasks

## 1. Convert rows and strings

- [x] 1.1 In `cmd/tail-watch/ui/app.js`, add DOM-free `checkToString(row, addr)` and `checkFromString(s, addr)` in one block between `// --- check conversion` and `// --- end check conversion ---`; cover ICMP, TSPing, TCP, HTTP (scheme, port, path, text), bracketed IPv6, and the Custom fallback; verify with task 1.2
- [x] 1.2 Add `cmd/tail-watch/ui_checks_test.go`, which runs those functions under bun or node like `ui_route_test.go` and checks round trips and the Custom cases (`tls:443`, other-host URL, `tcp:x`, `http://h:80/` keeps `:80`); verify it skips without a runtime and fails when the functions are broken

## 2. Form rows

- [x] 2.1 In `cmd/tail-watch/ui/index.html`, replace the `#f-checks` textarea with a `role="group"` container, an "Add check" button and an error line, labelled by the existing Checks label; in `ui/style.css`, style the rows; verify the page still has no external resource
- [x] 2.2 In `cmd/tail-watch/ui/app.js`, render rows with `el()` and `textContent`: type select, TCP port, HTTP scheme/port/path/text, Custom raw text, a remove button named "Remove check N", and focus on a new row; open with no rows on add and with parsed rows on edit; verify with task 2.4
- [x] 2.3 In `cmd/tail-watch/ui/app.js`, make `collect()` send every row through `checkToString` without dropping any, and make `showErrors` put a `checks[i]` message on row `i` and an unindexed one with the list; verify with task 2.4
- [x] 2.4 In `cmd/tail-watch/ui_browser_test.go`, add a `addCheck` helper and update the tests that fill `#f-checks`; add tests for: opens with no rows, empty save fails then TCP 445 succeeds, remove a row, incomplete row error on its row, HTTP host from address and after an address change, IPv6 brackets, edit shows rows, `tls:443` Custom row with the server error on it, other-host URL as Custom; verify each passes in Chromium and that the empty-save test fails if the server message is missing

## 3. Documentation

- [x] 3.1 Update "Add a monitor" in `docs/how-to/monitor-ui.md` for the check rows and the Custom row, and retake `docs/images/monitor-ui-form-error.png` from the rows; verify the image shows no real host names
- [x] 3.2 Add the row conversion functions and their test markers to the "Monitor UI browser tests" notes in `CLAUDE.md`; verify markdownlint

## 4. Validation

- [x] 4.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test -race ./...`; verify all pass
- [x] 4.2 Run `mise run lint` and `govulncheck ./...`; verify no findings
- [x] 4.3 Run `markdownlint` over `README.md`, `docs/`, `CLAUDE.md` and `openspec/changes/add-check-builder/`; verify zero findings
- [x] 4.4 Run `mise exec -- openspec validate add-check-builder --strict`; verify it passes
- [x] 4.5 With `scripts/live-stack.sh up` and the collector container, add a monitor from rows in the UI and confirm it reaches `UP`; run `live-stack.sh down`; verify the devices are deleted
