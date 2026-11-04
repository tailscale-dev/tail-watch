# Spec Delta

## MODIFIED Requirements

### Requirement: Check syntax

Each entry in a target's `checks` list SHALL be one of: `icmp`; `tcp:<port>`;
`tls:<port>`; `http:<url>` optionally followed by `|<text>` where `<text>` must
appear in the response body; or `tsping`. `<port>` SHALL be an integer from 1 to
65535. The config loader SHALL reject any other form, including a `tls:` value
that is not `tls:<port>`.

#### Scenario: HTTP check with body match

- **WHEN** a target lists `"http:https://10.20.0.50:5001/|DSM"`
- **THEN** the check requests `https://10.20.0.50:5001/` and requires the body to
  contain `DSM`

#### Scenario: Invalid TCP port

- **WHEN** a target lists `tcp:70000`
- **THEN** loading fails with an error naming the target and the check

#### Scenario: TLS port check

- **WHEN** a target lists `tls:443`
- **THEN** the target loads and the check string is `tls:443`

#### Scenario: Invalid TLS port

- **WHEN** a target lists `tls:70000`
- **THEN** loading fails with an error naming the target and the check

#### Scenario: Phase 2 check type

- **WHEN** a target lists `tls:10.20.0.50:443`
- **THEN** loading fails with an error naming the target and the check
