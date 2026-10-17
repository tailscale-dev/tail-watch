# Design

## Context

See proposal.md for why. The behavior contract is `specs/ui-authorization/spec.md`, plus
the deltas to `monitor-management`, `notification-routes`, `collector-config` and
`monitor-ui`.

What the code does today:

- Writes go through `guard` in `cmd/tail-watch/monitor_guard.go`: audit the outcome,
  require JSON (415), reject a foreign Origin (403), then call `monitorManager.authorize`
  (403), which `newMonitorManager` sets to `func(*http.Request) error { return nil }`.
  An empty `op` means the handler is neither audited nor authorized: that is how
  `POST /validate` and `POST /route-preview` skip both. `GET`s are not wrapped.
- `callerIdentity` in `cmd/tail-watch/caller.go` returns `Tailscale-User-Login` and the
  first `X-Forwarded-For` address, and reads them only when the connection is from
  loopback. Its comment says a local process can forge both.
- The manager holds a `netEnv`, whose `LocalClient()` is the LocalAPI client the
  collector already uses for Status, Ping and the IPN bus. `peerSource` is a precedent
  for a narrow interface over it that tests fake.
- `collector.ui` already requires a non-`local_only` collector with `state_dir`.

What the 2026-10-16 spike on a dev tailnet showed (run through `tailscale serve` on a
plain node and on a `svc:` Service over HTTPS, with tagged and user-owned callers):

- `serve --accept-app-caps=<cap>` forwards `Tailscale-App-Capabilities` as JSON,
  `{"<cap>":[{"role":"viewer"}]}`. With no matching grant the header is absent.
- `serve` overwrites a client's forged `Tailscale-App-Capabilities`,
  `Tailscale-User-Login` and `X-Forwarded-For`.
- The policy API rejects a capability name in the `tailscale.com` domain (400).
- The header appeared only once a grant targeted the host node's tag. A user-owned
  caller with a grant to `svc:` alone received `Tailscale-User-Login` but no
  capability header; adding an `app`-only grant to the node's tag produced it.
- Over a Service, `X-Forwarded-For` can be the caller's IPv6 address.
- `tailscale whois` on the host showed the same capability map the header carried.

## Goals / Non-Goals

**Goals:**

- A role from the tailnet policy decides who can read and who can change monitors.
- The caller's identity comes from tailscaled, so the audit log and the decision do not
  rest on a header.
- Off by default; nothing changes for an operator who does not opt in.
- No user database, session or password.

**Non-Goals:** the list in proposal.md.

## Decisions

### D1: Ask tailscaled who the caller is; do not read the capability header

`serve` already hands the backend the role in `Tailscale-App-Capabilities`, so reading
it is the least code. The spike showed it is also trustworthy when `serve` is the only
way in. But a header is a claim about the request, and the same claim appears in a
request from any local process. Resolving the `X-Forwarded-For` address with
`LocalClient().WhoIs` makes tailscaled the source of truth for role, login, node and
tags, and gives the audit log one verified source. Costs: one LocalAPI call per request
(a Unix-socket round trip, not a network call), and a hard dependency on tailscaled
being up, which the collector already has.

This also means an operator does not need `--accept-app-caps` on `serve`. They need
only the proxy and the grants. The how-to says the flag is unnecessary and harmless.

The local-process gap is unchanged: a process on the host can connect to the loopback
listener and send `X-Forwarded-For` for an editor's address, and `whois` will report
that editor. This design does not claim to close it. Closing it needs a listener only
`serve` can reach or a `tsnet` listener (ROADMAP.md `tsnetEnv`). ROADMAP.md tracks the
Unix-socket listener as its own item, "Close the local-process gap in UI identity", and
`docs/explanation/monitor-ui-risks.md` states the trust boundary plainly.

### D2: Which `whois` call: plain, or scoped to the Service

`local.Client` has `WhoIs`, `WhoIsForService(ctx, addr, svc)` and `WhoIsForIP`. The
last two scope the returned capability map to a destination. The spike's finding that
the header came from grants to the node, and not to `svc:`, was observed in what `serve`
forwards. It may not describe what `WhoIsForService` returns. So `collector.authorization.service`
is optional: unset, the plain `WhoIs` is used and operators grant the capability to the
host's tag; set, `WhoIsForService` is used and the grant may name the Service.
Task 1.1 settles which grants each call honors against a real tailnet before the
code and docs commit to a recommendation. If `WhoIsForService` honors `svc:` grants,
the how-to leads with it. The `service` key stays optional either way.

### D3: Two roles, hierarchical

`viewer` reads, `editor` reads and changes. The proposal considered a `probe`-only role,
because a probe is a one-shot request to an address the caller names. But a saved
monitor probes that address on every interval for as long as it exists, so any role that
can write already has the probe's reach. Granting probe apart from write would protect
nothing the write role does not already expose, and it would add a name to the policy
vocabulary, so it is left out (Q1). Roles are strings in the
capability value, so a later role is added without a config change.

The capability value is a list of objects (one per matching grant), so the policy author
can combine grants; the highest role wins. A value with an unknown role is ignored, not
an error: policy is the operator's and shared across tools, and an unknown value should
fail closed to less access.

### D4: Authorization applies to reads too

`GET /peers` lists the tailnet peers the collector sees, and `GET /targets` lists every
monitored address. Both are reconnaissance for an attacker who can reach the port. So
with authorization on, reads need `viewer`. The static UI files, `/metrics`, `/healthz`
and the prober status page stay open: the files hold no data, and Prometheus scrapers
must keep working. `serve` exposes the whole listener, so the how-to notes that
`/metrics` stays reachable by everyone the network grant allows.

`GET /api/v1/me` is exempt and returns `role: ""` for a caller without one, so the UI
can show an explanation and not a bare 403.

### D5: Guard structure

`guard` keeps its order (audit, content type, origin, authorize) and gains the role an
operation needs, separate from whether it is audited. Today an empty `op` means both
"not audited" and "not authorized"; that coupling has to go, because `validate` and
`route-preview` are unaudited but still need `editor`. `guard(next, op, need)` takes
the audit op and a required role. A new `guardRead(next)` wraps the `GET`s with `viewer`.
Handlers do not change. The decision stays a single function on the manager
(`authorize`), now set from config, so the spec's "single decision" stays true.

The resolver is an interface in `authz.go`, `identitySource` with
`WhoIs(ctx, addr string) (*apitype.WhoIsResponse, error)` and a
`WhoIsForService` counterpart, implemented by `*local.Client`. Tests use a fake,
as `peerSource` does.

### D6: Audit identity replaces header identity everywhere

`callerAttrs` switches from the headers to the resolver, with authorization on or off,
so the audit log is verified in both modes (this is ROADMAP.md "Harden the audit
identity"). It adds `node`, `tags` and `role`, and `identity_mismatch` when the
`Tailscale-User-Login` header and tailscaled disagree, which is the signal a forged
header leaves. If the lookup fails the line carries only the remote address and no
`user`: an unverified login is worse than none, because it reads like a fact.

### D7: Config

`collector.authorization.capability` has no default. The capability must live in a
domain the operator controls (the policy API rejects `tailscale.com`), so a default
would be a name the operator must still change. `check-config` rejects a
`tailscale.com` name so the error appears before the policy API's. Authorization needs
the UI (it protects only the API the UI serves), so a config with authorization and no
UI is an error, as is `local_only`, which has no tailscaled to ask.

### D8: A non-loopback listen is allowed but warned

With authorization on and `listen: 0.0.0.0:9109`, a direct connection cannot be
identified, so it is denied. That is safe. It also stops Prometheus from being
affected, because `/metrics` is outside the check. So it is a warning, not an error, and
the how-to says operators should keep the listener on loopback behind `serve`.

## Boundary and spec crossings

- **CONTEXT.md "No UI authentication of its own"**: this change adds authorization, not
  authentication. Identity still comes from Tailscale, with no credential or user store in
  tail-watch. The bullet is reworded to say that, and the "UI risks" page changes its
  "no login, on purpose" section to describe the optional role check.
- **CONTEXT.md "No credentials for discovery"**: not crossed. The role check uses
  LocalAPI `whois`, which needs no credential beyond the socket access the collector
  already has.
- **CONTEXT.md "Dogfooding Tailscale"**: this is the rule's own example, identity from
  the tailnet policy instead of a login system.
- **`monitor-management` and `notification-routes`**: both say the decision "SHALL
  permit every request"; the deltas keep that as the off-by-default behavior.
- **`add-setec-secrets`**: also changes `collector-config`, but through "Collector
  section". This change adds a separate requirement, so the two do not conflict at
  archive.

## Risks / Trade-offs

- **tailscaled down means the UI is down.** With authorization on, a failed lookup is a
  403. Mitigation: the collector's checks already depend on tailscaled; the UI is not the
  alert path.
- **Per-request LocalAPI call.** A page load makes a handful of API calls. Mitigation:
  measure; a short cache keyed by address is a later option (Q3) and is not added now.
- **Policy shape is easy to get wrong.** The grant to `svc:` and the grant carrying the
  capability are separate, and the capability may need the node tag. Mitigation: the
  how-to gives a tested policy snippet and a troubleshooting row for "no role was
  granted".
- **Local forgery remains** (D1). It is the same exposure the UI has today.
- **Revoked grants take effect on the next request**, since there is no session. That is
  a property, not a cost.

## Not verified

These came from the spike or from reading `tailscale.com@v1.102.5`, and are checked by
tasks, not assumed:

- Whether `WhoIsForService` returns capabilities granted to `svc:` (task 1.1).
- That `WhoIs` accepts a bare IPv6 address with no port (task 1.2).
- What `WhoIs` returns for a user-owned caller whose grant is only to the node's tag,
  and whether the capability name used in a real policy matches one a normal operator
  would choose.

## Open Questions

- **Q1.** Is a probe-only role worth offering despite D3? It adds a name and no
  reduction in exposure, so this design says no.
- **Q2.** How does an operator on the collector host use the API with authorization on?
  A direct loopback request has no `X-Forwarded-For` and is denied. Do we want an opt-in
  `local_role` for loopback requests with no header, or is "use the Service" enough?
- **Q3.** Is a short `whois` cache needed once measured?
- **Q4.** Should `GET /api/v1/me` also be exempt from the content-type check? It is a
  `GET`, so it is today.
