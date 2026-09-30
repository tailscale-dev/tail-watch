# Spec Delta

## ADDED Requirements

### Requirement: Peer list

When the UI is on, the collector SHALL serve `GET /api/v1/peers`, returning the tailnet
peers the collector can currently see, so a client can offer them as monitor addresses.
The response SHALL be JSON with `seeded`, a boolean, and `peers`, a list sorted by
`name`. Each peer SHALL carry `name` (the MagicDNS short name, or the host name when
there is no MagicDNS name), `dns_name`, `host_name`, `ips` (its Tailscale IPs), `tags`,
`online` (`true`, `false`, or `null` when the state is unknown), and `routes` (the subnet
routes for which the peer is primary). The list SHALL NOT include the collector's own
node. It SHALL contain only peers the collector's own view holds, so a device the tailnet
policy hides from the collector SHALL NOT appear. While the view is not seeded, or while
the connection to `tailscaled` is down, the response SHALL be 200 with `seeded: false` and
an empty `peers` list. The endpoint SHALL read no credential, SHALL NOT change any
monitor, and SHALL NOT require a JSON request body or an audit log entry. When the UI is
off, `/api/v1/peers` SHALL return 404 like the rest of `/api/v1/`.

#### Scenario: List visible peers

- **WHEN** the collector sees `nyc-router` (tagged `tag:router`, primary for
  `10.20.0.0/16`, online) and `lab-nas` (offline)
- **THEN** `GET /api/v1/peers` returns 200 with `seeded: true` and both peers, sorted by
  name, `nyc-router` with `routes: ["10.20.0.0/16"]` and `online: true`, `lab-nas` with
  `online: false`

#### Scenario: Unknown online state

- **WHEN** a peer's online state has not been reported
- **THEN** its entry has `online: null`

#### Scenario: Not yet seeded

- **WHEN** `GET /api/v1/peers` is requested before the first status from `tailscaled`
  has arrived
- **THEN** the response is 200 with `seeded: false` and `peers: []`

#### Scenario: Bus connection down

- **WHEN** the connection to `tailscaled` drops after the view was seeded
- **THEN** `GET /api/v1/peers` returns 200 with `seeded: false` and `peers: []` until the
  view is seeded again

#### Scenario: Peer with no MagicDNS name

- **WHEN** a peer has no MagicDNS name and host name `printer`
- **THEN** its `name` is `printer` and its `dns_name` is empty

#### Scenario: Hidden device

- **WHEN** the tailnet policy gives `tag:monitor` no access to a device
- **THEN** that device is not in the response

#### Scenario: UI off

- **WHEN** the collector runs without the UI
- **THEN** `GET /api/v1/peers` returns 404
