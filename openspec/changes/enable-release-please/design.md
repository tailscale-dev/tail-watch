# Design

## Context

- `release.yml` has three jobs: `checks` (calls `ci.yml`), `release-please` and `publish`.
  `release-please` has `needs: checks` and runs `googleapis/release-please-action@v5` with no
  `token:`, so it uses `GITHUB_TOKEN` with `contents`, `issues` and `pull-requests: write`.
  `publish` runs on `release_created == 'true'` or a `workflow_dispatch` with a tag, checks
  out the tag, and runs GoReleaser with `packages: write` for the image.
- The job is gated by `vars.RELEASE_PLEASE_ENABLED == 'true'`. A literal `if: false` was
  rejected by `actionlint` as a constant condition, and a variable also lets the
  maintainer turn the automation on without a commit.
- The first run, on the `400e370` push, got as far as updating the branch
  `release-please--branches--main` and failed creating the PR. The run found 27 commits
  and no open or snoozed release PR. It also logged `⚠ No entries modified in $.version` for
  `sidecar.yaml`, `kyverno-clusterpolicy.yaml` and `mutatingadmissionpolicy.yaml`, and
  nothing for `freeze-and-patch.sh` or `docs/how-to/kubernetes-proxygroup.md`.
- `release-please-config.json` lists the five files as bare strings. That the three YAML
  files draw the warning and the other two do not matches release-please picking an
  updater by extension, with the YAML updater reading `$.version`. This is inferred from
  that log, not confirmed against the release-please source, so a task confirms it.
- All 18 remote `uses:` lines in the two workflows, covering eight actions, use a floating
  major tag (`actions/checkout@v6`, `docker/login-action@v4`,
  `goreleaser/goreleaser-action@v7` and so on). There is no `.github/dependabot.yml`.

## Goals / Non-Goals

**Goals:**

- Turning the automation on is the last step of a checklist whose earlier steps are
  verified, not a change that is tested by shipping it.
- No third-party action runs with `GITHUB_TOKEN` write access from a mutable tag.
- The first release PR changes all five image tags, so the examples never name a tag that
  `container-image` does not publish.

**Non-Goals:**

- Changing release type, version rules or changelog sections.
- A personal access token.
- Cutting the release; the variable is set by the maintainer at launch.

## Decisions

**Keep the switch, do not delete the job's gate on launch.** After launch the variable
stays set. Removing the `if:` would save one line and lose the ability to pause a bad
release without a commit. Alternative: delete the condition once it is on. Rejected for that
reason.

**Pin all remote actions, not only the three the review named.** The review flagged
`docker/*` because `login-action` receives `GITHUB_TOKEN`. The same tag-moves-under-you risk
applies to `release-please-action`, which holds `contents` and `pull-requests: write`, and to
the GitHub-owned actions. One rule is simpler to check than a list of exceptions. Alternative:
pin only the actions that receive a write token. Rejected: the list drifts, and Dependabot
makes the cost of pinning low. A local `./.github/workflows/ci.yml` call is exempt because
it is this repository's own code.

**Check the pins with a test, not a convention.** A Go test in `cmd/tail-watch/` reads
`.github/workflows/*.yml` and fails on a remote `uses:` that is not a 40-character SHA. This
follows `TestDockerfilesAgree` and `TestSidecarCopiesAgree`, which already guard the release
files. Alternative: a lint step in CI with an extra tool. Rejected: it adds a dependency for
a rule a short test enforces with the tooling already pinned.

**Fix `extra-files` by naming the updater.** Change the three YAML entries to
`{"type": "generic", "path": "..."}` so the `x-release-please-version` marker is what is
rewritten. The two non-YAML entries get the same form for consistency. Alternative: give the
YAML files a top-level `version` key. Rejected: the sidecar manifests are Kubernetes
documents and must not carry a stray key. Whether the generic updater honors the marker on
these lines is confirmed by the release PR's diff, because release-please cannot be run
against GitHub without a token here.

**Credential: `GITHUB_TOKEN` first, an App token second.** The repository setting is a
checkbox that an org admin controls. If the org will not allow it, a GitHub App token
passed as `token:` is the supported alternative and is not a personal credential. A PAT is
excluded because none exists and it ties releases to a person.

**Verification is a checklist, because it needs a real release.** The release-check proof
(a broken `Dockerfile.release` fails a pull request) needs a scratch PR, and the registry
checks need a published release. These cannot be unit tests.

## Risks / Trade-offs

- **The updater inference may be wrong.** If the YAML updater is not why the tags do not
  change, the release PR will still show unchanged tags and the guard test fails on it.
  Mitigation: the tasks include reviewing the release PR diff before merging, and the spec
  scenario names the warning as the signal.
- **Pinning adds Dependabot pull requests.** Mitigation: group `github-actions` updates in
  `dependabot.yml` so they arrive as one weekly PR.
- **The release PR does not start CI.** PRs created with `GITHUB_TOKEN` do not trigger
  workflows, so `TestSidecarCopiesAgree` does not run on it until the PR is closed and
  reopened, as `docs/contributing/releases.md` already says. An App token avoids that.
- **The stale branch.** `release-please--branches--main` holds a commit from before the
  fix, and release-please rewrites it on the next run. Deleting it first costs nothing and
  avoids a confusing first diff.
- **No CONTEXT.md boundary is crossed.** Nothing here touches tailscaled, secrets handling
  or discovery, and no existing spec is contradicted. `container-image` stays as it is.
