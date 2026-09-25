# Design

## Context

The repo is currently the "what" tsnet template: a root `main.go` that embeds
tsnet and serves Tailwind-styled templates. None of that carries over; see
proposal.md for why. The collector is built from `tailscale.com` v1.102.5
packages. Their shapes were checked against the module cache:

- `prober.New()` registers with `prometheus.DefaultRegisterer`. A process can
  therefore create only one `Prober`, and a second `New()` panics on the
  duplicate registration. Its fake ticker (`newForTest`) is unexported.
- `prober.ProbeClass{Probe, Class, Labels, Timeout, Concurrency, Metrics}`.
  `Prober.Run(name, interval, labels, pc)` schedules it.
  `Prober.StatusHandler` returns a `tsweb.ReturnHandlerFunc`.
- `prober.HTTP` requires exactly status 200 and follows redirects.
  `prober.TCP` uses a bare `net.Dialer`. Neither goes through an interface.
- `net/ping.New(ctx, logf, ListenPacketer)` takes a
  `ListenPacket(ctx, typ, addr)` source. `Pinger.Unprivileged` selects
  `udp4`/`udp6` ICMP sockets up front; it is not an automatic fallback.
- `local.Client.Ping(ctx, netip.Addr, tailcfg.PingDisco)` returns
  `*ipnstate.PingResult`, whose fields include `NodeIP`, `Endpoint`,
  `DERPRegionCode`, `PeerRelay`, `LatencySeconds` and `Err`.
- `go.yaml.in/yaml/v3` is already in `tailscale.com`'s module graph.

## Goals / Non-Goals

**Goals:**

- Give the later changes stable seams:
  - a parsed `Config` and `Target` model
  - a `Result` type
  - a result-consumer hook
  - a `netEnv` interface
- Keep all check logic unit-testable without constructing a `prober.Prober`.

**Non-Goals:**

- Interpreting failures. The collector only records and exposes results.
  Classification and alerting arrive in later changes.
- Parsing sinks, heartbeat or discovery config. Until `add-notifications`
  lands, those keys are rejected as unknown.

## Decisions

### D1. Package layout: `cmd/tsmon`, `package main`

The code follows the design doc's file layout: `tsmon.go`, `netenv.go`,
`config.go`, `checks.go` and `results.go`. The doc intends it as a standalone
command that might later move into `tailscale/tailscale`'s `cmd/`. A `main`
package keeps that move a directory copy. Alternative considered: `internal/`
packages. That gives cleaner boundaries, but it makes an upstream move harder,
and nothing else imports the code.

### D2. `netEnv` from day one

```go
type netEnv interface {
    Dial(ctx context.Context, network, addr string) (net.Conn, error)
    ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error)
    LocalClient() *local.Client
}
```

`kernelEnv` backs it with `net.Dialer`, `net.ListenConfig` and
`local.Client{Socket: *socket}`. Tests use a `fakeEnv` built on `net.Pipe`,
loopback listeners and an `httptest.Server`. `netEnv` is our own interface,
so faking it follows the rule against mocking types we don't own.

### D3. Checks reimplement prober logic over `netEnv`

Each check is a small type implementing:

```go
type check interface {
    String() string // "tcp:445"
    Kind() string   // "tcp"
    Run(ctx context.Context, env netEnv, t *Target) (Result, error)
}
```

`Target.Checks` holds these parsed values. `Target.Network` is `tailnet` by
default or explicitly `local`. Startup requires kernel networking and route
acceptance for remote subnet targets; deployment checks the effective host
route. Local targets keep host routing but never learn tailnet dependencies.
An HTTP check must address its target (or an equivalent DNS name); independent
URLs become separate targets so classification uses the right dependency. `http` diverges from `prober.HTTP`
on purpose. It accepts any 2xx or 3xx status, uses
`CheckRedirect: http.ErrUseLastResponse`, supports `insecure_tls`, and gives
each run a fresh `http.Transport` whose `DialContext` is `env.Dial`. This
follows the design doc's table ("Status 2xx/3xx") rather than
`prober.HTTP`'s 200-only rule.

### D4. ICMP permission detection

At startup, `kernelEnv` tries to open an unprivileged `udp4` ICMP socket
(`icmp.ListenPacket("udp4", "0.0.0.0")`) and records whether it worked. ICMP
checks then create `ping.New(ctx, logf, env)` with
`Unprivileged = env.unprivilegedICMP`. When neither socket type opens, each
`icmp` check fails with the actionable error from the spec, rather than tsmon
refusing to start. A config may contain only TCP checks and still be useful.
Each run uses one `Pinger`, as `net/ping` requires ("should not be reused").

### D5. `tsping` address resolution

A literal IP is used as is. For a name, the check first matches it against
the `Status()` peer list: `HostName`, and `DNSName` with and without the
trailing dot or tailnet suffix. The peer list is cached for 30s. If nothing
matches, it falls back to the host resolver. This avoids depending on
`--accept-dns` for Tailscale node names.

tailscaled's own `no matching peer` error is mapped to the spec's
`no peer handles <addr>; check grants to tag:monitor` message.

The path kind is `direct` when `Endpoint != ""`, `peer_relay` when
`PeerRelay != ""`, and `derp` when `DERPRegionCode != ""`.

### D6. `Result` and consumers

```go
type Result struct {
    Target  *Target
    Check   string // "tcp:445"
    Kind    string // "tcp"
    Start   time.Time
    Generation uint64 // runtime release generation, consumed by alerting
    Latency time.Duration
    Err     error
    Ping    *ipnstate.PingResult // tsping only
}
type resultConsumer interface{ Observe(Result) }
```

The prober `Probe` func runs the check, then calls `c.publish(res)` before it
returns `res.Err` to prober. `publish` takes a per-target mutex, so a
consumer sees one target's results in completion order. In this change the
only consumer is a debug logger. `add-failure-classification` inserts the
classifier, and `add-alert-state-machine` adds the alert machine.

The check execution path (`runCheck` → `publish`) is a plain method, not
code buried inside the prober closure. That lets `add-alert-state-machine`
add `recheck(target)`, which runs a target's checks once, outside prober's
schedule. prober exposes no way to run a probe on demand: `RunHandler` is
HTTP-only and refuses continuous probes.

### D7. Clock

`Result.Start` and every later timing read the clock through
`tstime.Clock`, which is `tstime.StdClock` in production and `tstest.Clock`
in tests. prober's own schedule keeps its real ticker, and no test depends on
it.

### D8. Config parsing

- `yaml.v3` with `Decoder.KnownFields(true)` rejects unknown keys.
- Durations are a custom `duration` type that implements
  `yaml.Unmarshaler`.
- Validation is a separate pass that collects all errors (`errors.Join`),
  each prefixed with its location.
- `check-config` shares the same loader.

### D9. HTTP server

This follows `cmd/derpprobe`: `/` is served by
`tsweb.StdHandler(p.StatusHandler(...))`, and `/healthz` returns a plain
`ok`. `/metrics` is `promhttp.Handler()`, since prober already registers on
the default registry, and later changes' custom metrics register there too.

### D10. Flags

`--config`, `--listen`, `--socket`, `--once`, `--version` and `--log-level`,
plus the subcommand `check-config`. The env fallbacks are `TSMON_CONFIG` and
`TSMON_LOG_LEVEL`. The version comes from `-ldflags "-X main.version=…"`,
falling back to `debug.ReadBuildInfo`.

### D11. Repo cleanup and tooling

- Delete `main.go`, `tmpl/`, `static/`, `css/`, `package.json` and
  `bun.lock`.
- Keep `bun` in `mise.toml` and set `[settings] npm.package_manager = "bun"`.
  The only npm tool left is the OpenSpec CLI, and this avoids adding Node back.
- Add mise tasks `build` (`go build ./cmd/tsmon`), `test`, `lint` and
  `check`.
- Build the Dockerfile from `golang:1.27-alpine` into `alpine:3.24`. The
  README documents `--network host`, mounting `/var/run/tailscale`,
  `tailscale set --accept-routes` for remote subnet monitoring, and explicit
  `network: local` for site-local LAN checks.

## Deviations from the design doc

- **`http` checks.** They add `insecure_tls` (per target). The doc's own
  example (`https://10.20.0.50:5001/` on a Synology NAS) usually serves a
  self-signed certificate. Certificate checking belongs to phase 2's `tls`
  check.
- **`tls` checks.** They are rejected in the MVP. The doc's Goals list `tls`,
  but its Phases section places it in phase 2, and this change follows
  Phases.
- **`parent` label.** The prober series omit it, because prober labels are
  fixed at `Run` time and learned parents change. `add-failure-classification`
  exposes parents as their own series.
- **ICMP fallback.** The doc says "unprivileged `udp4` fallback". `net/ping`
  has no fallback, so D4 does the detection itself.

## Risks / Trade-offs

- **[Risk] `prober.New()` panics if called twice.** → Only `main` constructs
  it. Unit tests call check `Run` methods and consumers directly.
- **[Risk] Removing the template breaks anyone building "what" from this
  repo.** → It is a template, not a released service. The proposal marks it
  BREAKING.
- **[Trade-off] A per-target mutex serializes result publishing.** →
  Publishing is in memory and cheap. Checks themselves still run
  concurrently.
- **[Risk] Name resolution for `tsping` may pick the wrong peer when two
  nodes share a `HostName`.** → Prefer an exact `DNSName` match, and log a
  warning on ambiguity.

## Migration Plan

This is not a service migration. The change replaces a template, so
rollback is a `git revert`.
