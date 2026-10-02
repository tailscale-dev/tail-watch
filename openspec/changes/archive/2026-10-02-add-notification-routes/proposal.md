# Proposal

## Why

A monitor's `severity` (`page` or `warn`) is only a number that each sink compares
against its own `min_severity` and `match` filters. Someone adding a monitor in the UI
cannot see the sinks, so they cannot tell where a page or a warning goes, or whether it
goes anywhere. Several sinks are already supported, but nobody can see from the monitor
how they are wired. Sending one group's monitors to its own webhook means editing
`match` filters on every sink in the config file.

This builds on the "Notifications" section of `tailnet-reachability-monitor-design.md`
("Events", and the rule that targets set `severity: warn`), and on the monitor UI
changes. It is a post-MVP addition. None of the design's phases covers it.

## What Changes

- Add named **routes**. A route has a `name`, a `severity` (`page` or `warn`), a list of
  sink names, and an optional Slack `mention`. Routes come from a new `routes:` config
  section (read-only in the UI) or from the UI. UI routes are stored in `routes.json` in
  `collector.state_dir`, alongside `targets.json`.
- A target MAY set `route: <name>` instead of `severity`. An event from that target
  takes the route's severity and has the route's sinks as its candidates. A target with
  no `route` keeps today's behavior: every sink is a candidate, and `severity` defaults
  to `page`.
- Routing is the route's candidate sinks **AND** each sink's own filters (`events`,
  `min_severity`, `match`). A sink can still drop an event that a route sends it, and
  the preview below must show that drop.
- `target.flapping` from a routed target goes to the route's sinks at `warn`.
  `target.recovered` keeps going to the sinks that took its down. `collector.started`
  and `test` are not routed and keep today's sink filtering.
- A route's `mention` replaces `mention_on_page` for `target.down` events of severity
  `page` on that route. `mention_on_page` still applies to unrouted targets.
- Config validation rejects a route that names an unknown sink, a target that names an
  unknown route, a target that sets both `route` and `severity`, and a UI route whose
  name collides with a YAML route.
- New monitor API endpoints: list sinks (name, type, filters; never URLs or secrets);
  list, create, replace and delete routes; and a routing preview. The preview takes a
  draft monitor and returns, for each event type, every sink with `sent` or the filter
  that drops it. Route changes use the same Content-Type, Origin, `authorize` and audit
  log rules as monitor changes. Deleting a route that a monitor uses is refused with
  409, listing those monitors.
- Monitor UI:
  - The severity field becomes a route picker. Its options explain what `page` and
    `warn` mean, and each route shows its destinations.
  - The form shows the routing preview live.
  - A new Routes page lists sinks read-only and routes with the monitors that use
    them. It adds, edits and deletes UI routes; YAML routes are read-only there.
- `check-config` prints each route and the resolved sinks of each target.

Non-goals:

- Creating or editing sinks from the UI. Sinks hold secrets and stay config-only.
- Removing or deprecating `min_severity` and `match`.
- Per-check or per-layer routing, schedules, escalation and on-call rotations.
- Phase 2 sinks (PagerDuty, Slack bot-token mode) and `target.degraded`. They get
  routes for free when they arrive.
- Phase 3: routes shared across collectors.

## Capabilities

### New Capabilities

- `notification-routes`: Covers the route definition and the `routes:` config section,
  the `routes.json` store and its merge with YAML routes, and resolving a target's
  candidate sinks and severity. Also covers the routes, sinks and preview API
  endpoints, their validation, the in-use delete refusal, and audit logging.

### Modified Capabilities

- `collector-config`: Targets accept `route`; `route` and `severity` are mutually
  exclusive; the `routes:` section is validated; `check-config` prints routes and
  per-target sinks.
- `notification-events`: `target.down` severity comes from the route when one is set.
  Sink selection is the route's sinks ANDed with sink filters. `target.flapping` from a
  routed target goes to the route's sinks.
- `slack-sink`: A route's `mention` is used for page-severity downs on that route, in
  place of `mention_on_page`.
- `monitor-management`: Dynamic monitors persist `route`. A replace that changes a
  monitor's route follows the existing replace semantics.
- `monitor-ui`: Route picker with page/warn explanations, a live routing preview in
  the form, a Routes page, and a Route column in the monitor list, replacing the
  Severity column.

## Impact

- Code in `cmd/tail-watch/`: `config.go` and `config_sinks.go` (the `routes:` section,
  target `route`, cross-reference validation); `event.go` (`accepts` and candidate
  selection, flapping routing); `slack.go` (mention source); `dynamic.go` (target
  `route`, plus a `routes.json` load, save and merge, or a sibling file);
  `monitor_api.go` and `monitor_guard.go` (route, sink and preview handlers);
  `ui/index.html`, `ui/app.js`, `ui/style.css` (picker, preview, Routes page).
- Tests: config validation tables, routing tables over `accepts`, payload and Slack
  goldens for route severity and mention, API handler tests, and Playwright browser
  tests for the picker, the preview and the Routes page.
- Persisted state: new `routes.json`. `notification-state.json` is unchanged, because
  incidents already record their selected sinks.
- Docs: `reference/config.md` (`routes`, target `route`), the monitor API reference,
  `reference/payload.md` (where severity comes from), a how-to for routing pages and
  warnings, and `how-to/troubleshoot.md` (why an event did not reach a sink).
- Ordering: independent of `add-proxygroup-sidecar`. In `local_only` mode the UI is
  off, so the Routes page does not apply there, but YAML routes still work.
