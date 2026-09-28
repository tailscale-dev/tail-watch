# Spec Delta

## Purpose

Defines what each tail-watch check type proves about a target, how it reaches the
target through the tailnet, and what it records for later classification and
alerting.

## ADDED Requirements

### Requirement: Checks follow the declared network path

Every check SHALL use host sockets without binding to a physical interface.
A target's `network` SHALL be `tailnet` (default) or `local`. `tailnet` means
that the operator intends IP checks to use Tailscale; `local` means the host's
ordinary non-Tailscale route, including a directly connected LAN. Local targets
SHALL NOT receive learned tailnet parents or supplemental disco probes, and
SHALL reject `tsping` checks. An explicit reachability `parent` is still allowed.
The HTTP URL SHALL address the target's `addr` (allowing an equivalent DNS name);
otherwise it SHALL be a separate target with its own network and dependency.

#### Scenario: Subnet-routed device

- **WHEN** a `network: tailnet` target is in an approved subnet and the Linux
  collector accepts subnet routes
- **THEN** its IP checks use the installed Tailscale route

#### Scenario: Site-local monitoring

- **WHEN** a `network: local` device remains reachable on the collector's LAN
  while the site's Tailscale router is down
- **THEN** checks keep passing and no learned router dependency suppresses it

#### Scenario: Disco check on a local target

- **WHEN** a `network: local` target lists `tsping`
- **THEN** config validation fails naming the target and incompatible check

### Requirement: ICMP check

An `icmp` check SHALL send one ICMP echo request to the target's address (IPv4
or IPv6) and pass when a matching echo reply arrives before the timeout. It
SHALL use an unprivileged ICMP socket when the host allows one and otherwise a
raw ICMP socket. When neither can be opened, the check SHALL fail with an error
that names `net.ipv4.ping_group_range` and `CAP_NET_RAW`.

#### Scenario: Host answers

- **WHEN** the target replies to echo within the timeout
- **THEN** the check passes and records the round-trip time as its latency

#### Scenario: No reply

- **WHEN** no reply arrives before the timeout
- **THEN** the check fails with a timeout error

#### Scenario: No ICMP permission

- **WHEN** the process can open neither an unprivileged nor a raw ICMP socket
- **THEN** the check fails with an error telling the operator to set
  `net.ipv4.ping_group_range` or grant `CAP_NET_RAW`

### Requirement: TCP check

A `tcp:<port>` check SHALL pass when a TCP connection to the target's address
on that port is established before the timeout, and SHALL close the connection
immediately without sending data.

#### Scenario: Port closed

- **WHEN** the target refuses the connection
- **THEN** the check fails with an error containing "connection refused"

### Requirement: HTTP check

An `http:<url>` check SHALL send a GET request and pass when the response
status is 2xx or 3xx and, when a body match text is configured, the first
1 MiB of the body contains that text. It SHALL NOT follow redirects. It SHALL
open a new connection for every run. It SHALL verify TLS certificates unless the
target sets `insecure_tls: true`.

#### Scenario: Redirect counts as up

- **WHEN** the URL answers `302 Found`
- **THEN** the check passes without requesting the redirect location

#### Scenario: Body text missing

- **WHEN** the response is `200` but the body lacks the configured text
- **THEN** the check fails with an error quoting the wanted text and the first
  300 bytes of the body

#### Scenario: Self-signed device certificate

- **WHEN** the device presents a self-signed certificate and the target sets
  `insecure_tls: true`
- **THEN** the check passes if the status and body match

### Requirement: Tailscale ping check

A `tsping` check SHALL resolve the target's address to a Tailscale or
subnet-routed IP and send a disco ping through the local tailscaled. It SHALL
pass when tailscaled reports a pong before the timeout. It SHALL record the
Tailscale IP of the node that handled the address, the path kind (`direct`,
`derp` or `peer_relay`), the endpoint or DERP region, and the latency.

#### Scenario: Direct path

- **WHEN** the pong arrives over a direct UDP path
- **THEN** the check passes and records path kind `direct` with the endpoint

#### Scenario: Subnet-routed address

- **WHEN** a `tsping` targets a LAN address routed by `nyc-router`
- **THEN** the recorded handling node is `nyc-router`'s Tailscale IP

#### Scenario: No peer handles the address

- **WHEN** no peer in the collector's netmap routes the target's address
- **THEN** the check fails with an error saying no peer handles that address
  and to check grants to `tag:monitor`

### Requirement: Check timeouts and scheduling

Each check SHALL run on its target's interval and be cancelled at its target's
timeout. The collector SHALL spread each check's first run across its interval
so that checks do not start in a burst. A check that times out SHALL count as a
failure.

#### Scenario: Slow target

- **WHEN** a target's timeout is 5s and a TCP connect takes 8s
- **THEN** the check is cancelled at 5s and recorded as a failure

### Requirement: Check result record

Every check run SHALL produce a result containing the target name, the check
name (for example `icmp` or `tcp:445`), the start time, the latency, the error
if any, and for `tsping` the path details above. Results SHALL be delivered to
the collector's result consumers in the order checks complete for that target.

#### Scenario: Result consumers see failures

- **WHEN** a `tcp:445` check fails
- **THEN** a result with target, check `tcp:445` and the dial error is
  delivered to every registered result consumer
