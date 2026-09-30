# Design

## Context

`app.js` reads `#f-checks` (a textarea) in `collect()` and `openEditor()`, splits it on
newlines, and posts `checks` as a list of strings. `showErrors` already maps a server
message `checks[i]: ...` to the field `checks` and prefixes it "Line i+1". `checks.go`
defines the accepted strings: `icmp`, `tsping`, `tcp:<1-65535>`,
`http:<http(s) URL>[|<text>]`, where the URL host must equal the target's `addr`.

## Decisions

### Rows are a view of the existing strings

The API, `targets.json` and the config file keep the string form. Two pure functions in
`app.js` convert between them: `checkToString(row, addr)` and `checkFromString(s, addr)`.
They touch no DOM, so a test can run them under bun or node, as `ui_route_test.go` does
for route matching. Alternative: change the API to structured checks. Rejected: it would
fork the format from the config file and need a migration for `targets.json`.

### Row types

| Type | Fields | String |
| --- | --- | --- |
| ICMP | none | `icmp` |
| TSPing | none | `tsping` |
| TCP | port | `tcp:<port>` |
| HTTP | scheme (`http` or `https`), port (optional), path (default `/`), must-contain text (optional) | `http:<scheme>://<host>[:<port>]<path>[\|<text>]` |
| Custom | raw string | the raw string |

The type menu offers the first four. Custom appears only when a string cannot be parsed
into one of them, so the menu stays short.

### HTTP host comes from Address at submit time

`checkToString` builds the URL from the current Address value, wrapping an IPv6 literal in
brackets and leaving a name or IPv4 address as is. The host is never stored in the row, so
changing Address cannot leave a stale host, and the server rule that the host equals the
address cannot be broken from the UI. A path that does not start with `/` gets one. A path
that contains `|` is sent as typed and the server rejects it on that row.

### Parsing an existing string

`checkFromString` matches the four forms with anchored patterns. An `http:` string becomes
an HTTP row only when its host equals the Address, ignoring case, a trailing dot and IPv6
brackets, and it has no fragment; otherwise it becomes a Custom row. A port is kept as
written, so `:80` survives a round trip. A `tcp:` string with a non-numeric port, and any
unknown string such as `tls:...`, becomes a Custom row so the server's own message is what
the operator sees.

### Rows are never dropped, so indexes match

Every row is serialized, including incomplete ones: an empty TCP port becomes `tcp:` and
the server answers `checks[0]: invalid TCP port ""`. Because no row is skipped, the index
in a server message is the row index, and `showErrors` can put the message on that row.
A message with no index (`checks: ... checks must not be empty`) is shown under the list
next to the "Add check" button.

### Server validation stays the only rule

The UI does not block a submit with zero rows and does not check ports or paths. This keeps
validation parity with the config file and lets the Playwright test exercise the real error
path.

### Markup and accessibility

`#f-checks` becomes a `<div role="group" aria-labelledby>` holding the rows and the button,
so `collect()` and the label keep one id. Each row is a group labelled "Check N", its remove
button has an accessible name "Remove check N", and a new row takes focus on its type
select. The page stays plain HTML with `el()` and `textContent`, no `innerHTML`, under the
existing Content Security Policy.

### Deviation from the design doc

None. The check syntax in the "Check types" section is unchanged; only the form that
produces it changes.

## Risks / Trade-offs

- [A path with a query string or unusual characters] → the path field is free text and is
  appended as typed; the server validates it.
- [`http://host` with no path is stored and is shown as path empty] → serialized back as
  `/`, which checks the same resource.
- [Custom rows mean the UI can still show raw syntax] → only for strings it cannot parse,
  which today would have been invisible data loss.
- [A new control set is more markup to keep accessible] → covered by role and name
  assertions in the Playwright tests.

## Open Questions

- None that change what gets built.
