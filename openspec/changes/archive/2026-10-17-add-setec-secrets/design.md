# Design

## Context

See proposal.md for why. The behavior contract is `specs/setec-secrets/spec.md`,
plus the deltas to `notification-delivery`, `collector-config`, `webhook-sink`,
`slack-sink`, `teams-sink` and `collector-heartbeat`.

Every secret read goes through one type. `secretRef{Env, File}` in
`cmd/tail-watch/config_sinks.go` has `isSet()` and `resolve()`, and the call
sites (`webhook.go`, `slack.go`, `teams.go`, `heartbeat.go`, `checkSecrets`)
call `resolve()` per delivery. That is the seam: a third source is a new field
and a new case, and no delivery code changes.

Facts about the current code that shape this design:

- **`resolve()` takes no arguments and has no context.** Env and file reads need
  none. A setec read needs a store, which only exists after `netEnv` does, and
  `netEnv` is built in `run()` after the config loads.
- **There is precedent for late binding.** `app.bindChecks` gives each `tsping`
  check the shared resolver after config load, so a parsed value gets a runtime
  dependency without package-level state.
- **Startup is fail-fast.** `resolveSecrets()` runs in `run()` and an unreadable
  secret stops the process. `notify-test` calls `checkSecrets()` per sink.
  `check-config` and `--once` do not resolve secrets.
- **Network access goes through `netEnv`.** The setec client takes a
  `DoHTTP func(*http.Request) (*http.Response, error)`, so it can use the same
  transport shape as `newSinkClient(env)` and follow the host routing table.
- **The setec server is a tailnet service.** It authenticates callers by tailnet
  identity and an ACL grant. A kernel-mode collector already has that identity
  and route; `local_only` has neither.

## Goals / Non-Goals

**Goals:**

- Setec as an additional source with no change for anyone who does not use it.
- One store for the process, declared up front, so a bad name fails at startup.
- No secret value on disk, in a log, in an error or in the API.
- New secret keys added by other changes (`add-pagerduty-sink`,
  `add-incidentio-sink`) get setec by being a `secretRef`.

**Non-Goals:** the list in proposal.md.

## Decisions

### D1: Take the setec client as a dependency

CONTEXT.md says a feature crosses a boundary if it "adds a Go dependency other
than through `tailscale.com`", and CLAUDE.md says to upgrade only
`tailscale.com`. Importing `github.com/tailscale/setec/client/setec` crosses
that line. Alternatives: speak the setec HTTP API directly (about 60 lines for
`GET /api/get`, but then tail-watch owns the wire format, version handling and
the polling and retry logic the store already has), or shell out to a CLI
(rejected: a new runtime dependency and secrets in a process pipe).

Decision: depend on the client package. CONTEXT.md now allows Tailscale-owned
modules (`tailscale.com` and `github.com/tailscale/*`) and has a "Dogfooding
Tailscale" section that names setec as the secrets source, so no exception
needs recording here. The cost is small because setec is
Tailscale's own and its `go.mod` requires `tailscale.com` and Go versions at or
below ours (checked against `main`: `tailscale.com v1.102.3`, `go 1.26.6`, ours
are `v1.102.5` and `1.27.2`), so minimum version selection should keep our
pins. Two things are unverified until `go get` runs, and task 1.1 gates on
them: that the client package pulls in `creachadair/msync` and
`creachadair/mds` but not the AWS or Tink modules the server uses, and that
`go mod tidy` leaves `gvisor.dev/gvisor` on the commit Tailscale pins. If the
second fails, fall back to the direct HTTP client and record why here.

### D2: One source per secret, no runtime fallback

The three sources are mutually exclusive per secret, and tail-watch never falls
back from setec to env or file. A fallback makes the secret's origin depend on
network state at that moment, so a stale env value could shadow a rotated setec
value and the failure would be silent. Mixed sources across different secrets
are fine, which is the migration path: move one URL at a time.

### D3: Config shape: a shared server, a key per secret

`collector.setec.server` holds the one server address and each `*_setec` key
names only the secret. The alternative, a full URL per key, repeats the server,
invites two servers in one config, and puts a host in every sink. The
`*_setec` suffix follows the existing `_env` and `_file` convention, so the
parsing, "exactly one" validation and docs read the same. `poll_interval` is a
key because rotation latency is the one operational knob; its floor of `1m`
keeps a typo from hammering the server.

### D4: Bind the store to `secretRef`s after config load

`secretRef` gains `Setec string` and an unexported `src secretSource`, an
interface with `get(name string) (string, error)`. `Config.bindSecrets(src)`
walks the sinks, `headers_setec` maps and heartbeat once, in `run()`, after the
store is built; `resolve()` gains a `Setec` case that calls `src.get`, and
returns "setec is not configured" if `src` is nil. The store wrapper in
`setec.go` implements `secretSource`, trims the value and rejects an empty one,
so the three sources return the same shape. Alternatives:

- A package-level store. Rejected: the repo forbids package-level network state
  and it makes tests share a store.
- Changing `resolve()` to take a source argument. Rejected: it touches every
  call site and every test for a feature most configs do not use.

The dynamic-target and route code never holds a `secretRef`, so the UI paths do
not need binding. The check for this is task 3.3.

### D5: Declare every secret up front; memory cache only

`setec.NewStore` with `Secrets: <all names>` and `AllowLookup: false` blocks
until every name is present or the context ends, which maps onto the existing
fail-fast rule. A 30 second startup context bounds it. The store's default
in-memory cache means the secrets live only in process memory. `setec.FileCache`
would let a restart survive a setec outage, but it writes secrets to disk,
which the boundary forbids and which would have to be explained to every
reviewer. The price is that tail-watch cannot start while the setec server is
down; document it, and note `*_env` stays available for operators who need
startup independent of setec.

`Logf` is an adapter to `slog` at `warn`, so a failed poll is visible without
logging values. `PollInterval` is `collector.setec.poll_interval`.

### D6: Reach the server through `netEnv`

`DoHTTP` is built from the same constructor shape as `newSinkClient(env)`
(`DialContext: env.Dial`, no redirect following), so the request follows the
host routing table to a tailnet address. The setec server's name resolves
through the host resolver, as any sink URL does; MagicDNS is the operator's
job, as it is for a Slack URL.

### D7: Where `check-config`, `--once` and `notify-test` stand

`check-config` validates shape only (an `https` server, non-empty names, one
source per secret, not `local_only`), so it still runs offline in CI, like it
does for `*_env` today. `--once` does not resolve secrets today, so it builds no
store. `notify-test` calls `checkSecrets()` and sends through real sink code, so
it must build the store, and does so through the same helper as `run()`.

### D8: Authorization is the operator's ACL

tail-watch sends no credential. The setec server decides from the caller's
tailnet identity, and the operator grants `tag:monitor` read access to exactly
the secrets tail-watch names. The how-to states the grant and says to verify its
syntax against the setec README at the version in use rather than copying it.

## Boundary and spec crossings

- **CONTEXT.md "Secrets stay in `*_env` / `*_file` keys"**: widened to
  `*_setec` as well. The rest of the bullet still holds: never logged,
  persisted, returned by the API or editable from the UI. The wording of the
  bullet changes in the same change.
- **CONTEXT.md dependency rule**: CONTEXT.md was amended to allow Tailscale-owned
  modules, so the setec client is within it; see D1. The gvisor pin check in task
  1.1 still applies.
- **CONTEXT.md "No credentials for discovery"**: not crossed. The setec read is
  for notification secrets, not the peer picker or target discovery.
- **CLAUDE.md "upgrade only `tailscale.com`"**: the new module is added once and
  is not upgraded with `go get -u ./...`. CLAUDE.md gets a line saying to bump
  setec on its own when needed.
- **Existing specs**: `notification-delivery`, `collector-config` and the four
  sink and heartbeat specs say "`url_env` or `url_file`"; the deltas widen that
  to include `url_setec`, with no loss of existing scenarios.

## Risks / Trade-offs

- **Startup depends on setec for operators who opt in.** Mitigation: opt-in per
  secret, and `*_env` still works; the how-to says so.
- **Concurrent change in `add-pagerduty-sink`.** Its delta also MODIFIES
  "Secret handling" in `notification-delivery` (adds the PagerDuty routing key
  and body-secret rule). Both are full-requirement replacements, so whichever
  archives second must merge the other's text. Resolve at archive time; the two
  edits are independent (a routing key is one more `secretRef`).
- **Rotation latency up to `poll_interval`.** A leaked URL rotated in setec is
  live in tail-watch only after the next poll. Default `5m`; `Refresh` on a
  signal is a later option and out of scope.
- **Empty or whitespace secret.** Treated as missing, as for env and file.
- **Unverified setec facts.** The ACL grant syntax and the client's transitive
  imports are unconfirmed; tasks 1.1 and 5.1 verify them before docs state them.

## Open Questions

- Should `notify-test` fail a sink whose setec secret is missing, or skip it?
  This design fails it, matching how an unset `*_env` is reported.
- Is `1m` the right `poll_interval` floor?
