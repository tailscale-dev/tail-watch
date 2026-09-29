# Spec Delta

## MODIFIED Requirements

### Requirement: HTTP endpoints

tail-watch SHALL serve on the configured listen address: `/` as the prober status
page listing every check with its latest result, `/metrics` in Prometheus text
format, and `/healthz` returning `200 ok` while the process is serving. When the UI
is on, it SHALL also serve `/ui/` and `/api/v1/` as defined by the monitor-ui and
monitor-management capabilities. When the UI is off, `/ui/` and `/api/v1/` SHALL return 404.

#### Scenario: Status page

- **WHEN** an operator opens `/` on the listen address
- **THEN** the page lists every target/check pair with its recent result and
  latency

#### Scenario: Health endpoint

- **WHEN** a client requests `/healthz`
- **THEN** the response is status 200 with body `ok`

#### Scenario: UI endpoints only when on

- **WHEN** the collector runs without the UI and a client requests `/api/v1/targets`
- **THEN** the response is 404
