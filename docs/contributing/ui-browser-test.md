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

## Teams card render test

`cmd/tail-watch/teams_render_test.go` uses the same Playwright setup to render every Teams
golden card in `cmd/tail-watch/testdata/teams/` the way the
[Adaptive Cards Designer](https://adaptivecards.microsoft.com/designer)'s Teams host does.
It loads Microsoft's `adaptivecards` renderer, the Designer's Teams light host config, and
`markdown-it` for TextBlock markdown. It needs no Teams tenant.

```bash
mise exec -- go test -count=1 -run TestTeamsCardRender -v ./cmd/tail-watch
```

It fails when the renderer reports a parse or validation message, a markdown escape or
`<`/`>` shows in the text, a card has a link other than the `status_url` link, detail lines
run together, or a headline color differs from the host config. To look at the cards, set
`TAIL_WATCH_CARD_SHOTS` to a directory and the test saves one PNG per card there.

The renderer files are vendored, with their MIT licenses, in
`cmd/tail-watch/testdata/teams/render/`: `adaptivecards.min.js` from `adaptivecards` 3.0.6,
`markdown-it.umd.min.js` from `markdown-it` 15.0.2, and `microsoft-teams-light.json` from
`adaptivecards-designer` 2.4.4 (`lib/hostConfigs/`). To update one, copy the same file from
the newer npm package, keep it unmodified, update the versions here, and rerun the test.

The test checks what the renderer draws, not the Teams client, so it cannot show the
"via Workflows" footer or catch a difference between the client and the Designer. A
`notify-test` against a real workflow is still the last check; see
[Send alerts to Microsoft Teams](../how-to/teams-sink.md).
