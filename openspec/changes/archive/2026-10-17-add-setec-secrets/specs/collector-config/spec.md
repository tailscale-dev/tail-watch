# Spec Delta

## MODIFIED Requirements

### Requirement: Collector section

The `collector` section SHALL accept `listen` (host:port for the HTTP server,
default `127.0.0.1:9109`), `state_dir` (optional directory path), `ui` (boolean,
default `false`), `local_only` (boolean, default `false`) and `setec` (a
mapping with `server` and `poll_interval`, defined by `setec-secrets`). The `--listen` flag SHALL
override `collector.listen`. The `--ui` flag SHALL override `collector.ui`, and
`--ui=false` SHALL turn it off. When the UI is on and `collector.state_dir` is unset,
tail-watch SHALL exit non-zero before contacting tailscaled, with an error saying the UI
needs `collector.state_dir` to store monitors. With `--once`, the UI setting SHALL be
ignored and only YAML targets run.

When `collector.local_only` is `true`, a target that sets no `network` SHALL default to
`local` instead of `tailnet`, and validation SHALL reject, with every problem listed and
each naming its field: a target with `network: tailnet` (`defaults` has no `network` key), any `tsping`
check, the UI being on from `collector.ui` or the `--ui` flag, and `collector.setec` or any
`*_setec` key. `tail-watch check-config` SHALL apply the same rules.

#### Scenario: Default listen address

- **WHEN** the config omits `collector.listen` and no `--listen` flag is given
- **THEN** the HTTP server listens on `127.0.0.1:9109`

#### Scenario: UI off by default

- **WHEN** the config omits `collector.ui` and no `--ui` flag is given
- **THEN** the UI and its API are not served

#### Scenario: Flag overrides config

- **WHEN** `collector.ui: false` is set and tail-watch runs with `--ui`
- **THEN** the UI is on

#### Scenario: Setec section accepted

- **WHEN** the config sets `collector.setec.server: https://setec.example.ts.net`
- **THEN** the config loads and `poll_interval` is `5m`

#### Scenario: UI without a state directory

- **WHEN** the UI is on and `collector.state_dir` is unset
- **THEN** tail-watch exits non-zero with an error naming `collector.state_dir`

#### Scenario: One-shot mode ignores the UI

- **WHEN** tail-watch runs with `--once --ui`
- **THEN** it runs each YAML target's checks once and starts no HTTP server

#### Scenario: Local-only defaults targets to local

- **WHEN** `collector.local_only: true` is set and a target sets no `network`
- **THEN** the target loads with `network: local`

#### Scenario: Local-only rejects tailnet targets and tsping

- **WHEN** `collector.local_only: true` is set, one target sets `network: tailnet` and
  another lists a `tsping` check
- **THEN** validation fails with an error for each, naming the target's `network` field
  and the `checks` entry

#### Scenario: Local-only rejects the UI

- **WHEN** `collector.local_only: true` is set and tail-watch runs with `--ui`
- **THEN** tail-watch exits non-zero with an error saying the UI is unavailable in
  local-only mode

#### Scenario: Local-only rejects setec

- **WHEN** `collector.local_only: true` is set and a sink sets `url_setec`
- **THEN** validation fails with an error naming the sink and the key
