# peer-presence Specification

## Purpose

Keeps tail-watch's live view of which tailnet peers exist, whether control reports
them online, and which addresses and routes they handle, as input to failure
classification and dependency learning.

## Requirements

### Requirement: Presence source

The collector SHALL build its peer view from the local tailscaled IPN bus,
seeded from the initial status snapshot and then updated incrementally. It
SHALL key peers by their node ID. For each peer it SHALL track online state,
Tailscale IPs, primary routes, allowed IPs, tags, host name and MagicDNS name.

#### Scenario: Seed from initial status

- **WHEN** the bus connection opens and the first message carries a status
  with peers `nyc-router` (online) and `sfo-router` (offline)
- **THEN** presence reports `nyc-router` online and `sfo-router` offline, both
  as known

#### Scenario: Online flip patch

- **WHEN** a later message carries a patch setting `nyc-router`'s online state
  to false
- **THEN** presence reports `nyc-router` offline without changing its other
  fields

#### Scenario: Full node replacement

- **WHEN** a later message carries a full node entry for `nyc-router` with new
  primary routes
- **THEN** presence replaces that peer's entry, including routes and online
  state

#### Scenario: Peer removed

- **WHEN** a later message lists `sfo-router`'s node ID as removed
- **THEN** presence no longer knows `sfo-router`

### Requirement: Unknown is not offline

Presence SHALL distinguish "known offline" from "unknown". A peer SHALL be
unknown when it is not in the view, when its online state was never reported,
or while the bus connection is down or not yet seeded. Consumers SHALL be able
to ask for a peer's online state and receive both the state and whether it is
known.

#### Scenario: Bus disconnected

- **WHEN** the bus connection drops
- **THEN** every peer reads as unknown until the next initial status seeds the
  view again

#### Scenario: Node without online state

- **WHEN** a full node entry arrives with no online field
- **THEN** that peer's online state reads as unknown

### Requirement: Reconnect with backoff

When the bus connection fails or tailscaled leaves the `Running` state, the
collector SHALL keep retrying with exponential backoff capped at 30 seconds,
log each disconnect once, and reseed from a fresh initial status on reconnect.
It SHALL NOT exit because of a bus failure.

#### Scenario: tailscaled restart

- **WHEN** tailscaled restarts while tail-watch is running
- **THEN** tail-watch logs the disconnect, retries, and presence becomes known again
  after tailscaled is back in `Running`

### Requirement: Address and route lookup

Presence SHALL answer, for an IP address, which peer owns it as a Tailscale IP.
Failing that, it SHALL answer which peer is primary for the longest matching
route. It SHALL answer, for a name, which peer has that host name or MagicDNS
name.

#### Scenario: Subnet-routed address

- **WHEN** `nyc-router` is primary for `10.20.0.0/16` and a lookup asks for
  `10.20.0.50`
- **THEN** presence returns `nyc-router`

#### Scenario: Longest prefix wins

- **WHEN** `nyc-router` is primary for `10.20.0.0/16`, `nyc-lab` is primary for
  `10.20.5.0/24`, and a lookup asks for `10.20.5.9`
- **THEN** presence returns `nyc-lab`

#### Scenario: Unhandled address

- **WHEN** no peer owns or routes `192.168.99.1`
- **THEN** the lookup reports no match

### Requirement: Tags for a target

The collector SHALL report, for any target name, the ACL tags that apply to
it. For a Tailscale-node target, these are the tags of the node that is the
target. For a remote LAN target, they are the tags of its actual handling node,
independent of an explicit suppression parent. Implicit targets are Tailscale-node targets. Local targets SHALL have no inherited tailnet tags. The result
SHALL be empty when the relevant node is unknown to presence or has no tags.

#### Scenario: Tailscale-node target

- **WHEN** target `build-1` addresses a node tagged `tag:server`
- **THEN** its tags are `tag:server`

#### Scenario: LAN target inherits router tags

- **WHEN** `nyc-nas` is handled by `nyc-router`, which is tagged `tag:router`
- **THEN** `nyc-nas`'s tags are `tag:router`

#### Scenario: Unknown handling node

- **WHEN** `lab-pdu` has no known handling node
- **THEN** its tags are empty

### Requirement: Presence never alerts

Presence changes SHALL NOT by themselves produce alerts, events or failure
results. They SHALL only feed classification and dependency learning.

#### Scenario: Router goes offline with no failing checks

- **WHEN** presence marks `nyc-router` offline but every check still passes
- **THEN** no failure layer is recorded and no alert input is produced
