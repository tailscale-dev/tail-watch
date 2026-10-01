# Proposal

## Why

The add/edit form and `POST /api/v1/validate` check only that a monitor definition is
well-formed. A monitor with a typo'd port, a wrong scheme, or a dead URL saves fine and
the operator learns about it only when the prober's first scheduled check fails — bad
monitors get in from the start, and the UI's "Check" button answers "Looks valid." for
a monitor that can never work.

## What Changes

- Add `POST /api/v1/probe`: validate a monitor definition with the same rules as
  `POST /api/v1/validate`, then run each of its checks once against the target and
  return per-check results (check string, ok, latency, error text). It persists and
  registers nothing. Because it emits network traffic on demand toward arbitrary
  addresses, it is authorized and audited like a write, unlike the read-only validate
  call.
- Gate the add and edit form's save on a probe: all checks passing saves immediately;
  any failing check opens a confirmation listing the per-check failures with "Save
  anyway" and "No, let me edit". "No" returns to the form with the failing check rows
  marked; "yes" saves through the unchanged create/replace call.
- A failing probe never blocks a save server-side: a monitor for a target that is down
  right now is a legitimate monitor. The gate informs; the operator decides.

This is a phase 1 follow-up to the monitor UI. The design doc's "Check types" section
defines the checks the probe runs and is unchanged; the probe reports exactly what the
prober would report, so "failed" means the same thing as at runtime (network errors,
TLS errors, a non-200 HTTP status). Non-goals:

- Server-side enforcement: create and replace keep accepting any valid definition.
- Probing targets that need authentication (for example an HTTP check behind login).
- Seeding alert state or the first incident from probe results.
- Phase 2/3 items: tsnet mode, PagerDuty, `target.degraded`, quorum.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `monitor-management`: the Monitor API gains `POST /api/v1/probe`, and a new
  requirement describes the live probe: parity validation first, one run per check,
  per-check timeout with a capped total, write-style authorization and audit, and no
  persistence.
- `monitor-ui`: the add and edit form gains a probe-gated save with a
  confirm-on-failure dialog.

## Impact

- Changed: `cmd/tail-watch/monitor_api.go` (new handler), `cmd/tail-watch/monitor_guard.go`
  (route), `cmd/tail-watch/ui/app.js` and `cmd/tail-watch/ui/index.html` (save flow and
  dialog), tests next to the code and the JS-block tests.
- The check implementations in `checks.go` are reused as-is; no check, config key,
  metric name or payload field changes.
- No new dependency.
