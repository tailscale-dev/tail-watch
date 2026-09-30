# Proposal

## Why

The monitor UI's Address field is free text (`10.20.0.50 or nas.lab`). An operator has to
know or look up the MagicDNS name or Tailscale IP of a node before adding a monitor, and
a typo only shows up as a failed check. The collector already holds a live, ACL-filtered
list of the peers it can reach (`presence.Peers()`), fed by the `tailscaled` IPN bus, and
nothing exposes it to the UI. Offering that list closes the gap without a credential.

## What Changes

- Add a read-only `GET /api/v1/peers` to the monitor API. It returns the peers the
  collector can currently see: name, MagicDNS name, host name, Tailscale IPs, tags, online
  state (true, false, or unknown) and primary subnet routes, plus a `seeded` flag.
- Before presence is seeded, or while the bus is down, the endpoint returns `200` with
  `seeded: false` and an empty list, not an error.
- The UI's Address field offers those peers as suggestions (`<datalist>`), filled with the
  peer's name (MagicDNS short name, else host name), or its first Tailscale IP when it
  has neither. Free-text
  entry stays fully supported and the form works with an empty list.
- When the typed address is an IP that falls inside a peer's subnet routes, the form shows
  which peer handles it, so the operator can pick a suitable parent. The Parent select is
  unchanged.
- Document the endpoint and the picker, and say in the risks page that anyone who can
  reach the UI can read the peer list.

The endpoint uses no Tailscale API key, no `tscli`, and no new dependency. It does not
change YAML config keys, metric names, payload fields or the checks themselves.

Implements no section of `tailnet-reachability-monitor-design.md`; it extends the monitor
UI added in `add-monitor-ui`. Phase: MVP (phase 1). Non-goals:

- The Tailscale HTTP API, `tscli`, or any credential (an API-backed inventory is a
  possible later change).
- Devices the collector cannot reach, key expiry, and route approval state.
- Changing which monitors the Parent select offers.
- Auto-creating monitors from the peer list.
- Phase 2 and 3 features (PagerDuty, `target.degraded`, quorum, tsnet mode).

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `monitor-management`: adds a requirement for the peer list endpoint, its fields and its
  unseeded state. Existing requirements are unchanged.
- `monitor-ui`: adds a requirement for address suggestions from the peer list, the empty
  and unseeded behavior, and the subnet-route hint. Existing requirements are unchanged.

## Impact

- Code: `cmd/tail-watch/monitor_api.go` (handler and view), `monitor_guard.go` (route),
  `main.go` (give the manager the presence view), `cmd/tail-watch/ui/app.js` and
  `index.html`, plus tests next to them.
- Docs: `docs/reference/cli.md` (endpoint table), `docs/how-to/monitor-ui.md`,
  `docs/explanation/monitor-ui-risks.md`, and `CLAUDE.md` only if the layout note changes.
- No config key, dependency, metric or runtime cost beyond a read of the in-memory view.

## Assumptions

- The list shows only peers, not the collector's own node. Monitoring itself is not useful,
  and presence tracks peers only.
- `routes` is the peer's primary routes, which is what presence tracks (and what the
  classifier uses). It is not the advertised-but-unapproved set.
- The suggestion inserts the peer's `name`, the MagicDNS short name or else the host
  name, because `addr` accepts it and the checks resolve it through the same presence
  view. A peer with neither uses its first Tailscale IP.
- The endpoint is a `GET` and follows `GET /targets`: no JSON content-type check, no audit
  log entry, no `authorize` call. Access control stays the tailnet ACL and the listen
  address.

These were proposed during exploration and not contradicted; revise them here if wrong.
