# Spec Delta

## ADDED Requirements

### Requirement: TLS expiry check

A `tls:<port>` check SHALL dial the target's `addr` on that port through the
collector's network path, complete a TLS handshake, and send no application
data. It SHALL verify the certificate chain with the system roots unless the
target sets `insecure_tls: true`. The name it verifies SHALL be chosen as
follows:

- When `addr` matches a peer in tailnet status, by Tailscale IP, full MagicDNS
  name, short MagicDNS name, or hostname, and that peer has a DNS name, the
  verified name SHALL be that DNS name with one trailing dot removed. A peer
  that only routes `addr` as a subnet SHALL NOT match.
- Otherwise, when `addr` is a DNS name, the verified name SHALL be `addr` with
  one trailing dot removed.
- Otherwise the verified name SHALL be the IP address.

When tailnet status cannot be read, including a `collector.local_only`
collector, matching a peer SHALL be skipped and the name rules above still
apply. A status failure SHALL NOT by itself fail the check.

The check SHALL pass only when the handshake and verification succeed and the
leaf certificate's `NotAfter` is at least 7 times 24 hours after the current
time. It SHALL fail when the leaf's `NotBefore` is after the current time.
With `insecure_tls: true` the check SHALL skip chain and hostname verification
and SHALL still enforce those leaf dates, and SHALL fail when the server
presents no certificate. The check SHALL NOT fetch a CRL or an OCSP response.

On expiry the error SHALL contain the text `certificate expires` and the leaf
`NotAfter` in RFC3339, and SHALL name the leaf by its first DNS SAN, or by its
subject when it has no DNS SAN. On a not-yet-valid leaf the error SHALL contain
the text `certificate is not valid before` and the leaf `NotBefore` in RFC3339.
The result's check name SHALL be the `tls:<port>` string. Latency SHALL be the
time to finish the handshake.

#### Scenario: Certificate valid for longer than 7 days

- **WHEN** the handshake succeeds and the leaf `NotAfter` is 8 days after the
  current time
- **THEN** the check passes and records the handshake latency

#### Scenario: Leaf expires in less than 7 days

- **WHEN** the handshake succeeds and the leaf `NotAfter` is 6 days after the
  current time
- **THEN** the check fails with an error containing `certificate expires` and
  that `NotAfter` time in RFC3339

#### Scenario: Leaf already expired

- **WHEN** the leaf `NotAfter` is before the current time and `insecure_tls` is
  unset
- **THEN** the check fails

#### Scenario: Serve certificate on a Tailscale IP

- **WHEN** `addr` is `100.64.0.8`, tailnet status shows that IP on a peer whose
  DNS name is `nas.tailnet.ts.net.`, and the server presents a chain trusted by
  the system roots whose leaf is valid for `nas.tailnet.ts.net` with `NotAfter`
  30 days away
- **THEN** the check passes

#### Scenario: Service certificate addressed by its ts.net name

- **WHEN** `addr` is `web.tailnet.ts.net`, that name matches no peer, and the
  server presents a chain trusted by the system roots whose leaf is valid for
  `web.tailnet.ts.net` with `NotAfter` 30 days away
- **THEN** the check passes

#### Scenario: Subnet route does not supply the certificate name

- **WHEN** `addr` is `10.20.0.50`, the only peer relationship is a subnet route
  advertised by a peer named `nyc-router.tailnet.ts.net`, and the certificate
  is valid only for `nyc-router.tailnet.ts.net`
- **THEN** the check fails name verification

#### Scenario: Short name with no peer keeps the short name

- **WHEN** `addr` is `nas`, tailnet status has no matching peer, and the
  certificate is valid only for `nas.tailnet.ts.net`
- **THEN** the check fails name verification

#### Scenario: Self-signed leaf watched for expiry

- **WHEN** the target sets `insecure_tls: true` and the server presents a
  self-signed leaf whose `NotAfter` is 30 days away
- **THEN** the check passes

#### Scenario: Self-signed leaf inside the window

- **WHEN** the target sets `insecure_tls: true` and the self-signed leaf's
  `NotAfter` is 2 days away
- **THEN** the check fails with an error containing `certificate expires`

#### Scenario: Status unavailable still dials a DNS name

- **WHEN** the collector is `collector.local_only`, `addr` is
  `web.example.ts.net`, and the leaf is valid for that name with `NotAfter`
  30 days away
- **THEN** the check passes without reading tailnet status
