# Spec Delta

## ADDED Requirements

### Requirement: Authorization is opt-in

Authorization SHALL be off unless `collector.authorization` is set. With it off, the
authorization decision SHALL permit every request that reaches the listener, exactly as
before, and `GET /api/v1/me` SHALL report `authorization: false`.

#### Scenario: Default off

- **WHEN** the config has no `collector.authorization` section and the UI is on
- **THEN** every `/api/v1` request that reaches the listener is permitted

### Requirement: Roles and what they allow

When authorization is on, every `/api/v1` request except `GET /api/v1/me` SHALL need a
role taken from the configured capability. The role `viewer` SHALL allow `GET
/api/v1/targets`, `/peers`, `/routes` and `/sinks`. The role `editor` SHALL allow
everything `viewer` allows and also `POST`, `PUT` and `DELETE` on targets and routes,
`POST /api/v1/validate`, `POST /api/v1/probe` and `POST /api/v1/route-preview`. A request
whose role does not allow its operation SHALL return 403 with a JSON error naming the
role the operation needs, and SHALL change nothing. A request that needs a role and has
none SHALL return 403 with a JSON error saying no role was granted. A request whose
caller cannot be identified SHALL return 403 with a JSON error saying the caller could
not be identified. The content-type
and origin checks SHALL run before the role check, as they do before the decision today.

#### Scenario: Viewer reads

- **WHEN** a caller with role `viewer` requests `GET /api/v1/targets`
- **THEN** the response is 200 with the monitor list

#### Scenario: Viewer cannot write

- **WHEN** a caller with role `viewer` sends `POST /api/v1/targets`
- **THEN** the response is 403 naming role `editor`, and no monitor is created

#### Scenario: Viewer cannot probe

- **WHEN** a caller with role `viewer` sends `POST /api/v1/probe`
- **THEN** the response is 403 and no probe runs

#### Scenario: Editor writes

- **WHEN** a caller with role `editor` sends a valid `POST /api/v1/targets`
- **THEN** the response is 201 and the monitor is created

#### Scenario: No role

- **WHEN** a caller the tailnet policy gives no role requests `GET /api/v1/targets`
- **THEN** the response is 403 saying no role was granted, and no monitor data is returned

### Requirement: Role from the capability

The role SHALL come from the values of the configured capability in the caller's
capability map. Each value SHALL be a JSON object with a string `role`. When several
values are present the highest role SHALL win, with `editor` above `viewer`. A value
that is not a JSON object, or whose `role` is not `viewer` or `editor`, SHALL be ignored.
A caller with only ignored values SHALL have no role.

#### Scenario: Two grants

- **WHEN** the caller's map holds `[{"role":"viewer"},{"role":"editor"}]`
- **THEN** the caller's role is `editor`

#### Scenario: Unknown role

- **WHEN** the caller's map holds `[{"role":"owner"}]`
- **THEN** the caller has no role and a request that needs one returns 403

### Requirement: Caller resolved by tailscaled

When authorization is on, tail-watch SHALL resolve the caller by asking the local
tailscaled `whois` for the address in `X-Forwarded-For`, and SHALL read that header only
when the connection itself is from a loopback address. The role, login, node name and
tags SHALL come from that answer, and SHALL NOT come from `Tailscale-App-Capabilities`,
`Tailscale-User-Login` or any other request header. When `collector.authorization.service`
is set, the lookup SHALL scope capabilities to that Service. The address MAY be IPv4 or
IPv6. A request on a non-loopback connection, a request with no `X-Forwarded-For`, an
address tailscaled does not know, and a failed lookup SHALL each leave the caller
unidentified and SHALL be denied with 403 and logged at `warn`.

#### Scenario: Forged header ignored

- **WHEN** a caller with role `viewer` sends `Tailscale-App-Capabilities` claiming `editor`
  through `tailscale serve`, which overwrites it
- **THEN** the role is `viewer`, taken from tailscaled and not from the header

#### Scenario: Non-loopback connection

- **WHEN** a request arrives from a non-loopback address with `X-Forwarded-For: 100.64.0.7`
- **THEN** the header is not read, the caller is unidentified and the response is 403

#### Scenario: Lookup fails

- **WHEN** the local tailscaled cannot be reached
- **THEN** the request is denied with 403 and nothing changes

#### Scenario: IPv6 caller

- **WHEN** `X-Forwarded-For` holds a tailnet IPv6 address
- **THEN** the caller is resolved from that address

#### Scenario: Tagged caller

- **WHEN** the caller is a tagged node with no user
- **THEN** the role comes from its capability map and the audit line records its tags

### Requirement: Caller endpoint

`GET /api/v1/me` SHALL be available to any request that reaches the listener and SHALL
return a JSON object with `authorization` (boolean), `user` (the login, empty for a
tagged node), `node` (the node name), `tags` (list), `role` (`viewer`, `editor` or an
empty string) and `identified` (boolean, false when tailscaled could not identify the caller). When authorization is off, `role` SHALL be `editor`, so the UI behaves as
it does today. A caller with no role SHALL receive 200 with `role: ""`, not 403, so the
UI can explain the denial.

#### Scenario: Me for an editor

- **WHEN** an `editor` requests `GET /api/v1/me`
- **THEN** the response holds `authorization: true` and `role: "editor"` with the caller's
  login and node

#### Scenario: Me with authorization off

- **WHEN** authorization is off and any request reaches `GET /api/v1/me`
- **THEN** the response holds `authorization: false` and `role: "editor"`

#### Scenario: Me with no role

- **WHEN** a caller with no role requests `GET /api/v1/me`
- **THEN** the response is 200 with `role: ""`

### Requirement: Endpoints outside the check

Authorization SHALL NOT apply to `/metrics`, `/healthz`, the prober status page or the
static files under `/ui/`. Those SHALL respond as they do with authorization off.

#### Scenario: Metrics unaffected

- **WHEN** authorization is on and a scraper requests `/metrics` with no identity
- **THEN** the response is 200

### Requirement: Startup logging

When authorization is on, startup SHALL log the capability name and whether a Service is
set. It SHALL log a warning when the listen address is not a loopback address, because a
caller that connects directly has no identity and is denied.

#### Scenario: Non-loopback listen

- **WHEN** authorization is on and `listen: 0.0.0.0:9109`
- **THEN** startup logs a warning that direct connections are denied

### Requirement: Verified audit identity

The audit line for every create, replace and delete of a monitor or route SHALL carry
the caller's `user`, `node` and `tags` as tailscaled reports them, and the `role` when
authorization is on, empty when the caller holds none. This SHALL apply with authorization on or off. When the request
carries a `Tailscale-User-Login` that differs from the login tailscaled reports, the line
SHALL carry `identity_mismatch: true` and the login tailscaled reports. When the caller
cannot be resolved, the line SHALL omit `user`, `node` and `tags`. An audit line SHALL
NOT take `user` from a request header.

#### Scenario: Verified login

- **WHEN** a monitor is created by a user whose device tailscaled resolves
- **THEN** the audit line carries that user, node and role

#### Scenario: Forged login

- **WHEN** a request carries `Tailscale-User-Login: admin@example.com` and tailscaled
  resolves a different login
- **THEN** the audit line carries the resolved login and `identity_mismatch: true`

#### Scenario: Unresolved caller

- **WHEN** tailscaled cannot resolve the caller
- **THEN** the audit line carries the remote address and no `user`
