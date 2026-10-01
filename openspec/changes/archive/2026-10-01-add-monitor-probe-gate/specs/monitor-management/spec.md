# Spec Delta

## ADDED Requirements

### Requirement: Live probe

When the UI is on, the collector SHALL serve `POST /api/v1/probe`, accepting the same
JSON body as `POST /api/v1/targets`. The collector SHALL first validate the definition
exactly as `POST /api/v1/validate` does; a validation failure SHALL return 422 with the
same `errors` shape and SHALL run no check. For a valid definition the collector SHALL
run each of its checks once against the address and SHALL answer 200 with a `results`
list holding one entry per check, in definition order, each carrying `check` (the check
string), `ok`, `latency_ms`, and, when the check failed, `error` with the failure's
text. A failing check SHALL NOT fail the response: the endpoint reports what the prober
would report, so a non-200 HTTP status or a refused connection is data, not an error of
the endpoint. Checks SHALL run concurrently, each bounded by the definition's `timeout`,
with the whole call bounded so one slow check cannot hold the response; a client
disconnect SHALL cancel the runs. The probe SHALL persist nothing, register no monitor,
and SHALL NOT change alert state. Because it emits network traffic on demand toward the
address the caller names, the probe SHALL be authorized by the same decision as a
write, SHALL be audited like one, and SHALL apply the same `Content-Type` and `Origin`
checks as a write.

#### Scenario: All checks pass

- **WHEN** `POST /api/v1/probe` sends a valid definition with checks `icmp` and
  `tcp:443` and both succeed
- **THEN** the response is 200 with two `results` entries, each `ok: true` with a
  `latency_ms`, in definition order

#### Scenario: Failing check is reported, not refused

- **WHEN** a valid definition's `tcp:443` check gets a connection refused
- **THEN** the response is 200, that entry has `ok: false` and an `error` naming the
  refusal, and no monitor is created

#### Scenario: Non-200 HTTP status fails the check

- **WHEN** a definition's HTTP check is answered with a 404
- **THEN** its entry has `ok: false` and an `error` naming the status, exactly as the
  prober would report it

#### Scenario: Invalid definition runs no check

- **WHEN** `POST /api/v1/probe` sends `tcp:70000` as a check
- **THEN** the response is 422, `errors` names the check problem, and no check runs

#### Scenario: Slow check is bounded

- **WHEN** one check of a valid definition does not answer within the definition's
  `timeout`
- **THEN** its entry has `ok: false` with a timeout error and the response still
  returns

#### Scenario: Denied probe runs nothing

- **WHEN** the authorization decision denies a probe request
- **THEN** the response is 403 and no check runs

#### Scenario: Probe leaves no trace

- **WHEN** a probe runs for a definition whose name is unused
- **THEN** `GET /api/v1/targets` is unchanged, no incident or alert state appears, and
  the audit log records the probe

#### Scenario: Probe with the UI off

- **WHEN** the collector runs without the UI
- **THEN** `POST /api/v1/probe` returns 404 like the rest of `/api/v1/`

## MODIFIED Requirements

### Requirement: Monitor API

When the UI is on, the collector SHALL serve under `/api/v1/`:
`GET /targets` returning every monitor, YAML and dynamic; `POST /targets` creating a
dynamic monitor; `PUT /targets/{name}` replacing one; `DELETE /targets/{name}` removing
one; `POST /validate` checking a monitor definition without applying it; and
`POST /probe` running a definition's checks once against the address without applying
it. Each entry
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

#### Scenario: Probe without applying

- **WHEN** `POST /api/v1/probe` sends a valid definition
- **THEN** the response is 200 with one result per check and no monitor is created
