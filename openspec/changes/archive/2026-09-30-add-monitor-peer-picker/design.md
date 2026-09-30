# Design

## Context

The monitor UI (`ui_serve.go`, `ui/`) talks to a JSON API mounted by
`monitorManager.routes` in `monitor_guard.go`. `GET /api/v1/targets` is registered
without `guard`; only writes and `POST /validate` go through it. The manager is built in
`mountMonitorUI` (`main.go`) from the config, the registry, the alerter and the logger, and
has no access to presence. The Address field in `ui/index.html` is a plain text input with a
placeholder, and `ui/app.js` fills the Parent select from the monitor list.

`presence` (`presence.go`) already holds one `peerInfo` per peer: `Name` (MagicDNS short
name, else host name), `DNSName`, `HostName`, `IPs`, `Routes` (primary routes),
`AllowedIPs`, `Tags` and `Online`. `Peers()` returns cloned entries sorted by `Name`. It
excludes the collector's own node. `seededNow()` reports whether the view is seeded;
`markUnseeded()` clears only that flag when the bus drops, so `Peers()` can still return
the last-known entries while unseeded. See proposal.md - Why for motivation.

## Goals / Non-Goals

**Goals:**

- The UI can suggest exactly the peers the collector can currently see, with no credential.
- An unseeded or empty view degrades to today's free-text behavior.
- The endpoint follows the existing read-endpoint pattern.

**Non-Goals:**

- Anything from the Tailscale HTTP API or `tscli`, devices hidden from the collector, key
  expiry, route approval state, and changes to the Parent select (see proposal.md).

## Decisions

### A narrow interface on the manager, not the whole `presence`

Give `monitorManager` a field of a small interface:

```text
type peerSource interface {
    Peers() []peerInfo
    seededNow() bool
}
```

set in `mountMonitorUI` from `a.presence`. A nil source behaves as unseeded. Alternative:
pass `*presence`. Rejected: the manager would depend on watcher internals, and a test would
need a real presence with a fake bus, where a two-method fake is enough.

### Handler returns empty when unseeded, even if `Peers()` has entries

`handlePeers` calls `seededNow()` first. If false it returns `{"seeded": false, "peers":
[]}` without calling `Peers()`. Reason: `markUnseeded` keeps the last-known entries, and
offering peers from a view that says it is unknown would contradict the peer-presence
requirement that a disconnected view reads as unknown. The response always carries
`"peers": []`, never `null`, so the client needs no null check.

### Response shape and field names

`{"seeded": bool, "peers": [{name, dns_name, host_name, ips, tags, online, routes}]}` with
snake_case names like the rest of the API. `online` is `true`, `false` or `null`, taken from
`Online` (an `opt.Bool`), because unknown must stay distinct from offline. `ips` and
`routes` are strings; `tags` is `[]` rather than `null`. Alternative: reuse `peerInfo`
directly with JSON tags. Rejected: it exposes `NodeID`, `StableID` and `AllowedIPs`, which the
picker does not need, and couples the API to an internal struct.

### Unguarded `GET`, like the list

Register `mux.HandleFunc("GET /api/v1/peers", m.handlePeers)` next to `GET /targets`. It
changes nothing, so it skips the JSON content-type check, the `authorize` decision and the
audit log, consistent with `GET /targets`. Alternative: run it through `guard(..., false)`.
Rejected: `guard` requires `Content-Type: application/json` on the request, which a plain
`fetch` `GET` does not send. Consequence, recorded in the risks page: the list is readable by
anything that can reach the listener, exactly like the monitor list, and DNS rebinding can read
it the same way it can already read `/targets`.

### UI: a `<datalist>` and a client-side hint

- Add `<datalist id="peers">` and `list="peers"` on `#f-addr`. Fetch `/api/v1/peers` when the
  form opens, not on the 10 second monitor refresh, since the list is only useful while
  the form is open. Each `<option>` has `value` set to the peer's `name` (or first IP when
  `name` is empty) and a `label` with the IP, tags and `online`/`offline`/`unknown`.
- The routing hint compares a typed IPv4 or IPv6 address with each peer's `routes` in the
  browser, choosing the longest matching prefix, and writes `Handled by <name>` in a
  `<p class="hint">` under the field. It does nothing for a name that is not an IP.
- A failed fetch, `seeded: false` and an empty list all leave the datalist empty and show
  nothing. No error is surfaced for the picker.
- The script keeps building DOM with `el()` and `textContent`, never `innerHTML`, so a
  hostile tag or host name cannot inject markup, in line with the page's Content Security
  Policy.

Alternative: a custom dropdown widget. Rejected: `<datalist>` is native, works without a
build step, allows free text, and needs no new CSS beyond a hint line.

### Parent select is unchanged

The Parent must name an existing monitor with exactly one `icmp` or `tsping` check. A peer is
not a monitor, so suggesting one there would create a value validation rejects. The hint
tells the operator which router handles an address so they can pick an existing monitor of
that router.

## Risks / Trade-offs

- [Tag and host names are attacker-influenced strings shown in the page] → Render only with
  `textContent`/attributes, covered by a test that a name containing `<img onerror>` is not
  parsed as markup.
- [The unauthenticated list reveals tailnet inventory to anyone who reaches the UI] → It
  shows only peers the collector can already reach, and the UI's access model is unchanged.
  State it in `monitor-ui-risks.md`. Operators who cannot accept it leave the UI off.
- [The suggestion shows a peer the collector cannot in fact probe with the chosen check
  types, for example a node with no port open] → The list means "visible", not "healthy".
  Say so in the how-to.
- [The list is large on a big tailnet] → The datalist filters as the operator types. No
  paging in this change; revisit if a tailnet with thousands of peers reports slowness.

## Open Questions

- None that change what gets built. Whether a later API-backed inventory should share this
  response shape is left for that change.
