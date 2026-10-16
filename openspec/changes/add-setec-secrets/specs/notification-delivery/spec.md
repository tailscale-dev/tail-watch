# Spec Delta

## MODIFIED Requirements

### Requirement: Secret handling

Sink URLs, HMAC secrets, `headers_env` and `headers_setec` values and the
heartbeat URL SHALL come only from `*_env`, `*_file` or `*_setec` keys; `*_file`
and setec values SHALL have surrounding whitespace trimmed. tail-watch SHALL
fail at startup when a referenced variable is unset or empty, a referenced file
is unreadable, or a referenced setec secret is missing or its server
unreachable, and SHALL re-read the value at send time so rotation needs no
restart. Logs and errors SHALL show URLs only as scheme and host, with
everything after the host replaced by `/…`.

#### Scenario: Unset variable

- **WHEN** a sink sets `url_env: SLACK_WEBHOOK_URL` and the variable is unset
- **THEN** tail-watch exits non-zero naming the sink and the variable

#### Scenario: Missing setec secret

- **WHEN** a sink sets `url_setec: slack-url` and the server has no such secret
- **THEN** tail-watch exits non-zero naming the sink and `slack-url`

#### Scenario: Redacted log

- **WHEN** delivery to `https://hooks.slack.com/services/T0/B0/xyz` fails
- **THEN** the log line shows `https://hooks.slack.com/…` and not the path
