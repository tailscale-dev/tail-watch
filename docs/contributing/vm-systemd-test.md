# VM systemd test

This page is for contributors who need to verify the systemd deployment against
a real guest OS.

`TestVMSystemd` boots an Ubuntu 24.04 cloud image in QEMU/KVM, joins an
ephemeral collector node and an ephemeral fixture node to the tailnet, and runs
the real `examples/tail-watch.service` unit plus the commands documented in
"Run under systemd" in [Deploy tail-watch](../how-to/deploy.md) inside the
guest, over SSH. It is off unless `TAIL_WATCH_VM_SYSTEMD=1`.

```bash
TAIL_WATCH_VM_SYSTEMD=1 \
  go test -count=1 -timeout 20m -run TestVMSystemd -v ./cmd/tail-watch
```

Subtests: `docs-commands` replays the documented install steps verbatim (the
release download is replaced by an sftp copy of the locally built binary, and
the interactive `sudoedit` by installing the rendered environment file), then
runs `systemd-analyze verify` in the guest; `active` checks the service runs as
the unprivileged `tail-watch` user and answers `/healthz`; `icmp-needs-capability`
proves with a positive and a negative control that `AmbientCapabilities=CAP_NET_RAW`
is what lets that user ping; `checks-and-queue` drives a deterministic
`target.down`/`target.recovered` pair against a fixture listener and checks the
`queue: disk` state under `/var/lib/tail-watch`; `clean-stop` stops the service
and requires exit status 0, `Result=success`, `NRestarts=0`, and no error-level
journal lines.

## Prerequisites

- QEMU (`qemu-system-x86_64`, `qemu-img`) and access to `/dev/kvm` (membership
  in the `kvm` group). No libvirt, no root on the host.
- The pulumi env for tailnet credentials, as in the
  [live notification test](live-notification-test.md): `TAIL_WATCH_PULUMI_ENV`
  overrides the default `tailscale-phase-2/tailscale`.
- With `TAIL_WATCH_LIVE_TAG` unset the collector joins as `tag:monitor`, so the
  tailnet policy needs the grants in `cmd/tail-watch/testdata/snippet.hujson`
  (`icmp` and `tcp:9000` to member nodes are the ones this test exercises).
  Setting `TAIL_WATCH_LIVE_TAG=` to empty runs the collector as an untagged
  member node, which needs no policy change beyond member-to-member traffic.
- Outbound access to `cloud-images.ubuntu.com` (first run), `tailscale.com`,
  and `pkgs.tailscale.com`.

The base image is downloaded once to
`tail-watch/vmtest/noble-server-cloudimg-amd64.img` under the user cache
directory (`$XDG_CACHE_HOME`, or `~/.cache` when unset; about 600 MB) and
verified against the published `SHA256SUMS` on every run; each run boots a
fresh `qemu-img` overlay, so nothing persists between runs. cloud-init is
seeded over HTTP (`ds=nocloud-net`) because no ISO tooling is needed; the seed
only plants a throwaway ed25519 key, and everything else happens over SSH.

## What it does not prove

- Both Tailscale nodes share one VM and one NAT, so path selection may differ
  from two real hosts; DERP versus direct-path behavior is not covered.
- The fixture runs `tailscaled --tun=userspace-networking` when that answers
  ICMP echo at its own tailnet IP (the test spikes this on every run and falls
  back to a kernel-mode fixture in a network namespace if not).
- The documented release download URLs are not exercised; the test installs
  a locally built binary laid out like an unpacked archive. Check the download
  steps by hand after each release.
