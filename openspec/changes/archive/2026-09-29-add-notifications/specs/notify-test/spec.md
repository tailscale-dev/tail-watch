# Spec Delta

## Purpose

Gives operators a one-command way to prove each configured sink works end to
end, through the real signing, rendering and retry code, after every config
change.

## ADDED Requirements

### Requirement: Notify-test command

`tail-watch notify-test [--sink NAME] [--config PATH] [--timeout DUR]` SHALL load
the config, build a `test` event (severity `info`), and send it through each
selected sink's real delivery code, including signing, Slack rendering and the
retry policy, bypassing routing filters and queues. Without `--sink` it SHALL
select every sink. It SHALL print one line per sink with the final HTTP status
or error, and exit 0 only when every selected sink returned `2xx` within
`--timeout` (default 30s).

#### Scenario: All sinks succeed

- **WHEN** two sinks both answer `200`
- **THEN** notify-test prints `200` for each and exits 0

#### Scenario: One sink fails

- **WHEN** the webhook sink answers `401`
- **THEN** notify-test prints `401` for that sink and exits 1

#### Scenario: Unknown sink

- **WHEN** `--sink nope` names no configured sink
- **THEN** notify-test exits non-zero listing the configured sink names

### Requirement: Notify-test without tailscaled

notify-test SHALL work when tailscaled is unreachable, filling
`collector.name` from the OS hostname and omitting `collector.tailscale_ip`.
It SHALL NOT write to `state_dir`.

#### Scenario: Daemon down

- **WHEN** notify-test runs on a host where the LocalAPI socket is missing
- **THEN** it still sends the test event and reports each sink's status
