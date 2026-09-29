# Proposal

## Why

`README.md` has grown to about 625 lines and carries four kinds of content at once:
explanation (failure layers, alert state), how-to (Docker, ACLs, Slack webhook), reference
(checks, sink options, payload) and contributor notes (live notification test,
releases). A new operator has no path from "what is this" to "first alert in Slack", and
nothing documents the full `tail-watch.yaml` schema, the `tailwatch_*` metrics, or the
flags in one place. Phase 1 is feature-complete once `add-notifications` lands, so the
behavior is stable enough to document now.

## What Changes

- Restructure user documentation into `docs/` following the Diataxis quadrants
  (tutorial, how-to, reference, explanation), written with the `documentation-writer`
  skill.
- Reduce `README.md` to a short overview: what tail-watch is, a five-line quickstart,
  and links into `docs/`. Move the existing README material into the matching pages
  rather than keeping two copies.
- Add pages that do not exist today: a first-monitor tutorial, a configuration reference,
  a metrics reference, a CLI reference (flags, `check-config`, `notify-test`), and a
  troubleshooting guide.
- Split contributor material (live notification test, development and releases) into
  `docs/contributing/` so it is out of the operator path.
- Lint the new Markdown with markdownlint and keep it in the existing lint task.

This change adds documentation only. It does not change code, config keys, metric names or
payload fields; every documented fact is checked against the source, the archived specs
and `tailnet-reachability-monitor-design.md`.

Implements no design-doc section. The design doc has no user-documentation section, and
documentation is not a phase 1 deliverable there. It describes behavior from the design
doc sections "Check types", "What a failed check means", "Alert state machine",
"Notifications", "Config", "ACLs" and "Metrics".

Phase: MVP (phase 1). Non-goals:

- Phase 2 and 3 features (PagerDuty, Slack bot-token mode, `target.degraded`, quorum
  across collectors, tsnet mode). Pages mention them only as "not yet supported".
- A generated documentation site or hosting (mkdocs, Hugo, GitHub Pages).
- Man pages, shell completion, or in-binary help text changes.
- Translating or versioning the docs per release.

## Capabilities

### New Capabilities

None. Documentation does not change system behavior, so no spec changes. The change sets
`skip_specs: true` in `.openspec.yaml`.

### Modified Capabilities

None.

## Impact

- New: `docs/` tree (tutorial, how-to, reference, explanation, contributing).
- Changed: `README.md` (slimmed), `CLAUDE.md` (layout note for `docs/`), markdownlint
  configuration if `docs/` needs exclusions or the line-length rule needs adjusting.
- No Go code, dependency, or runtime impact.
- Sequencing: this change should be applied after `add-notifications` is archived, so the
  notification pages describe final behavior. The uncommitted README edits from that
  change are the source for those pages.

## Assumptions

- The primary reader is a tailnet admin or SRE who knows Tailscale ACLs but not tail-watch.
- The README is restructured, not left alongside new pages.
- The tutorial builds on `examples/compose.yaml` and `examples/tail-watch.yaml`.

These were proposed during exploration and not contradicted; revise them here if wrong.
