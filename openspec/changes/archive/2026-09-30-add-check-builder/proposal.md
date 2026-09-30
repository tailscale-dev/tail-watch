# Proposal

## Why

The monitor UI's Checks field is a textarea holding the check syntax, one check per line
(`icmp`, `tcp:445`, `http:https://10.20.0.50:5001/|DSM`). An operator has to know the four
check kinds, the `tcp:<port>` and `http:<url>|<text>` forms, and the rule that the URL
host must equal the address. A typo shows up only as a server error after Save. The syntax
is the storage and config-file format, so the form exposes an internal encoding that the
operator should not need to learn.

## What Changes

- Replace the Checks textarea in the add and edit form with a list of check rows. The list
  starts empty. An "Add check" button appends a row, and each row has a remove button.
- A row has a type: ICMP, TSPing, TCP or HTTP. TCP shows a port field. HTTP shows a scheme
  select, an optional port, a path and an optional "must contain" text. The host of an
  HTTP check always comes from the Address field, so it cannot differ from the address.
- The form turns the rows into the existing check strings on submit and turns the strings
  of an existing monitor back into rows on edit. A string no row can express (for example a
  hand-edited `targets.json` entry) is shown as a read-and-edit "Custom" row holding the raw
  string, so nothing is lost on save.
- Server validation is the only rule. A submit with no rows is sent as it is and fails with
  the server's "checks must not be empty" message next to the list. A rejected check is
  reported on its own row, using the index the server already returns.
- Add Playwright tests for the list: the empty submit fails, then adding a check succeeds;
  per-row errors; HTTP URL building, including IPv6; editing; and the Custom row.
- Phase 1 follow-up to the opt-in monitor UI. The design doc's "Check types" section
  defines the check syntax, which is unchanged. No phase 2 or 3 item is involved and none is
  pulled in.

## Capabilities

### Modified Capabilities

- `monitor-ui`: the add and edit form takes checks as typed rows, not as syntax.

## Impact

- `cmd/tail-watch/ui/index.html`, `ui/app.js`, `ui/style.css`.
- Tests: `ui_browser_test.go` (new cases, and the existing ones that fill `#f-checks`), a
  new test for the pure serialize and parse functions.
- Docs: `docs/how-to/monitor-ui.md` and its form-error screenshot, `CLAUDE.md`.
- No change to the HTTP API, `targets.json`, the config file, or any check behavior.

## Non-Goals

- A `tls:` check (still rejected by the server), editing config-file monitors, and moving
  any validation into the browser.
