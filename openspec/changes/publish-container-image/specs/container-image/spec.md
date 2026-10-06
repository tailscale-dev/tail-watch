# Spec Delta

## Purpose

Defines the container image that every release publishes, so operators can pull a
versioned, multi-architecture tail-watch image from a public registry instead of building
it from source.

## ADDED Requirements

### Requirement: Release publishes a multi-architecture image

Every release SHALL publish a single multi-architecture image to
`ghcr.io/tailscale-dev/tail-watch` covering `linux/amd64` and `linux/arm64`. A client
that pulls a tag on either architecture SHALL receive the image built for that
architecture without naming a platform.

#### Scenario: Pull on each architecture

- **WHEN** a client on `linux/amd64` and a client on `linux/arm64` each pull
  `ghcr.io/tailscale-dev/tail-watch:<version>` for a published release
- **THEN** each receives an image whose platform matches its own

#### Scenario: Anonymous pull

- **WHEN** the package has been made public and a client pulls a release tag without
  logging in
- **THEN** the pull succeeds

### Requirement: Version tags

Each release SHALL tag its image `X.Y.Z`, where `X.Y.Z` is the release's git tag without
the leading `v`. It SHALL also move the `X.Y` tag and the `latest` tag to that image, so
`X.Y` always names the newest patch release of that minor version and `latest` the newest
release. The image SHALL NOT be published under a tag that begins with `v`.

#### Scenario: Release 0.2.1 is published

- **WHEN** release `v0.2.1` is published and the newest earlier release was `v0.2.0`
- **THEN** the image is tagged `0.2.1`, `0.2`, and `latest`, and `0.2.0` still names the
  `v0.2.0` image

#### Scenario: Retrying a publish

- **WHEN** the publish for an existing release tag is re-run from the workflow dispatch
  input
- **THEN** the tags for that release are pushed again from that tag's source and no other
  release's tags change, except that `X.Y` and `latest` move only when that release is
  the newest in their line

### Requirement: Runtime contract of the image

The image SHALL run `tail-watch` as the non-root user `tail-watch` (uid and gid 65532)
with entrypoint `/usr/local/bin/tail-watch`, so arguments given to `docker run` are
passed to the collector. It SHALL contain a directory `/var/lib/tail-watch` that the
user can write. It SHALL NOT contain `tailscaled`: the collector keeps reaching the
LocalAPI through a mounted socket or a shared network namespace.

#### Scenario: Default user and entrypoint

- **WHEN** a container starts from the image with `--version`
- **THEN** the process runs as uid 65532 and prints the version and exits

#### Scenario: State directory

- **WHEN** a container from the image is run with `collector.state_dir` set to
  `/var/lib/tail-watch` and no volume mounted
- **THEN** the collector can create its state files there

### Requirement: Image binary matches the release

The binary in the image for an architecture SHALL be the binary built for the release
archive of the same operating system and architecture, so `tail-watch --version` in the
image prints the release version, `X.Y.Z`, and the image and the archive behave
identically.

#### Scenario: Version matches the tag

- **WHEN** `docker run --rm ghcr.io/tailscale-dev/tail-watch:0.2.1 --version` runs
- **THEN** it prints `0.2.1`

### Requirement: Publish failure is visible and does not hide the release

The image publish SHALL run in the release workflow after the GitHub release and tag
exist. When it fails, the workflow run SHALL fail, and the release and its archives
SHALL remain available. Pull request builds and snapshot builds SHALL NOT push to the
registry, and a pull request SHALL fail its checks when the release image cannot be
built.

#### Scenario: Registry push fails

- **WHEN** the registry rejects the push during a release
- **THEN** the release workflow run is marked failed, the GitHub release and archives
  exist, and the publish can be re-run for that tag

#### Scenario: Pull request builds the image without publishing

- **WHEN** a pull request changes the release image definition so it no longer builds
- **THEN** the pull request's release check fails and no image is pushed to the registry
