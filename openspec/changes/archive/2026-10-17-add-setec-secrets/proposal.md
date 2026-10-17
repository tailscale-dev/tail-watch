# Proposal

## Why

Every secret tail-watch reads today lives in an environment variable or a file
that something else has to put on the host: an env file, a Kubernetes Secret, a
mounted volume. On a tailnet that already runs a
[setec](https://github.com/tailscale/setec) server, the collector can fetch the
same values from it, authenticated by its tailnet identity and an ACL grant,
with no credential on disk to rotate or leak. Operators who run setec cannot use
it for tail-watch without a wrapper that copies values into an env file.

## What Changes

- Add `setec` as a third secret source beside `*_env` and `*_file`. Every key
  that takes a secret gains a `*_setec` form that names a setec secret:
  `url_setec`, `secret_setec`, `headers_setec` (header name to secret name) and
  `heartbeat.url_setec`. "Exactly one source" now counts three.
- Add a `collector.setec` section with `server` (required when any `*_setec`
  key is used) and `poll_interval` (default `5m`).
- Keep `*_env` and `*_file` exactly as they are. A config that uses neither
  `collector.setec` nor a `*_setec` key behaves byte for byte as before, and no
  setec client is built.
- Build one setec store at startup, declaring every secret the config names.
  Startup stops, naming the secret, when the server is unreachable or a secret
  is missing, as it stops today for an unset variable. The store's in-memory
  cache is the only cache; no secret is written to disk.
- Read the current value at send time, so a rotation in setec reaches the next
  delivery after the next poll with no restart.
- Reject `collector.setec` and every `*_setec` key under `collector.local_only`,
  where there is no tailscaled to reach the server through.
- Make `check-config` validate the shape of setec settings without contacting
  the server, and make `notify-test` build the store so it exercises the real
  secret.
- Document the source, the ACL grant it needs and how to migrate one secret at a
  time, and amend the CONTEXT.md secrets boundary to name `*_setec`.

This does not deliver a ROADMAP.md item; it is new. It is the shared secret
source for the `add-pagerduty-sink` and `add-incidentio-sink` changes: a
sink key those changes add as a `secretRef` (the PagerDuty routing key, the
incident.io token) inherits `*_setec` without further work, and may land
before or after this change.

Non-goals:

- Writing, creating, rotating or deleting secrets in setec. tail-watch is a
  reader only.
- Caching secrets on disk (`setec.FileCache`). It would persist secrets, which
  CONTEXT.md forbids, and it would let startup succeed during a setec outage
  with a stale value.
- Falling back from setec to `*_env` or `*_file` when the server is down. The
  operator picks one source per secret.
- Using setec under `collector.local_only`, and the `tsnetEnv` collector
  (ROADMAP.md phase 2), which would also need a tsnet-backed dialer.
- Exporting the setec store's own metrics.
- Secrets that are not read through `secretRef` today (none), and the monitor UI
  editing any secret reference.

## Capabilities

### New Capabilities

- `setec-secrets`: the `setec` secret source: `*_setec` keys, the
  `collector.setec` section, startup and runtime behavior, `local_only`
  rejection, `check-config` and `notify-test` behavior, and what is logged.

### Modified Capabilities

- `notification-delivery`: the "Secret handling" requirement says secrets come
  only from `*_env` or `*_file`. It adds `*_setec`.
- `collector-config`: the "Collector section" requirement adds `collector.setec`
  and its `local_only` rejection.
- `webhook-sink`: "Webhook sink configuration" accepts `url_setec`,
  `secret_setec` and `headers_setec`.
- `slack-sink`: "Slack sink configuration" accepts `url_setec` and rejects
  `secret_setec` and `headers_setec`.
- `teams-sink`: "Teams sink configuration" accepts `url_setec` and rejects
  `secret_setec` and `headers_setec`.
- `collector-heartbeat`: "Heartbeat configuration" accepts `url_setec`.

## Impact

- Code: new `cmd/tail-watch/setec.go` and `setec_test.go`. `config_sinks.go`
  adds the `*_setec` keys to `rawSink` and `rawHeartbeat`, a `Setec` field on
  `secretRef`, and the three-way "exactly one" rule. `config.go` adds the
  `collector.setec` section and the `local_only` rule. `main.go` builds the
  store in `run()` and binds it to every `secretRef`, as `bindChecks` binds the
  resolver to `tsping`. `notifytest.go` builds the store before `checkSecrets`.
- Dependencies: adds `github.com/tailscale/setec` (client package only). It is
  a Tailscale-owned module, which CONTEXT.md now allows; see design D1. No
  change to `tailscale.com`.
- Config: `collector.setec.server`, `collector.setec.poll_interval` and the
  `*_setec` keys. Nothing existing changes meaning.
- Network: outbound HTTPS to the setec server over the tailnet through the
  `netEnv` dialer. No new listener.
- Docs: a how-to, the config reference, `reference/cli.md` for `check-config`,
  `how-to/troubleshoot.md`, `docs/README.md`, `CONTEXT.md` and `CLAUDE.md`.
- UI: unchanged. `GET /sinks` returns no secret reference.
