# tsmon

tsmon is a reachability collector for a tailnet. It runs beside a
kernel-mode `tailscaled`, probes the targets in a YAML file, and serves
a status page, Prometheus metrics, and a health check.

It answers a different question from the online/offline webhook: not
whether control thinks a node is connected, but whether traffic to that
node or to a subnet behind it actually gets through.

## Requirements

- A host running `tailscaled` in kernel mode. Userspace networking cannot
  carry ICMP to LAN addresses, so this build refuses it at startup.
- Tag the host `tag:monitor`. tsmon logs a warning when that tag is
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

`examples/tsmon.yaml` has both kinds of target. `nyc-core-switch` and
`nyc-nas` are remote subnet devices behind `nyc-subnet-router`.
`lab-printer` is on the collector's own LAN.

## Build and run

```bash
mise install
mise run build
./bin/tsmon --config examples/tsmon.yaml
```

Config path order is `--config`, then `TSMON_CONFIG`, then
`/etc/tsmon/tsmon.yaml`.

Validate a file without contacting tailscaled:

```bash
./bin/tsmon check-config --config examples/tsmon.yaml
```

Run every check once, print a line each, and exit non-zero on failure:

```bash
./bin/tsmon --once --config examples/tsmon.yaml
```

`--version` prints the build version. `--log-level` (or `TSMON_LOG_LEVEL`)
is `debug`, `info`, `warn`, or `error`. Logs are JSON on stderr. Debug
logs include every check result.

| Flag | Env | Default |
| --- | --- | --- |
| `--config` | `TSMON_CONFIG` | `/etc/tsmon/tsmon.yaml` |
| `--listen` | | `127.0.0.1:9109` or `collector.listen` |
| `--socket` | | platform LocalAPI socket |
| `--log-level` | `TSMON_LOG_LEVEL` | `info` |
| `--once` | | run one pass and exit |
| `--version` | | print the version and exit |

HTTP endpoints on the listen address:

- `/` prober status page
- `/metrics` Prometheus text, `tsmon_` series labeled `target`, `check`,
  `site`, and `class`
- `/healthz` returns `200` and `ok`

SIGINT and SIGTERM stop new work, cancel running checks, and exit 0.

## Docker

The image listens as user `tsmon` (uid 65532) and does not embed a
Tailscale node. Use host networking so checks share the host routes, and
mount the LocalAPI socket. That socket has to be readable by uid 65532,
or pass a `--user` / `--group-add` that can read it.

```bash
docker build -t tsmon .
docker run --rm --network host \
  -v /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock \
  -v /etc/tsmon/tsmon.yaml:/etc/tsmon/tsmon.yaml:ro \
  tsmon
```

### Docker Compose without a dedicated host

`examples/compose.yaml` runs a kernel-mode `tailscaled` container and puts
tsmon in its network namespace, so checks follow that node's `tailscale0`
routes, including accepted subnet routes. Mint an auth key tagged
`tag:monitor`:

```bash
TS_AUTHKEY=tskey-auth-... docker compose -f examples/compose.yaml up -d
```

tsmon starts once the node reports healthy. It reads
`examples/tsmon.yaml` unless `TSMON_CONFIG_FILE` names another file. The
`tailscale` service sets `net.ipv4.ping_group_range`, so the non-root tsmon
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
or `tsping`. Parent cycles are rejected. Learned tailnet parents arrive
in a later change; keep a router's reachability target separate from any
service check on that same node.

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
the archives and `checksums.txt`. Each archive includes `tsmon`, the license,
this README, and `examples/tsmon.yaml`. The binary's `--version` reports the
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
