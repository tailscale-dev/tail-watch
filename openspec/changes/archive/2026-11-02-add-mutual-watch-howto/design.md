# Design

## Context

See proposal.md for motivation. What the how-to builds on, all already shipped:

- `/healthz` returns `200 ok` while the process serves (`collector-runtime`, "HTTP
  endpoints"). Authorization never applies to it (`ui-authorization`), so a peer needs no
  role and the UI can stay off.
- [Expose the UI as a Tailscale Service](../../../docs/how-to/monitor-ui-service.md) already
  documents the pieces: define `svc:<name>` in the admin console, a grant, an optional
  `autoApprovers.services` entry, and `tailscale serve --service=svc:<name> --https=443
  --bg 127.0.0.1:9109`. The listener stays on loopback.
- An `http:` check's URL host must equal the target's `addr`. A non-IP `addr` that matches
  no peer is resolved with DNS (`docs/reference/checks.md`), and a Service name is not a
  peer, so the collector's tailscaled must accept tailnet DNS (`TS_ACCEPT_DNS` in
  containers).
- A `body` match is written `http:<url>|<text>` and fails with the first 300 bytes of the
  body.

The page is a recipe over these, not new behavior. It crosses no `CONTEXT.md` boundary:
each collector keeps its own state and alerts through its own sinks, and the check only
reports. No existing spec is contradicted.

## Goals / Non-Goals

**Goals:**

- One page an operator can follow end to end with two collectors they already run, ending
  in a verified alert when one stops.
- Least privilege on the policy side, given that a Service exposes the whole listener
  (`/metrics` included).

**Non-Goals:**

- A third collector, a ring, or any quorum. Two is the case the page covers; a longer
  ring is the same steps repeated and is left unsaid.
- Replacing the heartbeat. The page says when each fits and links both ways.
- Any automated test of the page's commands beyond the live check in the tasks.

## Decisions

**Check the Service name, not the peer node.** Each side uses
`addr: <svc>.<tailnet>.ts.net` and `http:https://<svc>.<tailnet>.ts.net/healthz|ok`.
Checking the peer's node address would need the peer to bind its tailnet IP, giving up the
loopback listener the Services how-to keeps, and would prove only that the node answers.
The Service path exercises `serve`, the Service's host approval and the collector's own
listener, which is what a user of the UI depends on.

**One distinct Service per collector** (for example `svc:tw-a` and `svc:tw-b`). One shared
Service hosted by both would make a stopped collector invisible: the other host would keep
answering. Distinct names also make the alert say which collector died.

**One grant on the shared `tag:monitor`.** `src: tag:monitor`, `dst: svc:tw-a, svc:tw-b`,
`ip: tcp:443`, plus `autoApprovers.services` for both names. Separate tags per collector
would give each Service a narrower audience, but both collectors are already the same
trusted class and a second tag costs tag owners and policy for no change in who can read
`/metrics`. The exposure is named in the page: only `tag:monitor` nodes reach it, not the
admins the UI how-to grants. A `tag:monitor` node can reach its own Service, which is
harmless and unused.

**HTTPS on 443 with verification on.** This matches the Services how-to and makes the
check fail on a broken certificate path, not hide it. `insecure_tls` is not used. Plain
`--http=80` avoids the tailnet HTTPS prerequisite but is left to a single note.

**Body match `|ok`.** A bare `200` also comes from a proxy or captive page in front of the
wrong host. `/healthz` returns `ok`, so the match costs nothing.

**Alerts use the collector's existing routes.** Nothing is added. The page tells the
operator to send the check's notifications through a sink that does not depend on the
other collector, since a dead peer cannot deliver anything, and points at
[Route notifications](../../../docs/how-to/notification-routes.md).

**Out of reach for `local_only` collectors.** A `local_only` ProxyGroup sidecar has no
tailscaled and rejects `network: tailnet`, so it cannot take part. The page lists this under
Prerequisites instead of leaving a reader to find the config error.

## Risks / Trade-offs

- [A Service VIP is not a node, so the failure layer for a dead peer might not read
  `service`] → Resolved by the live check: it reports `layer: service` with
  `HTTP status 502`, and the page's verify step says so. No classifier change.
- [`/healthz` proves the process serves, not that its targets are healthy or that its sinks
  deliver] → State it in the page; the heartbeat covers the delivery side.
- [Both collectors share a failure mode: the same tailnet, the same control plane, the same
  sink provider] → State it. A tailnet-wide outage silences both, which is why this is an
  alternative to the heartbeat for the process-death case, not for everything.
- [Service setup has several approval steps and `serve` prints the same warning whether or
  not the Service is defined] → Link the Services how-to's troubleshooting instead of
  copying it, per the one-fact-one-page rule.
- [Policy pushes need a human] → The agent cannot push tailnet policy here, so the live
  check hands over the exact `tail-acl` command.

## Migration Plan

Documentation and live-stack support only; no migration. Rollback is reverting the commit. The live check uses throwaway
Service names defined with `PUT /tailnet/-/services/<name>`, a temporary policy entry, and
the live-stack nodes, then deletes the Services and restores the policy, confirmed by
comparing parsed JSON because the policy API re-aligns whitespace.

## Open Questions

- Whether `scripts/live-stack.sh` can start two collector containers as it stands, or the
  check needs a second collector service in its compose file. The answer changes only how
  the check is run, not the page.
