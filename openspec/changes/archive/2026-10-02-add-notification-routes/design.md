# Design

## Context

See proposal.md - Why. Today routing is one function: `notifier.routeLocked`
(`notify.go`) walks `cfg.Sinks` in order and keeps every sink whose
`SinkConfig.accepts` (`event.go`) passes. Both incident-opening events and flap
notices (`enqueueLocked`) go through it, and a recovery reuses the sink set its
incident recorded. Severity is fixed when the event is built: `newDownEvent` reads
`tr.Target.Severity`, from the `Target` copy the alerter's transition carries. Slack
picks one mention per message (`renderSlack`): `cfg.MentionOnPage` when any event in
the group is a page-severity down.

The config is loaded once per process; there is no reload. UI monitors live in
`targets.json` (`dynamic.go`): atomic temp-file-and-rename writes, a lenient JSON
decoder, and a `mergeDynamic` that validates each entry against the YAML targets and
skips the bad ones. `monitorManager` (`monitor_api.go`) owns them under one mutex and
always persists before it applies. `monitor_guard.go` wraps handlers with content-type,
origin, authorization and audit checks; an empty `op` means validate-only.

The source design (`tailnet-reachability-monitor-design.md`, "Notifications" and
"Events") routes only by sink filters, with `severity` on the target. This change keeps
that path as the unrouted default, and adds routes as a narrowing step before it. That
is the one deviation from the design doc: severity can now come from a route instead
of the target, and a target can limit which sinks it reaches.

## Goals / Non-Goals

**Goals:**

- One routing function that both the notifier and the preview call, so the preview
  can never disagree with delivery.
- Route edits take effect on the next event, with no restart, and never block the
  notifier's intake path.
- Today's configs, `targets.json` files and payloads behave exactly the same when no
  route is used.

**Non-Goals:**

- Config hot reload. Sinks stay fixed for the process lifetime, so `missing_sinks`
  can only arise at startup.
- Adding the route name to the event payload. Receivers see `severity` as before.
- Any change to recovery routing. Recoveries already follow their incident's
  recorded sinks.

## Decisions

### Targets name a route and events resolve it when they are built

`Target` gains `Route string`. The loader copies the route's severity onto
`Target.Severity`, but only as a load-time default: the preview reports it, and an
event falls back to it if the route is gone. `newDownEvent` and the flap path
resolve the route from a live route table when the event is built: severity from the
route (or `Target.Severity` when unrouted or when the route is missing), candidate
sinks from the route, and the mention from the route. The monitor list reads the
severity from the same table, so a route edit shows without re-registering anything.

Alternative: bake route severity and sinks into each `Target` when it registers.
Rejected. Editing a route would then mean re-registering every monitor that uses it,
and a replace closes incidents (`Edit replaces the monitor`). That turns a routing
edit into an outage-handling event.

### The route table is a copy-on-write snapshot

The manager builds an immutable `routeTable` (route name to route, with its sinks
already resolved to `SinkConfig` indexes) and publishes it through an
`atomic.Pointer`. The notifier loads the pointer inside `routeLocked` and event
construction. Writers publish only after `routes.json` is persisted, which keeps the
manager's persist-then-apply order.

Alternative: share a mutex between the manager and the notifier. Rejected. The
notifier's `book` mutex already orders every commit, and nesting a second lock into
it adds lock-order risk for a value that changes a few times a day.

### Routing and the preview share one function that reports why

`accepts` becomes `explain(ev, tags) (ok bool, reason string)`, with the reasons from
the spec (`events`, `min_severity`, `match.sites`, `match.targets`, `match.tags`).
`accepts` is kept as a thin wrapper. `routeLocked` first narrows to the route's
sinks (or all sinks), then calls `explain` on each candidate. The preview calls the
same narrowing step and `explain` on a synthetic event built with the real
constructors, over every sink. A sink that the narrowing step rejects reports
`not_in_route`.

Alternative: a separate preview evaluator. Rejected. It would drift, and AND
semantics only work for users if the preview is exactly right.

Tags for the preview come from the presence view, by looking up the draft's address
(peer name, MagicDNS name or Tailscale IP; for a routed LAN address, the handling
peer) without registering it. If no lookup applies, or the view is unseeded, any sink
with a `match.tags` filter reports `unknown`. Every other filter is still evaluated.

### Mentions travel with the queued event, not in the payload

The route's `mention` is resolved when the event is created and stored on the queued
record as an internal field. It is never marshalled into the event body, and it is
persisted in disk queues as an added field in `notification-state.json`, whose decoder
already tolerates added fields. `renderSlack` collects the distinct mentions of the
page-severity downs in a group, in queue order. An event without a route mention falls
back to the sink's `mention_on_page`. Each distinct mention is printed once.

This corrects the proposal's Impact line: `notification-state.json` gains one optional
field per queued event. Its incident records are unchanged.

Alternative: look the mention up from the route table at render time. Rejected. A
route edited between enqueue and render would change a message that `Render` is meant
to build deterministically, and that disk sinks replay byte for byte.

### `routes.json` is a sibling store that shares the file plumbing

The atomic write in `saveDynamic` is generalized into one helper,
`writeJSONAtomic(dir, file, v)`. Both stores call it. `routes.json` gets its own small
`dynamicRoute` type and `loadRoutes`/`mergeRoutes` beside the target store. The
decoder is lenient like the `targets.json` one, so a newer file still loads on an older
binary.

Startup order:

```text
load YAML (sinks, YAML routes, YAML targets; YAML targets may name only YAML routes)
  -> loadRoutes + mergeRoutes      (UI routes; missing sinks noted, not fatal)
  -> mergeDynamic                  (dynamic monitors may name any loaded route)
  -> newNotifier(routeTable)       (before the prober, as today)
```

`validateCandidate` takes the route set, so a monitor naming an unknown route, or
setting both `route` and `severity`, gets the same error path in the loader, the API
and the startup merge.

Alternative: put routes inside `targets.json`. Rejected. That changes the file's
top-level shape from a list to an object, which breaks older binaries reading it.

### The manager owns routes under its existing mutex

Route create, replace and delete live on `monitorManager`, which already holds the
monitor list. That makes `used_by` and the in-use delete refusal a read of state the
manager holds under the same lock, with no cross-component check. Handlers register
through `guard` with ops `route.create`, `route.replace` and `route.delete`.
`POST /route-preview` is registered with an empty op: validated, but neither
authorized nor audited. `GET /routes` and `GET /sinks` are registered without `guard`,
like `GET /targets`, so a plain GET needs no JSON `Content-Type`.

### The UI: one picker, one preview call, one new page

The routing `<select>` has an "All sinks" group (`page`, `warn`) and a "Routes" group,
and each route option's label lists its severity and sinks. The explanation is static
text beside the field. The preview is debounced (about 300 ms) on changes to the
routing field, site, name and address. A stale response is ignored by comparing a
request counter, as the peer fetch does.

The Routes page is a second view in the same `index.html`, selected by the URL hash,
so it needs no new embedded files or CSP change. Route rows follow the monitor rows'
read-only and confirm-on-delete patterns. Pure helpers (option labels, mapping a
reason to its words) go in a DOM-free block, so a `ui_*_test.go` can run them under
bun, as `ui_checks_test.go` does.

## Risks / Trade-offs

- [AND semantics can still yield a silent drop] → The preview names each drop and
  warns when no sink would get a down. `check-config` prints the candidate sinks.
  `how-to/troubleshoot.md` gets a "why didn't sink X get it" entry.
- [A missing sink fails open] → The route keeps working with its remaining sinks.
  There is a startup warning, and `missing_sinks` in the API and on the Routes page.
  A route left with zero sinks shows "notifies no one" in every preview that uses it.
- [Rollback to a binary without routes] → An older binary rejects a YAML config
  containing `routes:` or `route:` (unknown keys are rejected), so remove those first.
  `targets.json` entries with `route` load with it ignored, as unrouted `page`
  monitors. `routes.json` is ignored. The added queue field is ignored by older
  decoders.
- [Route mention lists can grow in a large group] → Each distinct mention is printed
  once. A group spanning many routes is rare, because `group_wait` is short.
- [The preview's tag lookup can differ from runtime for a monitor not yet registered]
  → It reports `unknown` instead of guessing, and the runtime path is unchanged.

## Migration Plan

No migration is needed. With no `routes:` and no `route` keys, every code path
reduces to today's: all sinks are candidates, severity comes from the target, and the
sink's `mention_on_page` applies. To roll out:

1. Upgrade.
2. Define routes in YAML or on the Routes page.
3. Move monitors to them one at a time, checking the preview as you go.

To roll back, follow the risk entry above.
