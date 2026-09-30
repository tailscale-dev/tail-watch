# Spec Delta

## MODIFIED Requirements

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
