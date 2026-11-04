# Design

## Context

See proposal.md for why. `parseCheck` in `cmd/tail-watch/checks.go` rejects every
`tls:` string. `http:` already verifies TLS unless `insecure_tls` is set, and
its comment points expiry at this check. The original design named
`prober.TLS` as the model: handshake, hostname match, and "not close to
expiry". On the pinned `tailscale.com` that helper also downloads a CRL with
the default HTTP client and skips date checks on self-signed certificates so
DERP metacerts stay quiet.

Checks dial through `netEnv`. `tsping` learns peers from `presence` once that
view is seeded, and otherwise from the 30s `Status()` cache on `tsResolver`.
`bindChecks` attaches that resolver. `collector.local_only` never builds one.
`classifyErr` already maps `x509` and `crypto/tls` verification errors to
`errTLS`, and `answeredByError` treats that class as the device having
answered. An expiry error raised after a successful handshake is not one of
those types today, so it would fall through to an ICMP probe.

The check rows in `cmd/tail-watch/ui/app.js` (`checkToString` /
`checkFromString`, between the check-conversion markers) have no TLS type.
`tls:443` is the stored string the Custom-row tests use.

## Goals / Non-Goals

**Goals:**

- Implement the `tls:<port>` behavior in the delta specs, through `netEnv`,
  with the leaf window and the peer DNS name as the verified name.
- Keep a soon-to-expire leaf on the existing `service` path.
- Give the check builder a TLS port row.

**Non-Goals:**

- Calling `prober.TLS` or fetching a CRL. See proposal.md, Non-Goals, for the
  product exclusions (no new event, metric, or expiry knob).

## Decisions

### Dial, then handshake on that connection

`tlsCheck.Run` dials `tcp` to `net.JoinHostPort(addr, port)` with `env.Dial`
and calls `tls.Client` on that connection. `tls.Dialer` is the wrong tool: it
uses the process dialer, which skips `netEnv` and would break `local_only` and
a future `tsnetEnv`.

No HTTP request is sent. Latency is the time from dial start through
`HandshakeContext`.

Alternative considered: wrap `prober.TLS`. Rejected because it dials on its
own and treats a missing Let's Encrypt CRL as a failure.

### Leaf dates only, after verification

Constant `tlsExpiryWindow` is `7 * 24 * time.Hour`, the same window as
`prober.TLS`, and it is not configurable.

The system roots (a nil `RootCAs`) verify the chain during the handshake, which
already rejects an expired, not-yet-valid, or untrusted chain. After a
successful handshake the check reads `PeerCertificates[0]` and fails when
`NotAfter` is before `now + tlsExpiryWindow`. Equality with the window passes:
the leaf must be at least 7 days out.

`insecure_tls` sets `InsecureSkipVerify`. The handshake then succeeds for a
private certificate, and the same leaf-date checks run, including `NotBefore`
after now. A self-signed leaf is checked. `prober.TLS` skips those certificates
because DERP returns them beside the real cert. tail-watch's targets are the
operator's devices, and a NAS leaf is often the certificate they want watched.

Intermediates are left to normal verification. A CA certificate that is still
trusted and inside 7 days does not fail this check.

The expiry error is a `certExpiryError` whose text contains `certificate
expires`, the leaf `NotAfter` in RFC3339, and the first DNS SAN or, if there is
none, the subject. A not-yet-valid leaf found under `insecure_tls` is a
separate error containing `certificate is not valid before`. `classifyErr`
returns `errTLS` for both, next to the existing `certErr` cases, so
`answeredByError` is true and no ICMP probe runs.

Alternative considered: fail the whole chain on the 7-day window, as
`prober.TLS` does. Rejected. An intermediate the system still trusts is the
CA's renewal, and it would page every tailnet that happens to see that
intermediate.

### Verified name from the peer that owns the address

`ServerName` is chosen in this order:

1. The resolver's presence view, when it is seeded. `ByName` for a DNS name,
   `ByIP` for an IP. Not `Lookup` and not `Route`: a subnet router must not
   become the name on a LAN device's certificate.
2. Otherwise, when the view is unseeded or has no match, the status cache
   `tsping` already uses. Names go through `matchPeer`. IPs are compared to
   each peer's `TailscaleIPs`, including `Self`, which the presence view does
   not hold. A fetch error is ignored and the check continues.
3. The peer's `DNSName` with one trailing dot removed. An empty DNS name does
   not match.
4. Otherwise a DNS `addr` with one trailing dot removed, or the IP itself.

`bindChecks` sets `tlsCheck.resolver` from the app resolver when one exists,
the same way it sets `tspingCheck.resolver`. `collector.local_only` leaves the
resolver nil, so step 1 and 2 are skipped. A Service or Serve endpoint that is
not a peer is checked by putting its `*.ts.net` name in `addr`. A short name
with no peer stays that short name and will fail a certificate issued only for
the FQDN. That is the documented form, not a second suffix-appending rule,
which would mis-name a LAN host called `nas`.

Tests set `tlsCheck.roots` to a fixture pool. Nil means the system pool, which
is what production uses. Tests do not replace `crypto/tls` or `x509`.

### Clock on the check

`tlsCheck.clock` is a `tstime.Clock`. `bindChecks` copies `app.clock` even when
there is no resolver, so `local_only` still has one. `Run` uses
`tstime.StdClock` when the field is nil. Expiry tests set a `tstest.Clock` and
build the leaf relative to it. The check interface stays
`Run(ctx, env, target)`. Adding a clock argument would touch every check for
one comparison.

### UI row

`CHECK_TYPE_LABELS` gains `tls` / `TLS certificate`, with the same port input
TCP uses. `tls:<port>` round-trips. Anything else, including `snmp:1` and
`tls:host:443`, stays Custom. The browser test that saved `tls:443` as a Custom
row and expected `tls checks are not supported` moves to `snmp:1`, and a TLS
row test covers port 443.

### Docs

`docs/reference/checks.md` replaces the rejected section and the summary row.
`docs/reference/config.md` currently says `insecure_tls` skips verification for
`http:` only; it must mention `tls:` too. `docs/how-to/monitor-ui.md` gains the
TLS row in the type table. `CLAUDE.md` gains one sentence: the check dials
through `netEnv`, verifies the peer DNS name rather than a subnet router's, and
fails when the leaf expires within 7 days. `CHANGELOG.md` gets an
`Unreleased` entry, which the release commit renames to the version
(`docs/contributing/releases.md`). The ROADMAP box is checked in the same change.

## Risks / Trade-offs

- [A leaf inside 7 days pages at the target's severity] → Operators who want a
  quiet warning set `severity: warn` or a route. `target.degraded` is a
  different roadmap item and is not a side door for this check.
- [A Service addressed by its short name fails name verification] → The checks
  reference tells the operator to use `name.tailnet.ts.net` unless that name
  matches a peer. Inventing the suffix would mis-verify LAN names.
- [Hand-edited `tls:10.20.0.50:443` still will not load] → Nothing running today
  can contain a `tls:` check, because load rejected it. The error changes from
  "not supported" to an invalid port. `tls:443` in a hand-edited `targets.json`
  starts probing on upgrade, which is the point of the check.
- [Status is briefly empty at startup] → The seeded presence view is preferred,
  and the status cache covers the unseeded window, a peer the view does not
  list yet, and the collector's own address. A failed
  status read does not fail an otherwise good handshake.
- [Private CAs need `insecure_tls`] → The check does not take a custom CA file.
  Expiry of an untrusted leaf is still reported. Trusting a private CA is out
  of scope.

## Migration Plan

No config key, state file, or metric series changes. Deploy the binary.
Rollback is the previous binary. Configs that loaded before still load.
`tls:<port>` starts working. A string that is not a port still fails
`check-config`.

## Open Questions

None. The window, the paging behavior, and the name rules are fixed by the
delta specs.

## Boundaries

This change does not cross a CONTEXT.md boundary. It does not add a Go
dependency, a tailnet API credential, shared collector state, or an action on
the tailnet. Dialing stays on `netEnv`. The expiry comparison takes an injected
clock.

It does replace an existing requirement. `collector-config` "Check syntax"
requires `tls:` to be rejected as phase 2, and `monitor-ui` "Add and edit form"
uses `tls:443` as the Custom row the server rejects. Both deltas replace those
requirements in full. `failure-classification` already calls a TLS error a
device answer. The added requirement makes the new expiry error that kind of
error, so the layer walk does not change.
