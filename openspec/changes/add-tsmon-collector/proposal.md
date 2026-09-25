# Proposal

## Why

Tailscale has no way to alert when something on a tailnet stops being
reachable. The online/offline webhook requested in tailscale/tailscale#5967
answers the wrong question: it reports control-plane presence, not whether
traffic gets through. See `tailnet-reachability-monitor-design.md`, section
"Context". This repository still holds the "what" tsnet web-app template, so
the collector has nowhere to live yet.

This change is the first of four that make up phase 1 (MVP). It lays the
foundation the others build on: a running collector that loads a config and
polls targets. The other three changes are `add-failure-classification`,
`add-alert-state-machine` and `add-notifications`.

## What Changes

- Make target network intent explicit (`tailnet` or `local`), validate remote
  Linux subnet-route acceptance, and reserve dependency parents for reachability
  checks so service failures cannot suppress unrelated devices.

- **BREAKING**: Remove the "what" template: `main.go`, `tmpl/`, `static/`,
  `css/`, Tailwind, `package.json` and `bun.lock`. Rename the module to
  `github.com/tailscale-dev/tail-watch`.
- Add the `tsmon` binary under `cmd/tsmon/`. It runs beside a kernel-mode
  `tailscaled`, talks to it over the LocalAPI, and serves a status page,
  `/metrics` and `/healthz`.
- Load and validate one YAML config file: `collector`, `defaults` and
  `targets`.
- Add four check types over a single network-access interface (`netEnv`):
  `icmp`, `tcp`, `http` and `tsping`.
- Schedule checks with `tailscale.com/prober` and export Prometheus metrics
  in the `tsmon` namespace.
- Add a `--once` mode. It runs every check once, prints the results and
  exits non-zero on any failure.
- Replace the Dockerfile and README with ones for `tsmon`.

Implements design-doc sections "Runtime: kernel-mode tailscaled", "Check
types", "Network access", "Config" (collector, defaults and targets only),
"Metrics" (prober series) and "Implementation sketch".

Non-goals (later changes or phases):

- Failure layers, presence and dependency learning: `add-failure-classification`.
- Alert states, flap damping and suppression: `add-alert-state-machine`.
- Sinks, events, queues, incidents, heartbeat and `notify-test`:
  `add-notifications`.
- Phase 2: `tsnetEnv`, `tls` checks, `degraded` warnings, tag discovery.
- Phase 3: quorum, devices-API discovery, SNMP.

## Capabilities

### New Capabilities

- `collector-runtime`: The `tsmon` process. Covers flags, the tailscaled
  LocalAPI connection, startup preflight, the HTTP endpoints, `--once` mode,
  shutdown and the prober metric series.
- `collector-config`: The YAML config file. Covers the schema for
  `collector`, `defaults` and `targets`, default values, validation and error
  reporting.
- `reachability-checks`: The semantics of the `icmp`, `tcp`, `http` and
  `tsping` checks, including check syntax, timeouts and the data each check
  records.

### Modified Capabilities

None. The project has no specs yet.

## Impact

- **Code**: new `cmd/tsmon/`. The root `main.go` and template assets are
  deleted.
- **Module path**: changes from `github.com/tailscale-dev/service-design` to
  `github.com/tailscale-dev/tail-watch`.
- **Dependencies**: the only new direct dependencies are `tailscale.com`
  packages and `go.yaml.in/yaml/v3`, which is already in `tailscale.com`'s
  module graph.
- **Tooling**: Bun stays in `mise.toml` only as the package manager for
  mise's npm backend (the OpenSpec CLI). Tailwind scripts are removed.
- **Deployment**: the container image now needs host networking and the
  tailscaled socket mounted. It no longer embeds tsnet.
