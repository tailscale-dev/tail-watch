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
- [x] Add `/api/v1/routes`, `/api/v1/sinks` and `/api/v1/route-preview` to the endpoint
  table in `docs/reference/cli.md` [S]

## Near-Term Vision

Phase 2 of the original design, ordered by value to an operator who already runs it.

- [ ] PagerDuty sink: Events API v2, `dedup_key` set to `target/check` so recoveries
  auto-resolve [M]
- [ ] incident.io sink: HTTP alert source, `firing` and `resolved` paired by
  `deduplication_key`. OpenSpec change `add-incidentio-sink` drafted [M]
- [ ] Setec as a secret source (`*_setec` keys beside `*_env` / `*_file`). OpenSpec change
  `add-setec-secrets` drafted [M]
- [ ] TLS expiry check (`tls:` is rejected today). Also covers the `*.ts.net`
  certificates that Tailscale Services and Serve use [M]
- [ ] Emit `target.degraded`: path warnings (direct to DERP or relay) with latency
  baselines, without paging [L]
- [ ] `tsnetEnv` collector for hosts without a daemon or root. It also unlocks setec where
  `local_only` rejects it today, and identity from `WhoIs` on the connection instead of
  from headers [L]
- [ ] Decide: under `tsnetEnv`, is an `icmp` check a startup error, or skipped with a
  warning and an `unsupported` metric? Gates the `tsnetEnv` item [S]
- [ ] Tag discovery (`discover:`): build targets from peer tags [L]
- [ ] Slack bot-token mode with threaded recoveries [L]
- [x] Publish a container image [M]

### Dogfooding Tailscale

Items that use more of Tailscale's own features, per the "Dogfooding Tailscale" section of
[CONTEXT.md](CONTEXT.md).

- [ ] Identity-based UI authorization: fill in the `authorize` hook from the caller's
  tailnet identity or app capabilities (for example a `tailscale.com/cap/tail-watch` grant
  with a viewer, editor or admin role), so `probe` and monitor writes can be granted apart.
  Check whether `tailscale serve --accept-app-caps` and its header exist on the pinned
  version first [M]
- [ ] Harden the audit identity: cross-check `Tailscale-User-Login` and `X-Forwarded-For`
  against `LocalClient().WhoIs`, so a local process cannot forge the login in the audit
  log [S]
- [ ] Surface tailscaled's own health: `Status().Health` and the backend state as a metric
  and as a classifier input, so a collector that is itself partitioned does not page for
  its targets. Confirm what `presence.go` already reads first [M]
- [ ] How-to: watch the watcher. Two collectors each run an `http:` check against the
  other's `/healthz` through its Tailscale Service, with no shared state and no external
  heartbeat service [S]
- [ ] Declarative `tailscale serve` config for the systemd deployment, in place of the
  imperative `tailscale serve --service ... --bg`. Check which Tailscale version added
  it [S]

## Future Vision (Long-Term)

Phase 3 and deferred ideas.

- [ ] Multi-collector quorum with shared state. Crosses the "no shared state" boundary;
  today quorum lives in Alertmanager [L]
- [ ] Devices-API discovery. Needs a credential, which crosses a boundary unless it stays
  opt-in through `*_env` / `*_file` [L]
- [ ] SNMP `sysUpTime` check for devices that block ICMP [M]
- [ ] Routes shared across collectors [L]
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
- Secrets only in `*_env` / `*_file` keys (and `*_setec` once `add-setec-secrets` lands);
  never logged, persisted or shown.
- No credentials for discovery.
- Reports failures; never acts on them.
- New Go dependencies must be Tailscale-owned; upgrade `tailscale.com` on its own and
  keep the gvisor pin.
- Prefer Tailscale's own software and services; a non-Tailscale choice needs a written
  reason.
