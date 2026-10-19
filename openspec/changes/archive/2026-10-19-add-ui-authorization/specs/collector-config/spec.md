# Spec Delta

## ADDED Requirements

### Requirement: Authorization section

The `collector` section SHALL accept `authorization`, a mapping with `capability` (a
string, required when the mapping is present) and `service` (optional, a string starting
with `svc:`). Validation SHALL reject, listing every problem and naming each field: a
`capability` that is empty, that has no `/cap/` segment, or that is in the
`tailscale.com` domain; a `service` that does not start with `svc:`; authorization
with the UI off from `collector.ui` and the `--ui` flag; and authorization with
`collector.local_only` true. `tail-watch check-config` SHALL apply the same rules.

#### Scenario: Valid section

- **WHEN** `collector.authorization.capability: example.com/cap/tail-watch` is set with
  `collector.ui: true`
- **THEN** the config loads

#### Scenario: Tailscale-owned capability name

- **WHEN** `collector.authorization.capability: tailscale.com/cap/tail-watch` is set
- **THEN** validation fails with an error naming `collector.authorization.capability`

#### Scenario: Authorization without the UI

- **WHEN** `collector.authorization` is set and the UI is off
- **THEN** validation fails with an error naming `collector.authorization`

#### Scenario: Authorization under local_only

- **WHEN** `collector.local_only: true` and `collector.authorization` is set
- **THEN** validation fails with an error naming `collector.authorization`

#### Scenario: Bad service name

- **WHEN** `collector.authorization.service: tail-watch` is set
- **THEN** validation fails with an error naming `collector.authorization.service`
