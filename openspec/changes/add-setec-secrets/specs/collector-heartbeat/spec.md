# Spec Delta

## MODIFIED Requirements

### Requirement: Heartbeat configuration

The optional top-level `heartbeat` section SHALL require exactly one of
`url_env`, `url_file` or `url_setec` and SHALL accept `interval` (default `1m`). The
heartbeat URL SHALL follow the same secret-handling rules as sink URLs.

#### Scenario: No heartbeat

- **WHEN** the config has no `heartbeat` section
- **THEN** tail-watch sends no heartbeat requests

#### Scenario: Heartbeat URL from setec

- **WHEN** `heartbeat.url_setec: hb-url` is set
- **THEN** pings go to the URL stored in setec under `hb-url`
