# Troubleshoot

Find the message tail-watch printed, then apply the fix. Startup failures
print to stderr and exit 1; log lines are JSON on stderr.

## Startup failures and warnings

| Symptom | Cause | Fix |
| --- | --- | --- |
| `tailscaled is using userspace networking; tail-watch requires kernel-mode tailscaled` | `tailscaled` runs with `--tun=userspace-networking`, which cannot carry ICMP to LAN addresses | Run `tailscaled` in kernel mode. In Compose set `TS_USERSPACE: "false"` as `examples/compose.yaml` does. See [Deploy](deploy.md) |
| `node is missing tag:monitor` (a warning) | The collector node does not carry the tag your grants are written for | Join with an auth key tagged `tag:monitor`, or re-tag the node. See [ACLs](acls.md) |
| `cannot open an ICMP socket: set net.ipv4.ping_group_range or grant CAP_NET_RAW` | The process cannot open an ICMP socket. TCP, HTTP and `tsping` checks still run | Allow unprivileged ping: `sysctl -w net.ipv4.ping_group_range="0 2147483647"` (or a range that includes the collector's group), or grant `CAP_NET_RAW`. In Docker, set the sysctl on the container as the example Compose file does |
| `tailscaled is not reachable at <socket>: ...` | The LocalAPI socket is missing, wrong, or unreadable by the collector's user. On Linux the default is `/var/run/tailscale/tailscaled.sock`. Under systemd, `tailscaled.service` may simply be down or still starting | Pass `--socket <path>`, check `tailscaled` is running (`systemctl status tailscaled`), and make the socket readable by the collector's user. In Docker mount the socket and use a `--user` or `--group-add` that can read it (the image runs as uid 65532). Under systemd the unit retries on its own; `journalctl -u tail-watch` shows the attempts |
| `state_dir <path>: mkdir <path>: read-only file system` | The unit's `ProtectSystem=strict` makes everything except `StateDirectory` (`/var/lib/tail-watch`) read-only, and `collector.state_dir` points at a directory that does not exist yet. If the directory already exists, the same cause shows up later as a different `read-only file system` error on the first state write | Keep `state_dir: /var/lib/tail-watch`, or add `ReadWritePaths=<path>` in a drop-in (`systemctl edit tail-watch`). See [Deploy](deploy.md) |
| `tailscaled state is <state>; run tailscale up` | `tailscaled` is running but not logged in | Run `tailscale up` |
| `remote subnet targets require accepted routes; run tailscale set --accept-routes` | A `network: tailnet` target has a literal non-Tailscale IP and the host does not accept subnet routes | Run `tailscale set --accept-routes`, then check `ip route get <addr>`. See [Remote subnets](remote-subnets.md) |
| `exit node is active; notifications will route through the exit node` (a warning) | An exit node is set on the collector, so sinks and the heartbeat egress through it | Clear the exit node, or make sure receivers accept that source. See [Webhook](webhook-sink.md) |
| `sink <name>: url: environment variable <VAR> is unset or empty` (also `secret:`, `header <name>:`, `heartbeat:`) | A `*_env` variable is unset or empty at startup. Secret files report `secret file <path> is empty` | Export the variable in the service's environment. Compose and systemd do not inherit your shell. Run `notify-test` to confirm. See [Slack](slack-sink.md) and [Heartbeat](heartbeat.md) |
| `notification state <path> is corrupt (...); repair or move the file aside and restart, but note open incidents are forgotten if it is removed` | `notification-state.json` in `collector.state_dir` cannot be decoded, so startup stops rather than forget open incidents | Restore or repair the file from a backup. If you move it aside, open incidents are forgotten and their recoveries are not sent. See [Incidents and queues](../explanation/incidents-and-queues.md) |
| `the UI needs collector.state_dir to store monitors` | `--ui` or `collector.ui: true` is set with no `state_dir` | Set `collector.state_dir`, or turn the UI off with `--ui=false`. See [Monitor UI](monitor-ui.md) |
| `the UI is unavailable in local-only mode` (from `--ui` or `collector.ui`) | The UI assumes a tailnet and a state directory, which a local-only collector does not have | Drop the UI; the status page and `/metrics` still serve on `collector.listen`. See [Local-only mode](../reference/config.md#local-only-mode) |
| `network "tailnet" is not valid in local-only mode` | `collector.local_only: true` is set and a target asks for the tailnet | Remove `network: tailnet`; without `network` the target defaults to `local`. See [Local-only mode](../reference/config.md#local-only-mode) |
| `tsping is not valid in local-only mode` | A local-only config lists a `tsping` check, which needs tailscaled | Use `icmp`, `tcp:`, or `http:` checks. See [Local-only mode](../reference/config.md#local-only-mode) |
| `"msg":"collector identity","hostname":"<host>","local_only":true` (informational) | `collector.local_only: true` is set, so tail-watch starts without tailscaled and identifies itself by hostname | Nothing. Payloads carry `collector.name` and omit `collector.tailscale_ip`. See [Payload](../reference/payload.md) |
| `queue: disk requires collector.state_dir` | A sink uses the disk queue with no `state_dir` | Set `collector.state_dir` or use `queue: memory` |
| `dynamic monitors are inactive: the UI is off` (a warning) | `targets.json` has monitors but the UI is off, so they do not run | Turn the UI on, or ignore it |
| `routes.json is present but the UI is off; leaving it unread` (a warning) | `routes.json` exists and the UI is off, so UI routes are not loaded and the file is not modified | Turn the UI on, or leave the file for a later upgrade. YAML `routes:` still apply |
| `webhook sink is unsigned: no secret_env or secret_file` (a warning) | A webhook sink has no HMAC key | Add `secret_env`. See [Webhook](webhook-sink.md) |

## Check failures

| Symptom | Cause | Fix |
| --- | --- | --- |
| `no peer handles <addr>; check grants to tag:monitor` | No peer owns or routes the address for this collector: a missing grant, an unapproved or unaccepted route, or the wrong address | Add the grant from `tag:monitor` and approve the route. See [ACLs](acls.md) and [Remote subnets](remote-subnets.md). tail-watch logs it once per target, again only if the grant returns and is lost |
| TCP check ends with `i/o timeout` | The grant does not allow that port, so packets are dropped | Add `tcp:<port>` to the grant |

## A sink did not get an event

A sink receives an event only when it is a candidate and its own filters
accept the event. `check-config` prints each target's candidate sinks. The
monitor form's preview names the reason for each sink.

| What you see | Why |
| --- | --- |
| The sink is not a candidate | The target names a route, and this sink is not in that route (`not_in_route`). An unrouted target is a candidate for every sink. |
| `events` | The sink's `events` list does not include this event type. |
| `min_severity` | The event is below the sink's `min_severity`. `target.flapping` is `warn`, so a sink set to `page` drops it. A route's `warn` severity does the same for `target.down`. |
| `match.sites`, `match.targets`, or `match.tags` | The target's site, name, or tailnet tags do not match. A target with no tags never matches a `match.tags` filter. |
| `missing_sink` | The route names a sink the config no longer defines. The remaining sinks still receive the event. |
| The recovery went to a different sink | A recovery goes to the sinks that received its down. Editing the route during the incident changes the next down, not this recovery. |
| `collector.started` or `notify-test` ignored the route | Those events are not routed. `test` also bypasses sink filters. |

## Validate before you restart

- `tail-watch check-config --config <file>` validates the file without
  contacting `tailscaled`.
- `tail-watch notify-test` sends one test event per sink and reports the
  HTTP status or the error.
- `tail-watch --once --config <file>` runs every check once.

Flags are in the [command-line reference](../reference/cli.md). To see every
check result in the log, use `--log-level debug`.
