# Monitor from outside your network with Fly.io

A collector inside the network it watches goes quiet when that network fails. Run a
second one on Fly.io and it keeps probing your tailnet nodes from the public internet,
and still reaches Slack when your site or ISP is down.

`examples/fly/` packages tail-watch with a kernel-mode `tailscaled` in one Fly Machine.
The Machine joins your tailnet as `tag:monitor`, has no public address, and sends alerts
out through your sinks.

Prerequisites:

- A Fly.io account and `flyctl`, logged in.
- A reusable auth key tagged `tag:monitor`, from the Tailscale admin console. Also
  write the grants in [Write the ACLs](acls.md) so `tag:monitor` can reach the nodes
  you probe.
- A sink secret, such as a Slack webhook URL. See [Send alerts to Slack](slack-sink.md).

Steps:

1. Copy `examples/fly/` out of the repository and work from that directory. It holds
   `Dockerfile`, `start.sh`, `fly.toml`, and `tail-watch.yaml`.
2. Put your targets in `tail-watch.yaml`. Use `network: tailnet` and `tsping` for nodes
   on the tailnet. Fly's network cannot reach your LAN directly; to probe a subnet, run
   a subnet router and see [Monitor remote subnets and local LANs](remote-subnets.md).
3. Create the app and its volume. The volume holds the node's identity and the
   notification queue, so a restart keeps both:

   ```bash
   fly launch --no-deploy --copy-config --name <app>
   fly volumes create tail_watch_data --size 1 --region iad
   ```

4. Set the secrets. `--stage` queues them for the deploy:

   ```bash
   fly secrets set --stage TS_AUTHKEY=tskey-auth-... SLACK_WEBHOOK_URL=https://hooks.slack.com/...
   ```

5. Deploy one Machine. The default is two, which would send every alert twice:

   ```bash
   fly deploy --ha=false
   ```

Verify:

```bash
fly logs
```

The log shows a `collector identity` line with `"tags":["tag:monitor"]`, then
`listening`. To read the checks, run
`fly ssh console -C "wget -qO- http://127.0.0.1:9109/metrics"` and look for
`tailwatch_target_state` of `1` for each target. The node also appears in the admin
console as the `TS_HOSTNAME` from `fly.toml`.

## Reach the UI

The Machine has no public address, so reach the UI as a Tailscale Service, as in
[Expose the UI as a Tailscale Service](monitor-ui-service.md). Read
[Monitor UI risks](../explanation/monitor-ui-risks.md) first.

1. Uncomment `ui: true` in `tail-watch.yaml`.
2. Define a Service in the admin console, such as `tail-watch-outside`, on TCP port 443.
   Give it a name that differs from `TS_HOSTNAME`, and from any Service another
   collector hosts: two hosts for one Service share its traffic.
3. Grant the people who may use it `svc:tail-watch-outside` on `tcp:443`, and approve
   the Machine as a host (or add an `autoApprovers.services` entry for `tag:monitor`).
4. Set `TS_SERVE_SERVICE = "tail-watch-outside"` in the `[env]` section of `fly.toml` and
   run `fly deploy --ha=false`. `start.sh` then runs
   `tailscale serve --service=svc:tail-watch-outside --https=443 --bg 127.0.0.1:9109`
   on every start.
5. From an allowed device, open `https://tail-watch-outside.<tailnet>.ts.net/ui/`.

## Things to know

- The image takes tail-watch from `ghcr.io/tailscale-dev/tail-watch`. It builds the
  `latest` tag by default. To pin a release, run
  `fly deploy --ha=false --build-arg TAIL_WATCH_VERSION=X.Y.Z`. Fly may reuse a cached
  `latest` layer, so add `--no-cache` to pick up a newer release.
- `start.sh` turns on tailnet DNS (`TS_ACCEPT_DNS=true`). `tcp:` and `http:` checks
  resolve a node name through MagicDNS, so without it they fail with `no such host`.
  `tsping` resolves names itself and is not affected.
- `tailscaled` must run in kernel mode, because tail-watch refuses userspace
  networking. Fly Machines provide `/dev/net/tun` and run as root, so `start.sh` needs no
  extra setup, and ICMP checks work.
- The collector runs as root in this Machine, unlike the published image. The Machine
  has nothing else on it and no public listener.
- `tailscaled` logs `Extension CONNMARK revision 0 not supported` and a `router` health
  warning at startup. Fly's kernel has no CONNMARK module. It affects only an
  `rp_filter` workaround, and checks pass.
- A Machine that dies cannot alert. Add the `heartbeat` block in `tail-watch.yaml` and
  point it at a dead-man's-switch service. See [Watch the watcher with a
  heartbeat](heartbeat.md).
- `start.sh` advertises `tag:monitor`. If your auth key carries a different tag, set
  `TS_TAGS` in the `[env]` section of `fly.toml`. Pass a plain auth key through
  unchanged. An OAuth client secret is not tested with this image.
- The UI is off in the example. See [Reach the UI](#reach-the-ui) to turn it on.
- Fly bills the Machine while it runs, plus the volume. A `shared-cpu-1x` with 512 MB
  and a 1 GB volume costs about $3.84 a month at the 2026-10 Ashburn rates, and outbound
  data adds little at probe volumes. In a run with two targets the whole 512 MB Machine used
  about 70 MB of memory, so 256 MB ($2.19 a month) should also fit; that size is not
  tested.
  See [Fly.io pricing](https://docs.fly.io/about/pricing).
