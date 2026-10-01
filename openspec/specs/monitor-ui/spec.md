# monitor-ui Specification

## Purpose

Gives an operator a small browser page, served by the collector itself, to see every
monitor and to add, edit and delete the ones created through the UI.

## Requirements

### Requirement: UI availability

The collector SHALL serve the monitor UI at `/ui/` only when the UI is on, and SHALL
serve `/` as the prober status page whether or not it is on. When the UI is off,
requests under `/ui/` and `/api/v1/` SHALL return 404 and change nothing. The UI SHALL be
part of the binary and SHALL NOT load scripts, styles, fonts or other resources from any
other origin. Responses under `/ui/` SHALL send a `Content-Security-Policy` that permits
only same-origin resources (`default-src 'self'; base-uri 'none'; form-action 'self';
frame-ancestors 'none'`), `X-Content-Type-Options: nosniff` and `Cache-Control: no-cache`.

#### Scenario: UI off

- **WHEN** the collector runs without `--ui` and `collector.ui` unset
- **THEN** `GET /ui/` and `POST /api/v1/targets` return 404, and `GET /` still returns
  the status page

#### Scenario: UI on

- **WHEN** the collector runs with `--ui` and a `state_dir`
- **THEN** `GET /ui/` returns the page and `GET /` still returns the status page

#### Scenario: No external requests

- **WHEN** the page is loaded
- **THEN** every request the page makes goes to the collector's own origin

### Requirement: Monitor list

The page SHALL list every monitor with its name, address, site, severity, checks, source
and current alert state, and SHALL refresh the list without a page reload. YAML monitors
SHALL be shown as read-only, with no edit or delete control.

#### Scenario: State updates

- **WHEN** a listed monitor goes `DOWN`
- **THEN** its row shows `DOWN` without the operator reloading the page

#### Scenario: YAML monitor is read-only

- **WHEN** a monitor has `source: config`
- **THEN** its row has no edit or delete control and is marked as coming from the config
  file

### Requirement: Add and edit form

The page SHALL provide a form to add a monitor and to edit a UI-created one, covering
`name`, `addr`, `site`, `severity`, `parent`, `network`, `insecure_tls`, `checks`,
`interval`, `timeout`, `fail_threshold`, `recover_threshold`, `flap_limit` and
`flap_window`. The name field SHALL be read-only when editing. Server validation errors
SHALL be shown next to the field they name. The parent field SHALL offer only monitors
that have exactly one `icmp` or one `tsping` check.

The `checks` field SHALL be a list of check rows, not text in the check syntax. The list
SHALL start empty when the form opens to add a monitor. The operator SHALL add a row with
an "Add check" control and remove any row with its own control. A row SHALL have a type of
ICMP, TSPing, TCP or HTTP. A TCP row SHALL have a port. An HTTP row SHALL have a scheme
(`http` or `https`), an optional port, a path and an optional text the body must contain,
and its host SHALL be taken from the address field when the form is submitted, with an IPv6
address in brackets. On submit the form SHALL send each row, in order and without
dropping any, as the existing check string (`icmp`, `tsping`, `tcp:<port>`,
`http:<url>[|<text>]`). The form SHALL NOT reject an empty list itself: the server's
validation SHALL decide. A server error that carries a row index SHALL be shown on that
row, and one without an index SHALL be shown with the list. When editing, the form SHALL
show the monitor's existing check strings as rows. A string that no row type can express,
or an HTTP string whose host is not the address, SHALL be shown as a Custom row holding
the raw string and SHALL be sent unchanged.

#### Scenario: Validation error shown per field

- **WHEN** the operator submits a TCP row with port `70000`
- **THEN** the form shows the server's error on that row and creates nothing

#### Scenario: Name is fixed on edit

- **WHEN** the operator edits an existing UI-created monitor
- **THEN** the name field cannot be changed

#### Scenario: Form opens with no checks

- **WHEN** the operator opens the form to add a monitor
- **THEN** the checks list has no rows and an "Add check" control

#### Scenario: Empty checks fail, then a check succeeds

- **WHEN** the operator fills name and address, saves with no rows, then adds a TCP row
  with port `445` and saves
- **THEN** the first save creates nothing and shows the server's "checks must not be
  empty" message with the list, and the second save creates the monitor with `tcp:445`

#### Scenario: Remove a row

- **WHEN** the operator adds two rows and removes the first
- **THEN** only the second remains and is the one sent

#### Scenario: Incomplete row is reported on its row

- **WHEN** the operator adds an ICMP row and a TCP row with no port and saves
- **THEN** the error is shown on the second row and the first row is unmarked

#### Scenario: HTTP row takes its host from the address

- **WHEN** the address is `10.20.0.50` and the operator adds an HTTP row with scheme
  `https`, port `5001`, path `/` and text `DSM`
- **THEN** the form sends `http:https://10.20.0.50:5001/|DSM`

#### Scenario: HTTP host follows an address change

- **WHEN** the operator adds an HTTP row and then changes the address from `nas` to `nas2`
- **THEN** the form sends a URL whose host is `nas2`

#### Scenario: IPv6 address is bracketed

- **WHEN** the address is `fd7a:115c:a1e0::1` and the operator adds an HTTP row with path
  `/`
- **THEN** the form sends `http:http://[fd7a:115c:a1e0::1]/`

#### Scenario: Edit shows rows

- **WHEN** the operator edits a monitor stored with `icmp`, `tcp:445` and
  `http:https://10.20.0.50:5001/|DSM` at address `10.20.0.50`
- **THEN** the form shows an ICMP row, a TCP row with port `445`, and an HTTP row with
  scheme `https`, port `5001`, path `/` and text `DSM`

#### Scenario: Unparseable check kept as a Custom row

- **WHEN** the operator edits a monitor whose stored check is `tls:443`
- **THEN** the form shows a Custom row holding `tls:443` and sends it unchanged, and the
  server's error for it is shown on that row

#### Scenario: HTTP check for another host kept as a Custom row

- **WHEN** a stored check is `http:http://other.lab/` and the address is `nas.lab`
- **THEN** the form shows it as a Custom row, not as an HTTP row with a different host

### Requirement: Delete confirmation

Deleting from the page SHALL ask the operator to confirm. When the monitor is `DOWN`,
the confirmation SHALL say that deleting it closes its open incident.

#### Scenario: Confirm deleting a down monitor

- **WHEN** the operator deletes a monitor that is `DOWN`
- **THEN** the page states that its open incident will be closed and deletes only after
  confirmation

#### Scenario: Cancelled delete

- **WHEN** the operator cancels the confirmation
- **THEN** the monitor is unchanged

### Requirement: Address suggestions

The add and edit form SHALL offer the peers from the peer list as suggestions for the
address field. Choosing a suggestion SHALL fill the address field with the peer's `name`, or
with its first Tailscale IP when the peer has no name, and SHALL also fill the name field
with that same value when the name field is empty or still holds a value the suggestions
filled earlier. A name the operator typed SHALL NOT be overwritten, and the name field of
an existing monitor SHALL NOT change. A suggestion SHALL show the peer's Tailscale IP, tags
and online state so the operator can tell peers apart. The suggestions SHALL be fetched when
the form opens, and free-text entry SHALL always remain possible: an empty list, an
unseeded list, or a failed fetch SHALL NOT block, change or delay submitting an address the
operator typed. When the typed address is an IP address that falls inside the `routes` of a
peer, the form SHALL show which peer handles it. The parent field SHALL be unchanged by
suggestions.

#### Scenario: Pick a peer

- **WHEN** the operator opens the form and chooses the suggestion `lab-nas`
- **THEN** the address field holds `lab-nas` and the name field holds `lab-nas`

#### Scenario: Typed name is kept

- **WHEN** the operator types the name `nas` and then chooses the suggestion `lab-nas`
- **THEN** the address field holds `lab-nas` and the name field still holds `nas`

#### Scenario: Another suggestion replaces a filled name

- **WHEN** the operator chooses `lab-nas` and then chooses `nyc-router`
- **THEN** both fields hold `nyc-router`

#### Scenario: Editing keeps the name

- **WHEN** the operator edits an existing monitor and chooses a suggestion
- **THEN** the address field changes and the name field does not

#### Scenario: Suggestion shows what distinguishes peers

- **WHEN** two peers are offered
- **THEN** each suggestion shows its Tailscale IP, its tags and whether it is online

#### Scenario: Empty or unseeded list

- **WHEN** the peer list is empty or `seeded` is false
- **THEN** the form shows no suggestions and the operator can type any address and
  submit it

#### Scenario: Peer list cannot be fetched

- **WHEN** the request for the peer list fails
- **THEN** the form still opens, no error blocks it, and a typed address is submitted as
  usual

#### Scenario: Address behind a subnet router

- **WHEN** the operator types `10.20.0.50` and `nyc-router` is primary for `10.20.0.0/16`
- **THEN** the form shows that `nyc-router` handles that address, and the parent field
  is not changed

#### Scenario: Address handled by no peer

- **WHEN** the operator types `192.168.99.1` and no peer routes it
- **THEN** the form shows no handling peer

### Requirement: Probe-gated save

Before saving from the add or edit form, the page SHALL send the definition to
`POST /api/v1/probe`. When every check is `ok`, the page SHALL save without further
confirmation. When any check is not `ok`, the page SHALL ask the operator to confirm,
listing each failing check with its error, and SHALL save only on confirmation;
declining SHALL return to the form with every value intact and the rows of the failing
checks marked. A static validation failure from the probe SHALL be shown as field and
row errors and SHALL save nothing, as a failed save does today. When the probe call
itself cannot be made or fails, the page SHALL show that error and SHALL save nothing.
The gate SHALL apply equally to adding and to editing a monitor.

#### Scenario: Passing probe saves immediately

- **WHEN** the operator saves a new monitor whose checks all pass the probe
- **THEN** the monitor is created with no extra confirmation and the list shows it

#### Scenario: Failing probe asks to confirm

- **WHEN** the probe reports `tcp:443` with `connection refused`
- **THEN** the page asks to confirm, naming `tcp:443` and its error, and saves only
  after the operator confirms

#### Scenario: Declining returns to the form

- **WHEN** the operator declines the confirmation
- **THEN** nothing is saved, the form keeps every entered value, and the row of the
  failing check is marked

#### Scenario: Invalid definition shows field errors

- **WHEN** the probe answers 422 for a TCP row with port `70000`
- **THEN** the error is shown on that row, no confirmation is offered, and nothing is
  saved

#### Scenario: Edit is gated too

- **WHEN** the operator edits a UI-created monitor so its check would fail and saves
- **THEN** the same confirmation appears and the replace happens only after it

#### Scenario: Probe unreachable

- **WHEN** the probe request itself fails
- **THEN** the page shows the error and saves nothing

### Requirement: Check control probes

The form's check control SHALL send the definition to `POST /api/v1/probe` and show
the per-check results, each check with its outcome and latency, rather than only
whether the definition is well-formed. The results shown SHALL NOT block saving: they
inform, and the save gate decides.

#### Scenario: Check shows per-check results

- **WHEN** the operator uses the check control on a definition with `icmp` and
  `tcp:443`, `icmp` passing and `tcp:443` refused
- **THEN** the form shows `icmp` as passing with its latency and `tcp:443` as failing
  with `connection refused`
