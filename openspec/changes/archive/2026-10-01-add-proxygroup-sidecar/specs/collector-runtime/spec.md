# Spec Delta

## MODIFIED Requirements

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
