# Spec Delta

## ADDED Requirements

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
