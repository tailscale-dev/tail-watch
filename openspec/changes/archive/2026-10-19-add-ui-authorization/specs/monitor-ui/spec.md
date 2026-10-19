# Spec Delta

## ADDED Requirements

### Requirement: Role-aware controls

When the UI loads, it SHALL request `GET /api/v1/me` and adapt to the `role` it returns.
For `editor` the UI SHALL behave as it does with authorization off. For `viewer` it SHALL
list monitors, routes and sinks and SHALL hide or disable Add, Edit, Delete and Save
controls on monitors and routes. For an empty role it SHALL show a message that the
tailnet policy has not granted this user a role, or, when `identified` is false, that the
caller could not be identified, and SHALL show no monitor data. While the role is empty,
or a request for it failed, the UI SHALL ask again on its refresh interval and SHALL NOT
treat a failed request as an empty role. The UI
SHALL show the caller's login, or node name for a tagged node, and role. A write the
server still refuses with 403 SHALL be shown as an error naming the missing role.

#### Scenario: Viewer sees a read-only list

- **WHEN** a `viewer` opens the UI
- **THEN** the monitor list shows and there is no Add control

#### Scenario: No role

- **WHEN** a caller with no role opens the UI
- **THEN** the page shows that no role was granted and lists no monitors

#### Scenario: Editor unchanged

- **WHEN** an `editor` opens the UI
- **THEN** Add, Edit and Delete work as with authorization off

#### Scenario: Server refusal shown

- **WHEN** an `editor`'s save is refused with 403 because the policy changed
- **THEN** the form shows an error naming the missing role and the monitor is not saved
