# Monitor UI browser tests

`cmd/tail-watch/ui_browser_test.go` drives the embedded monitor page in Chromium with
Playwright. It mounts the real UI and monitor manager with a fake peer source, so it needs
no tailnet and no Docker.

It skips when the Playwright driver or its browser is not installed, so `go test ./...`
still passes without them. Install both once per machine:

```bash
mise exec -- go run github.com/mxschmitt/playwright-go/cmd/playwright install chromium
```

Run only these tests:

```bash
mise exec -- go test -count=1 -run UIBrowser -v ./cmd/tail-watch
```

A native `<datalist>` popup is browser chrome that Playwright cannot click. The tests fill
the Address field with a suggestion's value instead, which fires the same `input` event a
choice from the popup does.

For a check against real tailnet nodes, use the
[live stack](live-notification-test.md#bringing-up-only-the-tailnet-side) and open the UI
by hand.
