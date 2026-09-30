## Why

The peer picker fills only the Address field. An operator who picks `lab-nas` still has to
type `lab-nas` again into Name, although a peer name is already a valid monitor name. The UI
also has no automated browser test, so behavior such as this is checked by hand.

## What Changes

- Choosing a peer suggestion fills Name with the peer's name as well as Address, unless the
  operator already typed a Name.
- Add Playwright UI tests that drive the embedded page against a collector with a fake peer
  source, so they need no tailnet and run in CI.

## Capabilities

### Modified Capabilities

- `monitor-ui`: Address suggestions also fill the Name field.

## Impact

- `cmd/tail-watch/ui/app.js`, a new Playwright test in `cmd/tail-watch/`, the `playwright-go`
  test dependency in `go.mod`, `docs/how-to/monitor-ui.md`, `docs/contributing/`.
