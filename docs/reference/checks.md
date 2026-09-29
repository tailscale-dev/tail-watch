# Checks reference

A check is one string in a target's `checks` list. Each check runs on its target's
`interval` and is cancelled at its target's `timeout`. A check that times out counts
as a failure. First runs are spread across the interval so checks do not start in a
burst.

| Check | Syntax | Passes when |
| --- | --- | --- |
| ICMP echo | `icmp` | One echo reply arrives before the timeout. |
| TCP connect | `tcp:<port>` | A TCP connection to `addr:<port>` is established. |
| HTTP request | `http:<url>` or `http:<url>\|<text>` | The status is 2xx or 3xx and, if `<text>` is given, the body contains it. |
| Tailscale ping | `tsping` | tailscaled reports a disco pong. |
| TLS | `tls:...` | Rejected. Not supported in this version. |

The same check string cannot appear twice in one target. Every check that fails
gets a failure layer; see [Failure layers](../explanation/failure-layers.md).

## Path and network

Checks use host sockets and do not bind to an interface. The target's `network`
declares the intended path:

- `tailnet` (default): IP checks should use Tailscale. On Linux, a target whose
  `addr` is a literal non-Tailscale IP is a remote subnet and needs accepted
  routes at startup.
- `local`: the host's ordinary route. `tsping` is rejected on a `local` target.

See [Monitor remote subnets and local LANs](../how-to/remote-subnets.md).

## `icmp`

Sends one echo request to the target's address, IPv4 or IPv6. A DNS name is
resolved first and an IPv4 address is preferred. It uses an unprivileged ICMP
socket when the host allows one and otherwise a raw socket.

| Result | Detail |
| --- | --- |
| Pass | Latency is the round-trip time. |
| Fail | `timeout: ...` when no reply arrives. |
| Fail | `cannot open an ICMP socket: set net.ipv4.ping_group_range or grant CAP_NET_RAW` when neither socket type can be opened. |

## `tcp:<port>`

`<port>` is an integer from 1 to 65535. The check opens a connection to
`addr:<port>` and closes it immediately without sending data. Latency is the
connect time. A refused connection fails with an error containing
`connection refused`.

## `http:<url>`

```yaml
checks:
  - "http:https://10.20.0.50:5001/"
  - "http:https://10.20.0.50:5001/|DSM"
```

The URL must use `http` or `https` and include a host. The host must equal the
target's `addr`, ignoring case, a trailing dot, and equivalent IP forms. To check
a different host, define a separate target. Quote the string in YAML because it
contains a colon.

Behavior:

- Sends `GET`. A response of 200 through 399 passes the status test. Any other
  status fails with `HTTP status <code>`.
- Redirects are not followed. A `302` counts as up.
- The text after the first `|` is a literal, case-sensitive substring that must
  appear in the first 1 MiB of the body. If it is missing, the check fails with
  `body missing "<text>"; first 300 bytes: "<excerpt>"`. The URL part therefore
  cannot contain `|`.
- Opens a new connection for every run, ignores `HTTP_PROXY` and related
  environment variables, and verifies TLS certificates.
- With `insecure_tls: true` on the target, certificate verification is skipped for
  every `http:` check on that target.

## `tsping`

Resolves `addr` to a Tailscale or subnet-routed IP and sends a disco ping through
the local tailscaled. It passes when tailscaled reports a pong before the timeout,
and records the path kind, the endpoint or DERP region, and the latency.

Name resolution for a non-IP `addr` prefers, in order: the peer's full MagicDNS
name, its short MagicDNS name, its hostname. Matching ignores case. If several
peers tie, the first by name is used and a warning is logged. If no peer matches,
the name is resolved with DNS.

| Path kind | Meaning |
| --- | --- |
| `direct` | Direct UDP path. The endpoint is recorded. |
| `derp` | Relayed through a DERP region. The region code is recorded. |
| `peer_relay` | Relayed through a peer relay. |

Failures:

| Error | Cause |
| --- | --- |
| `no peer handles <addr>; check grants to tag:monitor` | No peer in the collector's netmap routes the address, usually a missing grant. See [Write the ACLs](../how-to/acls.md). |
| `tsping returned no result` | tailscaled answered without a result. |
| other text | The error reported by tailscaled. |

## `tls:` (rejected)

Any check starting with `tls:` is rejected at load time with
`tls checks are not supported in this version`.

## Parent rules

`parent` is for suppression only. It does not change which node handles the
address. A parent must:

- name another target in the same config;
- have exactly one check, and that check must be `icmp` or `tsping`;
- not create a cycle.

An explicit `parent` is allowed on a `local` target. See
[Alert state](../explanation/alert-state.md) for what a down parent does to its
dependents.

## Result record

Every run produces a result with the target name, the check string (for example
`icmp` or `tcp:445`), the start time, the latency, the error if any, and for
`tsping` the path details. Under `--once`, each result is printed as
`<target> <check> ok <latency>` or `<target> <check> fail <error>`.
