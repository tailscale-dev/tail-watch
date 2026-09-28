# tail-watch

tail-watch is a reachability collector for a tailnet. It runs beside a
kernel-mode `tailscaled`, probes the targets in a YAML file, and serves
a status page, Prometheus metrics, and a health check.

It answers a different question from the online/offline webhook: not
whether control thinks a node is connected, but whether traffic to that
node or to a subnet behind it actually gets through.

## Requirements

- A host running `tailscaled` in kernel mode. Userspace networking cannot
  carry ICMP to LAN addresses, so this build refuses it at startup.
- Tag the host `tag:monitor`. tail-watch logs a warning when that tag is
  missing, because ACL grants are usually written for the tag.
- The process must be able to open the LocalAPI socket. On Linux that is
  `/var/run/tailscale/tailscaled.sock` unless you pass `--socket`.

ICMP needs either unprivileged ping (`net.ipv4.ping_group_range`) or
`CAP_NET_RAW`. If neither is available, ICMP checks fail with that
guidance and TCP, HTTP, and tsping checks still run.

## Remote subnets and local LANs

`network` selects the path a target is supposed to take:

- `tailnet` (the default) means IP checks should use Tailscale. On Linux,
  a target whose address is a literal non-Tailscale IP is treated as a
  remote subnet. Startup fails until the host accepts subnet routes:

  ```bash
  tailscale set --accept-routes
  ```

  An advertised route is not an installed route. After accepting routes,
  check the route the host will actually use:

  ```bash
  ip route get 10.20.0.1
  ```

  The answer should go out the Tailscale interface (`tailscale0`), not a
  local gateway. Repeat that for each remote subnet you monitor.

- `local` means the host's ordinary route, including a LAN the collector
  is plugged into at a site. These checks keep working when that site's
  Tailscale router is down. They cannot use `tsping`. Put a `network:
  local` device in its own target; do not point an HTTP check at a
  different host than the target's `addr`.

`examples/tail-watch.yaml` has both kinds of target. `nyc-core-switch` and
`nyc-nas` are remote subnet devices behind `nyc-subnet-router`.
`lab-printer` is on the collector's own LAN.

## Build and run

```bash
mise install
mise run build
./bin/tail-watch --config examples/tail-watch.yaml
```

Config path order is `--config`, then `TAIL_WATCH_CONFIG`, then
`/etc/tail-watch/tail-watch.yaml`.

Validate a file without contacting tailscaled:

```bash
./bin/tail-watch check-config --config examples/tail-watch.yaml
```

Run every check once, print a line each, and exit non-zero on failure:

```bash
./bin/tail-watch --once --config examples/tail-watch.yaml
```

`--version` prints the build version. `--log-level` (or `TAIL_WATCH_LOG_LEVEL`)
is `debug`, `info`, `warn`, or `error`. Logs are JSON on stderr. Debug
logs include every check result.

| Flag | Env | Default |
| --- | --- | --- |
| `--config` | `TAIL_WATCH_CONFIG` | `/etc/tail-watch/tail-watch.yaml` |
| `--listen` | | `127.0.0.1:9109` or `collector.listen` |
| `--socket` | | platform LocalAPI socket |
| `--log-level` | `TAIL_WATCH_LOG_LEVEL` | `info` |
| `--once` | | run one pass and exit |
| `--version` | | print the version and exit |

HTTP endpoints on the listen address:

- `/` prober status page
- `/metrics` Prometheus text, `tailwatch_` series labeled `target`, `check`,
  `site`, and `class`. The prefix has no hyphen because Prometheus metric
  names cannot contain one.
- `/healthz` returns `200` and `ok`

SIGINT and SIGTERM stop new work, cancel running checks, and exit 0.

## Docker

The image listens as user `tail-watch` (uid 65532) and does not embed a
Tailscale node. Use host networking so checks share the host routes, and
mount the LocalAPI socket. That socket has to be readable by uid 65532,
or pass a `--user` / `--group-add` that can read it.

```bash
docker build -t tail-watch .
docker run --rm --network host \
  -v /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock \
  -v /etc/tail-watch/tail-watch.yaml:/etc/tail-watch/tail-watch.yaml:ro \
  tail-watch
```

### Docker Compose without a dedicated host

`examples/compose.yaml` runs a kernel-mode `tailscaled` container and puts
tail-watch in its network namespace, so checks follow that node's `tailscale0`
routes, including accepted subnet routes. Mint an auth key tagged
`tag:monitor`:

```bash
TS_AUTHKEY=tskey-auth-... docker compose -f examples/compose.yaml up -d
```

tail-watch starts once the node reports healthy. It reads
`examples/tail-watch.yaml` unless `TAIL_WATCH_CONFIG_FILE` names another file. The
`tailscale` service sets `net.ipv4.ping_group_range`, so the non-root tail-watch
image can send ICMP without `CAP_NET_RAW`. `network: local` targets reach
only what the container's Docker network can reach.

`collector.state_dir` is accepted and unused until notification delivery
lands. Do not put secrets in the config file.

## ACLs

The collector sees only what policy allows. A missing grant shows up as
`no peer handles <addr>; check grants to tag:monitor`.

```jsonc
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

## Checks

| Check | Passes when |
| --- | --- |
| `icmp` | one echo reply arrives before the timeout |
| `tcp:<port>` | a TCP handshake completes; the socket is closed without sending |
| `http:<url>` | status is 2xx or 3xx; optional `\|text` must appear in the first 1 MiB |
| `tsping` | tailscaled reports a disco pong for the target address |

`http` does not follow redirects. `insecure_tls: true` on a target skips
certificate verification for devices that present a private certificate.
`tls` checks are rejected until phase 2.

An explicit `parent` must name another target whose only check is `icmp`
or `tsping`. Parent cycles are rejected. That parent is for suppression
only. It does not change which tailnet node handles the address.

## Failure layers

A passing check has no layer. A failing check is labeled with one layer:

| Layer | Meaning |
| --- | --- |
| `unrouted` | no peer owns or routes the address |
| `control` | the handling node is known offline |
| `tunnel` | the handling node is up, but disco fails |
| `service` | the device answered, or the target is a Tailscale node |
| `lan` | disco works and the device does not answer |

`network: local` targets use only `service` and `lan`. They do not consult
tailnet presence or disco.

For a remote LAN target, tail-watch learns the handling node with one disco
ping at startup, every 10 minutes, and when primary routes change. A failed
ping keeps the previous node unless the primary route changed, in which case
the route is used. An explicit `parent` still wins for suppression.

The learned parent is a configured target only when that target's address is
the handling node and its only check is `tsping`. A service check on the same
node does not suppress the LAN devices behind it. Otherwise tail-watch adds
an implicit `tsping` target named for the node's short MagicDNS name, adding
the stable node ID and then a number if that name is taken. An idle implicit
target that is still down stays until it recovers. While a target has no
handling node, tail-watch logs this once, and again only if the grant returns
and is then lost:

```text
target lab-pdu: no peer handles 10.99.0.5; check grants to tag:monitor
```

Prometheus series use the `tailwatch_` prefix:

| Metric | Kind |
| --- | --- |
| `tailwatch_failure_layer_total{target,layer}` | counter, one increment per failure |
| `tailwatch_path{target,kind}` | gauge, `direct`, `derp`, and `peer_relay` |
| `tailwatch_target_parent{target,parent}` | gauge, 1 for the current parent |

## Development and releases

`mise run check` builds, tests, and lints with the pinned tools. The lint
configuration is based on
[maratori/golangci-lint-config](https://github.com/maratori/golangci-lint-config),
with this module's import prefix. CI also runs race tests and builds release
archives for Linux, macOS, and Windows on amd64 and arm64.

Use Conventional Commits in squash-merge titles (`fix: ...`, `feat: ...`,
or `feat!: ...` for breaking changes). After checks pass on `main`,
release-please opens or updates a release PR containing the version and
changelog. Merge that PR to create a GitHub release; GoReleaser then attaches
the archives and `checksums.txt`. Each archive includes `tail-watch`, the license,
this README, and `examples/tail-watch.yaml`. The binary's `--version` reports the
release version.

Repository setup: in **Settings → Actions → General → Workflow permissions**,
enable **Allow GitHub Actions to create and approve pull requests**.
The workflow uses the built-in `GITHUB_TOKEN`. PRs created with that token
do not automatically start CI; close and reopen the release PR to trigger
its checks. Publishing runs in the release workflow itself because tags
created with that token also do not trigger a separate workflow.

To validate packaging locally without publishing:

```bash
mise run release-check
```

Snapshot archives appear in `dist/`. If publishing fails after the release
has been created, rerun the failed job or manually run the **Release** workflow
with its existing tag (for example, `v0.1.0`).
