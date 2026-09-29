# Spec Delta

## MODIFIED Requirements

### Requirement: Collector section

The `collector` section SHALL accept `listen` (host:port for the HTTP server,
default `127.0.0.1:9109`), `state_dir` (optional directory path) and `ui` (boolean,
default `false`). The `--listen` flag SHALL override `collector.listen`. The `--ui` flag
SHALL override `collector.ui`, and `--ui=false` SHALL turn it off. When the UI is on and
`collector.state_dir` is unset, tail-watch SHALL exit non-zero before contacting
tailscaled, with an error saying the UI needs `collector.state_dir` to store monitors.
With `--once`, the UI setting SHALL be ignored and only YAML targets run.

#### Scenario: Default listen address

- **WHEN** the config omits `collector.listen` and no `--listen` flag is given
- **THEN** the HTTP server listens on `127.0.0.1:9109`

#### Scenario: UI off by default

- **WHEN** the config omits `collector.ui` and no `--ui` flag is given
- **THEN** the UI and its API are not served

#### Scenario: Flag overrides config

- **WHEN** `collector.ui: false` is set and tail-watch runs with `--ui`
- **THEN** the UI is on

#### Scenario: UI without a state directory

- **WHEN** the UI is on and `collector.state_dir` is unset
- **THEN** tail-watch exits non-zero with an error naming `collector.state_dir`

#### Scenario: One-shot mode ignores the UI

- **WHEN** tail-watch runs with `--once --ui`
- **THEN** it runs each YAML target's checks once and starts no HTTP server
