# collector-config Specification

## Purpose

Defines the single YAML file that configures a tail-watch collector: its listener,
per-check defaults, and the targets it polls, plus how invalid configs are
rejected before any check runs.

## Requirements

### Requirement: Config file location

The collector SHALL read its config from the path given by the `--config` flag,
falling back to the `TAIL_WATCH_CONFIG` environment variable, falling back to
`/etc/tail-watch/tail-watch.yaml`.

#### Scenario: Flag overrides environment

- **WHEN** `TAIL_WATCH_CONFIG=/a.yaml` is set and tail-watch runs with `--config /b.yaml`
- **THEN** the collector loads `/b.yaml`

#### Scenario: Missing file

- **WHEN** the resolved config path does not exist
- **THEN** tail-watch exits non-zero with an error naming the path, before contacting
  tailscaled

### Requirement: Collector section

The `collector` section SHALL accept `listen` (host:port for the HTTP server,
default `127.0.0.1:9109`) and `state_dir` (optional directory path). The
`--listen` flag SHALL override `collector.listen`.

#### Scenario: Default listen address

- **WHEN** the config omits `collector.listen` and no `--listen` flag is given
- **THEN** the HTTP server listens on `127.0.0.1:9109`

### Requirement: Defaults section

The `defaults` section SHALL accept `interval`, `timeout`, `fail_threshold`,
`recover_threshold`, `flap_limit` and `flap_window`. Omitted keys SHALL take the
values 30s, 5s, 3, 2, 4 and 15m respectively. Durations SHALL use Go duration
syntax (for example `30s`, `15m`).

#### Scenario: Built-in defaults

- **WHEN** the config has no `defaults` section
- **THEN** every target uses interval 30s, timeout 5s, fail_threshold 3,
  recover_threshold 2, flap_limit 4 and flap_window 15m

#### Scenario: Partial defaults

- **WHEN** `defaults` sets only `interval: 1m`
- **THEN** targets use interval 1m and the built-in values for all other keys

### Requirement: Target definition

Each entry in `targets` SHALL have a `name` and an `addr` and a non-empty
`checks` list, and MAY set `site`, `severity`, `parent`, `insecure_tls`,
`interval`, `timeout`, `fail_threshold`, `recover_threshold`, `flap_limit`,
`flap_window` and `network` (`tailnet` by default, or `local`). Per-target values SHALL override `defaults`. `addr` SHALL be an
IP address or a DNS name (including a MagicDNS short name). `severity` SHALL be
`page` or `warn` and default to `page`.

#### Scenario: Per-target override

- **WHEN** `defaults.interval` is `30s` and target `nyc-subnet-router` sets
  `interval: 15s`
- **THEN** that target's checks run every 15s and other targets every 30s

#### Scenario: Missing addr

- **WHEN** a target omits `addr`
- **THEN** loading fails with an error naming that target

### Requirement: Check syntax

Each entry in a target's `checks` list SHALL be one of: `icmp`; `tcp:<port>`;
`http:<url>` optionally followed by `|<text>` where `<text>` must appear in the
response body; or `tsping`. The config loader SHALL reject any other form. It
SHALL reject `tls` checks with an error stating that they arrive in phase 2.

#### Scenario: HTTP check with body match

- **WHEN** a target lists `"http:https://10.20.0.50:5001/|DSM"`
- **THEN** the check requests `https://10.20.0.50:5001/` and requires the body to
  contain `DSM`

#### Scenario: Invalid TCP port

- **WHEN** a target lists `tcp:70000`
- **THEN** loading fails with an error naming the target and the check

#### Scenario: Phase 2 check type

- **WHEN** a target lists `tls:10.20.0.50:443`
- **THEN** loading fails with an error saying `tls` checks are not supported in
  this version

### Requirement: Config validation

The config loader SHALL reject unknown keys, duplicate target names, target
names outside `[A-Za-z0-9._-]`, non-positive intervals, a timeout greater than
or equal to the target's interval, thresholds below 1, a `parent` that does not
name another defined target, a parent target whose checks are not exactly one
`icmp` or one `tsping`, and `parent` chains that form a cycle. A parent used
for a learned tailnet routing dependency MUST have exactly one `tsping` check;
a separate target SHALL represent service health on the same node. It SHALL
report every problem found in one error, each prefixed with its location (for
example `targets[2].checks[1]`), and SHALL NOT start any check when validation
fails.

#### Scenario: Several errors at once

- **WHEN** the config has a duplicate target name and an unknown key
  `colector`
- **THEN** tail-watch exits non-zero and the error lists both problems with their
  locations

#### Scenario: Parent cycle

- **WHEN** target `a` has `parent: b` and target `b` has `parent: a`
- **THEN** loading fails with an error naming the cycle

#### Scenario: Timeout not below interval

- **WHEN** a target has `interval: 5s` and `timeout: 5s`
- **THEN** loading fails with an error naming that target

### Requirement: Config check command

tail-watch SHALL provide a `check-config` subcommand that loads and validates the
config, prints the effective per-target settings, and exits zero only when the
config is valid. It SHALL NOT contact tailscaled.

#### Scenario: Valid config

- **WHEN** an operator runs `tail-watch check-config --config ok.yaml` on a valid
  file
- **THEN** it prints each target with its resolved interval, timeout and checks
  and exits 0
