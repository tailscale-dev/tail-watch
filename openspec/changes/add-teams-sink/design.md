# Design

## Context

See proposal.md for why. The behavior contract is
`specs/teams-sink/spec.md`.

Slack and the generic webhook already share one delivery loop. `slack.go`
renders Block Kit and implements `sink`. `worker.grouped` and
`notifytest.sendTest` special-case `type == slack` (or webhook `batch`) for
the group window, the group cap, and the `bat_` id. `httpPost` in `sink.go`
does the POST: 10s timeout, no redirects, `redactURL` on errors.
`classifyStatus` already treats `2xx` as delivered, `429` as retry with
`Retry-After`, and other `4xx` as permanent. `describeSink` prints `type=`
from the config value, so `check-config` needs no Teams branch.

A Workflows "When a Teams webhook request is received" template posts an
Adaptive Card only when the body is a `type: message` envelope around the
card. The raw event payload, which `type: webhook` sends, is rejected.

## Goals / Non-Goals

**Goals:**

- One new renderer on the existing sink interface, client, and retry loop.
- One helper for "this sink groups messages", used by the worker and by
  `notify-test`, so the three Slack special cases do not grow a fourth copy.
- A golden Adaptive Card a reviewer can diff.

**Non-Goals:**

- A live Teams tenant in CI. `notify-test` against the operator's URL is the
  manual check. `TestLiveTeams` (opt-in, `TAIL_WATCH_LIVE_TEAMS=1`) posts
  real cards to a contributor's workflow without a tailnet, and
  `TestLiveNotifications` adds a Teams sink when a Teams URL is configured, as
  it does for Slack. Neither runs in CI.
- Changing `GET /sinks`. It already returns `type`. Mentions stay Slack-only
  in `sinkViewOf`.

## Decisions

### D1. Deviation from the design doc

The design doc, under "Notifications", says the MVP has two sink types, and
"Phases" assigns PagerDuty and a Slack bot to phase 2. Teams is the same
shape as the phase 1 Slack incoming webhook: a URL in `url_env` or
`url_file`, no OAuth, no new module. It is not the phase 2 bot (no thread,
no edit) and not PagerDuty (no `dedup_key`).

Reconcile the doc in this change. Leave the historical MVP sentence in
place, and add a "Teams" subsection after "Slack" that points at this sink:
the envelope, `group_wait`, the one-card-per-second cap, `status_url`, and
the rejected keys. Add one sentence that a Teams Workflows webhook was added
beside the Slack incoming webhook. Do not move Teams onto the phase 2 list.

MessageCard is intentionally absent. Connectors that required it were
switched off in May 2026, and the stock template does not render that body
as the card.

### D2. Renderer (`teams.go`)

`newTeamsSink` matches `newSlackSink`: config, collector info, and the
shared `http.Client` from `newSinkWithEnv`. `Send` is Slack's `Send`:
resolve `url_env` / `url_file`, set `Content-Type` and `User-Agent`, call
`httpPost`. No signature header and no `headers_env`.

The sink does not implement `mentionRenderer`. `renderBody` then keeps
`Render`, so a route `mention` stored on the incident never reaches the
card. Page severity is the headline color `Attention`.

`MinInterval` returns the same one-second floor Slack uses. Rename that
constant to `messageMinInterval` and use it from both sinks.

Card layout, one Adaptive Card per request:

- Envelope: `type: message`, one attachment, `contentType`
  `application/vnd.microsoft.card.adaptive`, card `$schema`
  `http://adaptivecards.io/schemas/adaptive-card.json`, `type: AdaptiveCard`,
  `version: 1.4`. Omit `contentUrl`.
- Each event is two `TextBlock`s with `wrap: true`. The first is the
  headline. Its `color` is `Attention` for `target.down`, `Good` for
  `target.recovered`, and `Warning` for `target.flapping`. Started and test
  omit `color`.
- The second block is the detail lines Slack already prints: address, site,
  parent, path kind, latency, error, down-since, failure count, collector,
  and the dependents line. Reuse `truncate` (300 bytes plus `…`),
  `humanDuration`, and `layerNote`. Join the lines with `\n\n`: TextBlock
  markdown treats a single `\n` as a soft break, and Microsoft's Teams
  formatting guide says to use `\n\n` for a newline outside a list.
- A group appends the events in queue order, at most 10, then one TextBlock
  `and N more`.

Alternative considered: a `FactSet` per event. It looks more native and
locks a second schema into the golden files. Two text blocks keep the card
aligned with the Slack line and with the spec's "card text includes"
scenarios.

### D3. Escaping and the status link

Teams renders `TextBlock.text` as markdown. `teamsEscape` inserts `\` before
`\`, `` ` ``, `*`, `_`, `[`, `]`, `(`, `)`, `<`, and `>`. Apply it to the
error, check, address, site, parent, and collector name before those strings
enter either block. Target names match `[A-Za-z0-9._-]` and are safe as link
text.

When `status_url` is set, the headline's target name is
`[name](<status_url>/#<name>)` after trimming trailing slashes. That link
is built from the name and the operator's URL, not passed through
`teamsEscape`. A `status_url` that itself contains a parenthesis or a space
produces a broken markdown link. Accept that. The documented value is an
absolute URL without either character, and adding a validator the spec does
not require is extra surface.

Do not put the check in a markdown link. An `http:` check contains `://`
and `|`, which is how Slack's auto-link broke the pipe. Escaping is the
whole defense.

### D4. Grouping seams

Add `SinkConfig.groupsMessages()`: true for `slack`, `teams`, and a webhook
with `batch`. Use it in:

- `worker.grouped`
- `worker.groupCap`, where `slack` and `teams` return `QueueSize` and a
  batched webhook returns `MaxBatch`
- `sendTest`, which allocates a `bat_` id for a grouped sink

`group_wait` is already the window. No new timer.

### D5. Config

`sinkTeams = "teams"`. The type switch accepts it. The unknown-type error
becomes `must be webhook, slack or teams`.

`resolveSinkTypeKeys` rejects, for Teams, the Slack rejection list
(`batch`, `max_batch`, `secret_env`, `secret_file`, `headers_env`) and
`mention_on_page`. `status_url` stays valid. Webhook still rejects both
`mention_on_page` and `status_url`.

No host allowlist. Workflow hosts include `logic.azure.com`,
`logic.azure.us`, `logic.azure.cn`, and Power Platform hosts, and Microsoft
adds hosts. Slack does not pin `hooks.slack.com` either. The URL is a
bearer secret because of `sig`. `redactURL` already drops the query in
process errors. Extend the live-test `redactSecrets` pattern so a workflow
URL or a `sig=` value printed in a test log is scrubbed. That helper is
test-only. Runtime logging stays on `redactURL`.

Startup does not warn about a missing HMAC secret for Teams. The warning in
`main.go` is already limited to `type: webhook`.

### D6. Tests

Golden files in `cmd/tail-watch/testdata/teams/`, regenerated with
`go test ./cmd/tail-watch -run 'TestTeamsGolden' -update`. Cases: down at
layer `service`, recovered, implicit router with 14 dependents, flapping,
the phishing-error escape, a 301-byte error, a `status_url` link with a
trailing slash, a route mention that must be absent, collector started, and
test.

Clock tests, copied from the Slack ones: 25 events inside `group_wait`
become one card with 10 events and "and 15 more"; two immediate `200`s are
at least one second apart; `429` with `Retry-After: 2` waits at least two
seconds and resends the same body.

Render, offline: `TestTeamsCardRender` (`teams_render_test.go`) renders every
golden card in Chromium the way the Adaptive Cards Designer's Teams host does:
Microsoft's `adaptivecards` renderer, the Designer's Teams light host config,
and `markdown-it`, vendored under `testdata/teams/render/`. It fails on a
parse or validation message, a visible escape or markup, a link other than
the status link, merged detail lines, or a wrong headline color. It skips
without the Playwright driver, like the UI browser tests. It cannot see the
Teams client itself, so a real channel stays the last check.

Live, opt-in: `TestLiveTeams` reads the URL from `TEAMS_WEBHOOK_URL` or the
pulumi env named by `TAIL_WATCH_TEAMS_PULUMI_ENV`, sends a `notify-test` card,
a grouped card of the golden events, and a 12-event overflow card, and
requires `2xx` (a Workflows trigger answers `202`). Whether the cards render
is checked by eye in the channel. `redactSecrets` scrubs `logic.azure.com`
and `powerplatform.com` workflow URLs and any `sig=` value from its logs.

Config table rows: `batch`, `mention_on_page`, both URL keys, and a valid
Teams sink that `check-config` prints as `type=teams` without the URL.

## Risks / Trade-offs

- [A future template expects a different body] → A `400` is permanent and
  counted. The golden file is the contract we send. The how-to names the
  stock template.
- [The flow is owned by one user and goes silent when they leave] → The
  how-to says to add a co-owner. The collector cannot see ownership. Failed
  delivery shows up in the existing drop and failure metrics.
- [Markdown in an error string becomes a link] → D3 escapes the untrusted
  fields. The only link we add is `status_url`.
- [A card over Teams' size limit] → Ten events and a 300-byte error stay
  far under the limit. Overflow past ten is the "and N more" line, not more
  text.
- [The `sig` query leaks into a test log] → `redactURL` on the runtime path,
  and a wider `redactSecrets` for the live-test logs.
- [Rollback of the binary] → An old binary rejects `type: teams` at load.
  Remove the sink from the config before rolling the binary back. Adding
  the sink is opt-in, so existing configs are unaffected.

## Migration Plan

1. Ship the sink. Existing configs load as before.
2. An operator creates a Workflows webhook from the stock template, stores
   the URL in `url_env` or `url_file`, adds `type: teams`, and runs
   `notify-test`.
3. Rollback of the config is deleting that sink. Rollback of the binary
   requires that deletion first.

## Open Questions

None. Mentions, MessageCard, and a custom card schema are non-goals in the
proposal, not deferred decisions.
