# Proposal

## Why

The monitor UI has no access control of its own. Whoever a tailnet grant lets reach
the listener can add monitors, delete them, and make the collector probe any address
it can route to (`docs/explanation/monitor-ui-risks.md`). The write path already
funnels through one `authorize` decision that permits everything, and the roadmap
lists identity-based authorization as the planned use of it.

A spike on 2026-10-16 showed the tailnet can supply the decision. A grant that carries
an app capability reaches the collector's host as a role, a caller with no matching
grant gets none, and `tailscale serve` overwrites any header a client forges. The same
spike showed that the identity tail-watch logs today (`Tailscale-User-Login` and
`X-Forwarded-For`) is only as trustworthy as the process that set it, and that a local
process can forge both. The collector can ask its own tailscaled who a caller is
instead of believing a header.

## What Changes

- Add an opt-in `collector.authorization` section naming an app capability (for
  example `example.com/cap/tail-watch`). With it set, every `/api/v1` request needs a
  role from that capability, granted in the tailnet policy:
  - `viewer` reads targets, peers, routes and sinks.
  - `editor` does everything `viewer` does and also creates, replaces and deletes
    monitors and routes, validates, previews routing and probes.
  - A caller with no role, or no verified identity, gets `403` and nothing changes.
- Resolve the caller from tailscaled, not from headers: `WhoIs` on the
  `X-Forwarded-For` address, read only on a loopback connection as today. The role,
  login and node (or tags) come from that answer. If the lookup fails, the request is
  denied.
- Optionally name the Tailscale Service (`collector.authorization.service`) so the role
  is read with `WhoIsForService`, which scopes capabilities to that Service.
- Add `GET /api/v1/me`, which reports the caller's login, node and role, so the UI can
  hide controls a role cannot use and show a clear message to a caller with none.
- Audit every write with the verified identity: `user`, `node`, `tags`, `role`, and
  `identity_mismatch` when the `Tailscale-User-Login` header disagrees with tailscaled.
  This replaces header-derived identity in the audit log, with or without
  authorization.
- Leave authorization off by default. With it off, behavior is unchanged: every
  request that reaches the listener is permitted, as today.
- Leave `/metrics`, `/healthz`, the prober status page and the static UI files
  outside the check. Prometheus scraping keeps working.
- Document the grants an operator needs, including the `app`-only grant to the host
  node's tag, and update the UI risk page and the Service how-to.

This delivers two ROADMAP.md items under "Dogfooding Tailscale": "Identity-based UI
authorization" and "Harden the audit identity". It also closes the "Monitor UI
authentication through the `authorize` hook" intent the roadmap carried before.

Non-goals:

- A login page, user database, session or password. Identity stays the tailnet's.
- A role that can probe but not write. A saved monitor already probes its address on
  every interval, so a write role includes the probe's risk; a probe-only role would
  add a name without removing exposure. See design Q1.
- Per-monitor or per-route permissions, and per-user rate limits.
- Authorizing `/metrics`, `/healthz` or the prober status page.
- Closing the local-process forgery gap. A process on the collector host can still
  connect to the loopback listener and send `X-Forwarded-For`. Closing it needs a
  listener only `serve` can reach, or `tsnetEnv` (ROADMAP.md).
- The `tsnetEnv` collector, setec secrets and the other roadmap items.
- Changing the UI's Origin and Content-Type guards.

## Capabilities

### New Capabilities

- `ui-authorization`: the `collector.authorization` section, the roles and what each
  allows, caller resolution through tailscaled, denial behavior, `GET /api/v1/me`,
  startup logging and the audit identity.

### Modified Capabilities

- `monitor-management`: "Write authorization" no longer says the decision permits
  every request, and "Write audit log" records the verified identity.
- `notification-routes`: "Route write authorization and audit" follows the same
  decision and the same audit identity.
- `collector-config`: a new "Authorization section" requirement (added, so it does not
  touch "Collector section", which `add-setec-secrets` also edits).
- `monitor-ui`: a new "Role-aware controls" requirement (added).

## Impact

- Code: new `cmd/tail-watch/authz.go` and `authz_test.go` (roles, caller resolution,
  the decision). `caller.go` takes identity from the resolver instead of headers.
  `monitor_guard.go` wraps read handlers and passes the role each operation needs.
  `monitor_api.go` gains `GET /api/v1/me` and sets `authorize` from the config.
  `config.go` adds and validates `collector.authorization`. `ui/` hides controls by
  role. `main.go` builds the resolver from `netEnv.LocalClient()`.
- Config: `collector.authorization.capability` and `collector.authorization.service`.
  Nothing existing changes meaning.
- Network: one LocalAPI `whois` call per `/api/v1` request. No outbound traffic.
- Dependencies: none. `tailcfg.UnmarshalCapJSON` and `local.Client.WhoIs` are in the
  `tailscale.com` version already pinned.
- Docs: a how-to for roles and grants, updates to `docs/how-to/monitor-ui-service.md`,
  `docs/explanation/monitor-ui-risks.md`, `docs/reference/config.md`,
  `docs/reference/cli.md`, `docs/how-to/troubleshoot.md`, `docs/README.md`,
  `CONTEXT.md`, `CLAUDE.md` and `ROADMAP.md`.
- UI: controls follow the role; the monitor list and routes page work read-only for a
  `viewer`.
