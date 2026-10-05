# Spec Delta

## Purpose

Defines how tail-watch posts events to a Microsoft Teams channel through a
Workflows webhook: Adaptive Card content, grouping, status links, and the
same delivery policy as the other sinks.

## ADDED Requirements

### Requirement: Teams sink configuration

A sink with `type: teams` SHALL require exactly one of `url_env` or
`url_file` holding a Teams Workflows webhook URL, and SHALL accept
`group_wait` (default `10s`) and `status_url`, plus the shared routing and
queue keys. It SHALL reject `batch`, `max_batch`, `secret_env`,
`secret_file`, `headers_env` and `mention_on_page`.

#### Scenario: Batch on Teams

- **WHEN** a Teams sink sets `batch: true`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Mention on Teams

- **WHEN** a Teams sink sets `mention_on_page`
- **THEN** config loading fails with an error naming the sink and key

#### Scenario: Both URL sources

- **WHEN** a Teams sink sets both `url_env` and `url_file`
- **THEN** config loading fails with an error naming the sink

### Requirement: Teams request format

Each delivery SHALL be an HTTP `POST` to the sink URL with
`Content-Type: application/json` and `User-Agent: tail-watch/<version>`. The
body SHALL be a JSON object with `type` equal to `message` and one attachment
whose `contentType` is `application/vnd.microsoft.card.adaptive`. That
attachment's `content` SHALL be an Adaptive Card with `type` equal to
`AdaptiveCard`, `$schema` equal to
`http://adaptivecards.io/schemas/adaptive-card.json`, and `version` equal to
`1.4`. The request SHALL NOT carry `Tail-Watch-Signature`. It SHALL NOT
follow redirects. Each request SHALL time out after 10s.

#### Scenario: Down envelope

- **WHEN** a `target.down` for `nyc-nas` is delivered
- **THEN** the body is that envelope and the card text includes `DOWN`,
  `nyc-nas`, the check, the layer and the error

#### Scenario: No signature

- **WHEN** a Teams sink delivers an event
- **THEN** the request has no `Tail-Watch-Signature` header

### Requirement: Card content

A `target.down` card SHALL show `DOWN`, the target name, the check and the
layer, then the address, site, parent, path kind and latency, the error, and
the down-since time, failure count and collector name. The layer SHALL be the
payload `layer` value (`unrouted`, `control`, `tunnel`, `lan` or `service`).
An implicit target's name SHALL be followed by `(learned)`. The down headline
SHALL use Adaptive Card color `Attention`. A `target.recovered` card SHALL
read `RECOVERED <target> <check> after <duration>` and SHALL use color
`Good`. A `target.down` with dependents SHALL add "and N targets behind it"
and list up to 10 dependent names. A `target.flapping` card SHALL show
`FLAPPING`, the target and the check, in color `Warning`. A
`collector.started` card SHALL show that tail-watch started and the collector
name. A `test` card SHALL show that it is a test notification and the
collector name. Detail lines SHALL be separated by a blank line (`\n\n`),
because Teams renders a single `\n` as a space.

#### Scenario: Down message

- **WHEN** `nyc-nas` goes DOWN on `tcp:445` at layer `service`
- **THEN** the card text includes `DOWN`, `nyc-nas`, `tcp:445`, `(service)`
  and the error `connection refused`, and the headline color is `Attention`

#### Scenario: Router with dependents

- **WHEN** `nyc-router` goes DOWN with 14 dependents
- **THEN** the card says "and 14 targets behind it" and names 10 of them

#### Scenario: Learned target

- **WHEN** an implicit target `nyc-router` goes DOWN
- **THEN** the card shows `nyc-router (learned)`

#### Scenario: Recovery

- **WHEN** `nyc-nas` recovers after 12 minutes
- **THEN** the card text includes `RECOVERED`, `nyc-nas` and `after`, and the
  headline color is `Good`

#### Scenario: Detail lines render apart

- **WHEN** a `target.down` card with an address, an error and a failure count
  is rendered with Teams markdown
- **THEN** the address, the error and the down-since line each show on their
  own line

### Requirement: Untrusted text

Text taken from the event (`error`, `check`, address, site, parent and
collector name) SHALL have `\`, `` ` ``, `*`, `_`, `[`, `]`, `(`, `)`, `<`
and `>` escaped with a leading backslash before it is placed in card text.
An error longer than 300 bytes SHALL be cut to 300 bytes and followed by
`…`. The `status_url` link is the only markdown link the card adds.

#### Scenario: Error that looks like a link

- **WHEN** the error is `see [phish](https://evil.example)`
- **THEN** the card text contains `\[phish\]` and does not contain the
  markdown link `[phish](https://evil.example)`

#### Scenario: Long error

- **WHEN** the error is longer than 300 bytes
- **THEN** the card includes the first 300 bytes of that error followed by
  `…`

### Requirement: Message grouping

The Teams sink SHALL group events that arrive within `group_wait` of the
first pending event into one card, keeping queue order. A grouped card SHALL
show at most 10 events followed by "and N more".

#### Scenario: Burst of 25

- **WHEN** 25 events arrive within one `group_wait`
- **THEN** Teams receives one card with 10 events and "and 15 more"

### Requirement: Status links

When `status_url` is set, each event for a target SHALL link that target's
name to `<status_url>/#<target>`, with trailing slashes trimmed from
`status_url`, as the Slack sink does.

#### Scenario: Link present

- **WHEN** `status_url` is `https://monitor-nyc.example.ts.net:9109`
- **THEN** the `nyc-nas` name links to
  `https://monitor-nyc.example.ts.net:9109/#nyc-nas`

#### Scenario: Trailing slash

- **WHEN** `status_url` is `https://monitor-nyc.example.ts.net:9109/`
- **THEN** the `nyc-nas` name links to
  `https://monitor-nyc.example.ts.net:9109/#nyc-nas`

### Requirement: Route mentions are not printed

A Teams card SHALL NOT contain a route `mention`. Page severity SHALL be
shown by the down headline color.

#### Scenario: Route mention

- **WHEN** a page-severity `target.down` is routed to a Teams sink with
  mention `<at>Jane</at>`
- **THEN** the card does not contain `<at>Jane</at>`

### Requirement: Teams delivery policy

The Teams sink SHALL apply the same retry, `Retry-After`, permanent-failure
and `max_age` rules as the webhook sink, and SHALL send at most one card per
second. A delivery error SHALL include only the scheme and host of the
webhook URL.

#### Scenario: Workflow rate limit

- **WHEN** the workflow answers `429` with `Retry-After: 2`
- **THEN** the sink waits at least 2 seconds before resending the same card

#### Scenario: Bad request

- **WHEN** the workflow answers `400`
- **THEN** tail-watch does not retry and increments
  `tailwatch_notify_dropped_total{reason="permanent"}`

#### Scenario: Spacing

- **WHEN** two cards are each ready and the receiver answers `200` at once
- **THEN** the second POST starts at least 1 second after the first

#### Scenario: Secret stays out of the error

- **WHEN** delivery fails for a URL whose query contains `sig=sekret`
- **THEN** the logged error contains the scheme and host and does not
  contain `sekret`
