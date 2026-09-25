# Tailnet reachability monitor

Status: draft
Related: tailscale/tailscale#5967 (FR: notification when nodes go online or offline)

## Context

Issue #5967 asks for an alert when a node goes online or offline. It has been open since
October 2022 and is mostly +1s. The public API still has no webhook event for it. The
current webhook list covers node lifecycle, key expiry, user, policy, and IP forwarding
events.

Tailscale staff pointed out the core mismatch in the thread. Control knows whether a node
holds a connection to control. Users want to know whether they can reach the thing. Those
come apart in both directions:

- A node can lose its control connection and still pass traffic to peers for hours.
- A node can stay connected to control while its tunnel, host firewall, or service is
  broken.
- Most of what people actually care about isn't a Tailscale node at all. Switches,
  APs, UPSes, printers, cameras, and NAS boxes sit behind a subnet router.

So an online/offline webhook would answer the wrong question for most users. What they
want is Pingdom or LogicMonitor running inside the tailnet. It should poll devices and
services, alert on state changes, and suppress alerts that depend on another failure.

This doc proposes that tool. It also uses the tailnet's own view (control presence, disco
pings, path type) to say why a check failed. A monitor outside the tailnet can't do that.

## Goals

- Poll targets on a schedule with ICMP, TCP, HTTP(S), TLS expiry, and Tailscale ping
  checks.
- Targets can be Tailscale nodes, subnet-routed LAN devices, or services on either.
- Alert on state transitions only, after N consecutive failures, and send a recovery
  notice.
- Tell a dead subnet router apart from a dead device behind it. Page once for the router,
  not 40 times.
- Report when a path degrades from direct to DERP or peer relay without paging.
- Notify through a signed generic webhook and Slack, with recoveries and retries.
- Export Prometheus metrics so existing Grafana and Alertmanager setups work unchanged.
- Run as one static binary with one YAML config.

## Non-goals

- SNMP polling, interface counters, and bandwidth graphs. This detects up or down. It is
  not an NMS. SNMP can come later.
- Changes to tailscaled or the control server. This is a client of existing APIs.
- A hosted service or a web UI beyond the prober status page we get for free.

## Architecture

```
                 +--------------------------------------------+
                 | collector (tag:monitor)                    |
                 |                                            |
 targets.yaml -->|  scheduler (prober.Prober)                 |---> /metrics (Prometheus)
                 |    icmp | tcp | http | tls | tsping        |
                 |                 |                          |
                 |  classifier <---+--- presence watcher      |
                 |    (failure layer, dependency suppression) |
                 |                 |        (IPN bus)         |
                 |  alert state machine --> dispatcher        |---> webhook sinks
                 |                          (per-sink queues) |---> Slack sinks
                 +--------------------------------------------+
                          | host sockets + LocalAPI
                          v
                 tailscaled (kernel mode, tailscale0)
                          v
                       tailnet ---> subnet router ---> LAN devices
```

A collector is a tailnet node tagged `tag:monitor`. It runs checks against targets and
feeds each result to a classifier. The classifier combines the result with the node's
live view of peer presence. An alert state machine per target decides what to send.

### Runtime: kernel-mode tailscaled

The collector is a normal program on a host running `tailscaled` in kernel mode. The host
is the tailnet node. The collector uses the host's sockets for checks and talks to
tailscaled over the LocalAPI socket for `Ping`, `WatchIPNBus`, and `Status`.

We picked this over embedding `tsnet` for these reasons:

- ICMP decides it. Pingdom-style "does the switch answer ping" is the most common ask in
  the issue, and many network devices have no TCP port worth checking. tsnet's userspace
  stack dials TCP and UDP only, so it can't ping a LAN IP. Kernel mode can, and needs no
  root when the host allows unprivileged ICMP (`udp4`, see `net.ipv4.ping_group_range`
  on Linux).
- Identity is still scoped. The host is dedicated to monitoring and tagged `tag:monitor`,
  so ACLs cover exactly what the collector needs, same as a tsnet node would.
- Debugging uses the same stack. When a check fails, an operator can SSH to the collector
  and run `ping`, `tailscale ping`, `curl`, or `tcpdump` and see what the collector sees.
  With tsnet the stack is inside the process and invisible to those tools.
- Deployment matches subnet routers. Sites already install tailscaled, so a collector can
  use the same image and automation.
- One code path for the MVP. No tsnet-specific dialers or check behavior to test.

tsnet support comes in phase 2, for places where a daemon or root isn't available
(locked-down Kubernetes, a PaaS, someone else's host) and for fleets that only need TCP,
HTTP, and tsping checks. The "Network access" section below keeps that addition to one new
file.

### Placement

- One central collector is enough for small tailnets.
- For multiple sites, run a collector on a separate small host or VM next to each subnet
  router, on the same LAN. LAN checks then keep working when the site's WAN link is down.
  The central collector checks the routers. Site collectors report metrics and alerts
  outward whenever they have a path.
- Don't run the collector on the subnet router itself. A router-hosted collector reaches
  the LAN directly and never crosses the tunnel, so it can't see `tunnel`-layer failures,
  and it goes down with the thing it is supposed to report on.
- Two collectors in different regions allow quorum. Only alert when both agree, which
  removes most false pages caused by the collector's own network.

## Check types

| Type | What it proves | Implementation |
|---|---|---|
| `icmp` | Host answers echo | `net/ping.Pinger` (`net/ping/ping.go`), built with a plain `&net.ListenConfig{}` |
| `tcp` | Port accepts connections | `prober.TCP(addr)` (`prober/tcp.go`) |
| `http` | Status 2xx/3xx, optional body match | `prober.HTTP(url, wantText)` (`prober/http.go`) |
| `tls` | Handshake works and cert isn't close to expiry | `prober.TLS(hostPort, cfg)` (`prober/tls.go`) |
| `tsping` | WireGuard path to the node that handles this IP | `local.Client.Ping(ctx, ip, tailcfg.PingDisco)` (`client/local/local.go`) |

Gotcha for `icmp`: `netcheck` and `magicsock` build their pingers with
`netns.Listener(...)`. That listener binds to the physical interface on purpose so
traffic skips the tunnel. The collector needs the opposite, so it must not copy that
line.

In kernel mode the host's routing table already sends tailnet and subnet-routed traffic
into `tailscale0`, so an ordinary dialer reaches every target. The `prober` check
constructors in the table show the logic to copy. The next section explains why checks
reimplement that logic over `netEnv` instead of calling the constructors directly.

### Network access

All checks reach the network through one small interface, not through package-level
dialers:

```go
// netEnv is how the collector reaches the tailnet.
type netEnv interface {
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) // for net/ping
	LocalClient() *local.Client // Ping, WatchIPNBus, Status
}
```

The MVP has one implementation, `kernelEnv`, backed by `net.Dialer`, `net.ListenConfig`,
and a `local.Client` on the default socket. Phase 2 adds `tsnetEnv`, backed by
`tsnet.Server.Dial`, `Server.ListenPacket`, and `Server.LocalClient`. Its constructor
refuses configs that contain `icmp` checks.

Because `prober.TCP` and `prober.HTTP` use the host dialer, checks are built as
`prober.FuncProbe` wrappers over `netEnv` from the start, even though the kernel
implementation could use the stock ones. That costs a few lines now and means tsnet
support doesn't touch any check code.

## What a failed check means

A plain poller only knows the target is down. The classifier sorts each failure into one
layer, checking from the bottom up:

| Layer | Condition | Alert |
|---|---|---|
| `control` | The handling node is offline in the collector's netmap | Page on the router or node, then suppress everything behind it |
| `tunnel` | Handling node is online in control but `tsping` fails | Page, "no WireGuard path to <router>". Usually NAT, firewall, or a stuck magicsock |
| `lan` | `tsping` to the router works but ICMP or TCP to the device fails | Page on the device |
| `service` | Device answers ICMP but the port or HTTP check fails | Page on the service |
| `degraded` | Reachable, but the path moved from direct to DERP or peer relay, or latency is 3x the 1h baseline | Warn, no page |

`tsping` gives both the path type and the handling node for free. The result is an
`ipnstate.PingResult` (`ipn/ipnstate/ipnstate.go`). `Endpoint` is set for a direct path,
`DERPRegionCode` for a DERP relay, and `PeerRelay` for a peer relay. `NodeIP` is the
Tailscale IP of the node that handles the destination. For a subnet-routed address, that
is the subnet router.

A disco ping never touches the IP stack on either end. So "disco works, TCP fails" means
the problem is in the host's IP stack, firewall, or service, not in the tunnel.

### Dependency learning

On startup and every 10 minutes, the collector runs one `tsping` per LAN target. It
records `NodeIP` as that target's parent. No hand-written parent config is needed. If a
subnet is routed by an HA pair, `NodeIP` follows whichever router is primary, which is
the one that matters.

An optional `parent:` override in the config covers cases the ping can't see. One example
is a device behind a second switch that is itself monitored.

### Presence watcher

The collector keeps a live `map[tailcfg.NodeID]bool` of peer online state from the IPN
bus:

```go
w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialStatus|ipn.NotifyPeerPatches)
// First Notify: seed from n.InitialStatus.Peer (PeerStatus has NodeID, Online, Tags).
// After that:
//   n.PeerChangedPatch[i].Online != nil  -> flip presence
//   n.PeersChanged                       -> replace entry from the full Node
//   n.PeersRemoved                       -> delete entry
```

The bits are documented in `ipn/backend.go`. `NotifyPeerPatches` means an online flip
arrives as a small `tailcfg.PeerChange`, not a full node. The legacy `Notify.NetMap` path
is gone (`ab4262680`), so don't build on it.

Presence never raises an alert on its own. It only feeds the classifier. The ~15s delay
before control reports a node offline doesn't matter, because data-plane checks decide
the timing.

## Alert state machine

Each target has a state: `UNKNOWN -> UP <-> DOWN`, plus `SUPPRESSED` for targets whose
parent is DOWN.

- `UP -> DOWN` after `fail_threshold` failures in a row (default 3).
- `DOWN -> UP` after `recover_threshold` successes in a row (default 2). Sends a recovery
  notice with the outage length.
- A target that changes state more than `flap_limit` times in `flap_window` is marked
  flapping. It sends one "flapping" notice and then stays quiet until it is stable for
  `flap_window`.
- If a target's parent goes DOWN, the target moves to `SUPPRESSED`. Its failures don't
  page. The router's alert lists how many dependents it took down. When the parent
  recovers, the dependents re-check at once and return to UP or DOWN.
- With quorum on, a DOWN needs agreement from `quorum` collectors. Sharing state across
  collectors is phase 3. Before then, run quorum in Alertmanager over each collector's
  metrics.

State is in memory. After a restart, every target starts as UNKNOWN and reaches UP
silently, so a restart doesn't send a burst of recoveries. The exception is open
incidents when `state_dir` is set. See "Open incidents" under Notifications.

## Notifications

Every state change in the alert state machine becomes an event. The dispatcher sends
each event to the configured sinks. The MVP has two sink types: a generic webhook and
Slack. Both can be configured more than once, for example a webhook for pages and a
Slack channel for everything.

### Events

| Event | When | Severity |
|---|---|---|
| `target.down` | `UP -> DOWN` | `page`, or the target's `severity` override |
| `target.recovered` | `DOWN -> UP` | Same as the `target.down` it resolves |
| `target.flapping` | Target crossed `flap_limit` | `warn` |
| `target.degraded` | Path fell back to DERP or peer relay, or latency spiked (phase 2) | `warn` |
| `collector.started` | Collector finished its first full round of checks | `info` |
| `test` | Sent by `tsmon notify-test` | `info` |

Two rules keep the noise down:

- Suppressed targets don't emit events. The parent's `target.down` lists them in
  `dependents`, and the parent's `target.recovered` lists them too.
- A `target.recovered` always goes to every sink that received the matching
  `target.down`, even if the sink's filters would otherwise drop it. A page with no
  resolve is worse than no page.

Targets can set `severity: warn` in config. Use this for things like printers that should
show up in Slack but never page anyone.

### Payload

The JSON body is a public contract, so it carries a `version` and only changes by adding
fields.

```json
{
  "version": 1,
  "id": "evt_01JAB3K9T6M4X2Q8R7N5W0YZCD",
  "type": "target.down",
  "severity": "page",
  "time": "2026-09-25T14:03:11Z",
  "collector": { "name": "monitor-nyc", "tailscale_ip": "100.101.102.103" },
  "target": {
    "name": "nyc-nas",
    "addr": "10.20.0.50",
    "site": "nyc",
    "parent": "nyc-router"
  },
  "check": "tcp:445",
  "layer": "service",
  "error": "dial tcp 10.20.0.50:445: connect: connection refused",
  "since": "2026-09-25T14:02:11Z",
  "failures": 3,
  "path": { "kind": "direct", "endpoint": "203.0.113.7:41641", "latency_ms": 18 },
  "dependents": []
}
```

`target.recovered` adds `duration_seconds` and `down_event_id`, which points at the
`target.down` it resolves. Receivers can then close the right ticket without matching on
names. `id` stays the same across retries, so receivers can dedupe on it.

### Generic webhook

- `POST` to the configured URL with `Content-Type: application/json` and
  `User-Agent: tsmon/<version>`. By default, one event per request. Dependency
  suppression already collapses most alert storms, and many simple receivers (n8n,
  Zapier, Lambda URLs) expect a single flat object. Rate-limited receivers can use
  batched mode, described below.
- Headers: `Tsmon-Event-Id: <id>` and `Tsmon-Signature: t=<unix>,v1=<hex>`, where `v1`
  is HMAC-SHA256 over `"<t>.<body>"` keyed by the sink's secret. This follows the format
  of Tailscale's own `Tailscale-Webhook-Signature`, so anyone who already verifies
  Tailscale webhooks can reuse that code. Receivers should reject a `t` more than 5
  minutes old.
- Extra headers, such as an `Authorization` token for a receiver that doesn't check
  HMAC, come from environment variables through `headers_env`.
- Result handling:
  - `2xx` means delivered.
  - `408`, `429`, `5xx`, and timeouts are retried with `util/backoff` (max 5m between
    tries). On `429`, the sink honors `Retry-After`.
  - Any other `4xx` is a permanent failure. The sink logs it once, counts it in a
    metric, and doesn't retry. Retrying a `400` or `404` won't change the answer.
  - Events older than `max_age` (default 1h) are dropped and counted.
- Per-request timeout is 10s.

#### Batched mode

Set `batch: true` on a webhook sink for receivers that are rate-limited or charge per
request.

- After the first event arrives, the sink waits `group_wait` (default 10s) or until it
  has `max_batch` events (default 100), whichever comes first. Then it sends one POST:

  ```json
  { "version": 1, "batch_id": "bat_01JAB3M2F8K1V7C4R9D6T0PQXS", "events": [ { ... }, { ... } ] }
  ```

- Each element of `events` is exactly the single-event payload, so receivers can share
  parsing code between the two modes.
- `Tsmon-Signature` covers the whole body. `Tsmon-Batch-Id` replaces `Tsmon-Event-Id`.
- A retry resends the same body bytes with the same `batch_id`. Only the signature's
  `t` is recomputed. Events that arrive during retries go into the next batch, so order
  holds across batches.
- If a `down` and its `recovered` land in the same window, both are sent in order. The
  sink doesn't cancel them out. The receiver still learns that something blipped, and
  suppressing repeated blips is the state machine's flap handling, not the sink's job.
- A permanent failure (non-retryable `4xx`) drops the whole batch and counts every
  event in it.

### Slack

The MVP uses a Slack incoming webhook URL, which needs no bot or OAuth setup.

- Messages use Block Kit with a plain-text `text` fallback for notifications and
  screen readers. Example `target.down`:

  ```
  :red_circle: DOWN  nyc-nas  tcp:445  (service)
  10.20.0.50 at nyc, behind nyc-router, direct path, 18 ms
  dial tcp 10.20.0.50:445: connect: connection refused
  down since 14:02:11 UTC, 3 failures, from monitor-nyc
  ```

  A `target.recovered` reads `:large_green_circle: RECOVERED nyc-nas tcp:445 after 12m`.
  A router's `target.down` adds "and 14 targets behind it", then lists up to 10 names.
- `mention_on_page` (for example `<!subteam^S0123ABC>` or `<!here>`) is prepended only to
  `page`-severity down events, never to recoveries or warnings.
- Slack limits incoming webhooks to about one message per second. The Slack sink groups
  events that arrive within `group_wait` (default 10s) into one message, capped at 10
  lines plus "and N more". Webhook sinks only group in batched mode.
- If `status_url` is set, each message links to the collector's status page for that
  target.

Phase 2 adds a bot-token mode (`chat:write`, `chat.postMessage`). It posts each recovery
as a thread reply under the original down message and edits the original to show it
resolved, which keeps busy channels readable. Incoming webhooks can't do either.

### Delivery

- Each sink has its own goroutine and a bounded queue (`queue_size`, default 1000
  events), held in memory or on disk (see "Queue modes"). Probes hand events to the
  dispatcher and never block on network calls to Slack or a webhook.
- Delivery within a sink is in order, so a receiver never sees `recovered` before
  `down` for the same target. A slow webhook doesn't delay Slack.
- When a queue is full, the oldest event is dropped and counted. A newer event reflects
  current state, which matters more.
- Events queued while the collector's own uplink is down go out when it returns, with
  their original `time`. Any event delivered more than 1m after its `time` carries
  `"late": true`.
- Webhook URLs and Slack URLs are secrets. They come from `*_env` or `*_file` keys, never
  from the YAML itself, and logs redact everything after the host.

### Queue modes

Each sink sets `queue: memory` (the default) or `queue: disk`.

- `memory`: a restart or crash loses whatever is still queued. This is fine for Slack,
  where a message about a blip from before the restart has little value, and the
  heartbeat already covers a dead collector.
- `disk`: for receivers that open and close tickets. There, a lost `down` or
  `recovered` leaves a ticket wrong until a human notices. Requires `state_dir`.

Layout under `state_dir` (for example `/var/lib/tsmon`, created with mode 0700):

```
state_dir/
  queue/<sink>/000000000142-evt_01JAB3K9T6M4X2Q8R7N5W0YZCD.json   one pending event per file
  incidents.json                                                  open incidents, see below
```

- Enqueue writes the event to a temp file, fsyncs it, and renames it into place. The
  event counts as queued only after the rename. On load, temp files are ignored and
  removed. Events are state changes, not check results, so they are rare and an fsync
  per event costs nothing noticeable.
- The sink deletes the file after a `2xx` or a permanent failure. If the process dies
  between the `2xx` and the delete, the event is sent again after restart. This is
  at-least-once delivery, and receivers dedupe on `id`.
- On startup the sink loads files in sequence order, drops those older than `max_age`
  (counted as `expired`), applies `queue_size` (oldest dropped first), and resumes.
- In batched mode, events are still stored one per file. A batch is only assembled at
  send time, and all of its files are deleted after a `2xx`.
- Files hold event payloads only. URLs, secrets, and headers are read from the
  environment at send time and never written to disk. Payloads contain internal
  addresses and hostnames, which is why the directory is 0700.
- If a write fails (disk full, read-only filesystem), that event goes to the in-memory
  queue instead. The sink logs once and increments `tsmon_notify_persist_errors_total`.
  Losing durability is better than losing the alert.

### Open incidents

A persistent queue alone doesn't make restarts safe. Suppose the collector restarts
while `nyc-nas` is down. The in-memory state machine forgets that, so when `nyc-nas`
comes back it goes `UNKNOWN -> UP` without a word, and the receiver's ticket never
closes.

So when `state_dir` is set, the collector also keeps `incidents.json`. Each entry is one
open `target.down`: target, check, event `id`, `since`, and the sinks it went to. The file
is rewritten (temp file, fsync, rename) whenever an incident opens or closes. After a
restart:

- A target with an open incident starts in `DOWN`, not `UNKNOWN`, and doesn't send a
  second `target.down`.
- When it recovers, `target.recovered` carries the original `down_event_id` and a
  `duration_seconds` measured from the original `since`. It goes to the sinks recorded
  in the incident, even if the config for those sinks' filters has changed since.
- Incidents for targets or sinks removed from the config are closed with a
  `target.recovered` that has `"reason": "removed"`, so no ticket stays open forever.
  A removed sink can't receive it, so its entry is just dropped and logged.

### Egress path

Kernel-mode tailscaled means alert traffic follows the host's routing table.

- A webhook URL can be a tailnet address, for example an internal receiver reached over
  MagicDNS. It then needs a grant from `tag:monitor` like any other target.
- Don't set an exit node on the collector host. If you do, Slack and public webhooks go
  through that exit node, and when it fails, the alerts about it fail too.

### Watching the watcher

A dead collector can't report that it is dead. An optional `heartbeat` block pings a URL
every `interval` (healthchecks.io, Cronitor, Better Stack, or any "alert me if this stops"
service). That service is what pages when the collector, its host, or its uplink goes
away.

### Trying it out

`tsmon notify-test [--sink NAME]` sends a `test` event through the real sink code, with
signing and retries. It prints the HTTP status for each sink. Run it after every config
change.

## Config

```yaml
collector:
  listen: 127.0.0.1:9109  # /metrics and prober status page
  state_dir: /var/lib/tsmon  # optional; enables queue: disk and open-incident tracking

defaults:
  interval: 30s
  timeout: 5s
  fail_threshold: 3
  recover_threshold: 2
  flap_limit: 4
  flap_window: 15m

sinks:
  - name: ops-webhook
    type: webhook
    url_env: OPS_WEBHOOK_URL
    secret_env: OPS_WEBHOOK_SECRET      # HMAC key for Tsmon-Signature
    headers_env:
      Authorization: OPS_WEBHOOK_AUTH   # optional, value read from this env var
    min_severity: page
    events: [target.down, target.recovered]
    max_age: 1h
    queue: disk                         # survives restarts; needs collector.state_dir
    queue_size: 1000
    batch: true                         # one POST with an events array per window
    group_wait: 10s
    max_batch: 100

  - name: ops-slack
    type: slack
    url_env: SLACK_WEBHOOK_URL
    min_severity: warn
    mention_on_page: "<!subteam^S0123ABC>"
    group_wait: 10s
    queue: memory                       # default
    status_url: https://monitor-nyc.example.ts.net:9109
    match:
      sites: [nyc, sfo]                 # also: targets (globs), tags

heartbeat:
  url_env: HEARTBEAT_URL
  interval: 1m

targets:
  - name: nyc-core-switch
    addr: 10.20.0.1
    checks: [icmp]
  - name: nyc-nas
    addr: 10.20.0.50
    checks: [icmp, tcp:445, "http:https://10.20.0.50:5001/|DSM"]
    site: nyc
  - name: nyc-printer
    addr: 10.20.0.80
    checks: [icmp]
    severity: warn            # shows up in Slack, never pages
  - name: nyc-subnet-router
    addr: nyc-router          # MagicDNS name, a Tailscale node
    checks: [tsping]
    interval: 15s

discover:
  # Optional. Add every node with these tags as a tsping target.
  - tags: [tag:router, tag:server]
    checks: [tsping]
```

Discovery reads tags from the presence watcher, so it needs no API credentials.
Discovery via the devices API (`GET /api/v2/tailnet/-/devices`, fields `tags`,
`connectedToControl`, `lastSeen`) is a later option for nodes the collector's ACLs can't
see.

## ACLs

The collector only sees and reaches what policy allows. Minimal grant:

```jsonc
{
  "tagOwners": { "tag:monitor": ["autogroup:admin"] },
  "grants": [
    { "src": ["tag:monitor"], "dst": ["tag:router", "tag:server"], "ip": ["*"] },
    { "src": ["tag:monitor"], "dst": ["10.20.0.0/16"],             "ip": ["icmp:*", "tcp:22", "tcp:445", "tcp:443", "tcp:5001"] }
  ]
}
```

If a target never appears in the collector's netmap, the fix is almost always a missing
grant. The collector should log that plainly at startup ("target X: no peer handles
10.20.0.50; check grants to tag:monitor"). It should not keep retrying quietly.

## Metrics

Everything goes through `prober.Prober` with `WithMetricNamespace("tsmon")`. We get the
standard series for free (result, latency, attempts, and so on), labeled `name` and
`class`, with our own labels added:

- `target`, `check` (`icmp`, `tcp:445`, ...), `parent` (router hostname or empty),
  `site`.
- Custom gauge `tsmon_target_state{target}`: 0=unknown, 1=up, 2=down, 3=suppressed,
  4=flapping.
- Custom gauge `tsmon_path{target,kind}`: kind is `direct`, `derp`, or `peer_relay`,
  from `tsping` results.
- `tsmon_failure_layer_total{target,layer}` counter.
- `tsmon_notify_sent_total{sink,type}` and `tsmon_notify_failed_total{sink,reason}`,
  where reason is `timeout`, `http_5xx`, `http_429`, or `http_4xx_permanent`.
- `tsmon_notify_dropped_total{sink,reason}`: `queue_full`, `expired`, or `permanent`.
- `tsmon_notify_persist_errors_total{sink}`: disk writes that failed and fell back to
  memory.
- `tsmon_notify_batch_size{sink}` histogram, batched webhook sinks only.
- `tsmon_open_incidents` gauge, from `incidents.json` when `state_dir` is set.
- `tsmon_notify_queue_len{sink}` gauge and `tsmon_notify_latency_seconds{sink}`, time from
  event to confirmed delivery.

Alert on `tsmon_notify_dropped_total` rising, or `tsmon_notify_queue_len` staying above
zero, from Prometheus. That is how you find out alerts aren't getting through.

The prober status page (`Prober.StatusHandler`, as in `cmd/derpprobe`) gives a basic
dashboard with no extra work.

## Implementation sketch

Layout, if built in this repo:

```
cmd/tsmon/
  tsmon.go          flags, config load, wiring
  netenv.go         netEnv interface + kernelEnv (phase 2 adds tsnetEnv.go)
  config.go         YAML schema and validation
  checks.go         check -> prober.FuncProbe over netEnv (icmp via net/ping, tsping via LocalClient)
  presence.go       IPN bus watcher, NodeID -> online map
  classify.go       failure layer + dependency learning
  alert.go          per-target state machine, flap damping
  notify.go         Event type, dispatcher, filters, heartbeat
  queue.go          memory and disk queues behind one interface
  incidents.go      open-incident tracking in state_dir/incidents.json
  webhook.go        generic webhook sink, HMAC signing, retry policy, batched mode
  slack.go          Slack incoming-webhook sink, Block Kit rendering, grouping
  notifytest.go     `tsmon notify-test` subcommand
```

Reused as-is:

- `prober`: scheduling, jitter (`WithSpread`), timeouts, Prometheus, status page.
- `net/ping`: ICMP echo with unprivileged `udp4` fallback.
- `client/local`: `Ping`, `WatchIPNBus`, `Status`.
- `util/backoff`: retry delays for webhook and Slack delivery.
- `tsnet` (phase 2): `Server.Dial`, `Server.ListenPacket`, `Server.LocalClient` behind
  `tsnetEnv`.
- `cmd/derpprobe/derpprobe.go`: model for flags, the status page, and the overall-health
  endpoint.

Wiring each check into `prober` looks like this. The probe func runs the check, calls
`classify` on failure, and hands the result to the target's state machine:

```go
p := prober.New().WithSpread(true).WithMetricNamespace("tsmon")
for _, t := range cfg.Targets {
	for _, c := range t.Checks {
		p.Run(t.Name+"/"+c.String(), t.Interval, prober.Labels{"target": t.Name, "check": c.String()},
			prober.ProbeClass{
				Class:   c.Kind(),
				Timeout: t.Timeout,
				Probe: func(ctx context.Context) error {
					err := c.Do(ctx)
					alerts.Observe(t, c, classify(ctx, t, c, err))
					return err
				},
			})
	}
}
```

Where it should live. This is a standalone client of stable APIs, not a tailscaled feature.
It doesn't belong under `feature/` and shouldn't add anything to tailscaled's dependency
graph. As a new `cmd/`, it touches no existing depaware snapshot. Start it in a separate
repo and propose it for `cmd/` once it has proven itself. Keeping it separate also
settles the license and ownership questions.

## Phases

1. MVP. Kernel-mode tailscaled only, with `kernelEnv` behind the `netEnv` interface.
   `icmp`, `tcp`, `http`, and `tsping` checks, the presence watcher, learned
   dependencies, and the state machine. Notifications through the generic signed webhook
   (single and batched) and Slack incoming webhooks, memory and disk queues, open-incident
   tracking, heartbeat, `notify-test`, and `/metrics`.
2. `tsnetEnv` for hosts without a daemon or root (no `icmp` checks), TLS expiry, Slack
   bot-token mode with threaded recoveries, PagerDuty (Events API v2, `dedup_key` set to
   `target/check` so recoveries auto-resolve), tag discovery, and `degraded` path
   warnings with latency baselines.
3. Multi-collector quorum with shared state, devices-API discovery, an SNMP `sysUpTime`
   check for devices that block ICMP.

## Testing

- Unit tests for the state machine and classifier using a fake clock (`tstest.Clock`).
  `prober`'s own fake ticker is unexported, so the alert logic should keep its timing
  separate from `prober`'s scheduling.
- Table tests for `classify` over synthetic `PingResult`s and presence maps. Include
  direct, DERP, and peer relay paths, a router that is offline, and a missing `NodeIP`.
- Integration in `tstest/natlab/vnet`. Two sites behind NAT, a subnet router with a fake
  LAN device, and a collector. Break things in order: stop the device, block the port,
  kill the router, force DERP by blocking UDP. Assert on the layer and which alerts were
  suppressed.
- Sink tests against `httptest.Server` receivers:
  - The signature verifies with the documented algorithm, and fails with the wrong
    secret or a stale `t`.
  - `500`, `429` with `Retry-After`, and timeouts retry. `400` doesn't.
  - Events expire at `max_age`. A full queue drops the oldest event.
  - Per-sink ordering holds: `down` is always delivered before its `recovered`.
  - A recovery reaches every sink that got the down, even when filters would drop it.
- Batched mode: 250 events inside one window become batches of 100, 100, and 50, in
  order. A retried batch has identical body bytes and `batch_id`, with a fresh signature.
- Disk queue crash points, run with a fake sink:
  - Die after the temp write, before the rename. The event is gone and the temp file is
    cleaned up.
  - Die after the `2xx`, before the delete. The event is redelivered with the same `id`.
  - Read-only `state_dir`. Events fall back to memory and still deliver.
  - Files older than `max_age` at startup are expired, not sent.
- Open incidents: restart while a target is down, then bring it back. Exactly one
  `target.recovered` goes out, with the original `down_event_id`, and no second
  `target.down`. Remove a down target from config and restart. It is closed with
  `"reason": "removed"`.
- Golden-file tests for the JSON payload and the Slack Block Kit output, so a change to
  the public contract shows up as a diff in review.
- Slack grouping under `tstest.Clock`: 25 events inside `group_wait` become one message
  with 10 lines and "and 15 more".
- Manual check on a real tailnet. Unplug a switch behind a subnet router and confirm one
  `lan` page. Stop tailscaled on the router and confirm one `control` page, with the
  dependents suppressed.

## Open questions

- Standalone repo first, or propose `cmd/tsmon` upstream from the start?
- Is YAML acceptable, or should targets also come from node attributes or tags in the
  policy file so admins manage them in one place?
- Should quorum stay in Alertmanager permanently? That keeps collectors stateless.
- For phase 2 tsnet collectors, should a config with `icmp` checks be a startup error, or
  should those checks be skipped with a warning and an `unsupported` metric?
