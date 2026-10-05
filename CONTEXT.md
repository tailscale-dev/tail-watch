# tail-watch Context & Boundaries

## Core Architectural Identity

tail-watch is a reachability monitor that runs inside a tailnet. It answers "can I reach
the thing", which a control-plane online/offline signal cannot
(tailscale/tailscale#5967). It is one static Go binary with one YAML config, running as a
`tag:monitor` node beside kernel-mode tailscaled. It polls targets with `icmp`, `tcp`,
`http` and `tsping` checks, classifies each failure by layer (`unrouted`, `control`,
`tunnel`, `lan`, `service`), learns which subnet router a LAN target depends on, and
pages once for a dead router instead of once per device behind it. It alerts on state
transitions through webhook, Slack and Teams sinks, and exports Prometheus metrics for
existing Grafana and Alertmanager setups. An opt-in monitor UI edits dynamic monitors and
routes, and `collector.local_only` runs it with no tailscaled as a Kubernetes ProxyGroup
sidecar.

## Technical Boundaries ("Hard No's")

- **Not an NMS.** It detects up or down. No interface counters, bandwidth graphs or
  general SNMP polling.
- **No changes to tailscaled or the control server.** It is a client of the LocalAPI
  (Status, Ping, WatchIPNBus) and adds nothing to tailscaled's dependency graph.
- **No shared state between collectors.** Each collector decides alone; quorum across
  collectors happens in Alertmanager.
- **Secrets stay in `*_env` / `*_file` keys.** Sink URLs, HMAC keys and headers are never
  logged, persisted, returned by the API, or editable from the UI.
- **No credentials for discovery.** The peer picker uses the local node's view, never the
  Tailscale HTTP API, `tscli` or an API key.
- **No UI authentication of its own.** Access is the tailnet ACL plus the Origin and
  Content-Type guards. The UI does not edit YAML targets, sinks, `defaults` or heartbeat.
- **No acting on failures.** tail-watch reports; it does not unadvertise a Service,
  restart a peer or change routes.
- **Dependencies:** upgrade only `tailscale.com`. Never `go get -u ./...`; it moves the
  gvisor commit Tailscale pins.
- **Code conventions that are load-bearing:** all network access goes through `netEnv`,
  time-dependent logic takes an injected clock, and tests do not mock types we do not
  own.

## Data Source of Truth

| Data | Source |
| ---- | ------ |
| Peer presence, routes, tags | LocalAPI and the IPN bus (`presence.go`) |
| Static targets, sinks, routes, defaults | The YAML config file |
| UI-created monitors and routes | `targets.json` and `routes.json` under `collector.state_dir` |
| Open incidents and delivery intents | `notification-state.json` and the disk queues, committed atomically |
| Behavior contract | `openspec/specs/` (archived OpenSpec specs) |
| User-facing facts | `docs/reference/`, one fact per page |

## Interaction Model

### Inbound

- CLI flags (`--config`, `--listen`, `--socket`, `--log-level`, `--once`, `--ui`) and the
  `check-config` and `notify-test` subcommands
- `/api/v1` and `/ui/` when the UI is on
- LocalAPI Status, Ping and WatchIPNBus

### Outbound

- Sinks: signed `webhook` (single or batched), Slack incoming webhook, Teams Workflows
  webhook; plus the optional heartbeat URL
- Prometheus metrics under the `tailwatch_` prefix, `/healthz`, and the prober status page
- Structured `log/slog` logs

## Verification

A feature crosses a boundary if it:

- needs a tailnet API key, OAuth client or other credential to run a check or discover
  targets
- needs two collectors to agree, or stores state another collector reads
- changes tailscaled, the control server, or tailscaled's dependency graph
- writes, logs or returns a sink secret
- takes an action on the tailnet or the workload instead of reporting
- adds a Go dependency other than through `tailscale.com`

Phase 2 and phase 3 items that need credentials (PagerDuty routing keys, a Slack bot
token) are allowed only through `*_env` / `*_file` keys, like the existing sinks.

## Roadmap Process

ROADMAP.md is maintained read-only with respect to GitHub: `/roadmap sync` pulls issues
in when people file them, and does not push `roadmap/*` or `effort/*` labels. Items with
no issue are plain bullets. New behavior lands through an OpenSpec change under
`openspec/changes/`.
