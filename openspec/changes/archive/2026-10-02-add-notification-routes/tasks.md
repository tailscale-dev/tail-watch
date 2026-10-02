# Tasks

## 1. Route config and validation

- [x] 1.1 In `cmd/tail-watch/config_sinks.go` add the `routes` section: `rawRoute`
  (`name`, `severity`, `sinks`, `mention`), the resolved `Route` with its sinks as
  `SinkConfig` indexes, and validation (name characters, duplicates, unknown keys,
  empty `sinks`, a sink listed twice, an unknown sink) reported at `routes[i].<key>`
  in the shared error list; verify table-driven cases in `config_sinks_test.go` for each
  rejection, the default `page` severity, and several errors in one load
- [x] 1.2 In `cmd/tail-watch/config.go` add `route` to `rawTarget` and `Target.Route`;
  reject `route` together with `severity`, and reject a YAML target naming an unknown
  or UI-only route; make `validateCandidate` take the route set; verify cases in
  `config_test.go` for the routed target, both keys set, and an unknown route
- [x] 1.3 Extend `check-config` output (`cmd/tail-watch/main.go`, `config_sinks.go`
  print helpers) to list each route with its severity and sinks, and each target's route
  (or none) and candidate sinks; verify `main_test.go` asserts the routed and unrouted
  lines
- [x] 1.4 Document `routes` and target `route` in `docs/reference/config.md`
  (keys table, mutual exclusion, an example block) and the new `check-config` lines in
  `docs/reference/cli.md`; verify the example passes `tail-watch check-config` and is
  added to `cmd/tail-watch/testdata/snippet.hujson` if that file mirrors the docs

## 2. Routing and severity at delivery

- [x] 2.1 In `cmd/tail-watch/event.go` replace `accepts` with
  `explain(ev, tags) (bool, string)` that returns the reasons `events`, `min_severity`,
  `match.sites`, `match.targets` and `match.tags`, keeping `accepts` as a wrapper;
  verify `event_test.go` covers each reason and that existing `accepts` cases still
  pass unchanged
- [x] 2.2 Add the copy-on-write `routeTable` (immutable snapshot behind an
  `atomic.Pointer`, in a new `cmd/tail-watch/route.go`) with lookups for severity,
  candidate sink indexes and mention; verify `route_test.go` covers lookup, a
  missing-sink route routing to the rest, and concurrent read during publish under
  `go test -race`
- [x] 2.3 In `cmd/tail-watch/event.go` and `cmd/tail-watch/notify.go` resolve severity
  from the route in `newDownEvent`, narrow `routeLocked` to the route's sinks before
  `explain`, and leave `collector.started`, `test` and recoveries on their existing
  paths; verify `notify_test.go` cases: route narrows sinks, a sink filter still drops
  (AND), flapping goes only to route sinks at `warn`, an unrouted target behaves as
  before, a route edited mid-incident still sends the recovery to the down's sinks, and
  `collector.started` ignores routes
- [x] 2.4 Carry the resolved route mention on the queued record (`queue.go`, an
  optional JSON field in `notification-state.json`, never in the event body) and in
  `cmd/tail-watch/slack.go` render the distinct page-down mentions of a group, falling
  back to `mention_on_page`; verify `slack_test.go` cases (route mention replaces the
  sink's, no route mention falls back, warn and recovery get none, two routes in one
  group print each mention once), a `TestSlackGolden` update reviewed by diff,
  `TestPayloadGolden` unchanged, and `queue_test.go` reading an old state file without
  the field
- [x] 2.5 Update `docs/reference/payload.md` (severity comes from the route when set),
  `docs/how-to/slack-sink.md` (route `mention`), `docs/explanation/incidents-and-queues.md`
  (the added queue field), and add a "why didn't sink X get the event" entry to
  `docs/how-to/troubleshoot.md`; verify markdownlint passes on those files

## 3. UI route store and startup order

- [x] 3.1 In `cmd/tail-watch/dynamic.go` factor `saveDynamic`'s atomic write into
  `writeJSONAtomic(dir, file, v)` and switch `saveDynamic` to it; verify the existing
  `dynamic_test.go` store tests pass unchanged
- [x] 3.2 Add `routes.json` (`dynamicRoute`, `loadRoutes`, `saveRoutes`, `mergeRoutes`,
  in `cmd/tail-watch/dynamic_routes.go`): lenient decode, YAML-name clash and invalid
  entries skipped with a warning and left in the file, missing sinks kept and warned,
  corrupt file stops startup, UI off leaves it alone with a warning; verify
  `dynamic_routes_test.go` covers each case plus file mode 0600
- [x] 3.3 In `cmd/tail-watch/main.go` and `dynamic.go` order startup as YAML, then
  UI routes, then `mergeDynamic` (now checking `route`), then `newNotifier` with the
  route table; store `route` in `dynamicTarget`; verify `dynamic_test.go` cases: a
  dynamic monitor on a UI route survives a restart, one naming a missing route is
  skipped and kept in the file, and `--once` reads neither store
- [x] 3.4 Document `routes.json` next to `targets.json` in `docs/how-to/monitor-ui.md`
  and the rollback note (remove `routes:` and `route:` first) in
  `docs/explanation/monitor-ui-risks.md`; verify markdownlint passes

## 4. Route, sink and preview API

- [x] 4.1 In `cmd/tail-watch/monitor_api.go` add route create, replace and delete on
  `monitorManager` under its mutex (persist `routes.json`, then publish the route
  table), with `used_by`, `missing_sinks`, 409 for a YAML route, a duplicate name or a
  route in use, 422 for validation, and 400 for an unknown field or a body over 64 KiB;
  verify `monitor_api_test.go` table cases for each status, the persist-failure 500
  leaving the table unchanged, and a monitor created on a new route using it for its
  next down
- [x] 4.2 Add `GET /api/v1/sinks` in `monitor_api.go`, returning name, type, filters
  and `mention_on_page` only; verify a test that the body contains no URL, secret,
  header value, or `*_env`/`*_file` name for a webhook sink configured with all of them
- [x] 4.3 Add `POST /api/v1/route-preview` in `monitor_api.go`: validate as `validate`
  does, build the down and flapping events with the real constructors, narrow, then
  `explain` every sink, with tags looked up from the presence view by address and
  `unknown` when they cannot be decided; verify tests: a dropped sink with
  `match.sites`, `not_in_route`, `min_severity` on flapping, unknown tags with an
  unseeded view, 422 on an invalid definition, and nothing changed afterwards
- [x] 4.4 Register the routes in `cmd/tail-watch/monitor_guard.go`: writes with ops
  `route.create`, `route.replace` and `route.delete`; `POST /route-preview` with an
  empty op; `GET /routes` and `GET /sinks` unwrapped, like `GET /targets`; verify tests for 415, a mismatched Origin
  (403), a denied write changing nothing (403), an audit line with the remote address,
  and 404 for every new path with the UI off
- [x] 4.5 Document the new endpoints and their bodies in `docs/how-to/monitor-ui.md`
  (or the page that already documents `/api/v1/targets`); verify markdownlint passes

## 5. Monitor form: picker, explanation and preview

- [x] 5.1 Add the DOM-free helpers to `cmd/tail-watch/ui/app.js` in a marked block
  (route option label, reason-to-words, the "notifies no one" test), plus a
  `ui_routes_test.go` that runs them under bun or node as `ui_checks_test.go` does;
  verify that test passes
- [x] 5.2 In `cmd/tail-watch/ui/index.html` and `ui/app.js` replace the severity
  select with the routing picker ("All sinks": `page`, `warn`; "Routes": each route
  with its severity and sinks), send `route` or `severity` but never both, select the
  stored value on edit, and fall back to the two default choices when routes cannot be
  fetched; show the static page/warn/flapping/recovery explanation beside it; change
  the list's Severity column to Routing; verify browser tests in
  `cmd/tail-watch/ui_routes_browser_test.go`: choosing a route sends `route` only, edit
  preselects, routes fetch failure still saves, and the list shows route and severity
- [x] 5.3 Add the debounced routing preview to the form (`ui/app.js`, `ui/style.css`),
  ignoring stale responses and showing sent, dropped-with-reason and unknown per sink
  for down and flapping, plus the "notifies no one" warning, without blocking save;
  verify browser tests: a dropped sink is named, changing site updates the preview, an
  all-dropped down warns and still saves, and a failed preview does not block
- [x] 5.4 Update `docs/how-to/monitor-ui.md` (choosing a route, reading the preview)
  and replace any affected screenshots in `docs/images/`; verify markdownlint passes

## 6. Routes page

- [x] 6.1 Add the hash-selected Routes view to `cmd/tail-watch/ui/index.html` and
  `ui/app.js`: read-only sinks table with filters, a note that sinks are defined in the
  config file, the routes table with source, sinks, mention, `used_by` and missing sinks
  marked, YAML rows read-only, add and edit (name fixed) with sinks chosen from the
  configured names and field errors shown inline, and delete with confirmation showing
  `used_by` on a 409; verify browser tests: add a route and see it in the monitor
  picker, a refused delete names the monitor, a YAML route has no controls, and a
  missing sink is marked
- [x] 6.2 Write `docs/how-to/notification-routes.md` (when to use page vs warn, define
  a route in YAML or on the page, move a monitor onto it, check the preview), link it
  from `docs/README.md`, and add a Routes page screenshot to `docs/images/`; verify
  markdownlint passes and the YAML example passes `tail-watch check-config`

## 7. Validation

- [x] 7.1 Run `mise exec -- go build ./...` and `mise exec -- go vet ./...`; verify both
  succeed
- [x] 7.2 Run `mise exec -- go test -race ./...`, including the browser tests with the
  Playwright driver installed (`docs/contributing/ui-browser-test.md`); verify all pass
  and none are skipped for a missing driver
- [x] 7.3 Run `mise run lint` (golangci-lint) and `govulncheck ./...`; verify no findings
- [x] 7.4 Run markdownlint over `docs/`, `README.md` and this change's artifacts; verify
  no issues
- [x] 7.5 Run `mise exec -- openspec validate add-notification-routes --strict`; verify
  it reports the change as valid
- [x] 7.6 Through `scripts/live-stack.sh up` with the UI on, create a route on the
  Routes page, move a monitor onto it, stop the monitored node, and confirm the down
  reaches only the route's sinks with the route mention in Slack; then `down`; verify
  by the received messages
