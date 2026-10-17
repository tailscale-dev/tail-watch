# Deploy tail-watch

Run the collector as a binary (download a release or build from source),
under systemd, as a Docker container next to the host's `tailscaled`, or
with Docker Compose when you have no dedicated host.

tail-watch needs a kernel-mode `tailscaled`. It refuses to start against
userspace networking. See [Troubleshoot](troubleshoot.md) if startup fails.

## Download a release

Releases ship static binaries (`CGO_ENABLED=0`) for Linux, macOS and Windows
on amd64 and arm64, so no Go toolchain is needed. Archives are named
`tail-watch_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows). The download
URL uses the tag, which has a leading `v`; the archive name does not.

```bash
VERSION=0.2.0   # release version, without the leading v
OS=linux        # linux, darwin or windows
ARCH=amd64      # amd64 or arm64

curl -fLO "https://github.com/tailscale-dev/tail-watch/releases/download/v${VERSION}/tail-watch_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fLO "https://github.com/tailscale-dev/tail-watch/releases/download/v${VERSION}/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
```

`--ignore-missing` skips the checksum lines for the platforms you did not
download. Then extract and install:

```bash
tar -xzf "tail-watch_${VERSION}_${OS}_${ARCH}.tar.gz"
sudo install -m 0755 tail-watch /usr/local/bin/tail-watch
tail-watch --version
```

The archive also carries `examples/tail-watch.yaml` and
`examples/tail-watch.service`; the [systemd section](#run-under-systemd)
uses both.

## Run the binary

Prerequisites:

- A host running `tailscaled` in kernel mode, tagged `tag:monitor`.
- The [ACL grants](acls.md) for that tag.
- [mise](https://mise.jdx.dev/) to install the pinned Go toolchain.

Steps:

1. Build the binary:

   ```bash
   mise install
   mise run build
   ```

2. Validate the config. This does not contact `tailscaled`:

   ```bash
   ./bin/tail-watch check-config --config examples/tail-watch.yaml
   ```

3. Start the collector:

   ```bash
   ./bin/tail-watch --config examples/tail-watch.yaml
   ```

   The config path is `--config`, then `TAIL_WATCH_CONFIG`, then
   `/etc/tail-watch/tail-watch.yaml`.

Verify:

```bash
curl -s http://127.0.0.1:9109/healthz
```

The answer is `ok`. Open `http://127.0.0.1:9109/` for the status page.
To run every check once and exit non-zero on failure, use
`./bin/tail-watch --once --config examples/tail-watch.yaml`.

Flags, environment variables and defaults are in the
[command-line reference](../reference/cli.md).

## Run under systemd

[`examples/tail-watch.service`](../../examples/tail-watch.service) runs the
collector as a dedicated `tail-watch` user after `tailscaled.service`,
restarts it on failure, and confines it to its state directory. The unit
ships in the release archive, so the download above already has it.

Prerequisites: the same host as above, with systemd.

Steps:

1. Create the service user:

   ```bash
   sudo useradd --system --shell /usr/sbin/nologin tail-watch
   ```

2. Install the binary, the config and the unit:

   ```bash
   sudo install -m 0755 tail-watch /usr/local/bin/tail-watch
   sudo install -d -m 0755 /etc/tail-watch
   sudo install -m 0644 examples/tail-watch.yaml /etc/tail-watch/tail-watch.yaml
   sudo install -m 0644 examples/tail-watch.service /etc/systemd/system/tail-watch.service
   sudo systemctl daemon-reload
   ```

   Run this from the unpacked release archive. From a checkout, the same
   `examples/` paths apply; install `bin/tail-watch` from `mise run build`
   as the binary.

3. Put sink secrets in `/etc/tail-watch/tail-watch.env`, one `NAME=value`
   per line:

   ```bash
   sudo install -m 0600 /dev/null /etc/tail-watch/tail-watch.env
   sudoedit /etc/tail-watch/tail-watch.env
   ```

   The unit loads it with `EnvironmentFile=-`; the leading dash keeps it
   optional, so hosts with no sinks can skip this step. Sinks read secrets
   only from `*_env` or `*_file` keys, never from the config file.

4. Enable and start:

   ```bash
   sudo systemctl enable --now tail-watch
   ```

Verify:

```bash
systemctl status tail-watch
curl -s http://127.0.0.1:9109/healthz
```

`status` shows `active (running)` and `/healthz` answers `ok`. Stopping is a
plain `sudo systemctl stop tail-watch`: the collector exits cleanly on
`SIGTERM`.

How the unit fits the collector:

- `Wants=tailscaled.service` rather than `Requires=`: a `tailscaled` restart
  does not stop the collector. If `tailscaled` is not reachable when the
  collector starts, it logs `tailscaled is not reachable` and exits, and
  `Restart=on-failure` starts it again.
- `StateDirectory=tail-watch` creates `/var/lib/tail-watch` owned by the
  service user, and `ProtectSystem=strict` keeps it writable while the rest
  of the filesystem is read-only. This matches `collector.state_dir` in
  `examples/tail-watch.yaml`. `StateDirectoryMode=0700` keeps other users out
  of the directory, which holds the notification queue and the monitors from
  the UI; without it systemd creates it as `0755` and the collector logs
  `state_dir is readable by group or others`. An existing install picks the
  mode up when you add the setting, run `sudo systemctl daemon-reload`, and
  restart the service.
- `AmbientCapabilities=CAP_NET_RAW` lets the non-root user send ICMP checks.
- `Restart=on-failure` with `RestartSec=5`: a corrupt notification state or
  an unset secret stops startup on purpose, and the slow restart loop keeps
  the failure visible in `journalctl -u tail-watch`.

No `tailscale set --operator` step is needed. `tailscaled` creates its
socket with mode 0666, and on the local socket every caller gets read
access; write access needs root or the configured operator. tail-watch only
calls `Status`, `WatchIPNBus`, `GetPrefs` and `Ping` on the LocalAPI, and
all four work with read access, so the `tail-watch` user stays unprivileged.

Variations, each as a drop-in from `sudo systemctl edit tail-watch`:

- Secrets in `*_file` keys: instead of the environment file, use a systemd
  credential. Add `LoadCredential=slack-url:/etc/tail-watch/slack-url` and
  point the config at `url_file: /run/credentials/tail-watch.service/slack-url`.
- Secrets in setec: the collector must be on the tailnet (not
  `local_only`). Point `collector.setec.server` at the server and name each
  secret with `*_setec`. Startup fails if the server is unreachable. See
  [Read secrets from setec](setec-secrets.md).
- A `state_dir` outside `/var/lib/tail-watch`: `ProtectSystem=strict` makes
  only `StateDirectory` writable, so add `ReadWritePaths=/data/tail-watch`
  (your path) for the directory in `collector.state_dir`.
- No `icmp` checks configured: drop the capability by setting
  `AmbientCapabilities=` and `CapabilityBoundingSet=` to empty. The sysctl
  alternative, `net.ipv4.ping_group_range`, is in
  [Troubleshoot](troubleshoot.md).

## Run in Docker

The image runs as user `tail-watch` (uid 65532) and does not embed a
Tailscale node. Use host networking so checks share the host routes, and
mount the LocalAPI socket.

Prerequisites: the same host as above, and a socket that uid 65532 can read.
If it cannot, pass a `--user` or `--group-add` that can.

Steps:

1. Pull the image for a release:

   ```bash
   docker pull ghcr.io/tailscale-dev/tail-watch:<version>
   ```

   `<version>` is a release number without the `v`, as listed on the
   [releases page](https://github.com/tailscale-dev/tail-watch/releases). The image
   is built for `linux/amd64` and `linux/arm64`, and Docker picks the one for your
   host. Each release is also tagged with its `X.Y` line and `latest`, but pin the full
   version for anything you run unattended. Releases after `v0.1.0` have an image;
   for `v0.1.0` and earlier, build from source instead:

   ```bash
   docker build -t tail-watch .
   ```

2. Run it, with the image name you pulled or built in place of
   `ghcr.io/tailscale-dev/tail-watch:<version>`:

   ```bash
   docker run --rm --network host \
     -v /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock \
     -v /etc/tail-watch/tail-watch.yaml:/etc/tail-watch/tail-watch.yaml:ro \
     ghcr.io/tailscale-dev/tail-watch:<version>
   ```

Verify: `curl -s http://127.0.0.1:9109/healthz` answers `ok`. If the
container exits, its JSON log on stderr names the reason. For ICMP checks the
container needs `net.ipv4.ping_group_range` to include uid 65532; see
[Troubleshoot](troubleshoot.md).

If you use `collector.state_dir` (`/var/lib/tail-watch` in the example), mount
a volume there that uid 65532 can write.

## Run with Docker Compose and no dedicated host

`examples/compose.yaml` runs a kernel-mode `tailscaled` container and puts
tail-watch in its network namespace. Checks follow that node's `tailscale0`
routes, including accepted subnet routes.

Prerequisites:

- Docker with the Compose plugin, and access to `/dev/net/tun`.
- An auth key tagged `tag:monitor`. Create it in the Tailscale admin console.

Steps:

1. Put your targets in a config file. The example reads
   `examples/tail-watch.yaml`. To use another file, set
   `TAIL_WATCH_CONFIG_FILE`.
2. Start the stack:

   ```bash
   TS_AUTHKEY=tskey-auth-... docker compose -f examples/compose.yaml up -d
   ```

   tail-watch starts once the `tailscale` service reports healthy.

Verify:

```bash
docker compose -f examples/compose.yaml ps
docker compose -f examples/compose.yaml logs tail-watch
```

The log shows a `collector identity` line with the tag `tag:monitor`. The
listener is on the loopback address of the shared namespace, so run
`docker compose -f examples/compose.yaml exec tailscale wget -qO- http://127.0.0.1:9109/healthz`
to reach it.

Things to know:

- The `tailscale` service sets `net.ipv4.ping_group_range`, so the non-root
  tail-watch image can send ICMP without `CAP_NET_RAW`.
- `network: local` targets reach only what the container's Docker network
  can reach. To monitor a LAN, use the host deployment instead. See
  [Monitor remote subnets and local LANs](remote-subnets.md).
- Do not put secrets in the config file. Sinks read them from `*_env` or
  `*_file` keys; see [Send alerts to Slack](slack-sink.md).
- To keep notification state and UI monitors across restarts, mount a volume
  at `collector.state_dir`. See
  [Manage monitors in the UI](monitor-ui.md).
