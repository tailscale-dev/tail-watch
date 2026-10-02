# Spec Delta

## MODIFIED Requirements

### Requirement: Page mentions

A page mention SHALL be prepended only to `target.down` events with severity
`page`. For a target whose route sets `mention`, the mention SHALL be the route's
`mention`, in place of the sink's `mention_on_page`. Otherwise it SHALL be the sink's
`mention_on_page`, when set. A mention SHALL NOT appear on recoveries, flapping notices,
warnings or info events.

#### Scenario: Warn target

- **WHEN** a `severity: warn` target goes DOWN on a sink with
  `mention_on_page: "<!here>"`
- **THEN** the message does not contain `<!here>`

#### Scenario: Recovery

- **WHEN** a paged target recovers
- **THEN** the recovery message does not contain the mention

#### Scenario: Route mention replaces the sink's

- **WHEN** a target on route `db-team` (`severity: page`,
  `mention: "<!subteam^S0DB>"`) goes DOWN on a sink with
  `mention_on_page: "<!here>"`
- **THEN** the message contains `<!subteam^S0DB>` and not `<!here>`

#### Scenario: Route without a mention

- **WHEN** a target's route sets no `mention` and the sink has
  `mention_on_page: "<!here>"`
- **THEN** a page-severity down from that target contains `<!here>`
