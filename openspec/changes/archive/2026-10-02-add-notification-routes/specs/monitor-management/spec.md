# Spec Delta

## MODIFIED Requirements

### Requirement: Dynamic monitor store

When the UI is on, the collector SHALL persist monitors created through the API
("dynamic monitors") in `targets.json` inside `collector.state_dir`, using the same
field names as a YAML target (`name`, `addr`, `site`, `severity`, `route`, `parent`,
`insecure_tls`, `interval`, `timeout`, `fail_threshold`, `recover_threshold`,
`flap_limit`, `flap_window`, `network`, `checks`). The file SHALL be replaced
atomically on every change and SHALL NOT be readable by group or others. At startup the
collector SHALL register dynamic monitors after the YAML targets and the UI routes,
applying the YAML file's `defaults`. A missing file SHALL mean no dynamic monitors. When the UI is off, the collector SHALL
NOT run or modify dynamic monitors and SHALL log a warning if `targets.json` exists. A file that cannot be
parsed SHALL stop startup with an error naming the file. An individual entry that fails
validation, whose name matches a YAML target, or whose `route` names no loaded route,
SHALL be skipped with a logged warning and SHALL remain in the file untouched.

#### Scenario: Dynamic monitors survive a restart

- **WHEN** a monitor `lab-nas` is added through the API and the collector restarts
- **THEN** `lab-nas` is registered again and checked without further action

#### Scenario: Open incident survives a restart

- **WHEN** dynamic monitor `lab-nas` has an open incident and the collector restarts
- **THEN** the incident is restored and no second page is sent, as for a YAML target

#### Scenario: Name clash with a YAML target

- **WHEN** `targets.json` holds `lab-nas` and the YAML file also defines `lab-nas`
- **THEN** the YAML target is used, a warning names the skipped entry, and the entry
  stays in `targets.json`

#### Scenario: UI off leaves the store alone

- **WHEN** the collector starts without the UI and `targets.json` exists
- **THEN** its monitors are not run, the file is neither read for changes nor
  modified, and a startup warning says how many dynamic monitors are inactive

#### Scenario: Corrupt store

- **WHEN** `targets.json` is not valid JSON
- **THEN** startup fails with an error naming `targets.json`

#### Scenario: Monitor on a UI route survives a restart

- **WHEN** dynamic monitor `pg-primary` names UI route `db-team` and the collector
  restarts
- **THEN** `pg-primary` is registered again with route `db-team`

### Requirement: Validation parity with the config file

A monitor definition SHALL be accepted only if the config loader would accept the same
target in the YAML file alongside the existing targets: the same check syntax, name
characters, interval, timeout and threshold rules, and the same `parent` rules (the
parent names another target, has exactly one `icmp` or one `tsping` check, and creates
no cycle). Per-monitor values SHALL override the YAML file's `defaults`. A monitor's
`route` SHALL name a YAML route or a UI route, and a monitor SHALL NOT set both `route`
and `severity`.

#### Scenario: Parent must be a reachability target

- **WHEN** a create request names a parent whose checks are `tcp:445`
- **THEN** the response is 422 and the error says the parent must have exactly one
  `icmp` or `tsping` check

#### Scenario: Dynamic parent

- **WHEN** a monitor names another dynamic monitor with a single `icmp` check as
  parent
- **THEN** the response is 201 and dependency suppression applies as for YAML targets

#### Scenario: Unknown route

- **WHEN** a create request sets `route: db-tem` and no such route exists
- **THEN** the response is 422 with an error at `route` naming `db-tem`
