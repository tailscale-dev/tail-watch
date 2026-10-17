# Spec Delta

## MODIFIED Requirements

### Requirement: Write authorization

Every request that changes a monitor SHALL be authorized by a single decision made
before any change. When `collector.authorization` is not set, the decision SHALL permit
every request that reaches the listener, so access control is the tailnet ACL and the
listen address. When it is set, the decision SHALL be the role check defined by
`ui-authorization`. A request the decision denies SHALL return 403 and change nothing.
When the UI is on, startup SHALL log that the write API is enabled and the listen
address it is served on, and SHALL log a warning when that address is not a loopback
address.

#### Scenario: Startup log

- **WHEN** the collector starts with the UI on and `listen: 0.0.0.0:9109`
- **THEN** it logs that the write API is enabled on that address and warns that access
  is controlled only by the network

#### Scenario: Denied write

- **WHEN** the authorization decision denies a request
- **THEN** the response is 403 and no monitor changes

#### Scenario: Authorization off permits

- **WHEN** `collector.authorization` is not set and a request reaches the listener
- **THEN** the decision permits it

### Requirement: Write audit log

The collector SHALL log every create, replace and delete, accepted or refused, at info
level with the operation, the monitor name, the outcome, the caller's remote address
and the verified identity fields defined by `ui-authorization`.

#### Scenario: Accepted write is logged

- **WHEN** a monitor is created from `100.64.0.7`
- **THEN** a log line records the create, the name and `100.64.0.7`

#### Scenario: Identity in the log

- **WHEN** a monitor is created by a user whose device tailscaled resolves
- **THEN** the log line also carries that user and node
