# collector-runtime Specification

## Purpose

Defines how the tail-watch process starts next to a kernel-mode tailscaled, what it
verifies before polling, which HTTP endpoints it serves, and how it runs in
one-shot mode and shuts down.

## Requirements

### Requirement: LocalAPI connection

tail-watch SHALL connect to the local tailscaled over its LocalAPI socket, using the
platform default path unless the `--socket` flag names another. It SHALL NOT
embed its own Tailscale node. When `collector.local_only` is `true`, tail-watch SHALL
NOT connect to tailscaled at all and SHALL ignore `--socket`.

#### Scenario: Custom socket

- **WHEN** tail-watch runs with `--socket /run/ts/tailscaled.sock`
- **THEN** all LocalAPI calls use that socket

#### Scenario: Local-only makes no LocalAPI call

- **WHEN** `collector.local_only: true` is set and no tailscaled runs on the host
- **THEN** tail-watch makes no LocalAPI call and runs its checks

### Requirement: Startup preflight

Before scheduling checks, tail-watch SHALL confirm that tailscaled is reachable and
its backend state is `Running`, and SHALL exit non-zero with an actionable
error otherwise. It SHALL log the collector's hostname, Tailscale IPs and tags.
It SHALL log a warning when the node lacks the `tag:monitor` tag, and a warning
when the host uses an exit node, because alerts would then depend on that exit
node. For `network: tailnet` targets behind subnet routers on Linux, preflight
SHALL verify route acceptance is enabled and report an actionable error naming
`tailscale set --accept-routes` otherwise. It SHALL reject userspace-networking
tailscaled for this kernel-mode runtime. A route advertised in presence SHALL
NOT be treated as proof of an installed host route. Deployment documentation
SHALL require checking the effective route for each remote subnet and selecting
`network: local` explicitly for locally monitored LANs.

When `collector.local_only` is `true`, preflight SHALL skip every tailscaled check
above, SHALL log the hostname and that it runs in local-only mode, and SHALL use the
hostname as the collector identity, so notifications carry `collector.name` and omit
`collector.tailscale_ip`.

#### Scenario: Subnet routes not accepted

- **WHEN** a Linux collector has a remote subnet target but does not accept routes
- **THEN** startup fails with the route-acceptance instruction before checks run

#### Scenario: tailscaled not running

- **WHEN** the LocalAPI socket does not exist or refuses connections
- **THEN** tail-watch exits non-zero with an error saying tailscaled is not
  reachable at the socket path

#### Scenario: Logged out

- **WHEN** tailscaled reports backend state `NeedsLogin`
- **THEN** tail-watch exits non-zero with an error telling the operator to run
  `tailscale up`

#### Scenario: Exit node in use

- **WHEN** the host has an active exit node
- **THEN** tail-watch starts and logs a warning that notifications will route
  through the exit node

#### Scenario: Local-only startup without tailscaled

- **WHEN** `collector.local_only: true` is set and the LocalAPI socket does not exist
- **THEN** tail-watch starts, logs its hostname and local-only mode, and schedules
  its checks

#### Scenario: Local-only collector identity

- **WHEN** a local-only collector on host `pg-ingress-0` sends `collector.started`
- **THEN** the payload has `collector.name` `pg-ingress-0` and no
  `collector.tailscale_ip`

### Requirement: HTTP endpoints

tail-watch SHALL serve on the configured listen address: `/` as the prober status
page listing every check with its latest result, `/metrics` in Prometheus text
format, and `/healthz` returning `200 ok` while the process is serving. When the UI
is on, it SHALL also serve `/ui/` and `/api/v1/` as defined by the monitor-ui and
monitor-management capabilities. When the UI is off, `/ui/` and `/api/v1/` SHALL return 404.

#### Scenario: Status page

- **WHEN** an operator opens `/` on the listen address
- **THEN** the page lists every target/check pair with its recent result and
  latency

#### Scenario: Health endpoint

- **WHEN** a client requests `/healthz`
- **THEN** the response is status 200 with body `ok`

#### Scenario: UI endpoints only when on

- **WHEN** the collector runs without the UI and a client requests `/api/v1/targets`
- **THEN** the response is 404

### Requirement: Probe metrics

tail-watch SHALL export the standard prober series (result, latency, attempts) in
the `tailwatch` Prometheus namespace, one series per target/check pair, labeled
`target`, `check` and `site` in addition to the prober's `name` and `class`
labels.

#### Scenario: Metric labels

- **WHEN** target `nyc-nas` in site `nyc` has a `tcp:445` check
- **THEN** its series carry `target="nyc-nas"`, `check="tcp:445"`, `site="nyc"`
  and `class="tcp"`

### Requirement: One-shot mode

With `--once`, tail-watch SHALL run every configured check exactly once, print one
line per check with its result, skip the HTTP server, and exit 0 only when every
check passed.

#### Scenario: One failure

- **WHEN** tail-watch runs with `--once` and one of five checks fails
- **THEN** it prints five result lines and exits 1

### Requirement: Graceful shutdown

On SIGINT or SIGTERM, tail-watch SHALL stop scheduling new checks, cancel running
checks, shut down the HTTP server, and exit 0.

#### Scenario: SIGTERM

- **WHEN** tail-watch receives SIGTERM while checks are running
- **THEN** it exits 0 within the longest configured check timeout plus 5s

### Requirement: Version and logging

tail-watch SHALL print its version and exit on `--version`. It SHALL log as
structured JSON to stderr at the level set by `--log-level` or `TAIL_WATCH_LOG_LEVEL`
(default `info`).

#### Scenario: Debug logging

- **WHEN** `TAIL_WATCH_LOG_LEVEL=debug` is set
- **THEN** every check result is logged at debug level

### Requirement: Runtime registration of configured targets

A configured target registered after startup, such as a dynamic monitor, SHALL be
registered with alerting, check scheduling, the notifier and failure classification,
and its `parent` SHALL join the dependency graph that the classifier publishes. Removing
it SHALL remove it from all of them and from that graph.

#### Scenario: Dynamic target is classified

- **WHEN** a dynamic target's check fails
- **THEN** the failure is classified into a layer as for a YAML target

#### Scenario: Removal clears the graph

- **WHEN** a dynamic target that has a parent is removed
- **THEN** the classifier's parent graph no longer has an edge for it and its
  `tailwatch_target_parent` series is dropped
