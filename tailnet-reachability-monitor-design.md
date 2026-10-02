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

```text
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
  outward whenever they have a path. Mark these targets `network: local` so
  they do not inherit a Tailscale router dependency.
- Don't run the collector on the subnet router itself. A router-hosted collector reaches
  the LAN directly and never crosses the tunnel, so it can't see `tunnel`-layer failures,
  and it goes down with the thing it is supposed to report on.
- In Kubernetes, break that rule deliberately for the operator's ProxyGroup proxy pods:
  run the collector as a sidecar in the proxy pod. Its subject is hop 2, the path from
  the proxy pod to the workload, which is only visible from the proxy's network
  namespace. The don't-run-on-the-device rule is about tunnel visibility, and hop-2
  checks never cross the tunnel, so running on the proxy costs nothing there. Hop 1
  (tailnet client to proxy) stays with a regular tailnet collector probing the Service's
  tailnet name. See `docs/how-to/kubernetes-proxygroup.md`.
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

Checks use ordinary host sockets. `network: tailnet` (the default) requires
kernel-mode tailscaled and installed routes; Linux collectors monitoring remote
subnets must enable `tailscale set --accept-routes`. Preflight checks route
acceptance, and deployment must verify effective routes. An advertised route alone
is not proof that host sockets use it. Use `network: local` for directly monitored
LAN devices; these targets do not learn tailnet dependencies or use disco probes.
The `prober` check
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
| `tunnel` | Handling node is online in control but `tsping` fails | Page, "no WireGuard path to router". Usually NAT, firewall, or a stuck magicsock |
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

For `network: tailnet` LAN targets, learning runs at startup and every ten minutes,
and reconciles when primary routes change or presence reseeds. It records the
handling node's stable identity and uses its Tailscale IP for reachability probes.
A failed learning ping retains the previous handler unless a current primary-route
change supersedes it. Local targets have no learned tailnet parent.

A learned parent reuses a configured target only if its checks are exactly
`[tsping]` and it addresses that node. Otherwise create a separate implicit
reachability target, even when a service target already covers the router. Service
failures on a router must not suppress its LAN. Explicit `parent:` overrides may
name a target with exactly one `icmp` or `tsping` check, such as a core switch.
Keep physical dependencies separate from the Tailscale handling-node lookup.

Validate the combined configured and learned graph before every update. Reject a
cycle and retain the old graph. Apply accepted updates atomically to alerting:
children moved from a down router to a healthy one are released immediately,
without waiting for the old router to recover.

First-pass completion means attempts finished, not that discovery succeeded.
Continuously seeded presence and resolved handlers for all remote LAN targets
make a reconciliation authoritative for removing unresolved implicit incidents.
If no remote LAN targets are configured, config alone proves no learned router
is required.
Persist stable node identity and target definition for open learned incidents;
recreate those targets before checks start and retain them through failed discovery.

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
  recovers, or a topology update removes every down ancestor, enqueue dependent
  rechecks within one second. Eight workers run them with per-check timeouts; all
  checks need not start within one second. An already-DOWN child stays DOWN and
  retains its own incident. Release resets counters and excludes pre-release results.
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
| `target.down` | `UP`, `UNKNOWN` or released `SUPPRESSED` to `DOWN` | `page`, or the target's `severity` override |
| `target.recovered` | `DOWN -> UP` | Same as the `target.down` it resolves |
| `target.flapping` | Target crossed `flap_limit` | `warn` |
| `target.degraded` | Path fell back to DERP or peer relay, or latency spiked (phase 2) | `warn` |
| `collector.started` | Collector finished its first full round of checks | `info` |
| `test` | Sent by `tail-watch notify-test` | `info` |

Two rules keep the noise down:

- Suppressed targets do not emit events until a released target independently
  reaches its failure threshold. The parent's `target.down` lists them in
  `dependents`, and the parent's `target.recovered` lists them too.
- Recoveries bypass changed filters for eligible sinks selected for the matching
  down. They wait behind pending downs. A never-attempted down dropped from the queue
  discards its recovery; an attempted down keeps its recovery eligible even if no
  response was received. Receivers must tolerate unknown resolve IDs, because a
  request may have been accepted before its response was lost.

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
  `User-Agent: tail-watch/<version>`. By default, one event per request. Dependency
  suppression already collapses most alert storms, and many simple receivers (n8n,
  Zapier, Lambda URLs) expect a single flat object. Rate-limited receivers can use
  batched mode, described below.
- Headers: `Tail-Watch-Event-Id: <id>` and
  `Tail-Watch-Signature: t=<unix>,v1=<hex>`, where `v1`
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
- `Tail-Watch-Signature` covers the whole body. `Tail-Watch-Batch-Id` replaces `Tail-Watch-Event-Id`.
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

  ```text
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

Transitions get sequence numbers and enter a bounded shared FIFO under the alert
state lock. One drainer invokes consumers outside the lock. Notification intake is
also bounded and non-blocking. Overflow counts a drop and retains a dirty target
snapshot with its latest notification-visible state and sequence, respecting flap
damping. The notifier reconciles dirty snapshots once capacity returns, even if no
new transitions arrive. A stable UP with an open incident gets a recovery intent;
a stable DOWN without an incident gets a down. Intermediate unreported blips may
coalesce. Acknowledgment of a snapshot never clears a newer dirty generation.

Each sink has a network worker and a bounded queue (`queue_size`, default 1000).
A slow receiver does not delay another worker. One coordinator serializes incident
mutations, queue intents and persistence; a slow disk can delay all durable enqueues
but not checks. Overflow drops the oldest queued event with a counter and updates
its incident delivery state. Expiry and permanent failures are explicit delivery
limits, so resolution is not unconditional.

Each incident records per-sink pending, attempted, acknowledged and dropped state,
retaining whether any request was attempted. Mark durable events attempted before
sending, then record acknowledgments through the coordinator. Recoveries wait for
their down to succeed or become terminal; only sinks eligible under the rules above
receive them. An unattempted down and recovery may share one ordered batch, committing
their attempt states together. Events keep their original `time`; those first attempted
over one minute later carry `late: true`, fixed with their body for subsequent retries.
URLs, secrets and headers come from `*_env` or `*_file` keys and are never persisted.

### Queue modes and atomic persistence

Each sink chooses `queue: memory` (default) or `queue: disk`. Disk mode requires
`collector.state_dir`. Memory pending payloads are lost on restart and marked dropped;
retain whether their down was attempted when deciding recovery eligibility. When
`state_dir` is configured, one versioned file, `notification-state.json`, atomically stores:

- Open and closing incidents, target definitions and original down IDs.
- Per-sink delivery state and monotonically allocated queue sequences.
- All pending disk-sink event intents, plus active batch IDs and first-attempt bodies.

Write a same-directory temporary file, fsync it, rename it, and fsync the directory.
An incident mutation and all corresponding durable event intents commit together;
only after commit may workers see new disk-backed events. Separate `incidents.json`
and per-event queue files are superseded. Directories use mode 0700; files use 0600.

A recovery moves an incident to closing and creates durable intents in one commit.
Retain the closing record until its deliveries are acknowledged or explicitly
dropped. A new outage can open a new incident while old recovery intents retry.
Worker results update the same snapshot. A crash after commit replays intents with
the same IDs, including retries after a receiver accepted a request. This is
at-least-once delivery, subject to expiry, overflow and permanent failures.

On startup, replay in per-sink order and apply limits together with delivery-state
updates. Discard abandoned temporary files. Corrupt committed state is an actionable
startup error; never silently forget incidents. A failed write retains all event and
incident mutations together in memory, logs durability degradation and increments
`tailwatch_notify_persist_errors_total{sink="state"}`. Retry the full current snapshot;
crash durability is unavailable until a commit succeeds.

Disk retries preserve batch IDs and body bytes across restarts. If any member of an
already-attempted batch expires, drop the whole batch and count each event, rather
than mutate its body. Whole-state snapshots trade write volume for a simple atomic
contract; bound queues and measure commit latency.

### Open incidents

Track incidents in memory even without persistence, for recovery routing. Persist
them with queue intents when `state_dir` is set. Each includes target identity and
definition, check, original down ID, `since`, severity and per-sink delivery state.
Learned targets also retain stable node identity and address.

Before checks start, recreate recorded implicit reachability targets and restore
open incidents as DOWN without another page. Closing incidents replay recovery
intents without restoring DOWN. Recovery retains its original down ID and start time.
Explicitly removed configured targets close with `reason: removed`. Unresolved
implicit incidents remain open until authoritative topology reconciliation confirms
removal; failed discovery alone never closes them. Removed sinks are skipped and
logged. Retain persisted incident metadata even when all sinks use memory queues.

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

`tail-watch notify-test [--sink NAME]` sends a `test` event through the real sink code, with
signing and retries. It prints the HTTP status for each sink. Run it after every config
change.

## Config

```yaml
collector:
  listen: 127.0.0.1:9109  # /metrics and prober status page
  state_dir: /var/lib/tail-watch  # optional; atomic incident and disk-delivery snapshot

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
    secret_env: OPS_WEBHOOK_SECRET      # HMAC key for Tail-Watch-Signature
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
    network: tailnet          # use local for a directly monitored site LAN
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

For remote tailnet targets, if an address never appears in the collector's netmap,
the fix is often a missing
grant. The collector should log that plainly at startup ("target X: no peer handles
10.20.0.50; check grants to tag:monitor"). It should not keep retrying quietly.

## Metrics

Everything goes through `prober.Prober` with `WithMetricNamespace("tailwatch")`. We get the
standard series for free (result, latency, attempts, and so on), labeled `name` and
`class`, with our own labels added:

- `target`, `check` (`icmp`, `tcp:445`, ...), `parent` (router hostname or empty),
  `site`.
- Custom gauge `tailwatch_target_state{target}`: 0=unknown, 1=up, 2=down, 3=suppressed,
  4=flapping.
- Custom gauge `tailwatch_path{target,kind}`: kind is `direct`, `derp`, or `peer_relay`,
  from `tsping` results.
- `tailwatch_failure_layer_total{target,layer}` counter.
- `tailwatch_notify_sent_total{sink,type}` and `tailwatch_notify_failed_total{sink,reason}`,
  where reason is `timeout`, `http_5xx`, `http_429`, or `http_4xx_permanent`.
- `tailwatch_notify_dropped_total{sink,reason}`: `queue_full`, `expired`, `permanent`,
  or `restart` for lost memory-sink pending payloads.
- `tailwatch_notify_persist_errors_total{sink}`: disk writes that failed and fell back to
  memory.
- `tailwatch_notify_batch_size{sink}` histogram, batched webhook sinks only.
- `tailwatch_open_incidents` gauge, from the atomic snapshot when `state_dir` is set.
- `tailwatch_notify_queue_len{sink}` gauge and
  `tailwatch_notify_latency_seconds{sink}`, time from
  event to confirmed delivery.

Alert on `tailwatch_notify_dropped_total` rising, or `tailwatch_notify_queue_len` staying above
zero, from Prometheus. That is how you find out alerts aren't getting through.

The prober status page (`Prober.StatusHandler`, as in `cmd/derpprobe`) gives a basic
dashboard with no extra work.

## Implementation sketch

Layout, if built in this repo:

```text
cmd/tail-watch/
  main.go          flags, config load, wiring
  netenv.go         netEnv interface + kernelEnv (phase 2 adds tsnetEnv.go)
  config.go         YAML schema and validation
  checks.go         check -> prober.FuncProbe over netEnv (icmp via net/ping, tsping via LocalClient)
  presence.go       IPN bus watcher, NodeID -> online map
  classify.go       failure layer + dependency learning
  alert.go          per-target state machine, flap damping
  notify.go         Event type, dispatcher, filters, heartbeat
  queue.go          memory and disk queues behind one interface
  incidents.go      incident lifecycle and atomic notification snapshot coordinator
  webhook.go        generic webhook sink, HMAC signing, retry policy, batched mode
  slack.go          Slack incoming-webhook sink, Block Kit rendering, grouping
  notifytest.go     `tail-watch notify-test` subcommand
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
p := prober.New().WithSpread(true).WithMetricNamespace("tailwatch")
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
- Atomic snapshot crash points with fake sinks: before rename keeps the old state;
  after commit but before worker distribution replays the new intent; after receiver
  `2xx` but before acknowledgment commit may replay the same event ID. Test both
  opening and closing incidents and multiple sinks, including the new-outage case
  while an old recovery is retrying. A corrupt snapshot fails startup.
- Failed writes keep incident and queue changes together in memory; a later commit
  restores durability. Expiry and overflow also commit per-sink eligibility changes.
- Recover while a down retries; drop a never-attempted down; lose an acknowledgment
  after receiver acceptance. Assert the documented recovery eligibility in each case.
- Overflow intake during recovery and leave the target UP. Reconciliation must resolve
  its open incident without waiting for another outage; newer dirty generations survive.
- Restart with an implicit incident while presence and learning fail. Do not emit a
  removed recovery. Later authoritative reconciliation can establish actual removal.
- Fail a router management service while its reachability target passes: LAN devices
  remain alertable. Move suppressed children to healthy HA router B while A stays down:
  release them and page any child that still fails. Reject a mixed-edge parent cycle.
- Enqueue more than eight slow rechecks, enforce FIFO concurrency and exclude old
  pre-release results. Verify concurrent producers cannot reorder DOWN and recovery.
- Local LAN checks survive loss of the Tailscale router without acquiring its parent.
  Remote Linux subnet checks fail preflight if subnet-route acceptance is disabled.

- Golden-file tests for the JSON payload and the Slack Block Kit output, so a change to
  the public contract shows up as a diff in review.
- Slack grouping under `tstest.Clock`: 25 events inside `group_wait` become one message
  with 10 lines and "and 15 more".
- Manual check on a real tailnet. Unplug a switch behind a subnet router and confirm one
  `lan` page. Stop tailscaled on the router and confirm one `control` page, with the
  dependents suppressed.

## Open questions

- Standalone repo first, or propose `cmd/tail-watch` upstream from the start?
- Is YAML acceptable, or should targets also come from node attributes or tags in the
  policy file so admins manage them in one place?
- Should quorum stay in Alertmanager permanently? That keeps collectors stateless.
- For phase 2 tsnet collectors, should a config with `icmp` checks be a startup error, or
  should those checks be skipped with a warning and an `unsupported` metric?
