# Monitor remote subnets and local LANs

Each target has a `network` setting that says which path its checks should
take. Pick the value that matches where the device is.

## Monitor a subnet behind a Tailscale router

Use `network: tailnet` (the default) for devices reached through a subnet
router.

Prerequisites:

- A subnet router advertising the route, with the route approved in the
  admin console.
- [Grants](acls.md) from `tag:monitor` to the subnet.

Steps:

1. On the collector host, accept subnet routes. On Linux, startup fails
   until you do, with `remote subnet targets require accepted routes; run
   tailscale set --accept-routes`:

   ```bash
   tailscale set --accept-routes
   ```

2. Check the route the host will actually use. An advertised route is not an
   installed route:

   ```bash
   ip route get 10.20.0.1
   ```

   The answer must go out `tailscale0`, not a local gateway. Repeat this
   for each remote subnet you monitor.

3. Add the targets. Point a `tsping` target at the router, and name it as the
   `parent` of the devices behind it:

   ```yaml
   targets:
     - name: nyc-subnet-router
       addr: nyc-router
       site: nyc
       network: tailnet
       checks: [tsping]

     - name: nyc-core-switch
       addr: 10.20.0.1
       site: nyc
       network: tailnet
       parent: nyc-subnet-router
       checks: [icmp]
   ```

Verify: `./bin/tail-watch check-config --config <file>` accepts the file, and
`./bin/tail-watch --once --config <file>` prints `ok` for each check.

## Monitor a LAN the collector is plugged into

Use `network: local` for a device on the collector's own LAN. These checks
use the host's ordinary route and keep working when that site's Tailscale
router is down.

Steps:

1. Give the device its own target with `network: local`:

   ```yaml
   targets:
     - name: lab-printer
       addr: 192.168.1.50
       site: lab
       network: local
       severity: warn
       checks:
         - icmp
         - tcp:9100
         - "http:http://192.168.1.50/|ready"
   ```

2. Keep the HTTP check on the same host as `addr`.
3. Do not add `tsping`. It is rejected for `network: local`.

Verify: `./bin/tail-watch --once --config <file>` shows the checks passing.
A failure on a local target is labeled `service` or `lan`, never `control`
or `tunnel`. The layers are explained in
[Failure layers](../explanation/failure-layers.md).

`examples/tail-watch.yaml` has both kinds of target. The check syntax is in
the [checks reference](../reference/checks.md), and the target fields are in
the [configuration reference](../reference/config.md).
