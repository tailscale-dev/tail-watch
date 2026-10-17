# Spec Delta

## MODIFIED Requirements

### Requirement: Teams sink configuration

A sink with `type: teams` SHALL require exactly one of `url_env`, `url_file` or
`url_setec` holding a Teams Workflows webhook URL, and SHALL accept
`group_wait` (default `10s`) and `status_url`, plus the shared routing and
queue keys. It SHALL reject `batch`, `max_batch`, `secret_env`,
`secret_file`, `secret_setec`, `headers_env`, `headers_setec` and `mention_on_page`.

#### Scenario: Batch on Teams

- **WHEN** a Teams sink sets `batch: true`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Mention on Teams

- **WHEN** a Teams sink sets `mention_on_page`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Both URL sources

- **WHEN** a Teams sink sets both `url_env` and `url_file`
- **THEN** config loading fails with an error naming the sink

#### Scenario: URL from setec

- **WHEN** a Teams sink sets `url_setec: teams-url`
- **THEN** the sink posts to the URL stored in setec under `teams-url`
