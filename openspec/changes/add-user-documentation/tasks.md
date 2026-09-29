# Tasks

## 1. Outline and scaffolding

- [ ] 1.1 Use the `documentation-writer` skill to confirm audience, goal and scope per page, and get the outline approved; verify the approved outline matches the tree in `design.md`
- [ ] 1.2 Create `docs/README.md` index and the empty quadrant directories; verify every planned page is listed and links resolve
- [ ] 1.3 Add a heading-by-heading map from current `README.md` sections to target pages (kept in the change directory, not committed to `docs/`); verify no README section is unassigned
- [ ] 1.4 Search the repo for `README.md#` anchors and inbound links (`CLAUDE.md`, `CHANGELOG.md`, `examples/`); verify each is listed with its new target

## 2. Reference pages

- [ ] 2.1 Write `docs/reference/config.md` from `cmd/tail-watch/config.go` and `config_sinks.go`; verify every key in `examples/tail-watch.yaml` and every `sinks` key appears with type, default and required/optional
- [ ] 2.2 Save each full-config snippet from `docs/reference/config.md` and run `tail-watch check-config`; verify all pass
- [ ] 2.3 Write `docs/reference/cli.md` (flags, `TAIL_WATCH_CONFIG`, `TAIL_WATCH_LOG_LEVEL`, `check-config`, `notify-test`, exit codes) from `cmd/tail-watch/main.go` and `notifytest.go`; verify against `tail-watch --help` and `notify-test --help`
- [ ] 2.4 Write `docs/reference/metrics.md` from the metric registrations under `cmd/tail-watch/`; verify each documented `tailwatch_*` name appears in the source and each registered name is documented
- [ ] 2.5 Write `docs/reference/checks.md` (ICMP, TCP, HTTP, tsping options and semantics) from `checks.go`; verify against the `reachability-checks` spec
- [ ] 2.6 Write `docs/reference/payload.md` (event types, fields, severities, signature header) from `event.go` and `testdata/payload`; verify the field list matches the golden files

## 3. Explanation pages

- [ ] 3.1 Write `docs/explanation/failure-layers.md` from the README "Failure layers" section and the `failure-classification` and `dependency-learning` specs; verify each layer name matches the `failure_layer` metric label values
- [ ] 3.2 Write `docs/explanation/alert-state.md` covering grace, flap suppression and dependency suppression, from the `alert-state-machine` and `dependency-suppression` specs; verify each state and timer name matches the config keys in `reference/config.md`
- [ ] 3.3 Write `docs/explanation/incidents-and-queues.md` covering `state_dir`, at-least-once delivery, replay and open incidents; verify against the `add-notifications` spec set once archived

## 4. How-to guides

- [ ] 4.1 Write `docs/how-to/deploy.md` (binary, Docker, Compose without a dedicated host) from README "Build and run" and "Docker"; verify the Docker build command runs and `--version` prints
- [ ] 4.2 Write `docs/how-to/acls.md` from README "ACLs"; verify the policy snippet is valid HuJSON
- [ ] 4.3 Write `docs/how-to/remote-subnets.md` from README "Remote subnets and local LANs"; verify each `network` value and startup failure named in it exists in `netenv.go` and `config.go`
- [ ] 4.4 Write `docs/how-to/slack-sink.md` including getting a webhook URL and using `*_env`; verify the config snippet passes `check-config` and `notify-test --sink` steps match `notifytest.go`
- [ ] 4.5 Write `docs/how-to/webhook-sink.md` (signing, headers, batching, retries); verify the signature verification example against `webhook_test.go`
- [ ] 4.6 Write `docs/how-to/troubleshoot.md` (userspace tailscaled refusal, missing `tag:monitor`, ICMP permissions, socket path, missing secrets, corrupt state); verify each symptom's message text against the source strings

## 5. Tutorial

- [ ] 5.1 Write `docs/tutorial/first-monitor.md` starting from `examples/compose.yaml`, adding a failing target, and ending with a delivered alert; verify by following it on a clean checkout with `scripts/live-stack.sh` or the Compose file
- [ ] 5.2 Record any step that needed a fix while following it, and update the page; verify a second read-through needs no unwritten step

## 6. README and contributor split

- [ ] 6.1 Move "Live notification test" and "Development and releases" from `README.md` to `docs/contributing/`; verify commands still match `CLAUDE.md` and `scripts/live-stack.sh`
- [ ] 6.2 Trim `README.md` to overview, quickstart and links; verify against the 1.3 map that every removed section exists in a page
- [ ] 6.3 Fix or redirect the inbound links found in 1.4; verify a grep for the old anchors finds none
- [ ] 6.4 Add a `docs/` layout and "update docs with config or metric changes" note to `CLAUDE.md`; verify the note matches the final tree
- [ ] 6.5 Add a `CHANGELOG.md` entry if the release process lists documentation; verify against `release-please-config.json`

## 7. Validation

- [ ] 7.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test ./...`; verify all pass, confirming no code changed
- [ ] 7.2 Run `mise run lint` (golangci-lint) and `govulncheck ./...`; verify no findings
- [ ] 7.3 Run `markdownlint` over `README.md`, `docs/` and `openspec/changes/add-user-documentation/`; verify zero findings
- [ ] 7.4 Run a link check over `docs/` and `README.md` (local, relative links); verify none are broken
- [ ] 7.5 Run `mise exec -- openspec validate add-user-documentation --strict`; verify it passes
