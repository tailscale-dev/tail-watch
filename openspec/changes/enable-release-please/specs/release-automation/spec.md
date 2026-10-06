# Spec Delta

## Purpose

Defines how a push to `main` becomes a release: the switch that enables release-please,
how its pull request is created, how the actions the workflows use are pinned and kept
current, and that the release pull request updates every versioned reference.

## ADDED Requirements

### Requirement: Release automation is switched by a repository variable

The `release-please` job in `.github/workflows/release.yml` SHALL run only when the
repository variable `RELEASE_PLEASE_ENABLED` is `true`. With the variable unset or any
other value, a push to `main` SHALL NOT open or update a release pull request and SHALL
NOT start the `publish` job. The `publish` job SHALL still run from the workflow
dispatch input for an existing release tag.

#### Scenario: Variable unset

- **WHEN** a commit is pushed to `main` and `RELEASE_PLEASE_ENABLED` is not set
- **THEN** the `release-please` job is skipped, no release pull request is created, and
  nothing is pushed to the registry

#### Scenario: Variable set

- **WHEN** a commit is pushed to `main`, the `checks` job passes and
  `RELEASE_PLEASE_ENABLED` is `true`
- **THEN** release-please opens or updates the release pull request

#### Scenario: Manual publish while automation is off

- **WHEN** the **Release** workflow is run with an existing tag as its input and
  `RELEASE_PLEASE_ENABLED` is not set
- **THEN** the `publish` job runs for that tag

### Requirement: The release pull request is created with the workflow's own credential

The `release-please` job SHALL create and update the release pull request with the built-in
`GITHUB_TOKEN`, or with a GitHub App installation token passed as `token:`, and SHALL hold
only `contents: write`, `issues: write` and `pull-requests: write`. It SHALL NOT use a
personal access token. When the repository forbids Actions from creating pull requests,
the run SHALL fail with the error release-please reports, and the release-please branch
SHALL NOT be treated as a release.

#### Scenario: Actions may create pull requests

- **WHEN** the repository setting **Allow GitHub Actions to create and approve pull
  requests** is on and release-please has releasable commits
- **THEN** the release pull request is opened by the `release-please` job

#### Scenario: Actions may not create pull requests

- **WHEN** the setting is off and release-please has releasable commits
- **THEN** the run fails with `GitHub Actions is not permitted to create or approve pull
  requests`, and no tag or GitHub release is created

### Requirement: Actions are pinned to commit SHAs

Every `uses:` in `.github/workflows/` that names a remote action SHALL pin it to a full
40-character commit SHA, with the version it resolves to in a trailing comment. A `uses:`
that names a workflow in this repository is exempt. The repository SHALL configure
Dependabot for the `github-actions` ecosystem so the pins are updated by pull request.

#### Scenario: A workflow names an action by tag

- **WHEN** a workflow step has `uses: docker/login-action@v4`
- **THEN** the check for pinned actions fails, because the reference is a tag and not a
  commit SHA

#### Scenario: A pinned action

- **WHEN** a workflow step has `uses: docker/login-action@<40-character SHA> # v4.0.0`
- **THEN** the step is accepted

#### Scenario: A new action version

- **WHEN** a pinned action publishes a new release
- **THEN** Dependabot opens a pull request that changes the SHA and its version comment

### Requirement: The release pull request updates every versioned reference

The release pull request SHALL set the image tag to the new release version, `X.Y.Z`, in
each file listed under `extra-files` in `release-please-config.json`, on the line that
carries the `x-release-please-version` marker, and SHALL update `CHANGELOG.md` and
`.release-please-manifest.json`. Each `extra-files` entry for a file that holds the marker
SHALL name the generic updater. After the version bump the image tags in the sidecar
examples SHALL equal the version in `.release-please-manifest.json`, so
`TestSidecarCopiesAgree` passes on the release pull request.

#### Scenario: Release 0.2.0 pull request

- **WHEN** release-please opens the pull request for 0.2.0
- **THEN** its diff changes the image tag to `0.2.0` in `examples/kubernetes/sidecar.yaml`,
  `kyverno-clusterpolicy.yaml`, `mutatingadmissionpolicy.yaml`, `freeze-and-patch.sh` and
  `docs/how-to/kubernetes-proxygroup.md`, and `.release-please-manifest.json` reads `0.2.0`

#### Scenario: A YAML example is not updated

- **WHEN** release-please logs `No entries modified in $.version` for a file in
  `extra-files`
- **THEN** that entry is not using the generic updater, and the change is not complete
