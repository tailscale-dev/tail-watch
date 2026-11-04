# Proposal

## Why

`tls:` is rejected at load time, so an operator cannot alert when a handshake fails
or a certificate is about to expire. Tailscale Serve and Tailscale Services present
publicly trusted certificates for `*.ts.net` names, and those certificates renew only
while the node keeps provisioning them. An HTTPS check proves the URL answers. It does
not say that the certificate itself is close to expiry, and it fails name verification
when the target address is a Tailscale IP or a short name rather than the name on the
certificate.

This delivers the ROADMAP.md near-term item "TLS expiry check (`tls:` is rejected
today). Also covers the `*.ts.net` certificates that Tailscale Services and Serve
use" [M].

## What Changes

- Accept `tls:<port>` on a target. The check dials `addr` on that port, completes a
  TLS handshake, and verifies the certificate with the system roots.
- Fail the check when the leaf certificate expires within 7 days, or is not yet
  valid, even though a client would still accept it today. An already-expired or
  untrusted certificate fails the handshake.
- When the address matches a peer in the collector's tailnet status, verify the
  certificate against that peer's MagicDNS name. That is the name on a Serve
  certificate. A Service or Serve endpoint addressed by its full `*.ts.net` name is
  verified as that name.
- `insecure_tls: true` skips chain and hostname verification and still enforces the
  leaf dates, so a device with a private certificate can be watched for expiry.
- The monitor UI gains a TLS row (a port, like TCP) that reads and writes
  `tls:<port>`. `tls:443` is no longer the example of a Custom row the server rejects.
- A TLS failure, including "expires within 7 days", is a device answer. The
  classifier keeps the existing `service` layer when the tunnel is up.
- The check follows the target's severity and thresholds. There is no new event type
  and no new metric.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `collector-config`: `tls:<port>` is valid check syntax. The phase-2 rejection of
  `tls:` is removed.
- `reachability-checks`: a TLS check proves the handshake and that the leaf
  certificate is outside the 7-day expiry window, including a `*.ts.net` name.
- `failure-classification`: a TLS expiry or verification failure counts as the
  device having answered.
- `monitor-ui`: the check builder has a TLS port row, and a still-unparseable string
  other than `tls:<port>` stays a Custom row.

## Impact

- `cmd/tail-watch/checks.go` and `checks_test.go` gain the check. `classify.go`
  treats the expiry error as a TLS error. The UI check conversion in
  `cmd/tail-watch/ui/app.js`, its node test, and the browser test follow.
- `docs/reference/checks.md` replaces the "rejected" section. `config.md` only if a
  key changes. No config key is added.
- Same `parseCheck` path as the config file, `POST /api/v1/targets`, and
  `POST /api/v1/probe`. No new dependency. The dial goes through `netEnv`, not
  `prober.TLS` (that helper uses the process dialer and fetches a CRL).

## Non-Goals

- PagerDuty sink, incident.io sink, `target.degraded`, `tsnetEnv`, tag discovery
  (`discover:`), and Slack bot-token mode.
- A configurable expiry window, a certificate-lifetime metric, CRL or OCSP fetches,
  or a new alert severity for "expires soon".
- Checking a chain certificate's remaining life. Standard verification still rejects
  a chain that is already expired or untrusted.
- Resolving a Tailscale Service that is not a peer and is not addressed by its
  `*.ts.net` name. The operator sets `addr` to that name.
