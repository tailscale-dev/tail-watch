# Spec Delta

## MODIFIED Requirements

### Requirement: Secret handling

Sink URLs, PagerDuty routing keys, HMAC secrets, `headers_env` values and the
heartbeat URL SHALL come only from `*_env` or `*_file` keys; `*_file` values
SHALL have surrounding whitespace trimmed. tail-watch SHALL fail at startup
when a referenced variable is unset or empty or a referenced file is
unreadable, and SHALL re-read the value at send time so rotation needs no
restart. A secret that is sent in a request body rather than a header or URL
(the PagerDuty routing key) SHALL be added to the body at send time and SHALL
NOT appear in a queued body, in `notification-state.json`, in logs or in
errors. Logs and errors SHALL show URLs only as scheme and host, with
everything after the host replaced by `/…`.

#### Scenario: Unset variable

- **WHEN** a sink sets `url_env: SLACK_WEBHOOK_URL` and the variable is unset
- **THEN** tail-watch exits non-zero naming the sink and the variable

#### Scenario: Unset routing key variable

- **WHEN** a PagerDuty sink sets `routing_key_env: PD_ROUTING_KEY` and the
  variable is unset
- **THEN** tail-watch exits non-zero naming the sink and the variable

#### Scenario: Redacted log

- **WHEN** delivery to `https://hooks.slack.com/services/T0/B0/xyz` fails
- **THEN** the log line shows `https://hooks.slack.com/…` and not the path

#### Scenario: Body secret not persisted

- **WHEN** a PagerDuty event is queued with `queue: disk`
- **THEN** the persisted body does not contain the routing key
