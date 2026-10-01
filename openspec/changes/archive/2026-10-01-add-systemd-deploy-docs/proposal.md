# Proposal

## Why

`docs/how-to/deploy.md` tells an operator to build the binary with `mise` or run a
container. A production host without Docker has no supported way to keep the collector
running across reboots and crashes, and no way to install a release without a Go
toolchain. The first release archives (`tail-watch_<ver>_<os>_<arch>.tar.gz` and
`checksums.txt`) are about to exist, so a download step is now possible.

## What Changes

- Add `examples/tail-watch.service`, a hardened systemd unit for a dedicated
  `tail-watch` user that runs after `tailscaled.service`.
- Add a "Download a release" section to `docs/how-to/deploy.md`: fetch the archive and
  `checksums.txt`, verify with `sha256sum -c`, and install the static binary.
- Add a "Run under systemd" section to `docs/how-to/deploy.md`: create the user, install
  the unit and config, keep secrets in an environment file or a systemd credential, and
  start the service.
- Record why no `tailscale set --operator` step is needed for the service user.
- Ship the unit in the release archive with one `files` entry in `.goreleaser.yaml`.
- Link the new sections from `docs/README.md` and `README.md` where they list deploy
  steps, and add a troubleshooting row for a service that cannot open the socket.

This change adds documentation and one example file. It does not change collector code,
config keys, metric names or payload fields.

Implements no design-doc section. `tailnet-reachability-monitor-design.md` describes
the runtime under "Runtime: kernel-mode tailscaled" and has no packaging or service
section. Phase: MVP (phase 1). Non-goals:

- A `.deb` or `.rpm` package, an install script, or a package repository.
- A published container image or registry push (a separate change).
- launchd, Windows service, or other init systems.
- Code changes to add `sd_notify`, socket activation, or config reload on `SIGHUP`.
- Phase 2 and 3 features (PagerDuty, `target.degraded`, quorum, tsnet mode).

## Capabilities

### New Capabilities

None. Documentation and an example file do not change system behavior, so there is no
spec delta. The change sets `skip_specs: true` in `.openspec.yaml`.

### Modified Capabilities

None.

## Impact

- New: `examples/tail-watch.service`.
- Changed: `docs/how-to/deploy.md`, `docs/how-to/troubleshoot.md`, `docs/README.md`,
  `README.md`, `.goreleaser.yaml` (one archive `files` entry), `CHANGELOG.md` only if
  release-please does not own it by then.
- No Go code or runtime impact. Two test-only dependencies join `go.mod` for the
  QEMU guest test (`golang.org/x/crypto/ssh` and `github.com/pkg/sftp`); the
  shipped binary is unchanged.
- Sequencing: the download commands cannot be run until v0.1.0 is published. Write them
  against the documented archive names and check them after the release.

## Assumptions

- The reader has a Linux host with systemd and a kernel-mode `tailscaled` already
  running as `tailscaled.service`.
- The unit ships in the archive, so a user who downloads a release gets it without
  cloning the repo. This adds one line to `.goreleaser.yaml`; revise here if that is
  unwanted.
- `AmbientCapabilities=CAP_NET_RAW` is the documented ICMP path, with
  `net.ipv4.ping_group_range` as the alternative already covered by the troubleshoot
  page.
