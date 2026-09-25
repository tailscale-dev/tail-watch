# Changelog

## Unreleased

### Added

- Add the initial `tsmon` reachability collector alongside the tsnet "what"
  template. Template removal and the module rename remain planned work.
- `tsmon` loads one YAML file, probes `icmp`, `tcp`, `http`, and `tsping`
  through a kernel-mode `tailscaled`, and serves `/`, `/metrics`, and
  `/healthz`. `--once` runs every check once. `check-config` validates a
  file without contacting tailscaled.
