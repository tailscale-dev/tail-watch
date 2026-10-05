# tail-watch Strategic Roadmap

## Vision

Pingdom inside the tailnet: poll devices and services, alert on state changes, and use the
tailnet's own view to say why a check failed. Boundaries are in [CONTEXT.md](CONTEXT.md).

Items have no GitHub issues yet; when one is filed, `/roadmap sync` links it here. LOE key:
`[S]` 1-2 hours, `[M]` half a day to a day, `[L]` multi-day.

## Immediate Focus

- [x] Archive the `add-teams-sink` OpenSpec change so `openspec/specs/teams-sink` exists [S]
- [x] Delete `tailnet-reachability-monitor-design.md`; point `openspec/config.yaml` and
  `CLAUDE.md` at `openspec/specs/` [S]
- [ ] Add `/api/v1/routes`, `/api/v1/sinks` and `/api/v1/route-preview` to the endpoint
  table in `docs/reference/cli.md` [S]
- [ ] Update or remove the stale root `tasks.md` [S]

## Near-Term Vision

Phase 2 of the original design, ordered by value to an operator who already runs it.

- [ ] PagerDuty sink: Events API v2, `dedup_key` set to `target/check` so recoveries
  auto-resolve [M]
- [ ] TLS expiry check (`tls:` is rejected today) [M]
- [ ] Emit `target.degraded`: path warnings (direct to DERP or relay) with latency
  baselines, without paging [L]
- [ ] `tsnetEnv` collector for hosts without a daemon or root [L]
- [ ] Decide: under `tsnetEnv`, is an `icmp` check a startup error, or skipped with a
  warning and an `unsupported` metric? Gates the `tsnetEnv` item [S]
- [ ] Tag discovery (`discover:`): build targets from peer tags [L]
- [ ] Slack bot-token mode with threaded recoveries [L]
- [ ] Publish a container image [M]

## Future Vision (Long-Term)

Phase 3 and deferred ideas.

- [ ] Multi-collector quorum with shared state. Crosses the "no shared state" boundary;
  today quorum lives in Alertmanager [L]
- [ ] Devices-API discovery. Needs a credential, which crosses a boundary unless it stays
  opt-in through `*_env` / `*_file` [L]
- [ ] SNMP `sysUpTime` check for devices that block ICMP [M]
- [ ] Routes shared across collectors [L]
- [ ] Monitor UI authentication through the `authorize` hook [L]
- [ ] Host-header allow-list against DNS rebinding for the UI [S]
- [ ] ProxyGroup sidecar support in the Tailscale operator itself [L]
- [ ] Decide: should targets also come from node attributes or tags in the policy file? [M]
- [ ] Decide: propose `cmd/tail-watch` upstream to tailscale/tailscale? [S]

## Completed Milestones

### 2026-Q4

- [x] `teams-sink`: Teams Workflows webhook sink with Adaptive Cards. Closed 2026-10-04. [L]
- [x] `notification-routes`: named routes from YAML or the UI. Closed 2026-10-02. [L]
- [x] `proxygroup-sidecar`: `local_only` collector as a ProxyGroup sidecar. Closed
  2026-10-01. [L]
- [x] `monitor-ui`: save gated on a live probe. Closed 2026-10-01. [M]
- [x] `docs`: systemd deployment guide, example unit and VM test. Closed 2026-10-01. [M]

### 2026-Q3

- [x] `monitor-ui`: peer picker, check builder and name fill. Closed 2026-09-30. [M]
- [x] `docs`: Diataxis documentation restructure under `docs/`. Closed 2026-09-29. [M]
- [x] `monitor-ui`: opt-in monitor UI and `/api/v1`. Closed 2026-09-29. [L]
- [x] `notification-delivery`: webhook and Slack sinks, queues, incidents, heartbeat.
  Closed 2026-09-29. [L]
- [x] `alert-state-machine`: states, flap damping, suppression, restore. Closed
  2026-09-29. [L]
- [x] `failure-classification`: presence, failure layers, dependency learning. Closed
  2026-09-28. [L]
- [x] `collector-runtime`: binary, config, four checks, prober, `/metrics`. Closed
  2026-09-28. [L]

## Boundary Safeguards

From [CONTEXT.md](CONTEXT.md). An item that crosses one needs that boundary revisited
first.

- Not an NMS: up or down only.
- No changes to tailscaled or the control server.
- No shared state between collectors.
- Secrets only in `*_env` / `*_file` keys; never logged, persisted or shown.
- No credentials for discovery.
- Reports failures; never acts on them.
- Upgrade only `tailscale.com`.
