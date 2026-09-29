# Proposal

## Why

Monitors exist only as entries in a hand-edited YAML file that is read once at
startup. Adding a printer or a NAS means editing the file and restarting the collector,
which drops in-flight grace timers and flap state. Operators who do not keep config in
git want to add, edit and remove monitors from a browser and see the change take effect
immediately, while operators who do keep config in git must be able to keep the
collector exactly as it is today.

## What Changes

- Add a `--ui` flag and a `collector.ui` config key, both off by default. The flag
  overrides the key, the same way `--listen` overrides `collector.listen`.
- With the UI on, serve a small single-page app from the binary under `/ui/` (embedded
  static files, no build step) and a JSON API under `/api/v1/` to list, add, edit and
  delete monitors, plus a dry-run validation call the form uses.
- Store UI-managed ("dynamic") monitors in `state_dir/targets.json`, written
  atomically, and load them at startup after the YAML targets. YAML targets stay in
  the list as read-only entries. A dynamic monitor whose name matches a YAML target is
  refused; the YAML target wins.
- Apply every change live: a new monitor is registered with alerting, scheduling and
  the notifier without a restart.
- Deleting a dynamic monitor removes it even while it is `DOWN`; its open incident is
  closed with `reason: removed`. An edit is a delete followed by an add, so editing a
  `DOWN` monitor resolves its incident and a new one opens after `fail_threshold`.
- Reuse the config loader's validation for dynamic monitors, so a monitor the UI
  accepts is one the YAML loader would accept.
- Route every write through one `authorize` hook that allows all requests. Access
  control is the tailnet ACL for now. Every write is logged with the caller's address.
- Reject cross-site writes: mutating requests must be `application/json`, and a
  request that carries an `Origin` header naming a different host is refused.
- Fail startup when `--ui` is set without `collector.state_dir`, since there is nowhere
  to persist.

This deviates from the design doc, whose Non-goals section lists "a web UI beyond the
prober status page we get for free". The deviation is deliberate and recorded in
`design.md`. No design-doc section is implemented; the change adds behavior after
phase 1 (MVP), touching the "Config" section (one new key) and the HTTP surface of
"Architecture".

Non-goals:

- Authentication or authorization beyond the tailnet ACL (a later proposal replaces the
  `authorize` hook body).
- Editing YAML targets, sinks, `defaults`, or heartbeat from the UI.
- Bulk import or export, undo, or edit history.
- Host-header allow-listing against DNS rebinding (see design.md risks).
- `--ui` in `--once` mode: `--once` runs only the YAML targets.
- Phase 2/3 items: tsnet mode, PagerDuty, Slack bot-token mode, quorum across
  collectors.

## Capabilities

### New Capabilities

- `monitor-management`: the dynamic monitor store, its merge with YAML targets at
  startup, the `/api/v1/` list/add/edit/delete/validate contract, live application,
  force-close on delete, the authorization hook, write logging and cross-site request
  rejection.
- `monitor-ui`: the `/ui/` single-page app, what it shows (all monitors with source and
  live state) and what it can change (dynamic monitors only), and its embedded,
  build-free delivery.

### Modified Capabilities

- `collector-config`: adds the `collector.ui` key and the `--ui` flag, and the rule
  that `--ui` requires `collector.state_dir`.
- `collector-runtime`: the HTTP endpoints requirement gains `/ui/` and `/api/v1/`,
  which are served only when the UI is on.
- `alert-state-machine`: the runtime removal rule gains dynamic targets, which may be
  removed while `DOWN` and close their incident as `removed`.

## Impact

- New Go code in `cmd/tail-watch/`: the dynamic store and merge, the API handlers, the
  authorize and origin checks, the `--ui` wiring, and a `Dynamic` marker on `Target`.
- New embedded static assets under `cmd/tail-watch/ui/` (HTML, CSS, JS), served with
  `go:embed`. No Node toolchain and no new Go dependency.
- Changed: `config.go` (key), `main.go` (flag, mux, startup merge), `alert_target.go`
  (removal rule), `README.md`/docs (the ACL grant and `listen` guidance for the UI).
- Security surface: with the UI on, anyone who can reach the listen address can make the
  collector probe arbitrary addresses. The default `127.0.0.1` bind keeps that local;
  exposing it is an operator decision covered by the ACL.
- Sequencing: `add-user-documentation` is independent; it gains a UI how-to page as a
  follow-up task once this lands.

## Assumptions

- Dynamic monitors use the same fields and validation as YAML targets; `defaults`
  from the YAML file apply to them.
- Monitor names are immutable in an edit; renaming is a delete plus an add.
- Deleting a monitor that other targets name as `parent` is refused (409) with the
  dependents listed, since that is a referential-integrity problem, not a `DOWN` one.
- The SPA is plain HTML, CSS and ES modules; no framework.
