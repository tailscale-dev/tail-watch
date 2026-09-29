# tail-watch

tail-watch is a reachability collector for a tailnet. It runs beside a
kernel-mode `tailscaled`, probes the targets in a YAML file, and serves
a status page, Prometheus metrics, and a health check.

It answers a different question from the online/offline webhook: not
whether control thinks a node is connected, but whether traffic to that
node or to a subnet behind it actually gets through. When a check fails, it
names the layer that broke and sends one alert per outage to Slack or a webhook.

## Requirements

- A host running `tailscaled` in kernel mode. Userspace networking cannot
  carry ICMP to LAN addresses, so this build refuses it at startup.
- Tag the host `tag:monitor`, and grant that tag access to what you monitor.
- A process that can open the LocalAPI socket, by default
  `/var/run/tailscale/tailscaled.sock`.

## Quickstart

```bash
mise install
mise run build
./bin/tail-watch check-config --config examples/tail-watch.yaml
./bin/tail-watch --config examples/tail-watch.yaml
```

The status page is at `http://127.0.0.1:9109/`, and metrics are at `/metrics`. To run
without a dedicated host, or to receive your first alert, follow the
[tutorial](docs/tutorial/first-monitor.md).

## Documentation

The [documentation index](docs/README.md) lists every page. The main ones:

- Learn: [Set up your first monitor](docs/tutorial/first-monitor.md)
- Do: [Deploy](docs/how-to/deploy.md), [write the ACLs](docs/how-to/acls.md),
  [send alerts to Slack](docs/how-to/slack-sink.md),
  [manage monitors in the UI](docs/how-to/monitor-ui.md), and
  [troubleshoot](docs/how-to/troubleshoot.md)
- Look up: [configuration](docs/reference/config.md),
  [command line](docs/reference/cli.md), [metrics](docs/reference/metrics.md), and
  [event payload](docs/reference/payload.md)
- Understand: [failure layers](docs/explanation/failure-layers.md),
  [alert state](docs/explanation/alert-state.md), and
  [monitor UI risks](docs/explanation/monitor-ui-risks.md)

## Contributing

`mise run check` builds, tests, and lints with the pinned tools. See
[Development and releases](docs/contributing/releases.md) and the
[live notification test](docs/contributing/live-notification-test.md).
