# Tasks

## 1. Outline and scaffolding

- [x] 1.1 Use the `documentation-writer` skill to confirm audience, goal and scope per page, and get the outline approved; verify the approved outline matches the tree in `design.md`
- [x] 1.2 Create `docs/README.md` index and the empty quadrant directories; verify every planned page is listed and links resolve
- [x] 1.3 Add a heading-by-heading map from current `README.md` sections to target pages (kept in the change directory, not committed to `docs/`); verify no README section is unassigned
- [x] 1.4 Search the repo for `README.md#` anchors and inbound links (`CLAUDE.md`, `CHANGELOG.md`, `examples/`); verify each is listed with its new target

## 2. Reference pages

- [x] 2.1 Write `docs/reference/config.md` from `cmd/tail-watch/config.go` and `config_sinks.go`; verify every key in `examples/tail-watch.yaml` and every `sinks` key, `heartbeat` key and monitor UI key (`collector.ui`, `state_dir`) appears with type, default and required/optional
- [x] 2.2 Save each full-config snippet from `docs/reference/config.md` and run `tail-watch check-config`; verify all pass
- [x] 2.3 Write `docs/reference/cli.md` (flags, `TAIL_WATCH_CONFIG`, `TAIL_WATCH_LOG_LEVEL`, `check-config`, `notify-test`, exit codes) from `cmd/tail-watch/main.go` and `notifytest.go`; verify against `tail-watch --help` and `notify-test --help`
- [x] 2.4 Write `docs/reference/metrics.md` from the metric registrations under `cmd/tail-watch/`; verify each documented `tailwatch_*` name appears in the source and each registered name is documented
- [x] 2.5 Write `docs/reference/checks.md` (ICMP, TCP, HTTP, tsping options and semantics) from `checks.go`; verify against the `reachability-checks` spec
- [x] 2.6 Write `docs/reference/payload.md` (event types, fields, severities, signature header) from `event.go` and `testdata/payload`; verify the field list matches the golden files

## 3. Explanation pages

- [x] 3.1 Write `docs/explanation/failure-layers.md` from the README "Failure layers" section and the `failure-classification` and `dependency-learning` specs; verify each layer name matches the `failure_layer` metric label values
- [x] 3.2 Write `docs/explanation/alert-state.md` covering grace, flap suppression and dependency suppression, from the `alert-state-machine` and `dependency-suppression` specs; verify each state and timer name matches the config keys in `reference/config.md`
- [x] 3.3 Write `docs/explanation/incidents-and-queues.md` covering `state_dir`, at-least-once delivery, replay and open incidents; verify against the `add-notifications` spec set once archived
- [x] 3.4 Write `docs/explanation/monitor-ui-risks.md` (no login, listen address, Origin and content-type guard, DNS rebinding, audit log) from README "Risks" and the `monitor-ui` and `monitor-management` specs; verify each claim against `monitor_guard.go`

## 4. How-to guides

- [x] 4.1 Write `docs/how-to/deploy.md` (binary, Docker, Compose without a dedicated host) from README "Build and run" and "Docker"; verify the Docker build command runs and `--version` prints
- [x] 4.2 Write `docs/how-to/acls.md` from README "ACLs"; verify the policy snippet is valid HuJSON
- [x] 4.3 Write `docs/how-to/remote-subnets.md` from README "Remote subnets and local LANs"; verify each `network` value and startup failure named in it exists in `netenv.go` and `config.go`
- [x] 4.4 Write `docs/how-to/slack-sink.md` including getting a webhook URL and using `*_env`; verify the config snippet passes `check-config` and `notify-test --sink` steps match `notifytest.go`
- [x] 4.5 Write `docs/how-to/webhook-sink.md` (signing, headers, batching, retries); verify the signature verification example against `webhook_test.go`
- [x] 4.6 Write `docs/how-to/troubleshoot.md` (userspace tailscaled refusal, missing `tag:monitor`, ICMP permissions, socket path, missing secrets, corrupt state); verify each symptom's message text against the source strings
- [x] 4.7 Write `docs/how-to/monitor-ui.md` (enable, `state_dir`, `targets.json`, reaching the UI over the tailnet, edit and delete semantics, screenshots from `docs/images/`); verify flags and behavior against `ui_serve.go`, `dynamic.go` and `monitor_api.go`
- [x] 4.8 Write `docs/how-to/heartbeat.md` (dead-man's-switch config, egress path and exit-node warning); verify against `heartbeat.go`

## 5. Tutorial

- [x] 5.1 Write `docs/tutorial/first-monitor.md` starting from `examples/compose.yaml`, adding a failing target, and ending with a delivered alert; verify by following it on a clean checkout with `scripts/live-stack.sh` or the Compose file
- [x] 5.2 Record any step that needed a fix while following it, and update the page; verify a second read-through needs no unwritten step

## 6. README and contributor split

- [x] 6.1 Move "Live notification test" and "Development and releases" from `README.md` to `docs/contributing/`; verify commands still match `CLAUDE.md` and `scripts/live-stack.sh`
- [x] 6.2 Trim `README.md` to overview, quickstart and links; verify against the 1.3 map that every removed section exists in a page
- [x] 6.3 Fix or redirect the inbound links found in 1.4; verify a grep for the old anchors finds none
- [x] 6.4 Add a `docs/` layout and "update docs with config or metric changes" note to `CLAUDE.md`; verify the note matches the final tree
- [x] 6.5 Add a `CHANGELOG.md` entry if the release process lists documentation; verify against `release-please-config.json`

## 7. Validation

- [x] 7.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test ./...`; verify all pass, confirming no code changed
- [x] 7.2 Run `mise run lint` (golangci-lint) and `govulncheck ./...`; verify no findings
- [x] 7.3 Run `markdownlint` over `README.md`, `docs/` and `openspec/changes/add-user-documentation/`; verify zero findings
- [x] 7.4 Run a link check over `docs/` and `README.md` (local, relative links); verify none are broken
- [x] 7.5 Run `mise exec -- openspec validate add-user-documentation --strict`; verify it passes
