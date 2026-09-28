# Changelog

## Unreleased

### Changed

- Replace the tsnet "what" template with the `tsmon` reachability collector.
  The module path is `github.com/tailscale-dev/tail-watch`.
- `tsmon` loads one YAML file, probes `icmp`, `tcp`, `http`, and `tsping`
  through a kernel-mode `tailscaled`, and serves `/`, `/metrics`, and
  `/healthz`. `--once` runs every check once. `check-config` validates a
  file without contacting tailscaled.
- `examples/compose.yaml` runs tsmon beside a kernel-mode `tailscaled`
  container for hosts without a system `tailscaled`.
