# Spec Delta

## MODIFIED Requirements

### Requirement: Monitor list

The page SHALL list every monitor with its name, address, site, routing, checks, source
and current alert state, and SHALL refresh the list without a page reload. The routing
column SHALL show the route name and its severity, or the severity alone with "all sinks"
for an unrouted monitor. YAML monitors SHALL be shown as read-only, with no edit or
delete control.

#### Scenario: State updates

- **WHEN** a listed monitor goes `DOWN`
- **THEN** its row shows `DOWN` without the operator reloading the page

#### Scenario: YAML monitor is read-only

- **WHEN** a monitor has `source: config`
- **THEN** its row has no edit or delete control and is marked as coming from the config
  file

#### Scenario: Routing column

- **WHEN** monitor `pg-primary` names route `db-team` (`page`) and `nyc-printer` has
  `severity: warn` and no route
- **THEN** `pg-primary`'s row shows `db-team` and `page`, and `nyc-printer`'s row shows
  `warn` and "all sinks"

### Requirement: Add and edit form

The page SHALL provide a form to add a monitor and to edit a UI-created one, covering
`name`, `addr`, `site`, routing (a `route`, or a `severity` with no route), `parent`,
`network`, `insecure_tls`, `checks`, `interval`, `timeout`, `fail_threshold`,
`recover_threshold`, `flap_limit` and `flap_window`. The name field SHALL be read-only when editing. Server validation errors
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

## ADDED Requirements

### Requirement: Route picker

The form's routing field SHALL be one control offering every route, YAML and UI, and
two unrouted choices, `page` and `warn`, which send to every sink. Each route option SHALL
show the route's name, severity and sink names. Choosing a route SHALL send `route` and no
`severity`; choosing an unrouted choice SHALL send `severity` and no `route`. When the
form opens to add a monitor, the unrouted `page` choice SHALL be selected. When editing,
the monitor's stored route or severity SHALL be selected. If the routes cannot be fetched,
the unrouted choices SHALL still be offered and the form SHALL still save.

#### Scenario: Route option shows its destinations

- **WHEN** route `db-team` has severity `page` and sinks `pagerduty` and `ops-slack`
- **THEN** its option shows `db-team`, `page`, `pagerduty` and `ops-slack`

#### Scenario: Choosing a route

- **WHEN** the operator chooses `db-team` and saves
- **THEN** the definition sent has `route: db-team` and no `severity`

#### Scenario: Routes unavailable

- **WHEN** the request for routes fails
- **THEN** the routing field offers `page` and `warn` and the monitor can be saved

### Requirement: Severity explanation

The form SHALL explain, next to the routing field and without the operator opening
anything, what each severity means: `page` is for failures someone must act on now,
reaches sinks that accept page events, and carries a Slack mention when one is
configured; `warn` is for failures that should be seen but never wake anyone, and is
dropped by sinks whose `min_severity` is `page`. It SHALL also say that flapping is always
sent as `warn`, and that a recovery goes wherever its down went.

#### Scenario: Explanation visible

- **WHEN** the operator opens the add form
- **THEN** the text describing `page` and `warn` is shown beside the routing field

### Requirement: Routing preview in the form

The form SHALL show, for the current values, where a `target.down` and a
`target.flapping` would be sent, using `POST /api/v1/route-preview`. Each sink SHALL be
shown as sent, dropped with the reason in words (for example "not in route", "filtered by
site", "below this sink's minimum severity"), or unknown. The preview SHALL update when the
routing field, site, name or address changes, without the operator saving. When every sink
drops the `target.down`, the form SHALL warn that this monitor's outage would notify no
one. The preview SHALL inform only: it SHALL NOT block or change saving, and a failed
preview SHALL NOT block the form.

#### Scenario: Preview names a dropped sink

- **WHEN** the operator picks route `db-team` (`[pagerduty, ops-slack]`) for a monitor
  in site `lab`, and `pagerduty` matches only site `nyc`
- **THEN** the preview shows `ops-slack` as sent and `pagerduty` as filtered by site

#### Scenario: Nobody would be told

- **WHEN** every sink would drop the monitor's `target.down`
- **THEN** the form shows a warning that this monitor's outage would notify no one, and
  saving is still possible

#### Scenario: Preview follows edits

- **WHEN** the operator changes the site from `lab` to `nyc`
- **THEN** the preview updates and shows `pagerduty` as sent

### Requirement: Routes page

The UI SHALL provide a Routes page, reachable from the monitor page, that lists every
sink read-only with its name, type and filters, and every route with its name, severity,
sinks, mention, source and the monitors that use it. A route with `missing_sinks` SHALL
show those sinks as missing. YAML routes SHALL be read-only. The page SHALL let the
operator add a UI route, edit one (the name fixed), and delete one, choosing sinks from
the configured sink names. Server validation errors SHALL be shown next to the field they
name. Deleting SHALL ask to confirm; a refused delete SHALL show the monitors that still
use the route. The page SHALL say that sinks are defined in the config file.

#### Scenario: Add a route

- **WHEN** the operator adds route `db-team` with severity `page` and sink `db-hook`
- **THEN** the route is created and appears as an option in the monitor form's routing
  field

#### Scenario: Route in use cannot be deleted

- **WHEN** the operator deletes `db-team` while `pg-primary` uses it
- **THEN** the page shows that `pg-primary` still uses it and the route remains

#### Scenario: YAML route is read-only

- **WHEN** a route has `source: config`
- **THEN** it has no edit or delete control and is marked as coming from the config file

#### Scenario: Missing sink shown

- **WHEN** a UI route reports `missing_sinks: ["db-hook"]`
- **THEN** its row marks `db-hook` as missing from the config
