# Changelog

## Unreleased

### Added

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
