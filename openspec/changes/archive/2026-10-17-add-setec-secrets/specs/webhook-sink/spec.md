# Spec Delta

## MODIFIED Requirements

### Requirement: Webhook sink configuration

A sink with `type: webhook` SHALL require exactly one of `url_env`, `url_file` or
`url_setec`, and SHALL accept at most one of `secret_env`, `secret_file` or
`secret_setec` (HMAC key), `headers_env` (map of header name to environment variable
name) and `headers_setec` (map of header name to setec secret name), `max_age`
(default `1h`), `batch` (default `false`), `group_wait` (default `10s`) and
`max_batch` (default `100`), plus the shared routing and queue keys. A header name
SHALL NOT appear in both `headers_env` and `headers_setec`. Sink names
SHALL be unique, match `[A-Za-z0-9._-]+`, and not be `incidents`, `intake` or `state`.

#### Scenario: Both URL sources

- **WHEN** a webhook sink sets both `url_env` and `url_file`
- **THEN** config loading fails with an error naming the sink

#### Scenario: URL from setec

- **WHEN** a webhook sink sets `url_setec: ops-url` and `collector.setec.server`
- **THEN** the sink posts to the URL stored in setec under `ops-url`

#### Scenario: Header from setec

- **WHEN** a webhook sink sets `headers_setec: {Authorization: ops-auth}`
- **THEN** each request carries `Authorization` with the value stored in setec

#### Scenario: Header in both maps

- **WHEN** a webhook sink names `Authorization` in both `headers_env` and
  `headers_setec`
- **THEN** config loading fails with an error naming the sink and header

#### Scenario: No signing secret

- **WHEN** a webhook sink has none of `secret_env`, `secret_file` or `secret_setec`
- **THEN** tail-watch starts, logs a warning that the sink is unsigned, and sends
  requests without a `Tail-Watch-Signature` header
