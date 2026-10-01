# Design

## Context

`POST /api/v1/validate` (`monitor_api.go`, `monitorManager.check`) validates a monitor
definition statically: it runs the config loader's candidate validation and returns the
resolved view, and it is deliberately neither authorized nor audited
(`monitor_guard.go` wraps it with `write=false`) because it changes nothing. The UI's
save flow (`ui/app.js`) POSTs the definition straight to create/replace; the UI's check
control calls `/api/v1/validate` and prints "Looks valid.". Every check type already
exposes `Run(ctx, env, *Target) (Result, error)` (`checks.go`), which is exactly what
the prober invokes, and `--once` (`main.go`, `runOnce`) is precedent for running checks
outside the prober's schedule. See proposal.md - Why.

## Goals / Non-Goals

**Goals:**

- `POST /api/v1/probe` answers "would this monitor's checks pass right now" with
  per-check results, running in the collector process so resolution, capabilities and
  TLS behavior match the prober exactly.
- The UI save flow (add and edit) probes first and asks for confirmation only when a
  check fails; a probe failure never blocks a save server-side.
- The endpoint stands alone so future features (for example a probe of an existing
  YAML monitor, or a pre-flight in other tooling) can reuse it.

**Non-Goals:**

- Server-side enforcement in create/replace (see Decisions).
- Probing targets that need authentication.
- Changing check semantics, config keys, metrics or payload fields.
- Removing `POST /api/v1/validate`; existing API consumers keep working.

## Decisions

### Standalone probe endpoint, gate in the UI

`POST /api/v1/probe` is separate from create, and the confirm-on-failure gate lives in
`ui/app.js`: probe, then either save (all pass, or operator confirms) or return to the
form. Alternative: run the probe inside `create()`/`replace()` and refuse with 409 plus
probe results unless a `confirmed` flag is set. Rejected for now: it slows every write,
adds a force flag to a stable API, and the probe-then-save race it would close is
harmless — the prober is the source of truth seconds later. The user picked this
explicitly so `/api/v1/probe` stays reusable by future enhancements.

### Probe runs `check.Run` directly, concurrently

The handler validates first (same path as `check()`, so a YAML-target name gets 409 and
a definition matching a dynamic monitor is validated as an edit of it), then runs each
check's `Run` once against the app's `netEnv`. Results are collected per check in
definition order. Alternative: drive `prober.WithOnce` like `runOnce`. Rejected: the
prober aggregates; the endpoint needs one result per check with its own timeout.

Failure is exactly "what `Run` reports": network errors, TLS errors, and a non-200 HTTP
status (`httpStatusError`). No probe-specific classification, so the gate can never
drift from runtime semantics; the error text carries the nuance to the operator.

### Lock only for validation

`monitorManager`'s mutex serializes writes; static validation under it takes
microseconds. The probe itself runs after the lock is released. Alternative: hold the
lock through the probe. Rejected: a slow `tsping` would stall every UI write for
seconds.

### Guarded and audited like a write

The route wraps `guard(handleProbe, true)`: JSON content type, same-origin, the
authorization decision, and an audit line with op `probe`. Alternative: `write=false`
like validate. Rejected: unlike validate, a probe emits network traffic on demand
toward an address the caller names; an unauthorized caller must not get a probing
oracle, and the audit log should record who probed what.

### Timeouts: per-check from the definition, whole call capped

Each check runs with a context bounded by the definition's `timeout`, and the whole
call is hard-capped (constant, 15 s) on top of the request context, so a disconnect
cancels the runs and one slow check cannot hold the response. Since checks run
concurrently, the response time is the slowest single check. Trade-off recorded under
Risks.

### UI flow reuses existing machinery

`save()` in `app.js` becomes probe-then-save: 422 answers go through the existing
`showErrors` (per-field and per-row), a 200 with all `ok` proceeds to the unchanged
create/replace call, and a 200 with failures opens a new `<dialog>` in `index.html`
(modeled on the delete confirmation) listing each failing check and its error with
"Save anyway" and "No, let me edit". Declining closes the dialog and marks the failing
check rows with the same per-row error display the server errors already use. The
check control switches from `/api/v1/validate` to `/api/v1/probe` and renders the
per-check results (outcome plus latency) in the existing result box.

### Response shape

200: `{ "results": [ { "check": "tcp:443", "ok": false, "latency_ms": 3, "error":
"dial tcp ...: connect: connection refused" } ] }`, one entry per check in definition
order; `error` is omitted when `ok` is true. 422: the same `errors` list as validate.
Nothing is persisted, registered or metered; alert state is untouched.

## Risks / Trade-offs

- [Probe endpoint is a port-scan oracle for anyone who can reach the UI] → Guarded like
  a write (authorization decision, origin check) and audited with the caller's remote
  address; access control stays the tailnet ACL and listen address, as for writes.
- [A definition with `timeout` above the 15 s ceiling can see a probe timeout the
  prober would not hit] → The error text says it timed out, the gate only warns, and
  the operator can save anyway; the ceiling keeps the UI responsive.
- [ICMP probe in an environment without `CAP_NET_RAW` reports a permission error the
  operator might misread as a target problem] → The error text names the permission
  failure exactly as the prober would log it; acceptable for a one-shot check.
- [Target flaps between probe and save] → Harmless by design; the gate informs and the
  prober remains the source of truth.

## Migration Plan

None. Additive endpoint and a client-side gate; no stored state or config changes.

## Open Questions

None.
