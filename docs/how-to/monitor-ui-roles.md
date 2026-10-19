# Give the monitor UI viewer and editor roles

The monitor UI can require a role from the tailnet policy. With
`collector.authorization` unset, whoever can open the listener can read and
change monitors, which is the behavior described in
[Monitor UI risks](../explanation/monitor-ui-risks.md). This page turns that
check on.

Identity stays the tailnet's. tail-watch has no login page, user database or
password. It asks the local tailscaled who the caller is.

## Choose a capability name

Pick a name in a domain you control, with a `/cap/` segment, for example
`example.com/cap/tail-watch`. The policy API rejects a name in the
`tailscale.com` domain, and `tail-watch check-config` rejects it too.

Each grant's value is a JSON object with `role` set to `viewer` or `editor`.
`viewer` can list monitors, routes and sinks. `editor` can also create,
change and delete monitors and routes, and can validate, preview and probe.
When several grants match, the higher role wins. An unknown role is ignored.

## Grant the role

A caller needs a network grant that reaches the UI, and an app grant that
carries the capability.

Set `collector.authorization.service` to the Service name and put the app
grant on that Service. Checked on the lab tailnet on 2026-10-17: a grant whose
destination is only `svc:tail-watch` is returned by `WhoIsForService` and is
not returned by a plain `WhoIs`. The node-tag app grant is not required when
`service` is set. Exposing the UI as a Service is in
[Expose the UI as a Tailscale Service](monitor-ui-service.md).

```hujson
{
  "grants": [
    {
      "src": ["group:ops"],
      "dst": ["svc:tail-watch"],
      "ip": ["tcp:443"]
    },
    {
      "src": ["group:ops"],
      "dst": ["svc:tail-watch"],
      "app": {
        "example.com/cap/tail-watch": [{"role": "viewer"}]
      }
    },
    {
      "src": ["group:admins"],
      "dst": ["svc:tail-watch"],
      "app": {
        "example.com/cap/tail-watch": [{"role": "editor"}]
      }
    }
  ]
}
```

Leave `service` unset only when there is no Service in front of the listener.
Plain `WhoIs` then reads a grant whose destination is the collector node's
tag, and that same grant was also visible to `WhoIsForService` in the lab run.
A listener reached by the node's own tailnet IP uses the port you bound,
often `tcp:9109`, in place of `tcp:443`.

```hujson
{
  "src": ["group:admins"],
  "dst": ["tag:monitor"],
  "app": {
    "example.com/cap/tail-watch": [{"role": "editor"}]
  }
}
```

The 2026-10-16 spike is about the header `serve` forwards, which needed the
node-tag grant. tail-watch does not read that header. The lab check is in
[Live authorization test](../contributing/live-authz-test.md).

## Turn the check on

```yaml
collector:
  listen: 127.0.0.1:9109
  state_dir: /var/lib/tail-watch
  ui: true
  authorization:
    capability: example.com/cap/tail-watch
    service: svc:tail-watch   # optional
```

`ui` must be on, and `local_only` must be off, because the check asks
tailscaled. `check-config` reports each problem with its field. `--ui=false`
turns the UI off and then refuses this section.

Keep `listen` on loopback and put `tailscale serve` in front of it. A client
that connects directly has no `X-Forwarded-For` from serve, so tail-watch
cannot tell who it is and denies the API call. Startup logs a warning when
the listen address is not loopback.

You do not need `--accept-app-caps` on `serve`. That flag copies the
capability into a request header. tail-watch does not read the header. It
asks tailscaled. The flag is unnecessary and harmless.

`/metrics`, `/healthz`, the status page and the files under `/ui/` are not
checked. A network grant that can open the UI can also scrape metrics.

## See the caller's role

`GET /api/v1/me` needs no role. It returns the login, the node, the tags and
the role tailscaled reported:

```json
{"authorization": true, "user": "alice@example.com", "node": "phone", "tags": [], "role": "viewer", "identified": true}
```

`role` is `""` when the policy granted none. `identified` is `false` when
tailscaled could not say who the caller is, for example a direct connection to
the collector's port or a failed lookup. The UI then says which of the two
happened and lists no monitors. It asks again every few seconds, so a caller
who is granted a role, or whose lookup recovers, gets the monitors without a
reload. A `viewer` sees the lists and no Add, Edit, Delete or Save control.
An `editor` uses the UI as it works with the check off.

A request that needs a role and does not have it gets `403` and changes
nothing. The JSON `errors` list names the role, or says that no role was
granted.
