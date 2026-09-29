# Design

## Context

Targets are parsed once by `loadConfig` (`config.go`) into `Config.Targets`, then
`startCollectors` (`main.go`) builds the alerter, the classifier and a
`runtimeRegistry`, seeds the targets, restores open incidents and schedules checks.
`runtimeRegistry.AddTarget`/`RemoveTarget` already register a target live across the
alerter, the prober and the notifier; the dependency learner uses them for implicit
routers. The alerter refuses to remove a configured target with an open incident
(`alert_target.go`, `removeLocked`) but allows an implicit one. The HTTP mux in
`runServer` has `/` (prober status, a catch-all), `/metrics` and `/healthz`. The
project has no Node toolchain. See proposal.md for motivation and scope.

## Goals / Non-Goals

**Goals:**

- Reuse the existing live add/remove path instead of a second registration mechanism.
- One validator for YAML and dynamic monitors.
- Zero change for a collector started without `--ui`, except two 404 routes.

**Non-Goals:**

- A configuration reload mechanism. YAML is still read once at startup.
- Any change to the notification payload, so `dynamic` is not added to events and the
  golden files stay as they are.

## Decisions

### Overlay store, YAML wins

Dynamic monitors live in `state_dir/targets.json` as an array of objects with the YAML
field names, written with the same atomic temp-file-and-rename approach as
`notification-state.json`. YAML targets are registered first; a dynamic entry that
clashes or fails validation is skipped with a warning and left in the file.

Alternatives: rewrite the YAML file (loses comments, breaks read-only mounts and GitOps);
one store for everything (needs a migration and makes the file no longer the contract).
Rejected for those reasons.

### Validate by resolving the merged set

`resolveConfig` validates the target list as a whole (duplicates, parents, cycles). Keep
the raw YAML targets and the resolved `defaults` timing on `Config`, then validate a
dynamic candidate by running the same resolution over YAML targets plus all dynamic
entries with the candidate substituted, and report only the errors located at the
candidate. Split the per-target part of `resolveConfig` into a function both callers
use. Field errors are re-prefixed from `targets[i].checks[1]` to `checks[1]`.

Alternative: a second validator for the API. Rejected: the rule lists would drift, and
"the UI accepts what YAML would accept" is a spec requirement.

### A `Dynamic` marker, not `Implicit`

`Implicit` means "learned by dependency learning" and drives classification, incident
restore and event payloads. Reusing it for UI monitors would change all three. Add
`Target.Dynamic bool`, consulted only by `removeLocked`
(`!ts.target.Implicit && !ts.target.Dynamic` may leave a `DOWN` target). At startup dynamic
targets join the `configured` name set passed to `restoreOpen`, so open incidents restore
through the ordinary configured path.

### Persist first, then apply

Order for a change: validate, write the new `targets.json` atomically, then call
`AddTarget`/`RemoveTarget`. If the write fails nothing has changed. Applying first was
rejected because a delete of a `DOWN` monitor cannot be undone: re-adding it would start
in `UNKNOWN` after its incident was already closed. A crash between the write and the
apply leaves the file ahead of the runtime, which the next start reconciles.

An edit is `RemoveTarget` then `AddTarget` of the new definition. If the add fails
after the remove (not expected after validation), the old definition is re-registered
and the old file bytes are rewritten.

### Force-closed deletes

Removing a `DOWN` dynamic target leaves the removed, dirty snapshot that the notifier's
`reconcile()` turns into a `reason: removed` recovery. `reconcile()` runs on a 1s ticker,
so the recovery is emitted within about a second of the `204`, not before it.

Deleting a target other targets name as `parent` is refused with 409 and the dependent
names. An edit of a parent is allowed: removing it releases suppressed dependents (as a
recovery would), and adding the new definition re-suppresses them if it is `DOWN`.

### API and routing

Go 1.27 mux patterns give method routing without a dependency:
`GET /api/v1/targets`, `POST /api/v1/targets`, `PUT /api/v1/targets/{name}`,
`DELETE /api/v1/targets/{name}`, `POST /api/v1/validate`. `/ui/` is
`http.FileServerFS` over an `embed.FS` rooted at `cmd/tail-watch/ui/`, with a
`Content-Security-Policy: default-src 'self'` header.

`/` is a catch-all, so when the UI is off `/ui/` and `/api/v1/` are registered to
return 404 explicitly; otherwise the status handler would answer them.

Middleware order for writes: content type (415), origin (403), `authorize(r)` (403),
handler. `authorize` is one function, `func(*http.Request) error`, that returns nil
today; the later auth proposal replaces its body. Responses use
`{"errors": [...]}` for failures.

The list joins the resolved targets with the alerter's `Snapshots()` to fill `state`.

### SPA: plain files, no build

`ui/index.html`, `ui/app.js`, `ui/style.css`. ES modules, `fetch`, a 5s poll of the
list, a `<dialog>` for the form. The parent selector is filtered client-side to
monitors with exactly one `icmp` or `tsping` check. No framework, no CDN.

Alternatives: htmx or Preact vendored into the repo. Rejected: it adds a vendored
license and update surface for one table and one form.

### Flag plumbing

`--ui` is a tri-state bool (`flag.Func` or a pointer wrapper) so `--ui=false` overrides a
config `collector.ui: true`. `rawCollector` gains `UI *bool`. The state-dir check runs in
`run()` next to `loadConfig`, before preflight.

## Deviations from the design doc

- The design doc's Non-goals exclude "a web UI beyond the prober status page". This
  change adds an opt-in one. Reason: operators who do not keep config in git have no
  other way to change monitors without a restart, and the opt-in keeps GitOps
  deployments unchanged.
- A new state file, `targets.json`, alongside `notification-state.json`.
- A new config key, `collector.ui`, and a new flag, `--ui`.

## Risks / Trade-offs

- [Any host that can reach the listener can make the collector probe arbitrary
  addresses] → Off by default, loopback default bind, startup warning on non-loopback
  bind, and the docs give the ACL grant. Stronger auth is a later proposal, and the
  `authorize` hook is the seam for it.
- [Browser-based attacks on an admin's session: cross-site form posts and DNS
  rebinding] → JSON-only writes and an `Origin` versus `Host` check stop form posts and
  cross-origin fetches. They do not stop DNS rebinding, where the attacker's page and the
  `Host` header agree; only a `Host` allow-list would. Accepted for this change and
  called out for the auth proposal.
- [The ACL grant covers the whole port, so UI access also exposes `/metrics`] →
  Documented; not new, since `/metrics` is already on that port.
- [In the Compose layout the collector shares the tailscale container's network, so any
  process in that namespace reaches loopback] → Documented with the `listen` guidance.
- [Editing a parent briefly releases its suppressed dependents, which can page while the
  parent is down] → The UI warns on editing a monitor that has dependents.
- [`--ui` turned off later leaves dynamic monitors silently stopped] → A startup
  warning counts them, and their open incidents close as `removed` through the existing
  restore path.
- [Skipped `targets.json` entries are not shown in the UI] → Logged only for now; see
  open questions.

## Migration Plan

None for existing deployments: without `--ui` nothing changes. To adopt, set
`collector.state_dir`, start with `--ui` (or `collector.ui: true`), and add the ACL
grant. To roll back, drop the flag; `targets.json` stays on disk and can be re-enabled.

## Open Questions

- Should `GET /api/v1/targets` also list skipped `targets.json` entries, marked
  inactive, so an operator can see and delete a shadowed one? Deferrable: the API
  shape gains a field, nothing else changes.
