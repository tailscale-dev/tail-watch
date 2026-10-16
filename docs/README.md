# tail-watch documentation

tail-watch checks whether traffic to your tailnet nodes and the subnets behind them
gets through, and tells you which layer failed. These pages are grouped by what you
need to do.

## Tutorial

- [Set up your first monitor](tutorial/first-monitor.md): from the example Compose
  file to an alert in Slack.
- [Receive your first webhook](tutorial/webhook-receiver.md): a local receiver that
  verifies signatures and ignores duplicates, with no tailnet.

## How-to guides

- [Deploy tail-watch](how-to/deploy.md): release download, systemd, Docker,
  and Compose without a dedicated host.
- [Write the ACLs](how-to/acls.md): grants for `tag:monitor`.
- [Monitor remote subnets and local LANs](how-to/remote-subnets.md)
- [Watch a Kubernetes ProxyGroup](how-to/kubernetes-proxygroup.md): an opt-in
  sidecar in the operator's proxy pods.
- [Manage monitors in the UI](how-to/monitor-ui.md)
- [Expose the UI as a Tailscale Service](how-to/monitor-ui-service.md): an
  HTTPS name on the tailnet, with the listener left on loopback.
- [Route notifications](how-to/notification-routes.md): choose `page` or `warn`
  and which sinks hear a monitor.
- [Send alerts to Slack](how-to/slack-sink.md)
- [Send alerts to Microsoft Teams](how-to/teams-sink.md): Adaptive Cards via a
  Workflows webhook.
- [Send alerts to a webhook](how-to/webhook-sink.md): signing, batching, retries.
- [Watch the watcher with a heartbeat](how-to/heartbeat.md)
- [Troubleshoot](how-to/troubleshoot.md)

## Reference

- [Configuration](reference/config.md)
- [Command line](reference/cli.md)
- [Checks](reference/checks.md)
- [Metrics](reference/metrics.md)
- [Event payload](reference/payload.md)

## Explanation

- [Failure layers](explanation/failure-layers.md)
- [Alert state](explanation/alert-state.md)
- [Incidents and queues](explanation/incidents-and-queues.md)
- [Monitor UI risks](explanation/monitor-ui-risks.md)
- [ProxyGroup sidecar](explanation/proxygroup-sidecar.md): why Kubernetes
  misses a broken hop 2, and what the sidecar cannot see.

## Contributing

- [Live notification test](contributing/live-notification-test.md)
- [kind ProxyGroup test](contributing/kind-proxygroup-test.md)
- [VM systemd test](contributing/vm-systemd-test.md)
- [Monitor UI browser tests](contributing/ui-browser-test.md)
- [Development and releases](contributing/releases.md)
