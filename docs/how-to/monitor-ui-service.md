# Expose the UI as a Tailscale Service

A Tailscale Service gives the monitor UI a stable HTTPS name on your tailnet,
for example `https://tail-watch.example.ts.net/ui/`, while tail-watch keeps
listening on loopback. Nothing binds the node's tailnet IP, and the grant names
the Service instead of a port on the node.

Read [Monitor UI risks](../explanation/monitor-ui-risks.md) first: there is no
login, so whoever a grant lets reach the Service can make the collector probe
any address it can route to.

## Prerequisites

- tail-watch running with the UI on and the default listener. See
  [Turn the UI on](monitor-ui.md#turn-the-ui-on). Leave
  `collector.listen` at `127.0.0.1:9109`.
- A node with a kernel-mode `tailscaled` tagged `tag:monitor`, as in
  [Deploy tail-watch](deploy.md).
- Admin access to the tailnet policy and the admin console.

The steps were run with Tailscale 1.102.5.

## Steps

1. Define the Service. In the admin console, open **Services** and add a
   Service named `tail-watch` on TCP port 443. The policy calls it
   `svc:tail-watch`. Advertising a Service that is not defined here does
   nothing visible, so do this first.
2. Allow people to reach it and let the node host it. Add to the tailnet
   policy:

   ```hujson
   {
     "grants": [
       {
         "src": ["autogroup:admin"],
         "dst": ["svc:tail-watch"],
         "ip": ["tcp:443"]
       }
     ],
     "autoApprovers": {
       "services": {
         "svc:tail-watch": ["tag:monitor"]
       }
     }
   }
   ```

   A `*` destination does not cover a `svc:` destination, so a tailnet that
   allows everything still needs this grant. `autoApprovers` is optional.
   Without it you approve the host by hand in step 4.
3. On the tail-watch node, point the Service at the loopback listener:

   ```bash
   sudo tailscale serve --service=svc:tail-watch --https=443 --bg 127.0.0.1:9109
   ```

   The command prints `This machine is configured as a service proxy ... but
   approval from an admin is required` until the host is approved. It prints
   that whether or not the Service is defined, so it does not tell you that
   step 1 is missing.
4. If the host is not auto-approved, open the Service in the admin console and
   approve the node as a host.
5. From a device the grant allows, open
   `https://tail-watch.<tailnet>.ts.net/ui/`.

Verify: `curl https://tail-watch.<tailnet>.ts.net/healthz` prints `ok`, and
`tailscale serve status` on the node lists `svc:tail-watch` with a proxy to
`http://127.0.0.1:9109`.

## Behavior to expect

- The request guard still passes. `Origin` and `Host` both carry the Service
  name, so creating and probing monitors works through the Service.
- The Service exposes the whole listener, so `/metrics` and `/healthz` are
  reachable by everyone the grant allows, not only the UI.
- `remote` in the audit log is the proxy, a loopback address such as
  `127.0.0.1:43322`. serve also tells tail-watch who called, so every `monitor write` and
  `route write` line carries `user` (the caller's tailnet login) and `caller` (their tailnet
  address), and so do the request lines. When serve sends no login, the line has no `user`.
  Keep the grant to the smallest set of admins. See
  [Monitor UI risks](../explanation/monitor-ui-risks.md) for what `user` does and does not
  prove.

## If the Service does not work

Check these in order:

1. The name does not resolve. The Service is not defined in the admin console,
   or no host is approved for it. Define it (step 1) and approve the node
   (step 4).
2. The name resolves and the connection times out. The host is still pending
   approval, or no grant lets your device reach `svc:tail-watch` on `tcp:443`.
3. You added `autoApprovers` after the node first advertised the Service. The
   node advertised before the policy had the entry, so approve it by hand once,
   or run `sudo tailscale serve clear svc:tail-watch` and then the command from
   step 3 again.
4. `tailscale serve status` shows no Service. The `serve` command did not
   persist; run it again with `--bg`.

To remove the Service from the node, run
`sudo tailscale serve clear svc:tail-watch`.
