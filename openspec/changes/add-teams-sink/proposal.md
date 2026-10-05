# Proposal

## Why

Operators who watch incidents in Microsoft Teams cannot use the stock
Workflows webhook. Office 365 Connectors, the old channel Incoming Webhook,
were switched off in May 2026, and the replacement template expects an
Adaptive Card envelope. Posting tail-watch's raw event JSON to that URL
returns `400`. A generic `webhook` sink still works for a flow the operator
designs, and it leaves everyone else to build that flow themselves.

## What Changes

- Add a `type: teams` sink that POSTs one Adaptive Card, wrapped in the
  Workflows message envelope, to a webhook URL held in `url_env` or
  `url_file`.
- Render the same facts as the Slack sink: down, recovered, flapping,
  collector started, and test, including layer, learned targets, dependents,
  and a `status_url` link.
- Group events that arrive within `group_wait` into one card (at most 10
  events, then "and N more") and send at most one request per second.
- Reject `batch`, `max_batch`, `secret_env`, `secret_file`, `headers_env`,
  and `mention_on_page` on a Teams sink. A route `mention` is not printed.
- Document the sink, add an example, and record the design-doc deviation.

This follows the rendering and delivery pattern in design-doc sections
"Slack" and "Delivery" under "Notifications". It deviates from that chapter's
statement that the MVP has two sink types, and from "Phases", which names
PagerDuty and a Slack bot for phase 2 and does not name Teams. The sink is
the same class of work as the phase 1 Slack incoming webhook: a pasted URL,
no OAuth, no new dependency.

Non-goals:

- Phase 2: PagerDuty (Events API v2), Slack bot-token mode with threaded
  recoveries, emitting `target.degraded`, and a Teams bot or Graph client
  that could edit a card or reply in a thread.
- Phase 3: sharing incident state across collectors.
- Office 365 Connector MessageCard payloads. Connectors are retired, and the
  stock Workflows template does not render them as the card.
- A configurable card schema. A flow that wants the raw event uses
  `type: webhook`.
- `@` mentions. Workflows cards need an Entra object id and still often do
  not ring the person. Page severity is a red headline.

## Capabilities

### New Capabilities

- `teams-sink`: Teams Workflows webhook configuration, Adaptive Card
  rendering, grouping, status links, and the shared delivery policy.

### Modified Capabilities

None. `notify-test` already sends through each selected sink's real delivery
code, so a Teams sink is covered without a requirement change. Routing,
queues, retries, and the JSON payload stay as specified.

## Impact

- Code: new `cmd/tail-watch/teams.go` and `teams_test.go`. `config_sinks.go`
  accepts `type: teams` and its key rules. `sink.go` constructs it.
  `notify.go` and `notifytest.go` group it the way they group Slack.
- Config: `type: teams` plus the existing `url_env` / `url_file`,
  `status_url`, `group_wait`, and the shared routing and queue keys. The
  webhook URL is a bearer secret (its `sig` query). Logs keep scheme and
  host only.
- Docs: a how-to, the sink tables in the config reference, the docs index,
  an example sink, and a short Teams subsection in the design doc.
- Network: outbound HTTPS to the workflow host, through the same sink client
  as Slack (no redirect following, 10s timeout).
- Dependencies: none. The public event payload is unchanged.
- UI: unchanged. `GET /sinks` already returns the configured `type`.
