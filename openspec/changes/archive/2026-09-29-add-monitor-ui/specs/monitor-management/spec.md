# Spec Delta

## Purpose

Lets an operator add, edit and delete monitors on a running collector through a JSON
API, keeps those monitors across restarts, and leaves YAML-defined targets untouched.

## ADDED Requirements

### Requirement: Dynamic monitor store

When the UI is on, the collector SHALL persist monitors created through the API
("dynamic monitors") in `targets.json` inside `collector.state_dir`, using the same
field names as a YAML target (`name`, `addr`, `site`, `severity`, `parent`,
`insecure_tls`, `interval`, `timeout`, `fail_threshold`, `recover_threshold`,
`flap_limit`, `flap_window`, `network`, `checks`). The file SHALL be replaced
atomically on every change and SHALL NOT be readable by group or others. At startup the
collector SHALL register dynamic monitors after the YAML targets, applying the YAML
file's `defaults`. A missing file SHALL mean no dynamic monitors. When the UI is off, the collector SHALL
NOT run or modify dynamic monitors and SHALL log a warning if `targets.json` exists. A file that cannot be
parsed SHALL stop startup with an error naming the file. An individual entry that fails
validation, or whose name matches a YAML target, SHALL be skipped with a logged warning
and SHALL remain in the file untouched.

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

### Requirement: Monitor API

When the UI is on, the collector SHALL serve under `/api/v1/`:
`GET /targets` returning every monitor, YAML and dynamic; `POST /targets` creating a
dynamic monitor; `PUT /targets/{name}` replacing one; `DELETE /targets/{name}` removing
one; and `POST /validate` checking a monitor definition without applying it. Each entry
in the list SHALL carry `source` (`config` or `ui`), `readonly` (true for `config`), the
resolved settings, and the current alert `state`. Request and response bodies SHALL be
JSON. Successful creation SHALL return 201, replace 200, delete 204. A validation
failure SHALL return 422 with every problem in an `errors` list, each prefixed with the
field, such as `checks[1]`. A name that already exists, YAML or dynamic, SHALL be
refused on create with 409. A request body over 64 KiB, a body that is not valid JSON, or a body with a field that
is not a target field SHALL return 400 and change nothing. `POST /validate` naming a YAML
target SHALL return 409, as create does. Each list entry SHALL also carry `dependents`,
the names of monitors that name it as `parent`, and for a `ui` entry `definition`, the
stored entry the edit form starts from. A missing name SHALL return 404. An attempt to change or
delete a YAML target SHALL return 409 and change nothing. A body `name` that differs
from the path name on replace SHALL return 422.

#### Scenario: List includes both sources

- **WHEN** the config defines `nyc-nas` and a dynamic monitor `lab-nas` exists
- **THEN** `GET /api/v1/targets` lists both, `nyc-nas` with `source: config` and
  `readonly: true`, `lab-nas` with `source: ui` and `readonly: false`

#### Scenario: Create a monitor

- **WHEN** `POST /api/v1/targets` sends a valid definition for `lab-nas`
- **THEN** the response is 201, the monitor is running, and it appears in the list

#### Scenario: Invalid definition

- **WHEN** `POST /api/v1/targets` sends `tcp:70000` as a check and a timeout not below
  the interval
- **THEN** the response is 422 and `errors` names both problems with their fields

#### Scenario: Duplicate name

- **WHEN** a create request uses the name of an existing YAML target
- **THEN** the response is 409 and nothing changes

#### Scenario: Unknown field

- **WHEN** `POST /api/v1/targets` sends a body with a field that is not a target field
- **THEN** the response is 400 and nothing changes

#### Scenario: Read-only YAML target

- **WHEN** `PUT` or `DELETE` names a YAML target
- **THEN** the response is 409 and the target is unchanged

#### Scenario: Validate without applying

- **WHEN** `POST /api/v1/validate` sends a valid definition
- **THEN** the response is 200 with the resolved settings and no monitor is created

### Requirement: Validation parity with the config file

A monitor definition SHALL be accepted only if the config loader would accept the same
target in the YAML file alongside the existing targets: the same check syntax, name
characters, interval, timeout and threshold rules, and the same `parent` rules (the
parent names another target, has exactly one `icmp` or one `tsping` check, and creates
no cycle). Per-monitor values SHALL override the YAML file's `defaults`.

#### Scenario: Parent must be a reachability target

- **WHEN** a create request names a parent whose checks are `tcp:445`
- **THEN** the response is 422 and the error says the parent must have exactly one
  `icmp` or `tsping` check

#### Scenario: Dynamic parent

- **WHEN** a monitor names another dynamic monitor with a single `icmp` check as
  parent
- **THEN** the response is 201 and dependency suppression applies as for YAML targets

### Requirement: Live application

A create, replace or delete SHALL take effect without a restart: checks start or stop,
alert state is registered or dropped, and the notifier is informed. A change SHALL be
persisted before it is applied, and SHALL be reported successful only after both
succeed; if persisting fails, nothing SHALL be applied and the response SHALL be 500.
Changes SHALL be applied one at a time. A delete's response SHALL NOT wait for the
notifier to deliver the incident's recovery.

#### Scenario: New monitor starts checking

- **WHEN** a monitor is created with `interval: 15s`
- **THEN** its first check runs within one interval and `tailwatch_target_state`
  gains a series for it

#### Scenario: Persist failure

- **WHEN** `state_dir` is not writable during a create
- **THEN** the response is 500, the monitor is not running, and it is not listed

### Requirement: Delete closes open incidents

Deleting a dynamic monitor SHALL succeed whether or not it is `DOWN`. If it has an open
incident, the notifier SHALL close it with `reason: removed`. A delete SHALL be refused
with 409, listing the dependents, while any other monitor names it as `parent`.

#### Scenario: Delete a down monitor

- **WHEN** dynamic monitor `lab-nas` is `DOWN` with an open incident and is deleted
- **THEN** the response is 204, checks stop, and the incident closes with
  `reason: removed`

#### Scenario: Delete a parent with dependents

- **WHEN** `lab-router` is the parent of `lab-nas` and `DELETE /targets/lab-router` is
  sent
- **THEN** the response is 409 naming `lab-nas` and `lab-router` stays registered

### Requirement: Edit replaces the monitor

Replacing a monitor SHALL behave as removing it and adding the new definition: an open
incident on the old definition closes with `reason: removed`, and the new definition
starts in `UNKNOWN` (or `SUPPRESSED` when its parent is down). The name SHALL NOT change
on edit. A replace SHALL be allowed while other monitors name the target as `parent`,
provided the result still validates. If registering the new definition fails, the old
one SHALL be restored.

#### Scenario: Edit a down monitor

- **WHEN** `lab-nas` is `DOWN` and is replaced with a new `addr`
- **THEN** the old incident closes with `reason: removed`, and a new incident opens only
  if the new definition then fails `fail_threshold` checks

#### Scenario: Edit a parent

- **WHEN** `lab-router`, parent of `lab-nas`, is replaced with a new check list that
  still validates
- **THEN** the response is 200 and `lab-nas` still has `lab-router` as parent

### Requirement: Write authorization

Every request that changes a monitor SHALL be authorized by a single decision made
before any change. The initial decision SHALL permit every request that reaches the
listener, so access control is the tailnet ACL and the listen address. A request the
decision denies SHALL return 403 and change nothing. When the UI is on, startup SHALL log
that the write API is enabled and the listen address it is served on, and SHALL log a
warning when that address is not a loopback address.

#### Scenario: Startup log

- **WHEN** the collector starts with the UI on and `listen: 0.0.0.0:9109`
- **THEN** it logs that the write API is enabled on that address and warns that access
  is controlled only by the network

#### Scenario: Denied write

- **WHEN** the authorization decision denies a request
- **THEN** the response is 403 and no monitor changes

### Requirement: Write audit log

The collector SHALL log every create, replace and delete, accepted or refused, at info
level with the operation, the monitor name, the outcome and the caller's remote address.

#### Scenario: Accepted write is logged

- **WHEN** a monitor is created from `100.64.0.7`
- **THEN** a log line records the create, the name and `100.64.0.7`

### Requirement: Cross-site request rejection

A request that changes a monitor SHALL carry `Content-Type: application/json`; any other
type SHALL return 415. A request whose `Origin` header names a host different from its
`Host` header SHALL return 403. Both checks SHALL run before the authorization decision
and before any change.

#### Scenario: Form post from another site

- **WHEN** a browser submits a cross-site HTML form to `POST /api/v1/targets`
- **THEN** the response is 415 and nothing changes

#### Scenario: Mismatched origin

- **WHEN** a `POST` carries `Origin: https://example.com` and `Host: tail-watch:9109`
- **THEN** the response is 403 and nothing changes
