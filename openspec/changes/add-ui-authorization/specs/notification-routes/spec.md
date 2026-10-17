# Spec Delta

## MODIFIED Requirements

### Requirement: Route write authorization and audit

Every request that changes a route SHALL pass the same `Content-Type`, `Origin` and
authorization decision as a monitor write, before any change. The collector SHALL log
every route create, replace and delete, accepted or refused, at info level with the
operation, the route name, the outcome, the caller's remote address and the verified
identity fields defined by `ui-authorization`.

#### Scenario: Route write is logged

- **WHEN** route `db-team` is deleted from `100.64.0.7`
- **THEN** a log line records the delete, `db-team`, the outcome and `100.64.0.7`

#### Scenario: Cross-site route write

- **WHEN** a `POST /api/v1/routes` carries `Content-Type: text/plain`
- **THEN** the response is 415 and no route changes

#### Scenario: Route write by a viewer

- **WHEN** authorization is on and a `viewer` sends `POST /api/v1/routes`
- **THEN** the response is 403 and no route changes
