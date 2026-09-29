# Spec Delta

## Purpose

Gives an operator a small browser page, served by the collector itself, to see every
monitor and to add, edit and delete the ones created through the UI.

## ADDED Requirements

### Requirement: UI availability

The collector SHALL serve the monitor UI at `/ui/` only when the UI is on, and SHALL
serve `/` as the prober status page whether or not it is on. When the UI is off,
requests under `/ui/` and `/api/v1/` SHALL return 404 and change nothing. The UI SHALL be
part of the binary and SHALL NOT load scripts, styles, fonts or other resources from any
other origin. Responses under `/ui/` SHALL send a `Content-Security-Policy` that permits
only same-origin resources.

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

#### Scenario: Validation error shown per field

- **WHEN** the operator submits `tcp:70000` as a check
- **THEN** the form shows the server's error next to the checks field and creates
  nothing

#### Scenario: Name is fixed on edit

- **WHEN** the operator edits an existing UI-created monitor
- **THEN** the name field cannot be changed

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
