# Tasks

## 1. TLS check

- [x] 1.1 Add the failing tests for `tls:<port>` in `cmd/tail-watch/checks_test.go` (`TestTLSCheck`) and update `TestCheckParsing` and `TestConfigValidation` in `cmd/tail-watch/config_test.go`. Cover: `tls:443` parses; `tls:70000`, `tls:0`, `tls:abc` and `tls:10.20.0.50:443` fail naming the port; a leaf at least 7 days out passes and records latency; a leaf sooner than 7 days fails with `certificate expires` and an RFC3339 `NotAfter`; an already-expired leaf fails verification; `insecure_tls` passes a self-signed leaf 30 days out and fails one 2 days out; `ServerName` is the peer DNS name for a Tailscale IP and for a short or full MagicDNS name; a subnet route does not supply that name; a full `*.ts.net` address with no peer verifies as itself; a short name with no peer does not gain the tailnet suffix; a nil resolver (the `local_only` case) still dials a DNS name. Use `fakeEnv` dialing a `tls.Listener`, a fixture CA on `tlsCheck.roots`, a `tstest.Clock`, and a `tsResolver` over an `ipnstate.Status`. Verify `mise exec -- go test ./cmd/tail-watch -run 'TestTLSCheck|TestCheckParsing|TestConfigValidation' -count=1` compiles and fails on these cases.

- [x] 1.2 Implement `tlsCheck` in `cmd/tail-watch/checks.go` and teach `bindChecks` in `cmd/tail-watch/main.go` to set its clock from `app.clock` and its resolver when the app has one. Dial with `env.Dial`, handshake with `tls.Client`, verify with the system roots unless `roots` or `insecure_tls` is set, and apply `tlsExpiryWindow` of 7 days to the leaf only. Add `TestBindChecksTLS` in `cmd/tail-watch/main_test.go` for the clock and resolver wiring, including a nil resolver. Verify `mise exec -- go test ./cmd/tail-watch -run 'TestTLSCheck|TestCheckParsing|TestConfigValidation|TestBindChecksTLS' -count=1` passes.

- [x] 1.3 Document the check in `docs/reference/checks.md` (summary row and a `tls:<port>` section in place of the rejected section) and extend the `insecure_tls` row in `docs/reference/config.md` to include `tls:` checks. Add one sentence to `CLAUDE.md`: the check dials through `netEnv`, takes the verified name from the owning peer's DNS name and not from a subnet route, and fails when the leaf expires within 7 days. Verify `markdownlint-cli2 docs/reference/checks.md docs/reference/config.md CLAUDE.md` passes and the new section names `tls:443`, the 7-day window, and the `*.ts.net` address form.

## 2. Classification

- [x] 2.1 Add a failing case in `cmd/tail-watch/classify_test.go`: a `tls:443` failure whose error contains `certificate expires`, with the tunnel up, is layer `service` and does not send ICMP; a hostname-mismatch failure takes the same path. Verify `mise exec -- go test ./cmd/tail-watch -run TestClassify -count=1` fails on that case.

- [x] 2.2 Teach `classifyErr` in `cmd/tail-watch/classify.go` to return `errTLS` for `certExpiryError` and the not-yet-valid leaf error. Verify `mise exec -- go test ./cmd/tail-watch -run TestClassify -count=1` passes.

## 3. Monitor UI

- [x] 3.1 Update `cmd/tail-watch/ui_checks_test.go` so `tls:443` round-trips as a TLS row with port `443`, and `snmp:1` stays Custom. Point `TestUIBrowserCustomRowKeepsUnparseableCheck` in `cmd/tail-watch/ui_checks_browser_test.go` at `snmp:1` and an `unknown check type` error, and add a browser case that an edited `tls:443` shows a TLS row with port `443`. Verify `mise exec -- go test ./cmd/tail-watch -run 'TestUICheckConversion|TestUICheckRoundTrip' -count=1` fails until 3.2, and the browser run is left for 3.2.

- [x] 3.2 Add the TLS certificate row in `cmd/tail-watch/ui/app.js` (`CHECK_TYPE_LABELS`, `checkToString`, `checkFromString`, `checkRowEl`), keeping the check-conversion block free of DOM access except `checkRowEl`. Verify `mise exec -- go test ./cmd/tail-watch -run 'TestUICheckConversion|TestUICheckRoundTrip|TestUIBrowserCustomRowKeepsUnparseableCheck|TestUIBrowserTLS' -count=1` passes. The browser tests skip when the Playwright driver is missing; say so if they skip.

- [x] 3.3 Add a TLS certificate row to the check-type table in `docs/how-to/monitor-ui.md`. Verify `markdownlint-cli2 docs/how-to/monitor-ui.md` passes.

## 4. Validation

- [x] 4.1 Run `mise exec -- go build ./...`, `mise exec -- go vet ./...`, `mise exec -- go test ./...`, `mise exec -- golangci-lint run ./...`, and `mise run vulncheck`. Verify each exits 0.

- [x] 4.2 Run `markdownlint-cli2` on the changed Markdown, including this change's artifacts, and `mise exec -- openspec validate add-tls-expiry-check --strict`. Verify both exit 0. Add an `Unreleased` entry to `CHANGELOG.md` and check the ROADMAP.md box.
