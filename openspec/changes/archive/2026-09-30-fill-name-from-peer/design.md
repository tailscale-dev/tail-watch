## Context

`ui/app.js` fills a `<datalist>` from `GET /api/v1/peers`. Choosing an option only sets the
Address input. Name is required and cannot change after saving, so a wrong guess is costly.

## Decisions

### Fill Name only when the operator did not type one

On `input` in the Address field, if the value equals a suggestion's value, set Name to that
value when Name is empty or still holds the name the picker filled earlier. A Name the
operator typed is never overwritten. Choosing another suggestion replaces a picker-filled
name. While editing an existing monitor, Name is read-only and is never touched.

Alternative: always overwrite Name. Rejected: it destroys typed input.

### Playwright through `playwright-go`, against a fake peer source

The repo has no Node app and no `package.json`, so the test uses `playwright-go` from a Go
test. It mounts the real `uiHandler` and `monitorManager` with a fake `peerSource`, so it
needs no tailnet. It skips when the browser driver is not installed. The live Docker stack
stays for the collector-level tests and is not used by the UI test.

## Risks / Trade-offs

- [New test dependency and a browser download] → The test skips when the driver is missing;
  `docs/contributing/` says how to install it.
