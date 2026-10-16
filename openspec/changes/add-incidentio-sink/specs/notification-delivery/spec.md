# Spec Delta

## MODIFIED Requirements

### Requirement: Secret handling

Sink URLs, incident.io tokens, HMAC secrets, `headers_env` values and the
heartbeat URL SHALL come only from `*_env` or `*_file` keys; `*_file` values
SHALL have surrounding whitespace trimmed. tail-watch SHALL fail at startup
when a referenced variable is unset or empty or a referenced file is
unreadable, and SHALL re-read the value at send time so rotation needs no
restart. A secret that is sent in a request header rather than in a queued
body (the incident.io token) SHALL be added to the request at send time and
SHALL NOT appear in a queued body, in `notification-state.json`, in logs or in
errors. Logs and errors SHALL show URLs only as scheme and host, with
everything after the host replaced by `/…`.

#### Scenario: Unset variable

- **WHEN** a sink sets `url_env: SLACK_WEBHOOK_URL` and the variable is unset
- **THEN** tail-watch exits non-zero naming the sink and the variable

#### Scenario: Unset incident.io token variable

- **WHEN** an incident.io sink sets `token_env: INCIDENTIO_TOKEN` and the
  variable is unset
- **THEN** tail-watch exits non-zero naming the sink and the variable

#### Scenario: Redacted log

- **WHEN** delivery to `https://hooks.slack.com/services/T0/B0/xyz` fails
- **THEN** the log line shows `https://hooks.slack.com/…` and not the path

#### Scenario: Header secret not persisted

- **WHEN** an incident.io event is queued with `queue: disk`
- **THEN** the persisted body does not contain the token
