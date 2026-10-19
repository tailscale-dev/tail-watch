# Configuration reference

tail-watch reads one YAML file. This page lists every key, its type, its default,
and the rules the loader enforces. Validate a file without contacting tailscaled:

```bash
tail-watch check-config --config /etc/tail-watch/tail-watch.yaml
```

See [Command line](cli.md) for how the file path is chosen and [Checks](checks.md)
for the `checks` syntax.

## File rules

- The file must not be empty.
- Unknown keys are rejected at every level.
- Durations use Go syntax with a unit, such as `500ms`, `30s`, `15m`, `1h`. A
  bare number is invalid.
- The loader reports every problem it finds in one pass, each prefixed with the
  key path, such as `targets[2].checks[1]`.
- Secret values are never written in the file. Keys ending in `_env` name an
  environment variable, and keys ending in `_file` name a file.

## Top-level keys

| Key | Type | Required | Default |
| --- | --- | --- | --- |
| `collector` | mapping | no | see below |
| `defaults` | mapping | no | see below |
| `targets` | list of targets | no | empty |
| `routes` | list of routes | no | empty |
| `sinks` | list of sinks | no | empty |
| `heartbeat` | mapping | no | off |

## Complete example

Every key, set to its default where it has one. This file passes
`tail-watch check-config`.

```yaml
collector:
  listen: 127.0.0.1:9109
  state_dir: /var/lib/tail-watch
  ui: false

defaults:
  interval: 30s
  timeout: 5s
  fail_threshold: 3
  recover_threshold: 2
  flap_limit: 4
  flap_window: 15m

targets:
  - name: nyc-router
    addr: nyc-router
    site: nyc
    severity: page
    network: tailnet
    interval: 15s
    timeout: 5s
    fail_threshold: 3
    recover_threshold: 2
    flap_limit: 4
    flap_window: 15m
    checks: [tsping]

  - name: nyc-nas
    addr: 10.20.0.50
    site: nyc
    severity: warn
    parent: nyc-router
    insecure_tls: true
    checks:
      - icmp
      - tcp:445
      - "http:https://10.20.0.50:5001/|DSM"

  - name: lab-printer
    addr: 192.168.1.50
    site: lab
    network: local
    checks: [icmp, tcp:9100]

sinks:
  - name: ops-webhook
    type: webhook
    url_env: OPS_WEBHOOK_URL
    secret_env: OPS_WEBHOOK_SECRET
    headers_env:
      Authorization: OPS_WEBHOOK_AUTH
    min_severity: page
    events: [target.down, target.recovered]
    match:
      sites: [nyc]
      targets: ["nyc-*"]
      tags: [tag:router]
    max_age: 1h
    queue: disk
    queue_size: 1000
    batch: true
    group_wait: 10s
    max_batch: 100

  - name: ops-slack
    type: slack
    url_env: SLACK_WEBHOOK_URL
    min_severity: warn
    mention_on_page: "<!subteam^S0123ABC>"
    status_url: https://monitor-nyc.example.ts.net:9109
    group_wait: 10s
    max_age: 1h
    queue: memory
    queue_size: 1000

heartbeat:
  url_env: HEARTBEAT_URL
  interval: 1m
```

## `collector`

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `listen` | string | no | `127.0.0.1:9109` | HTTP listen address, `host:port`. `--listen` overrides it. |
| `state_dir` | string | no | unset | Directory for durable state. Created with mode `0700` when missing. |
| `ui` | bool | no | `false` | Serve the monitor UI and API. `--ui` overrides it, and `--ui=false` turns it off. |
| `authorization` | mapping | no | unset | Optional role check for the monitor API. See below. |
| `local_only` | bool | no | `false` | Run without tailscaled. See below. |

`state_dir` holds:

- `notification-state.json`: open incidents, per-sink delivery state, and the
  pending events of `queue: disk` sinks. Mode `0600`. Written whenever
  `state_dir` is set and at least one sink exists.
- `routes.json`: routes created in the UI. Mode `0600`. Read only when the UI
  is on. See [Manage monitors in the UI](../how-to/monitor-ui.md).
- `targets.json`: monitors created in the UI. Mode `0600`. Read only when the UI
  is on.

`state_dir` is required when `ui` is on (startup exits with an error otherwise)
and when any sink sets `queue: disk`. `--once` ignores `ui`.

### `authorization`

Optional. When the section is absent, every request that reaches the listener
is permitted. When it is set, `/api/v1` requests other than `GET /api/v1/me`
need a role from the tailnet policy. See
[Give the monitor UI viewer and editor roles](../how-to/monitor-ui-roles.md).

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `capability` | string | yes, when the section is present | unset | Capability name in a domain you control, containing `/cap/`. Not a `tailscale.com` name. |
| `service` | string | no | unset | Tailscale Service, starting with `svc:`. Scopes the capability lookup to that Service. |

`check-config` applies the same rules and names each field. It rejects:

- an empty `capability`
- a `capability` with no `/cap/` segment
- a `capability` in the `tailscale.com` domain, including a subdomain
- a `service` that does not start with `svc:`
- the section while the UI is off (`collector.ui` and `--ui`)
- the section with `local_only: true`

### `setec`

The server for `*_setec` secret keys. See
[Read secrets from setec](../how-to/setec-secrets.md). The section is absent
when omitted. An empty `setec: {}` is also absent. A section that no key uses
is accepted and does not open a client.

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `server` | string | when any `*_setec` key is set | unset | `https` URL of the setec server, with a host. |
| `poll_interval` | duration | no | `5m` | How often to poll for new values. At least `1m`. |

### Local-only mode

With `local_only: true`, tail-watch runs without tailscaled: it makes no
LocalAPI call, skips the startup preflight, and takes the collector identity
from the hostname, so payloads carry `collector.name` and omit
`collector.tailscale_ip`. This mode is for running next to something that is
not a tailnet device, such as the ProxyGroup sidecar in
[Watch a Kubernetes ProxyGroup](../how-to/kubernetes-proxygroup.md).

Local-only mode changes what the loader accepts:

- A target without `network` defaults to `local` instead of `tailnet`.
- A target with `network: tailnet` is rejected.
- A `tsping` check is rejected.
- `ui: true` is rejected, and so is the `--ui` flag.
- `collector.authorization` is rejected.
- `collector.setec` is rejected.
- `url_setec`, `secret_setec`, `headers_setec`, and `heartbeat.url_setec`
  are rejected.

Each problem is reported with its key path, and `check-config` applies the same
rules. A minimal local-only config:

```yaml
collector:
  local_only: true

targets:
  - name: web
    addr: web.apps.svc.cluster.local
    checks: [tcp:443]
```

## `defaults`

Each key is the fallback for the same key on every target, including monitors
created in the UI.

| Key | Type | Default | Constraint |
| --- | --- | --- | --- |
| `interval` | duration | `30s` | greater than 0 |
| `timeout` | duration | `5s` | greater than 0 and less than `interval` |
| `fail_threshold` | integer | `3` | at least 1 |
| `recover_threshold` | integer | `2` | at least 1 |
| `flap_limit` | integer | `4` | at least 1 |
| `flap_window` | duration | `15m` | greater than 0 |

Constraints are checked on each target after defaults and overrides are merged.

## `targets[]`

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `name` | string | yes | | Unique identifier. Must match `[A-Za-z0-9._-]+`. Appears as the `target` metric label and in payloads. |
| `addr` | string | yes | | IP address or DNS name, including a MagicDNS short name. |
| `checks` | list of strings | yes | | Non-empty, no duplicates. See [Checks](checks.md). |
| `site` | string | no | empty | Free-form group label. Used for the `site` metric label and sink `match.sites`. |
| `severity` | string | no | `page` | `page` or `warn`. Severity of `target.down` when `route` is unset. Mutually exclusive with `route`. |
| `route` | string | no | empty | Name of a route in `routes`. That route's severity and sinks apply. Mutually exclusive with `severity`. |
| `network` | string | no | `tailnet` | `tailnet` or `local`. |
| `parent` | string | no | empty | Name of another target whose only check is `icmp` or `tsping`. |
| `insecure_tls` | bool | no | `false` | Skip certificate verification for `http:` checks on this target. |
| `interval` | duration | no | `defaults.interval` | Time between runs of each check. |
| `timeout` | duration | no | `defaults.timeout` | Per-run cancellation time. |
| `fail_threshold` | integer | no | `defaults.fail_threshold` | Consecutive failures that mark the target `DOWN`. |
| `recover_threshold` | integer | no | `defaults.recover_threshold` | Consecutive successes that mark it `UP` again. |
| `flap_limit` | integer | no | `defaults.flap_limit` | State changes within `flap_window` that mark it flapping. |
| `flap_window` | duration | no | `defaults.flap_window` | Window for counting state changes. |

Rules enforced across targets:

- `tsping` is invalid when `network` is `local`.
- An `http:` check's URL host must equal `addr`. Comparison ignores case and a
  trailing dot, and treats equivalent IP forms as equal.
- `parent` must name a target in the same list, and that target must have
  exactly one check, either `icmp` or `tsping`.
- Parent cycles are rejected.
- `name` must be unique.
- A target sets `route` or `severity`, not both. `route` must name a route in
  this file's `routes` list. A routed target's severity is the route's.

For how the state, flap, and parent settings behave at run time, see
[Alert state](../explanation/alert-state.md) and
[Failure layers](../explanation/failure-layers.md). For `network`, see
[Monitor remote subnets and local LANs](../how-to/remote-subnets.md).

## `routes[]`

A route names the sinks that receive a target's down and flapping events, and
the severity of `target.down`. A target with no `route` is a candidate for
every sink, at its own `severity`. `target.flapping` is always `warn`.
`target.recovered` goes to the sinks that received the down.

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `name` | string | yes | | Unique. Must match `[A-Za-z0-9._-]+`. |
| `severity` | string | no | `page` | `page` or `warn`. Severity of `target.down` for targets that name this route. |
| `sinks` | list of strings | yes | | Non-empty names of sinks in `sinks`. A name may appear once. |
| `mention` | string | no | empty | Slack text used instead of the sink's `mention_on_page` for a page-severity `target.down`. |

```yaml
sinks:
  - name: db-hook
    type: webhook
    url_env: DB_HOOK_URL

routes:
  - name: db-team
    severity: page
    sinks: [db-hook]
    mention: "<!subteam^S0DB>"

targets:
  - name: pg-primary
    addr: 10.0.0.1
    checks: [icmp]
    route: db-team
```

To choose `page` or `warn`, and to create a route in the UI, see
[Route notifications](../how-to/notification-routes.md).

## `sinks[]`

A sink is a notification destination. For delivery behavior, see
[Incidents and queues](../explanation/incidents-and-queues.md), and for setup, see
[Send alerts to Slack](../how-to/slack-sink.md),
[Send alerts to Microsoft Teams](../how-to/teams-sink.md) and
[Send alerts to a webhook](../how-to/webhook-sink.md).

### Common keys

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `name` | string | yes | | Unique. Must match `[A-Za-z0-9._-]+`. `incidents`, `intake`, and `state` are reserved. |
| `type` | string | yes | | `webhook`, `slack` or `teams`. |
| `url_env` | string | one of `url_env`, `url_file`, `url_setec` | | Environment variable holding the destination URL. |
| `url_file` | string | one of `url_env`, `url_file`, `url_setec` | | File holding the destination URL. |
| `url_setec` | string | one of `url_env`, `url_file`, `url_setec` | | setec secret name holding the destination URL. |
| `min_severity` | string | no | `info` | `info`, `warn`, or `page`. Events below it are not sent. |
| `events` | list of strings | no | all types | Event types to send. See the list below. |
| `match` | mapping | no | no filter | Target filters. See below. |
| `max_age` | duration | no | `1h` | Age after which an undelivered event is dropped. Greater than 0. |
| `queue` | string | no | `memory` | `memory` or `disk`. `disk` requires `collector.state_dir`. |
| `queue_size` | integer | no | `1000` | Maximum events waiting. At least 1. |
| `group_wait` | duration | no | `10s` | How long to collect events into one request. Greater than 0. |

Exactly one of `url_env`, `url_file`, and `url_setec` must be set. Values from
a file or from setec have surrounding whitespace trimmed. All secrets are read
again on every send. A file or environment value is picked up with no restart.
A setec value is picked up after the next `collector.setec.poll_interval`.

`group_wait` groups events for a `slack` or `teams` sink, and for a `webhook`
sink only when `batch` is `true`.

Valid `events` values:

| Value | Emitted |
| --- | --- |
| `target.down` | yes |
| `target.recovered` | yes |
| `target.flapping` | yes |
| `collector.started` | yes |
| `test` | yes, by `notify-test` only |
| `target.degraded` | never in this version; accepted and reserved |

An event reaches a sink only when its type is listed (or `events` is unset), its
severity is at least `min_severity`, and every key in `match` matches. The `test`
event ignores all filters. An event with no target, such as `collector.started`,
ignores `match`. A recovery goes to the sinks that were selected for its down,
regardless of the current filters.

### `match`

All keys are optional. Within one key, any listed value may match. Across keys,
all must match.

| Key | Type | Matches |
| --- | --- | --- |
| `sites` | list of strings | The target's `site`, exactly. |
| `targets` | list of strings | The target's `name`, as a glob (`*`, `?`, `[...]`). An invalid glob is rejected. |
| `tags` | list of strings | Any Tailscale tag on the node the target resolves to, such as `tag:router`. |

### Webhook-only keys

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `secret_env` | string | no | unset | Environment variable holding the HMAC key. |
| `secret_file` | string | no | unset | File holding the HMAC key. |
| `secret_setec` | string | no | unset | setec secret name holding the HMAC key. |
| `headers_env` | mapping | no | empty | Header name to the environment variable holding its value. |
| `headers_setec` | mapping | no | empty | Header name to the setec secret name holding its value. |
| `batch` | bool | no | `false` | Send several events per request. |
| `max_batch` | integer | no | `100` | Events per batch at most. At least 1. |

Set at most one of `secret_env`, `secret_file`, and `secret_setec`. A header
name must not appear in both `headers_env` and `headers_setec`. Without a
secret, requests are unsigned and the collector logs a warning at startup. See
[Event payload](payload.md) for the signature and batch envelope.

### Slack-only keys

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `mention_on_page` | string | no | empty | Text prepended to a `target.down` of severity `page`, such as `<!subteam^S0123ABC>`. |
| `status_url` | string | no | empty | Base URL. Each target line links to `<status_url>/#<target>`. |

A `slack` sink rejects `batch`, `max_batch`, `secret_env`, `secret_file`,
`secret_setec`, `headers_env`, and `headers_setec`. A `webhook` sink rejects
`mention_on_page` and `status_url`.

### Teams-only keys

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `status_url` | string | no | empty | Base URL. Each target's name links to `<status_url>/#<target>`. |

A `teams` sink rejects `batch`, `max_batch`, `secret_env`, `secret_file`,
`secret_setec`, `headers_env`, `headers_setec`, and `mention_on_page`. A
route's `mention` is not printed on a Teams card. See
[Send alerts to Microsoft Teams](../how-to/teams-sink.md).

## `heartbeat`

Pings a dead-man's-switch URL with `GET`. The section is off when omitted.

| Key | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `url_env` | string | one of `url_env`, `url_file`, `url_setec` | | Environment variable holding the URL. |
| `url_file` | string | one of `url_env`, `url_file`, `url_setec` | | File holding the URL. |
| `url_setec` | string | one of `url_env`, `url_file`, `url_setec` | | setec secret name holding the URL. |
| `interval` | duration | no | `1m` | Time between pings. Greater than 0. |

Pings start once every target and check has a first result. Each request times
out after 10s, and a non-2xx response or an error is counted in
`tailwatch_heartbeat_failures_total` and not retried early. See
[Watch the watcher with a heartbeat](../how-to/heartbeat.md).

## Secret resolution

At server startup, every `url_env`, `url_file`, `url_setec`, `secret_env`,
`secret_file`, `secret_setec`, `headers_env` value, `headers_setec` value, and
the heartbeat URL must resolve to a non-empty value, or startup exits with an
error. `check-config` and `--once` do not resolve secrets and do not contact a
setec server. `check-config` prints each sink's name, type, filters, and queue
mode, and never a secret value. A `url_setec` sink also prints
`url=setec:<name>`. See [Read secrets from setec](../how-to/setec-secrets.md).
