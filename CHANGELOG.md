# Changelog

<!-- markdownlint-disable MD024 -->

## 0.3.0 - 2026-10-23

### Added

- `collector.authorization` limits the monitor UI and API by tailnet identity. The
  collector asks `tailscaled` who the caller is and reads their app capability grant,
  which gives the `viewer` or `editor` role. With the section unset, every request is
  permitted as before. See [Give the monitor UI viewer and editor roles](docs/how-to/monitor-ui-roles.md).
- `collector.setec` reads sink and heartbeat secrets from a setec server, through
  `url_setec`, `secret_setec`, `headers_setec` and `heartbeat.url_setec`. A value
  changes after the next `poll_interval`. See
  [Read secrets from setec](docs/how-to/setec-secrets.md).
- A how-to and `examples/fly/` for running a second collector on Fly.io, to monitor the
  tailnet from outside your network. See
  [Monitor from outside your network with Fly.io](docs/how-to/deploy-fly.md).

### Changed

- Go is 1.27.3, in the module, the toolchain pin and the container image. 1.27.3 fixes
  GO-2026-6809 in `crypto/x509`.

## 0.2.0 - 2026-10-15

### Added

- Notification sinks: signed `webhook`, Slack and Microsoft Teams, with routes that pick
  sinks by severity and event, durable queues, retries, batching and a heartbeat.
  `notify-test` sends a test event to each sink.
- The monitor UI gains a peer picker, a check builder, a live probe that gates Save,
  and a Routes view with a route preview.
- `collector.local_only` runs the collector without `tailscaled`, for the Kubernetes
  ProxyGroup sidecar. The Kubernetes how-to and `examples/kubernetes/` show four ways
  to inject it.
- A container image, `ghcr.io/tailscale-dev/tail-watch`, for `linux/amd64` and
  `linux/arm64`.
- A systemd deployment guide and example unit, a how-to for exposing the UI as a
  Tailscale Service, and a tutorial for receiving a first webhook.

### Changed

- HTTP request logs are structured fields instead of a JSON string in `msg`.
- Audit and request logs record the tailnet caller behind `tailscale serve` instead
  of the proxy's loopback address.
- The example systemd unit creates the state directory with mode 0700, so the
  collector no longer warns about `state_dir` on first start.

## 0.1.0 - 2026-09-29

### Added

- An opt-in monitor UI (`--ui` or `collector.ui`). It serves `/ui/` and a JSON API
  under `/api/v1/` to add, edit, and delete monitors on a running collector. UI
  monitors are stored in `state_dir/targets.json`, YAML monitors stay read-only, and
  deleting a `DOWN` monitor closes its incident with `reason: removed`.
- Targets move between `UNKNOWN`, `UP`, `DOWN`, and `SUPPRESSED` after
  consecutive failures, with flap damping and suppression of dependents
  behind a down reachability parent. `tailwatch_target_state` exports that
  state.
- Failed checks are labeled `unrouted`, `control`, `tunnel`, `service`, or
  `lan`. Remote LAN targets learn a parent from disco and primary routes.
  `tailwatch_failure_layer_total`, `tailwatch_path`, and
  `tailwatch_target_parent` export that view.

### Changed

- Replace the tsnet "what" template with the `tail-watch` reachability collector.
  The module path is `github.com/tailscale-dev/tail-watch`.
- `tail-watch` loads one YAML file, probes `icmp`, `tcp`, `http`, and `tsping`
  through a kernel-mode `tailscaled`, and serves `/`, `/metrics`, and
  `/healthz`. `--once` runs every check once. `check-config` validates a
  file without contacting tailscaled.
- `examples/compose.yaml` runs tail-watch beside a kernel-mode `tailscaled`
  container for hosts without a system `tailscaled`.
- The binary, image, default config path, and `TAIL_WATCH_` environment
  variables follow the repository name. Prometheus series use the
  `tailwatch_` prefix.
