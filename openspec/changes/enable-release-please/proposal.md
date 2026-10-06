# Proposal

## Why

Release automation is off. The first `release-please` run on `main` created its branch
and then failed with `GitHub Actions is not permitted to create or approve pull
requests`, and no personal access token exists to work around it. The `release-please`
job in `.github/workflows/release.yml` now runs only when the repository variable
`RELEASE_PLEASE_ENABLED` is `true`, and the variable is unset. The `publish-container-image`
change was archived on 2026-10-06 with two checks that only a real release can answer,
and a log from the failed run suggests the release PR would not update the image tag in
three of the five sidecar copies. This change lists what has to be true before the
variable is set at launch, so turning it on is a checklist and not a surprise.

## What Changes

- Allow the release PR to be created: tick **Allow GitHub Actions to create and approve
  pull requests** (an org admin does it if the box is greyed out), or, if the org will not
  allow it, pass a GitHub App token to `release-please-action` as `token:`. No PAT.
- Pin every `uses:` in `.github/workflows/ci.yml` and `release.yml` to a full commit SHA
  with the version in a comment, and add `.github/dependabot.yml` for `github-actions` so
  the pins stay current. An automated security review flagged the three `docker/*` actions
  in the `publish` job, which receive `GITHUB_TOKEN` with `packages: write`.
- Make release-please rewrite the image tag in the five sidecar copies. The three `.yaml`
  entries in `release-please-config.json` `extra-files` are bare paths, and the first run
  logged `No entries modified in $.version` for each, which is the YAML updater looking
  for a `version` key, not the `x-release-please-version` marker. Declare them with
  `"type": "generic"`, and check the release PR's diff shows all five tags change.
- Delete the stale `release-please--branches--main` branch left by the failed run.
- Set `RELEASE_PLEASE_ENABLED=true`, review and merge the 0.2.0 release PR, and confirm the
  first release: the `publish` job pushes `X.Y.Z`, `X.Y` and `latest`, the GHCR package is
  made public once in the web UI, an anonymous `docker pull` works on amd64 and arm64, and
  `docker run --rm ghcr.io/tailscale-dev/tail-watch:<version> --version` prints the version.
- Prove on a scratch PR that `release-check` fails when `Dockerfile.release` is broken, the
  half of the old task 2.1 that was never exercised.
- Update `docs/contributing/releases.md` and `CLAUDE.md`, which now describe the automation
  as off.

No Go code in `cmd/tail-watch/` changes, and no config key, flag, metric, event field or
log message changes, so `docs/reference/` is untouched.

## Capabilities

### New Capabilities

- `release-automation`: how a push to `main` becomes a release: the switch that enables
  release-please, how the release PR is created, how third-party actions are pinned and kept
  current, and that the release PR updates every versioned reference.

### Modified Capabilities

None. The image tags, the runtime contract and the publish-failure behavior stay as
`openspec/specs/container-image` defines them.

## Impact

- `.github/workflows/ci.yml` and `.github/workflows/release.yml`: action pins.
- `.github/dependabot.yml`: new.
- `release-please-config.json`: `extra-files` entries for the three YAML examples.
- `docs/contributing/releases.md` and `CLAUDE.md`: the automation is on, and how.
- Repository settings and variables on GitHub, which no file records: the Actions
  workflow permission, the `RELEASE_PLEASE_ENABLED` variable, the GHCR package visibility
  and the stale branch.

Delivers no `ROADMAP.md` item: "Publish a container image" is already marked done. Related
items it does not deliver: none of the Near-Term or Future Vision items depend on it.

## Non-goals

- Changing `release-type`, the version rules, or the `CHANGELOG.md` sections.
- A personal access token, or any secret beyond the built-in `GITHUB_TOKEN` or an App token.
- Changing what `publish` builds. That is `openspec/specs/container-image`.
- Cutting a release. The variable is set at launch, by the maintainer, in a later step.
