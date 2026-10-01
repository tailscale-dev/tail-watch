# Design

## Context

`docs/how-to/deploy.md` covers a source build, a host-networked container and Compose.
The binary reads its config from `--config`, then `TAIL_WATCH_CONFIG`, then
`/etc/tail-watch/tail-watch.yaml`. It handles `SIGINT` and `SIGTERM` (`main.go`), so a
default systemd stop works. The LocalAPI socket defaults to
`/var/run/tailscale/tailscaled.sock`. `collector.state_dir` is
`/var/lib/tail-watch` in `examples/tail-watch.yaml`. Release archives come from
`.goreleaser.yaml`: `tail-watch_<ver>_<os>_<arch>.tar.gz` (`.zip` on Windows), static
binaries (`CGO_ENABLED=0`), plus `checksums.txt`. See proposal.md - Why.

Observed in `tailscale.com@v1.102.5`, the version this module pins:

- tail-watch calls only `Status`, `WatchIPNBus`, `GetPrefs` and `Ping` on the LocalAPI.
- On a Linux unix socket `actor.Permissions` always grants read; write needs root or the
  configured operator. `Status`, `WatchIPNBus` and `GetPrefs` (GET) need read. `servePing`
  has no permission check.
- `tailscaled` creates its socket with mode `0666`.

So a non-root service user works without `tailscale set --operator`.

## Goals / Non-Goals

**Goals:**

- An operator can install a release and keep it running under systemd without a Go
  toolchain or Docker.
- The unit is a real file that can be checked with `systemd-analyze verify`, not only a
  snippet in a page.
- Every command in the new sections is either run, or marked as needing a published
  release and checked after v0.1.0.

**Non-Goals:**

- Distro packages, install scripts, other init systems, code changes (see proposal.md).
- Documenting `tailscaled` installation or the ACLs; link to `acls.md`.

## Decisions

### One unit file at `examples/tail-watch.service`, linked from the page

The page shows the unit's key lines and explains each one, and links the file. Alternative:
embed the whole unit in `deploy.md` only. Rejected: it cannot be verified by
`systemd-analyze verify` and drifts from any copy that ships in the archive.

Unit shape:

```text
[Unit]
Description=tail-watch tailnet reachability monitor
After=tailscaled.service network-online.target
Wants=tailscaled.service network-online.target

[Service]
User=tail-watch
Group=tail-watch
ExecStart=/usr/local/bin/tail-watch --config /etc/tail-watch/tail-watch.yaml
EnvironmentFile=-/etc/tail-watch/tail-watch.env
StateDirectory=tail-watch
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

- `Wants=` rather than `Requires=` on `tailscaled.service`: tail-watch already retries and
  logs `tailscaled is not reachable`, and a `tailscaled` restart should not stop the
  collector.
- `StateDirectory=tail-watch` creates `/var/lib/tail-watch` owned by the service user, and
  it stays writable under `ProtectSystem=strict`, matching `collector.state_dir`.
- `EnvironmentFile=-` with a leading dash makes the file optional, for hosts with no
  sinks. `LoadCredential=` is documented as the alternative for `*_file` keys.
- `RestartSec=5` with `Restart=on-failure`: a corrupt `notification-state.json` or unset
  secret stops startup on purpose, and the operator should see the failure rather than a
  tight loop. `RestartSec` keeps the loop slow.

### `CAP_NET_RAW` ambient for ICMP, not a required sysctl

The unit grants the capability so it is self-contained. The page keeps
`net.ipv4.ping_group_range` as an alternative for an operator who wants no ambient
capability and points to `troubleshoot.md`. Alternative: document only the sysctl.
Rejected as the primary path because it changes a system-wide setting for one service.

### Ship the unit in the archive

Add `examples/tail-watch.service` to the archive `files` in `.goreleaser.yaml`, next to
`examples/tail-watch.yaml`. A user who downloads a release then has the unit without
cloning. Alternative: leave the unit repo-only and have the page link to GitHub.
Rejected: the page would point at `main`, which drifts from the installed version.

### Download commands use placeholders and are checked after release

The page uses `VERSION`, `OS` and `ARCH` variables and `sha256sum -c --ignore-missing`.
The URLs follow GitHub's `releases/download/v<ver>/<archive>` pattern. They cannot be run
until v0.1.0 exists, so a task verifies them then. Alternative: hard-code v0.1.0.
Rejected: it goes stale on the next release.

### Verified on a throwaway QEMU VM via `TestVMSystemd`

The dev host has no root and no running `tailscaled`, so the live verification runs in
a throwaway QEMU/KVM guest: `TestVMSystemd` in `cmd/tail-watch/vm_systemd_test.go`,
off unless `TAIL_WATCH_VM_SYSTEMD=1`. The test boots an Ubuntu 24.04 cloud image
(checksummed against the published `SHA256SUMS`, cached under the user cache dir),
seeds cloud-init over nocloud-net HTTP from the test process (no ISO tooling on the
host), and drives the guest over SSH (`x/crypto/ssh` + `pkg/sftp`). It runs the real
`examples/tail-watch.service` and the real fenced commands from the "Run under
systemd" section of `deploy.md`, so the test fails if the docs drift from a working
install. Patterns are borrowed from `tailscale.com/tstest/integration/vms`
(BSD-3-Clause), which is `_test.go`-only and cannot be imported.

Fixture mode, from the spike the plan called step 0: a
`tailscaled --tun=userspace-networking` node answers ICMP echo at its own tailnet IP
when pinged from a kernel-mode peer, so the fixture peer runs userspace mode in the
same guest. The kernel-mode-in-a-netns fallback is implemented in the test but never
exercised, because the spike always answers true.

Caveats: both tailnet nodes share one VM behind slirp NAT, so a green run does not
prove DERP relay or direct-path behavior. Green runs used `TAIL_WATCH_LIVE_TAG=`
(untagged collector): the lab tailnet policy has not merged the `icmp:*` and
`tcp:9000` grants from `testdata/snippet.hujson`, so with the default `tag:monitor`
the tcp fixture times out. With those grants merged, the default exercises the
tagged path.

## Risks / Trade-offs

- [Download commands are unverified until the release exists] → Mark the verification task
  as post-release, and run it against v0.1.0.
- [Hardening options break a valid setup, for example a `state_dir` outside
  `/var/lib/tail-watch`] → Say that `ProtectSystem=strict` makes only `StateDirectory`
  writable, and show how to add `ReadWritePaths=` for another path.
- [The unit drifts from the collector's needs] → `systemd-analyze verify` runs in the
  validation group, `deploy.md` links the file rather than copying it, and
  `TestVMSystemd` installs the shipped unit in a guest and runs the documented
  commands, so drift fails the test.
- [`CAP_NET_RAW` ambient is broader than needed for TCP-only setups] → Note that it can be
  dropped when no `icmp` checks are configured.

## Open Questions

- Should CI run `systemd-analyze verify examples/tail-watch.service`, or only the local
  validation group? Not needed to build; decide when it is cheap to add.
- Should `TestVMSystemd` run in CI? It needs KVM and tailnet credentials; for now it
  is a local, env-gated test like `TestLiveNotifications`.
