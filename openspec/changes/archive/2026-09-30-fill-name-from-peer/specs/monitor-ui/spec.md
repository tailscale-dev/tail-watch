## MODIFIED Requirements

### Requirement: Address suggestions

The add and edit form SHALL offer the peers from the peer list as suggestions for the
address field. Choosing a suggestion SHALL fill the address field with the peer's `name`, or
with its first Tailscale IP when the peer has no name, and SHALL also fill the name field
with that same value when the name field is empty or still holds a value the suggestions
filled earlier. A name the operator typed SHALL NOT be overwritten, and the name field of
an existing monitor SHALL NOT change. A suggestion SHALL show the peer's Tailscale IP, tags
and online state so the operator can tell peers apart. The suggestions SHALL be fetched when
the form opens, and free-text entry SHALL always remain possible: an empty list, an
unseeded list, or a failed fetch SHALL NOT block, change or delay submitting an address the
operator typed. When the typed address is an IP address that falls inside the `routes` of a
peer, the form SHALL show which peer handles it. The parent field SHALL be unchanged by
suggestions.

#### Scenario: Pick a peer

- **WHEN** the operator opens the form and chooses the suggestion `lab-nas`
- **THEN** the address field holds `lab-nas` and the name field holds `lab-nas`

#### Scenario: Typed name is kept

- **WHEN** the operator types the name `nas` and then chooses the suggestion `lab-nas`
- **THEN** the address field holds `lab-nas` and the name field still holds `nas`

#### Scenario: Another suggestion replaces a filled name

- **WHEN** the operator chooses `lab-nas` and then chooses `nyc-router`
- **THEN** both fields hold `nyc-router`

#### Scenario: Editing keeps the name

- **WHEN** the operator edits an existing monitor and chooses a suggestion
- **THEN** the address field changes and the name field does not

#### Scenario: Suggestion shows what distinguishes peers

- **WHEN** two peers are offered
- **THEN** each suggestion shows its Tailscale IP, its tags and whether it is online

#### Scenario: Empty or unseeded list

- **WHEN** the peer list is empty or `seeded` is false
- **THEN** the form shows no suggestions and the operator can type any address and
  submit it

#### Scenario: Peer list cannot be fetched

- **WHEN** the request for the peer list fails
- **THEN** the form still opens, no error blocks it, and a typed address is submitted as
  usual

#### Scenario: Address behind a subnet router

- **WHEN** the operator types `10.20.0.50` and `nyc-router` is primary for `10.20.0.0/16`
- **THEN** the form shows that `nyc-router` handles that address, and the parent field
  is not changed

#### Scenario: Address handled by no peer

- **WHEN** the operator types `192.168.99.1` and no peer routes it
- **THEN** the form shows no handling peer
