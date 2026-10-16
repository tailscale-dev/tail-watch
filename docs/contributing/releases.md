# Development and releases

This page is for contributors who build, check, and release tail-watch.

`mise run check` builds, tests, and lints with the pinned tools. The lint
configuration is based on
[maratori/golangci-lint-config](https://github.com/maratori/golangci-lint-config),
with this module's import prefix. CI also runs race tests and builds release
archives for Linux, macOS, and Windows on amd64 and arm64.

## Releasing

Releases are cut by hand; release-please does not work for this repository. The
`release-please` job in `.github/workflows/release.yml` runs only when the repository
variable `RELEASE_PLEASE_ENABLED` is `true`, and it is unset, so nothing opens a release
PR or publishes on a push to `main`.

To cut `vX.Y.Z`:

1. In one commit, set the version in `.release-please-manifest.json` and in the image
   tag on each line carrying `x-release-please-version` (listed in
   `release-please-config.json` `extra-files`). `TestSidecarCopiesAgree` fails when they
   disagree. Update the `VERSION` example in `docs/how-to/deploy.md`.
2. Put the changes under an `X.Y.Z - YYYY-MM-DD` heading in `CHANGELOG.md`. Edit it
   by hand.
3. Run `mise run check`, push the commit to `main`, and wait for CI.
4. Push the tag: `git tag -s vX.Y.Z && git push origin vX.Y.Z`.
5. Create the GitHub release for the tag, because the workflow's "Verify release tag"
   step runs `gh release view` and fails without one:
   `gh release create vX.Y.Z --verify-tag --title vX.Y.Z --notes-file <notes>`, with the
   changelog section as the notes.
6. Run the **Release** workflow from the Actions tab with the tag as its `tag` input.
   GoReleaser attaches the archives and `checksums.txt` to that release and pushes the
   container image (see [Container image](#container-image)). Each archive
   includes `tail-watch`, the license, the README, the `docs/` tree,
   `examples/tail-watch.yaml`, and `examples/tail-watch.service`. The binary's
   `--version` reports the release version.

The OpenSpec change `enable-release-please` lists what to do before turning the
automation on. Once it is on, use Conventional Commits in squash-merge titles and merge
the release PR instead; release-please then owns `CHANGELOG.md`.

## Repository setup

In **Settings → Actions → General → Workflow permissions**,
enable **Allow GitHub Actions to create and approve pull requests**.
The workflow uses the built-in `GITHUB_TOKEN`. PRs created with that token
do not automatically start CI; close and reopen the release PR to trigger
its checks. Publishing runs in the release workflow itself because tags
created with that token also do not trigger a separate workflow.

## Container image

Each release publishes `ghcr.io/tailscale-dev/tail-watch` for `linux/amd64` and
`linux/arm64`, built by GoReleaser's `dockers_v2` from the same binaries as the
archives, so `--version` in the image prints the release version. It is tagged with
the release version without the `v` (`X.Y.Z`), the `X.Y` line, and `latest`.
`Dockerfile.release` defines it; `Dockerfile` stays as the from-source build, and
`TestDockerfilesAgree` keeps their runtime stages the same.

The `publish` job has `packages: write` and logs in to `ghcr.io` with the workflow's
`GITHUB_TOKEN`, so no secret is involved. A failed push fails the run but leaves the
GitHub release and archives in place; re-run the workflow with the `tag` input to push
the image again.

The first push creates the package as private, and anonymous pulls then fail with
`401`. Once, after the first release that has an image, a member with admin rights on
the `tailscale-dev` organization must open the package's settings on GitHub, change
its visibility to **Public**, and confirm that `docker pull` works without logging in.
`gh` cannot do this for this organization.

The Kubernetes examples name this image at the current release version, on the lines
that carry an `x-release-please-version` comment. `TestSidecarCopiesAgree` fails when
they and `.release-please-manifest.json` disagree, so change them only in the release
commit, together with the manifest. With release-please on, it rewrites them in the
release PR instead.

## Verifying a published release

After the **Release** workflow succeeds, check the artifacts before announcing the
release. The checks below use the image and need Docker.

1. Confirm the release lists `checksums.txt` and an archive for each OS and architecture.
2. Pull the image with a Docker config that has no GHCR login, for example
   `DOCKER_CONFIG=$(mktemp -d) docker pull ghcr.io/tailscale-dev/tail-watch:X.Y.Z`. A
   `401` means the package is still private (see [Container image](#container-image)).
3. Check the version: `docker run --rm ghcr.io/tailscale-dev/tail-watch:X.Y.Z --version`
   prints `X.Y.Z`.
4. Run the image against real nodes with the Docker harness, not your host tailnet.
   Start `scripts/live-stack.sh up`, write a config at the path it prints, and add a
   compose override that names the release image:

   ```yaml
   services:
     tail-watch:
       image: ghcr.io/tailscale-dev/tail-watch:X.Y.Z
       pull_policy: never
   ```

   Start only the collector with the `docker compose` command that `up` prints, adding
   `-f <override>` and the `--no-build` flag so it does not rebuild from source. Then
   query `/healthz` and `/metrics` from the tailscale container, fail and heal a fixture,
   and watch `tailwatch_target_state`. Finish with `scripts/live-stack.sh down`, which
   deletes the test devices.

## Validating packaging locally

To validate packaging locally without publishing:

```bash
mise run release-check
```

This also builds the image once per platform without pushing it, tagged with a
`-amd64` or `-arm64` suffix and the snapshot version. The arm64 build runs under QEMU,
so it fails with `exec format error` on a host without arm64 emulation (Docker's
`tonistiigi/binfmt` installs it). CI sets that up. Remove the local images with
`docker image rm` when you are done.

Snapshot archives appear in `dist/`. If publishing fails after the release
has been created, rerun the failed job or manually run the **Release** workflow
with its existing tag (for example, `v0.2.0`). The workflow needs the GitHub release
for that tag to exist already.
