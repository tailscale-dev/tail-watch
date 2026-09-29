# Design

## Context

The only user documentation is `README.md` (about 625 lines, with 336 lines of
uncommitted additions from `add-notifications`). Reference facts live in three other
places: the Go source (`config.go`, `config_sinks.go`, `notify_metrics.go`, the flag
sets in `main.go`), the archived specs under `openspec/specs/`, and
`tailnet-reachability-monitor-design.md`. `examples/tail-watch.yaml` and
`examples/compose.yaml` are the only runnable samples. markdownlint runs with MD013 at
90 columns (tables and code blocks exempt) and ignores `.claude/**` and `node_modules/**`.

## Goals / Non-Goals

**Goals:**

- Every operator question maps to exactly one page: learn, do, look up, or understand.
- Every command and YAML snippet in the docs is checked against the built binary, not
  copied from memory.
- A fact lives in one page; other pages link to it.

**Non-Goals:**

- Documenting internal architecture (`netEnv`, clocks, mutex layout). That stays in
  `CLAUDE.md` and `openspec/`.
- Auto-generating reference pages from code. The config surface is small enough to write
  by hand and check.

## Decisions

### Diataxis layout under `docs/`

```text
docs/
  README.md            index, one line per page
  tutorial/first-monitor.md
  how-to/              deploy.md  acls.md  remote-subnets.md  monitor-ui.md
                       slack-sink.md  webhook-sink.md  heartbeat.md
                       troubleshoot.md
  reference/           config.md  checks.md  cli.md  metrics.md  payload.md
  explanation/         failure-layers.md  alert-state.md  incidents-and-queues.md
                       monitor-ui-risks.md
  contributing/        live-notification-test.md  releases.md
```

Alternative: a single long `docs/guide.md`. Rejected because it recreates the README
problem and gives readers no stable links.

The monitor UI, heartbeat and egress path sections of the README have no page in the
first outline. They get `how-to/monitor-ui.md`, `explanation/monitor-ui-risks.md` and
`how-to/heartbeat.md`; the egress-path grant goes into the sink how-tos, and
`notify-test` goes into `reference/cli.md` with links from each sink page.

### README becomes a landing page

Keep the purpose paragraph, requirements summary, a five-line quickstart, and a link
list. Alternative: keep the README complete and add `docs/` as extras. Rejected: two
copies of the sink options and payload drift as soon as one changes.

### Accuracy is checked mechanically where possible

- Each YAML snippet that is a full config is saved and run through
  `tail-watch check-config`.
- Each shell command is run, or, when it needs a tailnet, is marked as such and matches a
  command exercised by `scripts/live-stack.sh` or the live tests.
- Metric names in `reference/metrics.md` are compared against the `Name:` entries in
  `notify_metrics.go` and the collector metrics, so none is documented that does not exist.

Alternative: extract snippets into Go tests. Rejected as over-built for a docs change.

### Source of truth per page

| Page | Source |
| ---- | ------ |
| `reference/config.md` | `config.go`, `config_sinks.go`, design doc "Config" |
| `reference/cli.md` | flag sets in `main.go`, `notifytest.go` |
| `reference/metrics.md` | metric registrations, design doc "Metrics" |
| `reference/payload.md` | `event.go`, `testdata/payload` golden files |
| `explanation/*` | archived specs, design doc "What a failed check means", "Alert state machine" |

Where code and design doc disagree, the code and archived spec win and the page follows
them; note the mismatch in the task that found it.

### Tutorial uses the shipped example

The tutorial starts from `examples/compose.yaml`, adds one failing target, and ends with
the alert arriving (Slack webhook or a local webhook receiver). This keeps it runnable
without inventing a new sample. Alternative: a self-contained tutorial with its own
compose file. Deferred: revisit only if the example proves too far from a first run.

## Risks / Trade-offs

- [Docs describe behavior that `add-notifications` still changes] → Apply after that
  change is archived; the README diff is the input.
- [Restructure loses README content] → Move first, then trim; a task diffs old README
  headings against the new pages and confirms each is accounted for.
- [Existing README anchors are linked from elsewhere] → Search the repo and release notes
  for `README.md#` links and keep or redirect them.
- [Reference pages drift from code over time] → The metrics/config checks above are
  repeatable, and `CLAUDE.md` gets a note to update `docs/` with config or metric
  changes.

## Open Questions

- Should `docs/` link check run in CI, or only locally in the validation group?
