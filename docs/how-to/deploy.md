# Deploy tail-watch

Run the collector as a binary, as a Docker container next to the host's
`tailscaled`, or with Docker Compose when you have no dedicated host.

tail-watch needs a kernel-mode `tailscaled`. It refuses to start against
userspace networking. See [Troubleshoot](troubleshoot.md) if startup fails.

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

## Run in Docker

The image runs as user `tail-watch` (uid 65532) and does not embed a
Tailscale node. Use host networking so checks share the host routes, and
mount the LocalAPI socket.

Prerequisites: the same host as above, and a socket that uid 65532 can read.
If it cannot, pass a `--user` or `--group-add` that can.

Steps:

1. Build the image:

   ```bash
   docker build -t tail-watch .
   ```

2. Run it:

   ```bash
   docker run --rm --network host \
     -v /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock \
     -v /etc/tail-watch/tail-watch.yaml:/etc/tail-watch/tail-watch.yaml:ro \
     tail-watch
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
