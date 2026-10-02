# Spec Delta

## MODIFIED Requirements

### Requirement: Target definition

Each entry in `targets` SHALL have a `name` and an `addr` and a non-empty
`checks` list, and MAY set `site`, `severity`, `route`, `parent`, `insecure_tls`,
`interval`, `timeout`, `fail_threshold`, `recover_threshold`, `flap_limit`,
`flap_window` and `network` (`tailnet` by default, or `local`). Per-target values SHALL override `defaults`. `addr` SHALL be an
IP address or a DNS name (including a MagicDNS short name). `severity` SHALL be
`page` or `warn` and default to `page`. `route` SHALL name a route defined in the
`routes` section, and a target SHALL NOT set both `route` and `severity`; a routed
target's severity is the route's.

#### Scenario: Per-target override

- **WHEN** `defaults.interval` is `30s` and target `nyc-subnet-router` sets
  `interval: 15s`
- **THEN** that target's checks run every 15s and other targets every 30s

#### Scenario: Missing addr

- **WHEN** a target omits `addr`
- **THEN** loading fails with an error naming that target

#### Scenario: Routed target

- **WHEN** a target sets `route: db-team` and the `routes` section defines `db-team`
  with `severity: warn`
- **THEN** the target loads with severity `warn`

### Requirement: Config check command

tail-watch SHALL provide a `check-config` subcommand that loads and validates the
config, prints the effective per-target settings, and exits zero only when the
config is valid. It SHALL NOT contact tailscaled. It SHALL print each route with its
severity and sinks, and each target's route (or none) and its candidate sinks.

#### Scenario: Valid config

- **WHEN** an operator runs `tail-watch check-config --config ok.yaml` on a valid
  file
- **THEN** it prints each target with its resolved interval, timeout and checks
  and exits 0

#### Scenario: Routes printed

- **WHEN** the config defines route `db-team` with sinks `[db-hook]` and target
  `pg-primary` names it
- **THEN** the output lists `db-team` with severity `page` and sink `db-hook`, and
  `pg-primary` with route `db-team` and candidate sink `db-hook`
