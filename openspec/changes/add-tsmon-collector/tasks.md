# Tasks

## 1. Repo reset and tooling

- [ ] 1.1 Delete `main.go`, `tmpl/`, `static/`, `css/`, `package.json`, `bun.lock`; verify `git status` shows only these removals plus the tooling edits below
- [ ] 1.2 Rename the module in `go.mod` to `github.com/tailscale-dev/tail-watch`; verify `go mod tidy` leaves `tailscale.com v1.102.5` as the only direct require besides `go.yaml.in/yaml/v3`
- [ ] 1.3 Update `mise.toml`: add `[settings] npm.package_manager = "bun"`, replace the `install`/`css`/`dev` tasks with `build` (`go build -o bin/tsmon ./cmd/tsmon`), `test` (`go test ./...`), `lint` (`golangci-lint run ./...`), `check` (depends on build, test, lint); verify `mise run check` runs once code exists and `mise exec -- openspec --version` still works
- [ ] 1.4 Update `.gitignore` (drop node section, keep `bin/`) and `.dockerignore` (`bin`, `.git`, `openspec`); verify `git check-ignore bin/tsmon` matches

## 2. Config loading (`cmd/tsmon/config.go`)

- [ ] 2.1 Define `Config`, `CollectorConfig`, `Defaults`, `Target`, `Severity`, and the YAML `duration` type; decode with `yaml.v3` `KnownFields(true)`; verify a test decoding the design doc's `collector`/`defaults`/`targets` example succeeds
- [ ] 2.2 Implement defaults resolution (30s, 5s, 3, 2, 4, 15m; `severity: page`; per-target overrides); verify table tests for no-defaults, partial-defaults and per-target override
- [ ] 2.3 Implement check parsing for `icmp`, `tcp:<port>`, `http:<url>[|text]`, `tsping`, and the phase-2 rejection of `tls`; verify table tests for each valid form, port out of range, bad URL, `tls:...`, unknown type
- [ ] 2.4 Implement validation in `config.go`: unknown keys, duplicates, durations, thresholds, explicit parent reachability-only checks, static cycles, `network: tailnet|local`, local/tsping rejection and HTTP target-address consistency; verify table tests for each rule and multi-error locations.
- [ ] 2.5 Implement config path resolution (`--config` > `TSMON_CONFIG` > `/etc/tsmon/tsmon.yaml`) and a missing-file error naming the path; verify with `t.Setenv` tests

## 3. Network access (`cmd/tsmon/netenv.go`)

- [ ] 3.1 Define the `netEnv` interface and `kernelEnv` (net.Dialer, net.ListenConfig, `local.Client{Socket}`), including unprivileged-ICMP detection at construction; verify `go build ./cmd/tsmon`
- [ ] 3.2 Add a test-only `fakeEnv` in `netenv_test.go` backed by loopback listeners; verify it is used by the check tests in group 4

## 4. Checks (`cmd/tsmon/checks.go`, `cmd/tsmon/results.go`)

- [ ] 4.1 Define `Result`, `resultConsumer` and the per-target ordered `publish` fan-out in `results.go`; verify a test with two concurrent checks on one target sees results in completion order
- [ ] 4.2 Implement the `tcp` check over `env.Dial`; verify tests for an open loopback port (pass) and a closed port (fails with "connection refused")
- [ ] 4.3 Implement the `http` check (fresh transport via `env.Dial`, no redirects, 2xx/3xx pass, 1 MiB body limit, body-match error with 300-byte excerpt, `insecure_tls`); verify `httptest` tests for 200, 302, 500, missing text, and a TLS server with and without `insecure_tls`
- [ ] 4.4 Implement the `icmp` check over `net/ping` using `env` as the ListenPacketer and the detected privilege mode, one Pinger per run, IPv4 and IPv6; verify a test pinging `127.0.0.1` that skips with a clear message when the host allows no ICMP socket, and a test asserting the permission error text names `net.ipv4.ping_group_range` and `CAP_NET_RAW`
- [ ] 4.5 Implement `tsping` address resolution (literal IP, cached `Status` peer match by HostName/DNSName, host resolver fallback, ambiguity warning); verify table tests over a synthetic `ipnstate.Status`
- [ ] 4.6 Implement the `tsping` check via `LocalClient().Ping(..., tailcfg.PingDisco)`, path-kind derivation, and mapping of "no matching peer" to the grants error; verify table tests over synthetic `PingResult`s for direct, DERP, peer relay, `Err` set, and no-matching-peer
- [ ] 4.7 Enforce per-check timeouts via the probe context and record timeouts as failures; verify a TCP test against a non-accepting listener with a 100ms timeout fails within 1s

## 5. Runtime (`cmd/tsmon/tsmon.go`)

- [ ] 5.1 Implement flags and env fallbacks (`--config`, `--listen`, `--socket`, `--once`, `--version`, `--log-level`) and slog JSON logging; verify `go run ./cmd/tsmon --version` prints a version
- [ ] 5.2 Implement the `check-config` subcommand printing resolved per-target settings; verify a test running it on a valid and an invalid file (exit codes 0 and 1)
- [ ] 5.3 Implement preflight in `tsmon.go`: LocalAPI reachable/Running, kernel networking, identity/tag/exit-node logs, and Linux subnet-route acceptance for remote targets; verify synthetic status/preferences tests, including an actionable `tailscale set --accept-routes` error.
- [ ] 5.4 Wire one `prober.Prober` (`WithSpread(true)`, `WithMetricNamespace("tsmon")`) running every target/check with labels `target`, `check`, `site` and class = check kind; verify with `tsmon --once` against a local config of TCP checks on loopback
- [ ] 5.5 Serve `/` (StatusHandler), `/metrics` (promhttp), `/healthz`; verify with `curl` against a running collector that `/healthz` returns `ok` and `/metrics` contains `tsmon_` series with `target=` labels
- [ ] 5.6 Implement `--once` output and exit code, and graceful shutdown on SIGINT/SIGTERM; verify `--once` with one failing check exits 1, and SIGTERM exits 0
- [ ] 5.7 Register a debug-level logging `resultConsumer`; verify `TSMON_LOG_LEVEL=debug tsmon --once` logs each result

## 6. Packaging and docs

- [ ] 6.1 Rewrite `Dockerfile` (golang:1.27-alpine build of `./cmd/tsmon` into alpine:3.24, non-root user, `ENTRYPOINT ["/usr/local/bin/tsmon"]`); verify `docker build .` succeeds and `docker run --rm <img> --version` prints a version
- [ ] 6.2 Replace `README.md` with tsmon usage, kernel-mode requirements, remote subnet route acceptance and effective-route verification, explicit `network: local`, config/flags/Docker and ACL guidance; delete `README.project.md`; verify markdownlint passes and examples distinguish local from remote checks.
- [ ] 6.3 Add `examples/tsmon.yaml` covering every check type; verify `tsmon check-config --config examples/tsmon.yaml` exits 0
- [ ] 6.4 Update `CLAUDE.md` for the new layout, tasks and the single-Prober-per-process rule; verify markdownlint passes

## 7. Validation

- [ ] 7.1 Run `go build ./...`, `go vet ./...`, `go test -race ./...`; all pass
- [ ] 7.2 Run `golangci-lint run ./...` and `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`; zero issues and no reachable vulnerabilities
- [ ] 7.3 Run markdownlint on changed Markdown and `openspec validate add-tsmon-collector --strict`; both pass
- [ ] 7.4 On a host with kernel-mode tailscaled, run `tsmon --once` against a real tailnet peer (`tsping`, `tcp`) and a subnet-routed IP (`icmp`); record the output in the PR description
