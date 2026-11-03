# Watch the watcher with a second collector

A dead collector cannot report itself. With two collectors, each can check the
other's `/healthz` through a Tailscale Service, so a stopped process raises an
alert from its peer. Nothing outside the tailnet is involved: no shared state and
no heartbeat service. For a dead-man's-switch service instead, see
[Watch the watcher with a heartbeat](heartbeat.md).

This proves the peer process is running and reachable. `/healthz` answers `ok`
while the process serves, not that its targets are healthy or that its sinks
deliver. Both collectors share the same tailnet and control plane, so a
tailnet-wide outage silences both. Only nodes tagged `tag:monitor` can reach the
Services, and a Service exposes the whole listener, `/metrics` included.

## Prerequisites

- Two collectors on nodes tagged `tag:monitor`, each beside a kernel-mode
  `tailscaled` as in [Deploy tail-watch](deploy.md). A
  [`local_only` sidecar](../explanation/proxygroup-sidecar.md) has no tailscaled
  and cannot take part.
- Tailnet DNS accepted on both nodes (`TS_ACCEPT_DNS=true` in containers), so
  the collector resolves a Service name. See [Deploy on Fly.io](deploy-fly.md).
- HTTPS enabled for the tailnet.
- Admin access to the tailnet policy and the admin console.

The steps were run with Tailscale 1.102.5.

## Steps

The examples call the collectors A and B, with Services `svc:tw-a` and
`svc:tw-b`. Use names that mean something to you, one Service per collector.
Do not host one Service on both: the survivor would keep answering for a dead
peer.

1. Define both Services. In the admin console, open **Services** and add
   `tw-a` and `tw-b`, each on TCP port 443.
2. Allow `tag:monitor` to reach them and to host them. Add to the tailnet
   policy:

   ```hujson
   {
     "grants": [
       {
         "src": ["tag:monitor"],
         "dst": ["svc:tw-a", "svc:tw-b"],
         "ip": ["tcp:443"]
       }
     ],
     "autoApprovers": {
       "services": {
         "svc:tw-a": ["tag:monitor"],
         "svc:tw-b": ["tag:monitor"]
       }
     }
   }
   ```

   Without `autoApprovers`, approve each host by hand in the admin console. A
   `*` destination does not cover a `svc:` destination.
3. On A's node, publish A's listener. On B's node, do the same with `svc:tw-b`:

   ```bash
   sudo tailscale serve --service=svc:tw-a --https=443 --bg 127.0.0.1:9109
   ```

   The UI can stay off. `/healthz` is served either way and never needs a role.
4. Warm the certificate. The first request to a new Service name waits while
   its node obtains a certificate, up to about a minute. From B's node:

   ```bash
   curl -s https://tw-a.<tailnet>.ts.net/healthz
   ```

   It prints `ok`. Do the reverse from A's node for `tw-b`. Without it the first
   checks time out while the certificate is obtained. In testing that took 45 to
   50 seconds, long enough to mark the peer `DOWN` with a 5s interval and
   `fail_threshold: 3`, but not with the defaults (30s and 3).
5. Add the peer as a target in each collector's config. Here is A's, watching B:

   ```yaml
   targets:
     - name: collector-b
       addr: tw-b.<tailnet>.ts.net
       network: tailnet
       checks:
         - "http:https://tw-b.<tailnet>.ts.net/healthz|ok"
   ```

   The URL host must equal `addr`. `|ok` makes a proxy or error page that
   answers `200` fail. Leave `insecure_tls` off. Validate the file, then restart
   the collector:

   ```bash
   ./bin/tail-watch check-config --config /etc/tail-watch/tail-watch.yaml
   ```

6. Send the alert somewhere that does not depend on the peer. A dead peer
   cannot deliver anything for you. Choose the sink with a
   [route](notification-routes.md) on this target.

Verify: both targets show `UP` in `tailwatch_target_state` on `/metrics`. Stop
collector B. A logs an `alert transition` for `collector-b` to `DOWN` with
`layer: service` and the error `HTTP status 502`, because `serve` still
answers while nothing listens behind it. With a 5s interval and
`fail_threshold: 3` that took about 12 seconds. Start B again and A reports `UP`
within a few seconds.

## If it does not work

- `timeout: ... context deadline exceeded` for the first minute is the
  certificate wait in step 4. If it persists, check the grant and the host
  approval in
  [Expose the UI as a Tailscale Service](monitor-ui-service.md#if-the-service-does-not-work).
- `HTTP status 502` means the Service is published but the peer collector is not
  listening on `127.0.0.1:9109`. Start it, or check its `collector.listen`.
- `lookup <name>: no such host` means the node is not accepting tailnet DNS. See
  the prerequisites.
- `body missing "ok"` means the URL reached something other than a collector.
  Check which node the Service name points at.
