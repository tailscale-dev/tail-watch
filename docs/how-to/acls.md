# Write the ACLs for tag:monitor

The collector sees only what your tailnet policy allows. Without a grant, a
target fails with `no peer handles <addr>; check grants to tag:monitor`.

Prerequisites:

- Permission to edit the tailnet policy file.
- The collector's node joined with the tag `tag:monitor`. tail-watch logs
  `node is missing tag:monitor` at startup otherwise.

Steps:

1. Make sure the tag has an owner:

   ```hujson
   "tagOwners": { "tag:monitor": ["autogroup:admin"] },
   ```

2. Grant `tag:monitor` access to the tagged nodes you probe. Grant the
   subnets behind a router separately, and only the ports your checks use:

   ```hujson
   {
     "tagOwners": { "tag:monitor": ["autogroup:admin"] },
     "grants": [
       {
         "src": ["tag:monitor"],
         "dst": ["tag:router", "tag:server"],
         "ip": ["*"]
       },
       {
         "src": ["tag:monitor"],
         "dst": ["10.20.0.0/16"],
         "ip": ["icmp:*", "tcp:22", "tcp:445", "tcp:443", "tcp:5001"]
       }
     ]
   }
   ```

   Replace `tag:router`, `tag:server` and `10.20.0.0/16` with your own tags
   and subnets. Each `tcp:<port>` in a target's checks needs a matching
   entry, and `icmp:*` covers `icmp` checks.

3. If a notification receiver is a node on your tailnet, grant the collector
   access to it. Sinks use the host's ordinary routing table, so the
   collector's node must reach them:

   ```hujson
   { "src": ["tag:monitor"], "dst": ["tag:ops-receiver"], "ip": ["tcp:443"] }
   ```

   Add this object to the same `grants` list.

4. Save the policy. Subnet routes also need to be approved and accepted; see
   [Monitor remote subnets and local LANs](remote-subnets.md).

Verify:

```bash
./bin/tail-watch --once --config examples/tail-watch.yaml
```

Each line ends `ok` for a granted check. A blocked TCP check reports an
`i/o timeout`, because the packets are dropped. If a LAN target has no
handling node, the log has `no peer handles <addr>; check grants to
tag:monitor`. See [Troubleshoot](troubleshoot.md).

For the UI port and its grant, see [Manage monitors in the UI](monitor-ui.md).
