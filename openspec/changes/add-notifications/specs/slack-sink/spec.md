# Spec Delta

## Purpose

Defines how tsmon posts events to a Slack channel through an incoming webhook:
message content, mentions for pages, grouping to stay within Slack's rate
limit, and links to the collector's status page.

## ADDED Requirements

### Requirement: Slack sink configuration

A sink with `type: slack` SHALL require exactly one of `url_env` or
`url_file` holding a Slack incoming-webhook URL, and SHALL accept
`mention_on_page`, `group_wait` (default `10s`) and `status_url`, plus the
shared routing and queue keys. It SHALL reject `batch`, `max_batch`,
`secret_env`, `secret_file` and `headers_env`.

#### Scenario: Batch on Slack

- **WHEN** a Slack sink sets `batch: true`
- **THEN** config loading fails with an error naming the sink and key

### Requirement: Message content

Each Slack message SHALL contain Block Kit blocks and a plain-text `text`
fallback with the same information. A `target.down` line SHALL show a red
marker, `DOWN`, target name, check and layer, then the address, site, parent,
path kind and latency, the error, and the down-since time, failure count and
collector name. The layer SHALL be the payload `layer` value (`unrouted`,
`control`, `tunnel`, `lan` or `service`); an implicit target's name SHALL be
followed by `(learned)`. A `target.recovered` line SHALL read
`:large_green_circle: RECOVERED <target> <check> after <duration>`. A
`target.down` with dependents SHALL add "and N targets behind it" and list up
to 10 dependent names.

#### Scenario: Down message

- **WHEN** `nyc-nas` goes DOWN on `tcp:445` at layer `service`
- **THEN** the message text starts with
  `:red_circle: DOWN  nyc-nas  tcp:445  (service)` and includes the error
  `connection refused`

#### Scenario: Router with dependents

- **WHEN** `nyc-router` goes DOWN with 14 dependents
- **THEN** the message says "and 14 targets behind it" and names 10 of them

### Requirement: Page mentions

`mention_on_page` SHALL be prepended only to `target.down` events with
severity `page`. It SHALL NOT appear on recoveries, flapping notices, warnings
or info events.

#### Scenario: Warn target

- **WHEN** a `severity: warn` target goes DOWN on a sink with
  `mention_on_page: "<!here>"`
- **THEN** the message does not contain `<!here>`

#### Scenario: Recovery

- **WHEN** a paged target recovers
- **THEN** the recovery message does not contain the mention

### Requirement: Message grouping

The Slack sink SHALL group events that arrive within `group_wait` of the first
pending event into one message, keeping queue order. A grouped message SHALL
show at most 10 event lines followed by "and N more".

#### Scenario: Burst of 25

- **WHEN** 25 events arrive within one `group_wait`
- **THEN** Slack receives one message with 10 event lines and "and 15 more"

### Requirement: Status links

When `status_url` is set, each event line for a target SHALL link to the
collector status page for that target.

#### Scenario: Link present

- **WHEN** `status_url` is `https://monitor-nyc.example.ts.net:9109`
- **THEN** the `nyc-nas` line links to a URL under that address

### Requirement: Slack delivery policy

The Slack sink SHALL apply the same retry, `Retry-After`, permanent-failure and
`max_age` rules as the webhook sink, and SHALL send at most one message per
second.

#### Scenario: Slack rate limit

- **WHEN** Slack answers `429` with `Retry-After: 2`
- **THEN** the sink waits at least 2 seconds before resending the same
  message
