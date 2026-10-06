# Changelog

## [0.2.0](https://github.com/tailscale-dev/tail-watch/compare/v0.1.0...v0.2.0) (2026-10-06)


### Added

* add a Microsoft Teams sink ([f418c23](https://github.com/tailscale-dev/tail-watch/commit/f418c230697c3fdc14aedea462315653951e5432))
* add peer picker, check builder and name fill to the monitor UI ([609ba1d](https://github.com/tailscale-dev/tail-watch/commit/609ba1d9243c3a24405f5732272000d28bcf51c6))
* gate monitor saves on a live probe in the monitor UI ([649cc31](https://github.com/tailscale-dev/tail-watch/commit/649cc31a5584add01cfa71f3a8b5dd11e0dae886))
* publish a container image to GHCR ([400e370](https://github.com/tailscale-dev/tail-watch/commit/400e3706a048d12dba8f6ef973c96eab959c9a0a))
* run tail-watch as a sidecar in Kubernetes ProxyGroup pods ([2add5c6](https://github.com/tailscale-dev/tail-watch/commit/2add5c66ea757dd5feafec90499174f4489570a3))


### Documentation

* add a how-to for exposing the UI as a Tailscale Service ([37db6f4](https://github.com/tailscale-dev/tail-watch/commit/37db6f427940cf7fc4194bc60a061e866175cd14))
* add CONTEXT.md and ROADMAP.md ([162700d](https://github.com/tailscale-dev/tail-watch/commit/162700dd804df4afcba13b0ca8228fe37783a5ee))
* add systemd deployment guide and example unit ([88561db](https://github.com/tailscale-dev/tail-watch/commit/88561db606af0b3bb125f8a81e7ca5b73375dcba))
* list the route and sink endpoints in the CLI reference ([b4b51ac](https://github.com/tailscale-dev/tail-watch/commit/b4b51ac43ba2da4527dbee5535e9f08e59fb8fd1))

## Changelog

## Unreleased

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
