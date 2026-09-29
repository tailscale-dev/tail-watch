# README migration map

Working record for tasks 1.3 and 1.4. Not part of `docs/`.

## Heading map

| README section (before) | Target |
| ----------------------- | ------ |
| Intro (lines 1-9) | `README.md` overview |
| Requirements | `README.md` summary; `docs/how-to/deploy.md`; `docs/how-to/troubleshoot.md` |
| Remote subnets and local LANs | `docs/how-to/remote-subnets.md` |
| Build and run | `docs/how-to/deploy.md`; flags in `docs/reference/cli.md` |
| Docker | `docs/how-to/deploy.md` |
| Docker Compose without a dedicated host | `docs/how-to/deploy.md`; `docs/tutorial/first-monitor.md` |
| ACLs | `docs/how-to/acls.md` |
| Checks | `docs/reference/checks.md` |
| Failure layers | `docs/explanation/failure-layers.md` |
| Alert state | `docs/explanation/alert-state.md`; `docs/reference/metrics.md` |
| Monitor UI | `docs/how-to/monitor-ui.md` |
| Monitor UI / Reaching the UI | `docs/how-to/monitor-ui.md` |
| Monitor UI / Risks | `docs/explanation/monitor-ui-risks.md` |
| Notifications | `docs/reference/payload.md`; `docs/explanation/incidents-and-queues.md` |
| Notifications / Sink options | `docs/reference/config.md` |
| Notifications / Payload | `docs/reference/payload.md` |
| Notifications / Webhook sink | `docs/how-to/webhook-sink.md` |
| Notifications / Slack sink | `docs/how-to/slack-sink.md` |
| Notifications / Queues, `state_dir` and open incidents | `docs/explanation/incidents-and-queues.md`; metrics in `docs/reference/metrics.md` |
| Notifications / Watching the watcher | `docs/how-to/heartbeat.md` |
| Notifications / Trying it out | `docs/reference/cli.md`; linked from sink pages |
| Notifications / Egress path | `docs/how-to/heartbeat.md`; `docs/how-to/acls.md` |
| Notifications / Live notification test | `docs/contributing/live-notification-test.md` |
| Live notification test / Getting a Slack webhook URL | `docs/how-to/slack-sink.md` |
| Development and releases | `docs/contributing/releases.md` |

## Inbound links

A search for `README.md#` and `README#` found no anchor links outside
`openspec/changes/archive`. Other references to the README:

| Location | Reference | Action |
| -------- | --------- | ------ |
| `scripts/live-stack.sh` line 4 | `README "Live notification test"` | Point at `docs/contributing/live-notification-test.md` |
| `.goreleaser.yaml` `archives.files` | ships `README.md` | Trimmed README links into `docs/`; see open question in the change summary |
