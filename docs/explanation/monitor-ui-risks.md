# Monitor UI risks

The monitor UI lets someone add, edit and delete monitors while tail-watch
runs. That is a write capability on a service that otherwise only reads
configuration, and it is off by default for that reason. This page explains
what the UI protects, what it deliberately does not, and how to decide whether
the trade-off is acceptable on your tailnet.

To turn the UI on and use it, see the [monitor UI how-to](../how-to/monitor-ui.md).
The `listen` and `state_dir` keys are in the
[configuration reference](../reference/config.md), and the flag is in the
[command line reference](../reference/cli.md).

## What a write is worth to an attacker

A monitor is an instruction to the collector to send probes to an address of
the writer's choosing. The collector runs on a tailnet node, with whatever
routes and grants its tag has. Someone who can create monitors can therefore
make the collector probe any address it can route to, and read the outcome
back in the monitor's state. In a network with a permissive collector tag,
that is a port scanner planted inside your network, and the results appear on
the status page. The writer can also delete monitors, which silences alerts,
and close open incidents as a side effect (see
[Incidents and queues](incidents-and-queues.md)).

The UI does not expose secrets. Sink URLs and keys never pass through it, and
it can only change monitors that were created in the UI. Monitors from the YAML
file are shown as read-only. So the exposure is the ability to probe and to
silence, not credential theft.

## No login, on purpose

There is no login. tail-watch has no user database, no session handling and no
password to leak. Access control is the network: the listen address decides who
can open a socket, and the tailnet ACL decides who can reach that address.

The alternatives were weighed against a small tool that is meant to run
unattended:

- **Built-in accounts.** They add credentials to store and rotate, a login
  page to defend, and a second identity system next to the one the tailnet
  already has. For a monitor with a handful of operators, that is more attack
  surface than the feature is worth.
- **Tailscale identity from the request.** This is the natural fit for a
  tailnet service, because the caller's identity is already authenticated. It
  is not built in today. The write path passes every request through a single
  authorization decision made before any change, so identity-based
  authorization can be added at that one point later. The decision currently
  permits everything that reaches the listener.
- **Network scoping (chosen).** It relies on controls you already run and audit
  and it has no state of its own.

The cost is that everything reachable is trusted equally. Reading the monitor
list needs no more than reaching the port, and a grant gives write access to
everyone it covers.

## The listen address is the security boundary

The default `listen` address is `127.0.0.1:9109`. Only processes on the
collector host can reach it. That is a safe default, and it is also a reason
the UI is awkward to use from a laptop.

To reach the UI from another device, you bind an address that device can reach,
such as the node's tailnet IP, and grant that device access to the port. Two
things follow.

- **A grant covers a port, not a path.** The same listener serves `/metrics`
  and the status page. Whoever can open the UI can also read the metrics. If
  you scrape metrics from a different set of hosts than you want to edit
  monitors from, the shared port is a constraint, and worth planning your grants
  around (see the [ACL how-to](../how-to/acls.md)).
- **Loopback is shared inside the container network.** In the Compose layout,
  tail-watch shares the `tailscale` container's network namespace, so every
  process in that namespace can reach a loopback listener. "Loopback only"
  means "only things on this network stack", which is not always the same as
  "only this program".

When the UI is on, tail-watch logs at startup that the write API is enabled
and the address it is on, and logs a warning when that address is not
loopback. Binding `0.0.0.0` is the case the warning exists for: it exposes the
write API on every interface, including any that face a LAN.

## What the request guard does cover

Because there is no login, a browser is the main way an outside party could
trick an authorized person into writing. A web page can make your browser send
requests to an address only your machine can reach, carrying nothing but the
request itself. The write API is guarded against the two common forms of that
attack. Every write goes through the same guard, in this order:

1. **Content type.** A request that does not declare `application/json` is
   refused with 415. A browser can send a cross-site HTML form without any
   check, but it can only send form encoded content types. Refusing everything
   that is not JSON turns that whole class of attack into an error.
2. **Origin.** A request whose `Origin` header names a host different from its
   `Host` header is refused with 403. A cross-origin `fetch` with a JSON body
   would need a permission the page cannot obtain, since the server sends no
   cross-origin allowances, and the check refuses it in any case.

Both checks run before the authorization decision and before anything changes.
Two details about the second check are worth knowing. A request with no
`Origin` header is accepted, because command line clients and scripts do not
send one. That is intentional, since the guard targets browsers, and it
means the guard is not access control. The dry-run validation endpoint is
subject to the same two checks but is not audited or authorized, because it
changes nothing. The probe endpoint behind **Check** and **Save** also changes
nothing, but it is authorized and audited like a write. It sends traffic on
demand toward an address the caller names and returns what came back, so
without the guard it would be a port scanner for anyone who can reach the UI.

The list endpoint that returns the monitors is a read, and it is not guarded by
either check. The same is true of `GET /api/v1/peers`, which returns the
tailnet peers the collector can currently see: names, Tailscale IPs, tags,
online state and subnet routes. Anyone who can reach the UI can read that
inventory. It is limited to peers the collector itself can reach — a device the
tailnet policy hides from the collector never appears — and it is readable by
DNS rebinding (below) the same way `/api/v1/targets` is. If that exposure is
not acceptable, leave the UI off.

The pages under `/ui/` also send a Content Security Policy that allows only
same-origin resources, and the page loads nothing from other origins. This
limits what an injected script could do, but it is separate from the write
guard.

## What it does not cover: DNS rebinding

The Origin check compares the `Origin` header to the `Host` header. It assumes
that a page on a foreign site will announce a foreign host. DNS rebinding
breaks that assumption. An attacker serves a page from a name they control, then
changes the name's DNS answer to the collector's address. The browser now
treats the collector as the same origin as the attacker's page, so `Origin` and
`Host` both name the attacker's hostname, and they match. The request carries
the right content type, and the guard sees nothing wrong.

tail-watch does not defend against this. The guard cannot tell a rebound name
from a legitimate one without a list of names it should answer to, and it has
no such list, because the correct name depends on how you reach it: a MagicDNS
name, a tailnet IP, a hostname in the Compose network. Requiring a
configured `Host` allowlist would close the gap, at the cost of one more thing
to configure and to break.

The mitigation is in the network. A rebinding attack needs a browser on your
tailnet, or on the collector host, to visit a hostile page while the collector
is reachable from it. Keep the write API on loopback unless someone needs it,
keep the set of devices with access to the port small, and prefer an admin
browser profile that you do not use for arbitrary sites. If you cannot accept
this risk, leave the UI off, or use it briefly and turn it off again. Editing the
YAML file and restarting remains a complete alternative.

## The audit log

Every create, replace, delete and probe is logged at info level, whether it was
accepted or refused. Each entry has the operation, the monitor name, the
outcome and the caller's remote address. Refused requests are logged too,
including those rejected for content type or origin, which makes probing
visible. `remote` is the address of the connection, so on its own the log
answers "which device did this", never "which person", because tail-watch has no
login. A request that arrives through a proxy or a shared network namespace shows the
address of the intermediary instead.

Behind `tailscale serve` there is more. serve connects from loopback and tells the
backend who called, so when the connection is from loopback tail-watch adds `user`, the
caller's tailnet login from `Tailscale-User-Login`, and `caller`, their tailnet address
from `X-Forwarded-For`. A connection from anywhere else never gets these fields, because
a remote client could have written the headers. A process on the collector host can still
forge them (see
[the host trust boundary](#anyone-on-the-collector-host-is-trusted)). When serve sends
no login, for example for a tagged node, the line has no `user`. Treat `user` as
attribution for an honest operator, not as authentication.

The audit log is your way to reconstruct what changed. Ship it somewhere that
someone who can edit monitors cannot rewrite.

## Anyone on the collector host is trusted

The listener cannot tell a local process from `tailscale serve`. Both connect from
loopback, and the audit log's `user` and `caller` fields come from headers that `serve`
writes and a local process can write too. Anyone who can run a process in the collector's
network stack can therefore reach the write API and name any login or tailnet address as
the caller. They can also read the configuration, the environment and `state_dir`, so on a
host where only trusted administrators have a shell this adds little. The case to weigh
is a shared network namespace: in the Compose layout, and in a ProxyGroup sidecar pod,
every container in the namespace is "local".

tail-watch does not close this gap. Keep shell and container access to the collector host
as tight as the access you give the UI, and treat `user` and `caller` as attribution, not
proof. Closing the gap needs a listener that only `serve` can open, such as a Unix socket,
which is on the [roadmap](../../ROADMAP.md).

## Choosing whether to enable it

The UI makes sense when few, trusted operators manage monitors frequently and
the tailnet ACL already restricts the port to them. It makes less sense when
monitors change rarely, because the YAML file under version control gives review
and history that the UI does not, and monitors created in the UI live only in
`state_dir/targets.json`. That file is written atomically with owner-only
permissions, but nothing reviews it.

If you enable it, the useful controls are, in order: keep the listener on
loopback until needed, scope the grant to the smallest set of admins, watch the
startup warning and the audit log, and remember that the guard is a defense
against browsers, not against people who can reach the port.

## Rolling back

An older binary rejects a YAML file that contains `routes:` or a target
`route:` key, because unknown keys are an error. Remove both before
downgrading. A `targets.json` entry that sets `route` loads on that binary
as an unrouted `page` monitor: the field is ignored. `routes.json` is
ignored. A queued `mention` in `notification-state.json` is ignored.

## Related pages

- [Manage monitors in the UI](../how-to/monitor-ui.md)
- [Write the ACLs](../how-to/acls.md)
- [Incidents and queues](incidents-and-queues.md) for what closing an incident
  on delete means.
