# Tasks

## 1. Peer list endpoint

- [x] 1.1 Add the `peerSource` interface, the `peerView` response type and `handlePeers` to `cmd/tail-watch/monitor_api.go`; write the handler test first in `cmd/tail-watch/monitor_api_test.go` (table-driven over: seeded with peers, unknown online state, peer with no MagicDNS name, unseeded, nil source); verify the JSON has `peers: []` not `null` and `online` is `true`, `false` or `null`
- [x] 1.2 Return an empty list while unseeded even when the source still holds entries; add a test in `monitor_api_test.go` with a fake source whose `seededNow()` is false but `Peers()` is not empty; verify the response is `seeded: false` and `peers: []`
- [x] 1.3 Register `GET /api/v1/peers` in `cmd/tail-watch/monitor_guard.go` without `guard`, and give the manager the presence view in `mountMonitorUI` in `cmd/tail-watch/main.go`; verify a test with the UI off returns 404 (the existing `/api/v1/` not-found route) and the peers route needs no `Content-Type`
- [x] 1.4 Add a test that a peer the tailnet policy hides from the collector never appears, using a fake source without that peer; verify only visible peers are returned, sorted by `name`

## 2. Picker in the UI

- [x] 2.1 Add `<datalist id="peers">` and `list="peers"` on `#f-addr` in `cmd/tail-watch/ui/index.html`, plus a hint element under the field; verify the page still has no external resource and the field remains a plain text input
- [x] 2.2 In `cmd/tail-watch/ui/app.js`, fetch `/api/v1/peers` when the form opens and fill the datalist with `value` (peer name, else first IP) and a `label` of IP, tags and online state, using `el()` and `textContent` only; verify with the browser that choosing a suggestion fills the field
- [x] 2.3 Add the routing hint in `cmd/tail-watch/ui/app.js`: on input, if the value is an IP inside a peer's `routes`, show `Handled by <name>` using the longest matching prefix, and change nothing in the Parent select; verify `10.20.0.50` shows `nyc-router` and `192.168.99.1` shows nothing
- [x] 2.4 Make the empty, unseeded and failed-fetch cases silent in `cmd/tail-watch/ui/app.js`; verify with the collector stopped from `tailscaled` and with the endpoint blocked that a typed address still submits
- [x] 2.5 Test the embedded UI: extend `cmd/tail-watch/ui_flag_test.go` or add a `ui_serve` test that the served `index.html` references the datalist and that `app.js` contains no `innerHTML`; verify a peer name containing `<img onerror=alert(1)>` renders as text in a browser check
- [x] 2.6 Take one screenshot of the address suggestions into `docs/images/monitor-ui-peers.png`; verify it shows no real host names or tags

## 3. Documentation

- [x] 3.1 Add the `GET /api/v1/peers` row and its response fields to `docs/reference/cli.md`; verify the field names match `handlePeers` and the delta spec
- [x] 3.2 Add "Pick a node from the list" to `docs/how-to/monitor-ui.md`, with the screenshot, the unseeded and empty behavior and the routing hint; say the list means visible, not healthy; verify against the running UI
- [x] 3.3 Add the peer list to `docs/explanation/monitor-ui-risks.md`: readable by anyone who reaches the UI, limited to peers the collector can already reach, and readable by DNS rebinding the same way `/api/v1/targets` is; verify the wording matches the existing rebinding section
- [x] 3.4 Check `CLAUDE.md` for the monitor UI layout note and update it only if it lists the files or endpoints this change touches; verify the note still matches the tree
- [x] 3.5 Add a CHANGELOG entry only if release-please does not own it (see `docs/contributing/releases.md`); verify

## 4. Validation

- [x] 4.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test -race ./...`; verify all pass
- [x] 4.2 Run `mise run lint` (golangci-lint) and `govulncheck ./...`; verify no findings
- [x] 4.3 Run `markdownlint` over `README.md`, `docs/` and `openspec/changes/add-monitor-peer-picker/`; verify zero findings
- [x] 4.4 Run a local link check over `docs/` and `README.md`; verify none are broken
- [x] 4.5 Run `mise exec -- openspec validate add-monitor-peer-picker --strict`; verify it passes
- [x] 4.6 With the live stack (`scripts/live-stack.sh up`) or a real kernel-mode `tailscaled`, open the UI and confirm the suggestions list the fixture peers and a monitor added from a suggestion reaches `UP`; verify against `docs/how-to/monitor-ui.md`
