# Proposal

## Why

A dead collector cannot report itself, and the only documented answer is the
[heartbeat](../../../docs/how-to/heartbeat.md), which needs an external
dead-man's-switch service and an account with it. An operator who already runs two
collectors, such as the Fly.io one beside the Proxmox one, can watch each from the other
with the tools tail-watch already ships, and keep the whole loop inside the tailnet.

## What Changes

- Add a how-to, `docs/how-to/watch-the-watcher.md`, for two collectors that each run an
  `http:` check against the other's `/healthz` through that collector's Tailscale Service.
  It covers defining a `svc:` name per collector, the grants that let each collector's
  tag reach the other's Service, the `serve` command on each host, the `http:` target on
  each side, and how to confirm an outage is detected by killing one collector.
- State the limits plainly: `/healthz` answers `ok` while the process is serving, so the
  check proves the peer process is up and reachable, not that its targets are healthy. Each
  collector alerts through its own sinks, so a sink outage on both sides still goes
  unnoticed. There is no shared state or quorum.
- Link it from `docs/README.md`, from the heartbeat how-to as the option with no external
  service, and from the Services how-to.
- No code, config key, metric, payload field or endpoint changes.

## Capabilities

### New Capabilities

None. This change is documentation of behavior the `collector-runtime` (`/healthz`),
`reachability-checks` (`http:`) and `notification-delivery` capabilities already specify.

### Modified Capabilities

None. The change sets `skip_specs: true` in `.openspec.yaml`: specs describe behavior, and
none changes.

## Impact

- Docs: one new page under `docs/how-to/` and link edits in `docs/README.md`,
  `docs/how-to/heartbeat.md` and `docs/how-to/monitor-ui-service.md`.
- Test infrastructure, no product code: `scripts/live-stack.sh` and
  `cmd/tail-watch/testdata/compose.live-notify.yaml` gain an opt-in second collector node
  (`TAIL_WATCH_LIVE_MUTUAL=1`), described in `docs/contributing/live-notification-test.md`.
  The existing live tests are unaffected. A one-line `exhaustive` lint fix in
  `cmd/tail-watch/config_test.go` rides along because `mise run lint` could not pass without it.
- Delivers the ROADMAP.md item "How-to: watch the watcher" under Dogfooding Tailscale.
- Verification runs through `scripts/live-stack.sh` on the Docker test tailnet, never the
  host tailnet: two collector containers, a Service each, and a stopped collector raising
  an alert on the other. The page's fenced commands are checked by hand, not by a new Go
  test.
- Non-goals, each its own ROADMAP.md item:
  - A declarative `tailscale serve` config in place of the imperative `serve --bg` command.
  - Surfacing tailscaled's own health as a metric and classifier input.
  - The Unix-socket UI listener.
  - Multi-collector quorum with shared state, which crosses the "no shared state" boundary
    in CONTEXT.md.
