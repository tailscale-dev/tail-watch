# tail-watch

**Know whether the things on your tailnet are reachable, and which layer broke when
they are not.**

Tailscale connects your devices, services and networks into one secure tailnet, and
shows you which nodes are part of it. tail-watch builds on that foundation and
watches the rest: whether you can reach the things that matter, such as a NAS behind a
subnet router, an internal service, or a remote office LAN. It runs beside a node on
your tailnet, probes the devices you choose, and tells you what failed: the route, the
tunnel, the LAN behind a subnet router, or the service itself. When a subnet router
goes down, you get one alert for the router, not one for every device behind it.

![The tail-watch monitor UI listing monitors as up, down and suppressed, with a
suppressed NAS shown as "behind nyc-router"](docs/images/monitor-ui-list.png)

## What you get

- **Four checks.** `icmp`, `tcp`, `http` (with an optional body match) and `tsping`,
  each on its own interval and timeout.
- **A named failure layer.** Every failed check is labeled `unrouted`, `control`,
  `tunnel`, `lan` or `service`, so the alert starts the diagnosis.
- **Router-aware alerts.** tail-watch learns which subnet router a LAN target depends
  on and suppresses the devices behind it while the router is down.
- **One alert per outage.** Alerts fire on state changes, with flap damping, to
  Slack, Microsoft Teams or a signed webhook. Routes pick which sinks hear a monitor,
  and a sink can keep its queue on disk across restarts.
- **Prometheus metrics and a health check.** `/metrics` and `/healthz` sit beside a
  status page at `/`.
- **An opt-in monitor UI** to add, edit and delete monitors on a running collector.
- **One static binary and one YAML file.** It is also a container image, and it can
  run as a sidecar in a Kubernetes ProxyGroup.

## Quickstart

1. Download the archive for your platform from the
   [releases page](https://github.com/tailscale-dev/tail-watch/releases) and extract
   it. [Deploy](docs/how-to/deploy.md#download-a-release) has the checksum steps. No
   Go toolchain is needed.
2. Put your targets in a copy of `examples/tail-watch.yaml`, which is in the archive.
3. Check the file, then start the collector:

   ```bash
   tail-watch check-config --config tail-watch.yaml
   tail-watch --config tail-watch.yaml
   ```

The status page is at `http://127.0.0.1:9109/` and metrics are at `/metrics`.

To run it as a container, under systemd, or with Docker Compose when you have no
dedicated host, see [Deploy tail-watch](docs/how-to/deploy.md). To go from nothing to
your first alert, follow the [tutorial](docs/tutorial/first-monitor.md).

### Requirements

- A host running `tailscaled` in kernel mode. Userspace networking cannot carry ICMP
  to LAN addresses, so tail-watch refuses it at startup.
- The host tagged `tag:monitor`, with grants for what you monitor. See
  [Write the ACLs](docs/how-to/acls.md).
- A process that can open the LocalAPI socket, by default
  `/var/run/tailscale/tailscaled.sock`.

Setting `collector.local_only` drops the `tailscaled` requirement for the Kubernetes
sidecar. See [Watch a Kubernetes ProxyGroup](docs/how-to/kubernetes-proxygroup.md).

## How it works

The collector runs each check on its interval and keeps one state per target:
`UNKNOWN`, `UP`, `DOWN` or `SUPPRESSED`. A target goes `DOWN` after consecutive
failures, and the failure is classified by layer from the tailnet's own view of the
path. A target that depends on a down router becomes `SUPPRESSED` instead of paging.
Only transitions notify. For the reasoning, read
[Failure layers](docs/explanation/failure-layers.md) and
[Alert state](docs/explanation/alert-state.md).

## Documentation

Every page is listed in the [documentation index](docs/README.md). Start here:

| I want to | Read |
| --- | --- |
| Learn by doing | [Set up your first monitor](docs/tutorial/first-monitor.md), [Receive your first webhook](docs/tutorial/webhook-receiver.md) |
| Install and secure it | [Deploy](docs/how-to/deploy.md), [Write the ACLs](docs/how-to/acls.md), [Troubleshoot](docs/how-to/troubleshoot.md) |
| Monitor more | [Remote subnets and LANs](docs/how-to/remote-subnets.md), [Kubernetes ProxyGroup](docs/how-to/kubernetes-proxygroup.md), [Manage monitors in the UI](docs/how-to/monitor-ui.md) |
| Get alerts | [Slack](docs/how-to/slack-sink.md), [Microsoft Teams](docs/how-to/teams-sink.md), [Webhook](docs/how-to/webhook-sink.md), [Routes](docs/how-to/notification-routes.md), [Heartbeat](docs/how-to/heartbeat.md) |
| Look something up | [Configuration](docs/reference/config.md), [Command line](docs/reference/cli.md), [Checks](docs/reference/checks.md), [Metrics](docs/reference/metrics.md), [Event payload](docs/reference/payload.md) |
| Understand the design | [Failure layers](docs/explanation/failure-layers.md), [Alert state](docs/explanation/alert-state.md), [Incidents and queues](docs/explanation/incidents-and-queues.md), [Monitor UI risks](docs/explanation/monitor-ui-risks.md) |

## Build from source and contribute

Go and the other tools are pinned in `mise.toml`:

```bash
mise install
mise run build   # writes bin/tail-watch
mise run check   # builds, tests, and lints
```

See [Development and releases](docs/contributing/releases.md) for the release process
and [CONTEXT.md](CONTEXT.md) for what the project will not do.

## License

BSD 3-Clause. See [LICENSE](LICENSE).
