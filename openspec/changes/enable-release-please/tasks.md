# Tasks

## 1. Pin the actions

- [ ] 1.1 Write `TestWorkflowActionsPinned` in `cmd/tail-watch/workflow_pins_test.go`: for every `uses:` in `.github/workflows/*.yml` that is not a `./` reference, require a 40-character hex SHA after `@` and a trailing version comment on the same line; verify it fails today and lists each tag-pinned line
- [ ] 1.2 Resolve each of the eight actions to the commit SHA of its current release tag (`git ls-remote https://github.com/<owner>/<repo> 'refs/tags/<tag>^{}' 'refs/tags/<tag>'`, preferring the dereferenced line for annotated tags) and rewrite all 18 `uses:` lines in `.github/workflows/ci.yml` and `.github/workflows/release.yml` as `uses: <owner>/<repo>@<sha> # <tag>`; verify `TestWorkflowActionsPinned` passes, `actionlint .github/workflows/*.yml` is clean, and the SHAs in a spot check of three actions match their tags on github.com
- [ ] 1.3 Add `.github/dependabot.yml` with a weekly `github-actions` entry for `/` that groups all updates into one pull request; extend `TestWorkflowActionsPinned` (or a sibling test in the same file) to require the file and its `github-actions` ecosystem; verify the test passes and the file parses as YAML

## 2. Make the release PR update the sidecar tags

- [ ] 2.1 Write `TestReleasePleaseExtraFiles` in `cmd/tail-watch/release_please_test.go`: every `extra-files` entry in `release-please-config.json` must be an object with `"type": "generic"` and a `path`, and each such file must contain a line with `x-release-please-version`; verify it fails today on the five bare-string entries
- [ ] 2.2 Change the five `extra-files` entries in `release-please-config.json` to `{"type": "generic", "path": "<file>"}`; verify `TestReleasePleaseExtraFiles` and `TestSidecarCopiesAgree` pass, and the JSON still validates against the `$schema` it names
- [ ] 2.3 Confirm the inferred cause in the release-please source (`src/factories/...` or the extra-files handling in `src/manifest.ts`) at the version `release-please-action@v5` pins, recording in this task which updater a bare `.yaml` path selects; verify the finding is stated in this file's commit message and, if it was wrong, revise the design's Context before continuing

## 3. Repository settings (maintainer; no files)

- [ ] 3.1 Allow release PRs: in **Settings → Actions → General → Workflow permissions** tick **Allow GitHub Actions to create and approve pull requests** (org admin if greyed out), or create a GitHub App, install it on the repository, store its id and key as the secrets `RELEASE_APP_ID` and `RELEASE_APP_PRIVATE_KEY`, and pass its token to `release-please-action` as `token:` in `.github/workflows/release.yml` using `actions/create-github-app-token` pinned like the others; verify by a push to `main` with the variable set (task 5.1) not failing with `not permitted to create or approve pull requests`
- [ ] 3.2 Delete the stale remote branch `release-please--branches--main` on GitHub; verify it no longer appears in `git ls-remote origin 'refs/heads/release-please*'`
- [ ] 3.3 Prove `release-check` fails on a broken image: on a scratch branch add a syntax error to `Dockerfile.release`, open a pull request, confirm the `release-check` job fails and nothing is pushed to `ghcr.io`, then close the pull request and delete the branch; verify by the failed run's URL recorded in the change's commit message

## 4. Documentation

- [ ] 4.1 Update `docs/contributing/releases.md` and `CLAUDE.md`: replace the "automation is off" notes with how it runs (the `RELEASE_PLEASE_ENABLED` variable, the credential used, pinned actions and Dependabot, closing and reopening the release PR to start its checks) and keep the manual-publish path; verify both pass `markdownlint-cli2` and nothing in either still says the job is skipped
- [ ] 4.2 Add a line to `ROADMAP.md` under Completed Milestones for `enable-release-please` with the closing date, and drop the stale "`publish-container-image`" wording if present; verify `markdownlint-cli2 ROADMAP.md`

## 5. Turn it on and verify the first release

- [ ] 5.1 Set the repository variable `RELEASE_PLEASE_ENABLED` to `true` (Settings → Secrets and variables → Actions → Variables); verify the next push to `main` runs the `release-please` job and opens the release pull request
- [ ] 5.2 Review the release pull request before merging: its diff changes the image tag to the new version in `examples/kubernetes/sidecar.yaml`, `kyverno-clusterpolicy.yaml`, `mutatingadmissionpolicy.yaml`, `freeze-and-patch.sh` and `docs/how-to/kubernetes-proxygroup.md`, and updates `CHANGELOG.md` and `.release-please-manifest.json`; the run log has no `No entries modified in $.version`; close and reopen the pull request so CI runs, and `TestSidecarCopiesAgree` passes on it
- [ ] 5.3 Merge the release pull request; verify the `publish` job succeeds and the GitHub release `vX.Y.Z` has archives and `checksums.txt`
- [ ] 5.4 Verify the registry: `ghcr.io/tailscale-dev/tail-watch` carries tags `X.Y.Z`, `X.Y` and `latest`, none beginning with `v`, and a manifest list for `linux/amd64` and `linux/arm64`
- [ ] 5.5 Make the GHCR package public once in the web UI (org admin; `gh` cannot reach this org); verify an anonymous `docker pull ghcr.io/tailscale-dev/tail-watch:X.Y.Z` works on an amd64 host and on an arm64 host
- [ ] 5.6 Verify the image matches the release: `docker run --rm ghcr.io/tailscale-dev/tail-watch:X.Y.Z --version` prints `X.Y.Z`

## 6. Validation

- [ ] 6.1 `go build ./...`, `go vet ./...`, `go test ./...`, `mise run lint` (extended timeout) and `govulncheck ./...`
- [ ] 6.2 `actionlint .github/workflows/*.yml` and `markdownlint-cli2` on the changed `docs/`, `ROADMAP.md`, `CLAUDE.md` and change files
- [ ] 6.3 `openspec validate enable-release-please --strict`
