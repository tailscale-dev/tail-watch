# Live authorization test

This page is for contributors who need to see which grants `WhoIs` and
`WhoIsForService` honor before changing the monitor UI role check.

`TestLiveAuthzWhoIs` is off unless `TAIL_WATCH_LIVE_AUTHZ=1`. It asks the
tailscaled inside the live-stack Docker node, never the host tailscale client.
The call is `tailscale debug localapi` on that node's socket. The image's
`127.0.0.1:9002` port is only the health check, not LocalAPI. Give it the
caller's tailnet address in `TAIL_WATCH_LIVE_CALLER`. It logs the capability
map from plain `WhoIs` and from `WhoIsForService` for
`TAIL_WATCH_LIVE_SERVICE` (default `svc:tail-watch`), then repeats `WhoIs` with
a bare IPv4 address, a bare IPv6 address and an `ip:port`.

```bash
scripts/live-stack.sh up
TAIL_WATCH_LIVE_AUTHZ=1 TAIL_WATCH_LIVE_CALLER=<tailnet-ip> \
  mise exec -- go test -count=1 -timeout 2m -run TestLiveAuthzWhoIs -v ./cmd/tail-watch
scripts/live-stack.sh down
```

`TAIL_WATCH_LIVE_CONTAINER` names the tailscale container when it is not the
`tailscale` service of project `tail-watch-live-stack`.

## Tailnet policy

The collector node is the live-stack `tailscale` service. The caller is some
other node on the same tailnet. Use a capability in a domain you control, such
as `example.com/cap/tail-watch`. The policy API rejects a name in the
`tailscale.com` domain.

Three grants, kept separate so each run can drop one:

- A network grant that lets the caller reach the collector node, or
  `svc:tail-watch` if that Service is defined on it.
- An `app` grant of the capability to the collector node's tag, with
  `{"role":"viewer"}` or `{"role":"editor"}`. This is the grant the 2026-10-16
  spike found was required before `serve` forwarded the capability header.
- An `app` grant of the same capability to `svc:tail-watch`, and no grant to
  the node tag. This is the case the test exists to settle.

Run the test once for each caller state:

1. Grant to the node tag only.
2. Grant to the Service only.
3. No capability grant. The network grant can stay, so the node is still
   visible to `WhoIs`.

Record which call returns the capability in each state. A lookup that does not
know the address is a failed test, not a "no grant" result.

## What the log is for

The test prints the status and the capability map. It does not decide which
grant is the right one to document. Copy the three results into the design's
note on `WhoIs` and `WhoIsForService`, then tear the stack down.
