# CLAUDE.md

## Toolchain

- Go and Bun are pinned in `mise.toml`. Run commands through `mise exec --`
  or `mise run <task>` (`build`, `test`, `lint`, `check`).
- Bun is only the package manager for mise's npm backend (the OpenSpec CLI).
  `mise.toml` sets `npm.package_manager` to `bun`. There is no Node app and
  no `package.json`.
- `mise run build` writes `bin/tail-watch`. `mise run check` builds, tests, and
  lints.

## Layout

- Collector code is `package main` in `cmd/tail-watch/`: `main.go`, `netenv.go`,
  `config.go`, `checks.go`, `results.go`, `presence.go`, `classify.go`,
  `learn.go`, `alert.go` (plus `alert_emit.go`, `alert_flap.go`,
  `alert_graph.go`, `alert_target.go`, `alert_log.go`), and `recheck.go`. Tests sit next to
  that code.
  The binary is `tail-watch`. Prometheus series use the `tailwatch_` prefix
  because metric names cannot contain a hyphen.
- The monitor UI is off unless `--ui` or `collector.ui`: `dynamic.go` (`targets.json`
  store and startup merge), `monitor_api.go` (manager and handlers, one mutex,
  persist then apply), `monitor_routes.go` (route create, replace and delete,
  the sink list and route preview; persist `routes.json`, then publish the
  route table), `monitor_guard.go` (content type, Origin, `authorize`, audit
  log), `ui_serve.go` and `ui/` (embedded plain HTML, CSS and JS, no build step).
  `Target.Dynamic` marks UI monitors; the alerter may remove one while `DOWN`.
  `classifier.AddConfigured` registers them and republishes the parent graph, which
  the classifier owns, so never call `UpdateParents` from elsewhere. `runtimeRegistry`
  calls it for dynamic targets. Re-adding an edited monitor overwrites the removed
  snapshot, so the manager closes the incident directly with `closeRemoved`.
- `POST /api/v1/probe` holds the manager mutex only for validation, then runs each check
  through `runCheckWith` (the prober's error normalization) after `bindChecks` gives
  `tsping` the shared resolver. A failing probe never refuses a save server-side; the UI
  gates Save on it with a confirm dialog. `guard` takes the audit `op` at registration;
  an empty op means unaudited and unauthorized (validate and
  `POST /route-preview`). `GET /routes` and `GET /sinks`, like `GET /targets`,
  are not wrapped. Route writes use `route.create`,
  `route.replace` and `route.delete` and log `route write`. The manager's
  `published` pointer is the notifier's route table when a notifier exists, so
  the next down sees a route write. A route edit does not re-register monitors
  or close incidents. The field is not named `routes`: that is the method that
  registers the HTTP handlers.
- `presence.go` is the IPN bus view. `tsping` name resolution uses it and
  falls back to the 30s `Status()` cache only while that view is unseeded.
- `collector.local_only` runs the collector with no tailscaled (the ProxyGroup
  sidecar placement): `config.go` defaults a network-less target to `local`
  and rejects `network: tailnet`, `tsping` and the UI; `main.go` then skips
  the LocalAPI preflight, presence view, resolver binding and dependency
  learner, and takes the identity from `os.Hostname()`, so payloads carry
  `collector.name` and omit `collector.tailscale_ip`. The classifier already
  accepts a nil presence and classifies `network: local` failures from device
  evidence alone.
- `alert.go` is one mutex-guarded state machine. Grace and flap timers hand work
  to a goroutine from `AfterFunc`: `tstest.Clock.Advance` holds the clock
  lock while callbacks fire, so they cannot call back into the clock.
  `recheck.go` runs at most
  eight immediate rechecks. `Result.Generation` is the per-target release
  counter; `execute` stamps the value from when the check acquired its lock.
- Notifications: `event.go` (payload, ids, transition mapping, routing),
  `incidents.go` (incident and per-sink delivery state, `restoreOpen`), `queue.go`
  (bounded queues, atomic `notification-state.json`, startup replay), `notify.go` (the
  coordinator and per-sink workers), `deliver.go` (shared retry loop), `sink.go`,
  `webhook.go`, `slack.go`, `teams.go`, `heartbeat.go`, `notifytest.go`,
  `notify_metrics.go`, and sink schema in `config_sinks.go`.
- One `notifier` mutex guards the whole `book` (incidents plus queues), and every mutation
  commits the snapshot under it, so workers only see durable intents after the commit.
  `ObserveTransition` only writes the 4096-slot intake channel; never add I/O to it.
  A dropped transition is repaired by `reconcile()` from the alerter's dirty snapshots
  (1s ticker); target removal reaches the notifier only that way.
- Notification tests use `notify_helpers_test.go`: `countingClock` wraps `tstest.Clock`
  and counts timers and tickers. Wait with `waitTimers`/`tickers` before `Advance`, or a
  worker that has not armed its timer yet misses the advance. Retry timers and group-wait
  timers both come from `NewTimer`, so count them when scripting a sequence.
- Golden files live in `cmd/tail-watch/testdata/{payload,slack,teams}`; regenerate with
  `go test ./cmd/tail-watch -run 'TestPayloadGolden|TestSlackGolden|TestTeamsGolden'
  -update`, then review the diff. The package must come before `-update`, or Go stops
  reading packages at the unknown flag and fails with "no Go files in .".
- Startup order matters: `newNotifier` (corrupt state or unset secrets stop startup) runs
  in `run()` before the prober; `restoreOpen` runs in `startCollectors` after the alerter
  and classifier exist and before any target is scheduled. `--once` never builds a notifier,
  resolves secrets, or sends a heartbeat.
- `alerter.RemoveTarget` refuses a configured target with an open incident but allows a
  DOWN implicit one. The learner removes a restored learned router only after an
  authoritative reconciliation, and the removed dirty snapshot makes the notifier close
  the incident with `reason: removed`.

## Documentation

- User docs live in `docs/`, split by Diataxis quadrant: `tutorial/`, `how-to/`,
  `reference/`, `explanation/`, and `contributing/`. `docs/README.md` is the index and
  `README.md` is only an overview with links. A fact lives in one page; link to it.
- When a config key, flag, metric, event field, or log message changes, update
  `reference/config.md`, `reference/cli.md`, `reference/metrics.md`,
  `reference/payload.md`, or `how-to/troubleshoot.md` in the same change.
- `CONTEXT.md` holds the project's hard boundaries and `ROADMAP.md` the plan. The roadmap
  only pulls from GitHub: `/roadmap sync` links issues as they are filed and never pushes
  `roadmap/*` or `effort/*` labels (`gh` cannot reach this repo anyway).
- Check a config snippet with `tail-watch check-config`. `check-config` and `--once`
  do not resolve `*_env` secrets, so it passes with them unset.

## Commits

- Follow the Tailscale commit style in `AGENTS.md`: a `dir: verb ...`
  subject, a plain-text body, and a `Fixes #n` or `Updates #n` trailer. It
  does not use Conventional Commits, so do not run commitlint.
- The exception is the PR title: release-please reads the squash-merge title,
  so write it as `feat:`, `fix:`, `perf:`, `revert:` or `docs:`. Commits with
  only a `dir: verb` subject never open a release PR.

## Container image

- Releases push `ghcr.io/tailscale-dev/tail-watch` (`X.Y.Z`, `X.Y`, `latest`; amd64 and
  arm64) from the `publish` job through GoReleaser `dockers_v2` and `Dockerfile.release`,
  which copies the prebuilt binary from `$TARGETPLATFORM/`. `Dockerfile` is still the
  from-source build; `TestDockerfilesAgree` keeps their runtime stages equal.
- `mise run release-check` builds the image per platform without pushing, as
  `-amd64`/`-arm64` tags. The arm64 build needs QEMU and fails with `exec format error`
  on a host without it; CI installs it. The local `ghcr.io/...-amd64` tags it leaves are
  safe to `docker image rm`.
- The first push makes the GHCR package private; an org admin sets it public once in
  the web UI (`gh` cannot reach this org). See `docs/contributing/releases.md`.

## Upgrading Go dependencies

- Upgrade only `tailscale.com` (`go get tailscale.com@vX.Y.Z && go mod tidy`).
  Do not run `go get -u ./...`: it moves `gvisor.dev/gvisor` off the commit
  Tailscale pins and the build fails with "found packages stack and bridge".
- When bumping Go, update `mise.toml`, the `go` directive in `go.mod`, and
  `golang:<ver>-alpine` in the `Dockerfile` together.

## OpenSpec workflow

- The OpenSpec CLI is pinned in `mise.toml` (`npm:@fission-ai/openspec`); run
  it as `mise exec -- openspec ...`. Project context and artifact rules live
  in `openspec/config.yaml`.
- All 12 workflows are installed (`/opsx:propose`, `/opsx:apply`,
  `/opsx:verify`, `/opsx:archive`, ...). The workflow list is global OpenSpec
  config; after changing it, run `openspec update .` to regenerate `.claude/`.
- `openspec/specs/` is the behavior contract. The original design doc was deleted on
  2026-10-04; its remaining phases and open questions live in `ROADMAP.md`. Archived
  changes still cite it, and `git show 3c1f09a:tailnet-reachability-monitor-design.md`
  recovers it.
- Validate with `openspec validate <change> --strict` after editing artifacts.
- `.claude/` is generated by OpenSpec and excluded from markdownlint. MD013 is
  off under `openspec/` because apply tracks one task per checkbox line.

## Monitor UI browser tests

- `cmd/tail-watch/ui_browser_test.go` drives the embedded UI in Chromium with
  `playwright-go` against the real manager and a fake `peerSource`. It needs no tailnet,
  and skips when the driver is missing (`docs/contributing/ui-browser-test.md`).
- Playwright cannot click the native `<datalist>` popup, so tests `Fill` the Address
  with a suggestion's value. The datalist filters by the field's current value, so after
  a pick only the matching entry shows; this is accepted browser behavior.
- `ui_route_test.go` runs the route-matching functions of `app.js` under bun or node, so keep
  them between `function peerValue` and `// updateRouteHint` and free of DOM access.
- `ui_routes_test.go` runs the `// --- route form` block of `app.js`
  (`routeOptionLabel`, `reasonWords`, `previewWords`, `downNotifiesNoOne`,
  `routingChoice`, `routingPayload`, `routingText`) under bun or node, so keep
  that block free of DOM access. The Routing select sends `route` or
  `severity`, never both. The preview waits about 300 ms and drops a response
  once a newer one has been requested.
- `teams_render_test.go` renders every `testdata/teams/*.json` golden with the vendored
  `adaptivecards` renderer, Teams host config and `markdown-it` in
  `testdata/teams/render/` (versions in `docs/contributing/ui-browser-test.md`), through
  the shared `browserPage` helper. `teamsHeadlineColors` must list every golden, so a new
  golden needs a row there. Teams markdown turns a single `\n` into a space, so card detail
  lines are joined with `\n\n`.
- The Routes view is the `#routes` hash in the same `index.html`. Sinks on
  that page are read-only. A route from the config file has no Edit or Delete.
  Field errors are scoped to the open form, because both forms use
  `data-field="name"`.
- `ui_checks_test.go` runs the `// --- check conversion` block of `app.js` (`checkToString`,
  `checkFromString`) under bun or node, so keep that block free of DOM access. Check rows
  are the form's view of the check strings: the API and `targets.json` are unchanged, rows
  are never dropped on submit so a server `checks[i]` error is row `i`, and an HTTP row's
  host always comes from Address.
- To look at the UI against real nodes, use `scripts/live-stack.sh up` (not a host
  tailnet), run the collector container with `ui: true` and `listen: 0.0.0.0:9109`, and open
  the tailscale container's bridge IP from WSL. Run `down` afterwards to delete the devices.

## Live notification test

- `TestLiveNotifications` (same gate as the other live tests) uses
  `cmd/tail-watch/testdata/compose.live-notify.yaml`; `scripts/live-stack.sh up|down`
  brings up just the nodes for manual work. The tailscale image's busybox has no `httpd`,
  so the http fixture is an `nc` responder that must read the request before replying.
- The Slack webhook is read from `SLACK_WEBHOOK_URL` or from the pulumi env named by
  `TAIL_WATCH_SLACK_PULUMI_ENV`, and the Teams one from `TEAMS_WEBHOOK_URL` or
  `TAIL_WATCH_TEAMS_PULUMI_ENV` (both through `liveSinkURL`); never write them to a file
  or log them (`redactSecrets` strips `hooks.slack.com`, `logic.azure.com` and
  `powerplatform.com` URLs and `sig=` values).
- `TestLiveTeams` (`TAIL_WATCH_LIVE_TEAMS=1`) posts real cards to a Teams workflow with no
  tailnet. It needs its own gate because operators export `TEAMS_WEBHOOK_URL` to run the
  collector, and `mise run test` must not post to their channel. A Workflows trigger
  answers `202`, so assert `2xx`, never `200`.

## VM systemd test

- `TestVMSystemd` (`vm_systemd_test.go`, helpers in `vm_helpers_test.go`) is gated on
  `TAIL_WATCH_VM_SYSTEMD=1`, boots an Ubuntu cloud image under QEMU/KVM, and runs the
  fenced bash blocks under "Run under systemd" in `docs/how-to/deploy.md`, with
  `sudoedit` swapped for an `install` of the rendered env file. Editing those blocks
  changes the test (`docs/contributing/vm-systemd-test.md`).
- `stageRelease` lays files out like an unpacked GoReleaser archive, which keeps the
  `examples/` prefix; keep the staging, the docs and `.goreleaser.yaml` in step.
- `github.com/pkg/sftp` and `golang.org/x/crypto/ssh` are direct deps only for this test.

## kind ProxyGroup test

- `TestKindProxyGroup` (`kind_proxygroup_test.go`) is gated on `TAIL_WATCH_KIND=1`,
  skips when `kind`, `kubectl`, `helm` or `docker` is missing, and runs the fenced bash
  blocks under each method heading in `docs/how-to/kubernetes-proxygroup.md`, so editing
  those blocks changes the test. The sidecar contract the blocks produce: pods with both
  `tailscale.com/parent-resource-type: proxygroup` and `tail-watch/inject: "true"` get a
  local-only `tail-watch` container reading ConfigMap `tail-watch-<proxygroup>` (key
  `tail-watch.yaml`, optional); sink secrets come from a same-named Secret via `envFrom`.
  Every method builds that name from the pod's `tailscale.com/parent-resource` label;
  never hard-code `ingress`.
- `examples/kubernetes/sidecar.yaml` is the reference container. `TestSidecarCopiesAgree`
  (not gated) fails when the Kyverno, MutatingAdmissionPolicy, freeze-and-patch or
  how-to webhook copy drifts from it, so edit all five together. Each copy's image tag
  is written by release-please from the line carrying `x-release-please-version` (listed
  in `release-please-config.json` `extra-files`), and the test fails when it differs from
  `.release-please-manifest.json`. Never hand-edit a tag, and keep the marker on the line
  when you move the image (`#` in YAML and shell, `//` in CEL and Go; freeze-and-patch
  holds it in an `IMAGE=` variable because its JSON cannot carry comments).
- `scripts/kind-stack.sh up|down` is the harness (cluster config
  `cmd/tail-watch/testdata/kind-proxygroup.yaml`, fixtures in
  `cmd/tail-watch/testdata/kind/`); the test runs `up` itself when the cluster or the
  `ingress` ProxyGroup is missing. The operator chart is pinned to the newest chart at or
  below the `tailscale.com` version in `go.mod`; the OAuth client comes from the
  `tailscale-phase-2/tailscale` pulumi env as `tailscale:operatorClientId/Secret` and
  reaches Helm through a mode-0600 values file, never the command line or a log.
  The harness holds no image version: `TAIL_WATCH_KIND_IMAGE` defaults to the image in
  `examples/kubernetes/sidecar.yaml`, and `up` builds from source under that name and
  `kind load`s it, so the how-to blocks run against the local build, not GHCR.
- The test's `sidecarConfig` is the documented ConfigMap with tighter timing; keep its
  mechanics (local-only, `tcp:` check, webhook sink) in step with
  `examples/kubernetes/configmap.yaml`. The receiver is `alert-receiver` in `testdata/kind/`:
  it stores POST bodies and returns them on `GET /events`, and the test asserts new events
  by offset because the receiver accumulates.
- The test leaves the harness up. Teardown is `scripts/kind-stack.sh down`: it deletes the
  cluster, the Tailscale Service `svc:tw-pg-web`, and every tailnet device with the `tw-pg`
  hostname prefix.

## Dogfood VM (Proxmox)

- A small customer-style deployment runs on Proxmox VM 206, `tail-watch-1`, built from
  the maintainer's private homelab repo (`scripts/deploy-tail-watch.sh`; stages `vm`,
  `enroll`, `install`, `serve`, `status`). It builds `cmd/tail-watch` from this
  checkout, so redeploy with
  `esc run default/tail-watch-vm -- scripts/deploy-tail-watch.sh install` from that repo.
  Secrets live in the Pulumi ESC env `default/tail-watch-vm`; never print them.
- The UI is exposed as `svc:tail-watch` (`https://tail-watch.<tailnet>.ts.net/ui/`)
  through `tailscale serve --service=svc:tail-watch --https=443 --bg 127.0.0.1:9109`.
  The Service must be defined on the admin console's Services page first, the host
  approved (or covered by `autoApprovers.services`), and a grant must name `svc:tail-watch`:
  a `*` destination does not cover `svc:`. Reach it from Windows; the WSL Linux client is
  not on the tailnet.
- `examples/tail-watch.service` sets `StateDirectoryMode=0700`; without it systemd makes
  `/var/lib/tail-watch` 0755 and the collector logs `state_dir is readable by group or
  others`. `TestExampleUnitStateDirectoryMode` ties the unit to `stateDirMode`, and
  `TestVMSystemd` asserts the mode on a real VM.
- WSL cannot `ssh` to the Proxmox host; use `tailscale.exe ssh root@proxmox`, which may
  print a check URL once. Pipe nested remote work through the host, not `bash -s`.
