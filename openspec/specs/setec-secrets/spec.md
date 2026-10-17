# setec-secrets Specification

## Purpose

Defines how tail-watch reads sink, heartbeat and webhook secrets from a setec
server: the `*_setec` config keys, startup and polling behavior, local-only
and offline-command handling, and the rule that fetched values never reach logs
or state.

## Requirements

### Requirement: Setec secret source

Every config key that accepts a secret through `*_env` or `*_file` SHALL also
accept a `*_setec` form holding the name of a secret on a setec server: `url_setec`
for sink and heartbeat URLs, `secret_setec` for the webhook HMAC key, and
`headers_setec` (map of header name to secret name) for webhook headers. A
secret SHALL have exactly one source; setting more than one of `*_env`,
`*_file` and `*_setec` for the same secret SHALL fail config loading with an
error naming the sink and the keys. The `collector.setec` section SHALL accept
`server` (an `https` URL, required when any `*_setec` key is set) and
`poll_interval` (default `5m`, at least `1m`). A `*_setec` key without
`collector.setec.server` SHALL fail config loading, naming the key. A
`collector.setec` section that no key uses SHALL be accepted and SHALL NOT
build a setec client.

#### Scenario: Sink URL from setec

- **WHEN** a Slack sink sets `url_setec: tail-watch/slack-url` and
  `collector.setec.server` is `https://setec.example.ts.net`
- **THEN** the sink posts to the URL stored in setec under that name

#### Scenario: Two sources for one secret

- **WHEN** a webhook sink sets `url_env: OPS_URL` and `url_setec: ops-url`
- **THEN** config loading fails with an error naming the sink and both keys

#### Scenario: No server configured

- **WHEN** a sink sets `url_setec` and the config has no `collector.setec.server`
- **THEN** config loading fails with an error naming the sink and
  `collector.setec.server`

#### Scenario: Mixed sources across secrets

- **WHEN** one sink sets `url_env` and another sets `url_setec`
- **THEN** both load, and each secret is read from its own source

#### Scenario: Existing config unchanged

- **WHEN** a config sets only `*_env` and `*_file` keys and no `collector.setec`
- **THEN** tail-watch behaves exactly as before and builds no setec client

### Requirement: Setec startup and reads

When any `*_setec` key is set, tail-watch SHALL build one setec store before
the prober starts, declaring every setec secret the config names, and SHALL
reach the server through the collector's `netEnv` dialer. The store SHALL hold
secrets in memory only; tail-watch SHALL NOT configure a persistent cache. It
SHALL wait up to 30 seconds for every declared secret and SHALL exit non-zero
if the server is unreachable or a secret is missing or empty, with an error
naming the secret and the server host, as for an unset `*_env` variable. A
value SHALL have surrounding whitespace trimmed, as `*_file` values are. At
send time tail-watch SHALL read the store's current value, and the store SHALL
poll the server every `poll_interval`, so a value changed in setec reaches the
next delivery after the next poll with no restart. When a poll fails, tail-watch
SHALL keep delivering with the last good value and log the failure at `warn`.

#### Scenario: Server unreachable at startup

- **WHEN** a sink sets `url_setec` and the server does not answer within 30
  seconds
- **THEN** tail-watch exits non-zero naming the secret and the server host

#### Scenario: Missing secret

- **WHEN** `heartbeat.url_setec: hb-url` names a secret the server does not have
- **THEN** tail-watch exits non-zero naming `hb-url`

#### Scenario: Rotation

- **WHEN** the secret behind `url_setec` changes in setec and one `poll_interval`
  passes
- **THEN** the next delivery to that sink uses the new value and tail-watch was
  not restarted

#### Scenario: Server goes down while running

- **WHEN** a poll fails after startup
- **THEN** deliveries continue with the last good value and a warning is logged

#### Scenario: Nothing written to disk

- **WHEN** tail-watch runs with `queue: disk` and `collector.state_dir` set
- **THEN** no file under the state directory contains a setec secret value

### Requirement: Setec in local-only mode

When `collector.local_only` is `true`, validation SHALL reject
`collector.setec` and every `*_setec` key, listing each problem and naming its
field, because there is no tailscaled to reach the server through.
`tail-watch check-config` SHALL apply the same rule.

#### Scenario: Setec key under local_only

- **WHEN** `collector.local_only: true` and a sink sets `url_setec`
- **THEN** config loading fails with an error naming the sink and the key

#### Scenario: Setec section under local_only

- **WHEN** `collector.local_only: true` and `collector.setec.server` is set
- **THEN** config loading fails with an error naming `collector.setec`

### Requirement: Setec in check-config, --once and notify-test

`tail-watch check-config` SHALL validate the setec keys and section and SHALL NOT
contact the server or require network access. `--once` SHALL NOT build a
notifier, contact the server or read a setec secret. `tail-watch notify-test`
SHALL build the store the way a normal run does and SHALL send through each sink
using the setec value, reporting a setec failure on that sink's line without
printing the value.

#### Scenario: check-config offline

- **WHEN** `check-config` runs on a valid config that uses `url_setec` with no
  network
- **THEN** it exits 0

#### Scenario: notify-test uses the real secret

- **WHEN** `notify-test` runs for a sink with `url_setec`
- **THEN** the test event is posted to the URL stored in setec

#### Scenario: notify-test cannot reach setec

- **WHEN** `notify-test` runs and the server does not answer
- **THEN** it exits non-zero with a line naming the sink and the secret, and no
  value

### Requirement: Setec values stay secret

A setec secret value SHALL NOT be logged, returned by the API, shown by
`check-config` or `describe`, written to `notification-state.json` or a queue,
or included in an error. The secret's name and the server host MAY be logged.
A sink URL taken from setec SHALL be logged only as scheme and host, as for any
sink URL.

#### Scenario: Name but not value

- **WHEN** a secret named `slack-url` is missing
- **THEN** the error names `slack-url` and the server host and contains no value

#### Scenario: Redacted URL from setec

- **WHEN** delivery to a URL read from setec fails
- **THEN** the log line shows the scheme and host and not the path
