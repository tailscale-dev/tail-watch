# Command line reference

```text
tail-watch [flags]
tail-watch check-config [--config <file>]
tail-watch notify-test [flags]
```

Without a subcommand, tail-watch runs the collector.

## Collector flags

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `-config` | string | see below | Config file path. Overrides `TAIL_WATCH_CONFIG`. |
| `-listen` | string | `collector.listen`, else `127.0.0.1:9109` | HTTP listen address. |
| `-socket` | string | platform default | tailscaled LocalAPI socket. |
| `-log-level` | string | `info` | `debug`, `info`, `warn`, or `error`. Overrides `TAIL_WATCH_LOG_LEVEL`. |
| `-once` | bool | `false` | Run every check once, print one line each, and exit. |
| `-ui` | bool | `collector.ui`, else `false` | Serve the monitor UI and API. `--ui=false` turns off a config that enables it. |
| `-version` | bool | `false` | Print the version and exit. |

Go's flag syntax applies: `-flag` and `--flag` are equivalent, and a value is
given as `--flag value` or `--flag=value`. `-h` and `--help` print usage and
exit 0.

The default socket is `/var/run/tailscale/tailscaled.sock` on Linux.

### Precedence

| Setting | Order, first match wins |
| --- | --- |
| Config path | `--config`, `TAIL_WATCH_CONFIG`, `/etc/tail-watch/tail-watch.yaml` |
| Listen address | `--listen`, `collector.listen`, `127.0.0.1:9109` |
| UI | `--ui` or `--ui=false`, `collector.ui`, off |
| Log level | `--log-level`, `TAIL_WATCH_LOG_LEVEL`, `info` |

### Environment variables

| Variable | Meaning |
| --- | --- |
| `TAIL_WATCH_CONFIG` | Config file path when `--config` is not given. |
| `TAIL_WATCH_LOG_LEVEL` | Log level when `--log-level` is not given. |

Log levels are case-insensitive. `warning` is accepted as `warn`. Logs are JSON,
one object per line, on stderr. At `debug`, every check result is logged.

### `--once`

Runs the preflight, runs every check of every configured target once, prints one
line per result, and exits. It starts no HTTP server, builds no notifier, resolves
no secrets, sends no heartbeat, and ignores the UI.

```text
<target> <check> ok <latency>
<target> <check> fail <error>
```

### Startup checks

Before serving, the collector fails with exit code 1 when:

- the config file cannot be read or is invalid;
- the UI is on and `collector.state_dir` is unset;
- tailscaled is unreachable, or its state is not `Running`;
- tailscaled uses userspace networking (kernel mode is required);
- a target is a remote subnet address on Linux and the host has not accepted
  routes (`tailscale set --accept-routes`);
- a sink or heartbeat secret cannot be read or is empty (server mode only);
- the notification state file cannot be decoded (server mode only).

It logs a warning, and continues, when the node lacks `tag:monitor`, when an exit
node is active, when a webhook sink has no secret, and when the UI is reachable
beyond loopback.

## `check-config`

```bash
tail-watch check-config [--config <file>]
```

Loads and validates the config. It does not contact tailscaled and does not
resolve secrets, so it needs no environment variables. On success it prints one
line per target, one per sink, and one for the heartbeat if configured, then
exits 0.

```text
nyc-router interval=15s timeout=5s network=tailnet checks=tsping
nyc-nas interval=30s timeout=5s network=tailnet checks=icmp,tcp:445,http:https://10.20.0.50:5001/|DSM
sink ops-webhook type=webhook min_severity=page events=target.down,target.recovered queue=disk match=sites:nyc targets:nyc-* tags:tag:router
sink ops-slack type=slack min_severity=warn events=all queue=memory
heartbeat interval=1m0s
```

On failure it prints every problem to stderr and exits 1. The only flag is
`-config`.

## `notify-test`

```bash
tail-watch notify-test [--config <file>] [--sink <name>] [--timeout <duration>] [--socket <path>]
```

Sends one `test` event through each selected sink's real signing, rendering, and
retry code. It bypasses filters, queues, and `state_dir`. It needs the secrets
that the sinks reference, and it works when tailscaled is unreachable, in which
case the collector name in the payload is the OS hostname and `tailscale_ip` is
omitted.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `-config` | string | see [Precedence](#precedence) | Config file path. |
| `-sink` | string | every sink | Send only to the sink with this name. |
| `-timeout` | duration | `30s` | Give up on a sink after this long, including retries. |
| `-socket` | string | platform default | tailscaled LocalAPI socket, used to read the collector identity. |

Sinks are tried in parallel. One line is printed per sink, in config order:

| Line | Meaning |
| --- | --- |
| `<name> <type> <status>` | Delivered. `<status>` is the 2xx response code. |
| `<name> <type> <status-or-error>` | Failed. A permanent rejection such as `404` shows the status code. |
| `<name> <type> error: timed out after retries (last: <result>)` | Timeout reached while retrying. |
| `<name> <type> error: <detail>` | A secret could not be read, or the request could not be built. |

Retryable failures (`408`, `429`, `5xx`, network errors) are retried with backoff
until `--timeout`. See [Event payload](payload.md) for the request format.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. For the collector: clean shutdown, or `--once` with every check passing. For `notify-test`: every selected sink answered 2xx. `--help` also exits 0. |
| `1` | Failure. Config or startup error, HTTP server error, a check failed under `--once`, or a sink failed under `notify-test`. |
| `2` | Usage error. Unknown flag, invalid `--log-level` or `TAIL_WATCH_LOG_LEVEL`, or `notify-test` with no sinks configured or an unknown `--sink`. |

## HTTP endpoints

Served on the listen address by the collector, not under `--once`.

| Path | Response |
| --- | --- |
| `/` | Prober status page: each check's recent results, latency, and state. |
| `/metrics` | Prometheus text exposition. See [Metrics](metrics.md). |
| `/healthz` | `200` with body `ok`. Says the process is serving, not that targets are up. |
| `/ui/` | Monitor UI. `404` unless the UI is on. |
| `/api/v1/targets` | Monitor API: `GET` list, `POST` create. `404` unless the UI is on. |
| `/api/v1/targets/{name}` | Monitor API: `PUT` replace, `DELETE` remove. |
| `/api/v1/validate` | Monitor API: `POST` validate a monitor without saving it. |
| `/api/v1/probe` | Monitor API: `POST` validate a monitor, then run each of its checks once without saving it. Authorized and audited like a write. |
| `/api/v1/peers` | Monitor API: `GET` the tailnet peers the collector can currently see, for the address picker. `404` unless the UI is on. |

`POST /api/v1/probe` takes the same body as `POST /api/v1/targets`. An invalid
definition gets `422` with an `errors` list and runs no check. A valid one gets
`200` with `{"results": [...]}`, one entry per check in definition order, each
carrying `check`, `ok`, `latency_ms` and, for a failed check, `error`. A failing
check is data, not an error of the call. Each check is bounded by the
definition's `timeout`, and the whole call by 15 seconds.

`GET /api/v1/peers` answers `{"seeded": bool, "peers": [...]}`. Each peer
carries `name` (MagicDNS short name, else host name), `dns_name`,
`host_name`, `ips` (its Tailscale IPs), `tags`, `online` (`true`, `false` or
`null` when unknown) and `routes` (the subnet routes it is primary for).
`seeded` is `false`, with an empty list, until the first status arrives from
`tailscaled` and whenever the connection to it is down.

Write requests to `/api/v1/` must be `application/json` and must not carry an
`Origin` for another host. There is no authentication. See
[Manage monitors in the UI](../how-to/monitor-ui.md) and
[Monitor UI risks](../explanation/monitor-ui-risks.md).

## Signals

`SIGINT` and `SIGTERM` stop check scheduling and cancel running checks. The
collector then stops the notifier and shuts down the HTTP server, waiting up to the
longest target timeout plus 5 seconds. It exits 0. Under `--once`, a signal stops
the run and exits 0.
