# Spec Delta

## MODIFIED Requirements

### Requirement: Slack sink configuration

A sink with `type: slack` SHALL require exactly one of `url_env`, `url_file` or
`url_setec` holding a Slack incoming-webhook URL, and SHALL accept
`mention_on_page`, `group_wait` (default `10s`) and `status_url`, plus the
shared routing and queue keys. It SHALL reject `batch`, `max_batch`,
`secret_env`, `secret_file`, `secret_setec`, `headers_env` and `headers_setec`.

#### Scenario: Batch on Slack

- **WHEN** a Slack sink sets `batch: true`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: URL from setec

- **WHEN** a Slack sink sets `url_setec: slack-url`
- **THEN** the sink posts to the URL stored in setec under `slack-url`
