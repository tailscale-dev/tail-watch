# Design

## Context

See `proposal.md` for motivation. Current state:

- `release.yml` runs `release-please`, then a `publish` job that checks out the release
  tag and runs `goreleaser release --clean` (v2.18.2). The same job is the target of the
  `workflow_dispatch` retry, which takes an existing tag.
- `.goreleaser.yaml` builds `linux`, `darwin` and `windows` on `amd64` and `arm64` with
  `-trimpath` and `-X main.version={{.Version}}`. It has no image section.
- `Dockerfile` is a two-stage from-source build with a plain `go build`. It sets no
  `main.version`, so `--version` falls back to `debug.ReadBuildInfo()`, which reports
  `(devel)` for a local build. The `.dockerignore` excludes `.git`.
- `tail-watch:0.1.0` is hard-coded in `examples/kubernetes/sidecar.yaml`, the Kyverno
  policy, the MutatingAdmissionPolicy, `freeze-and-patch.sh`, the webhook handler in
  `docs/how-to/kubernetes-proxygroup.md`, and the default of `TAIL_WATCH_KIND_IMAGE` in
  `scripts/kind-stack.sh`. `TestSidecarCopiesAgree` keeps the five sidecar copies equal,
  and `TestKindProxyGroup` runs the how-to's blocks against an image that
  `kind-stack.sh` builds and `kind load`s under that exact name.
- `.release-please-manifest.json` is at `0.1.0`; `release-please-config.json` has no
  `extra-files`.

## Goals / Non-Goals

**Goals:**

- One build of each binary feeds both the archives and the image.
- A broken image definition fails on the pull request, not on release day.
- The version in the examples is correct at every release without a manual edit.

**Non-Goals:**

- Signing, SBOMs and provenance. `dockers_v2` exposes `sbom`; leave it off.
- Changing the runtime contract: base image, user, entrypoint and state directory carry
  over unchanged.

## Decisions

### GoReleaser `dockers_v2`, not a separate build job

`dockers_v2` is in the pinned v2.18.2 schema (`dockerfile`, `images`, `tags`,
`platforms`, `labels`, `build_args`, `extra_files`, `sbom`, `disable`). It builds the
image from the binaries GoReleaser already produced, so the image gets `-trimpath` and
`main.version` for free, an arm64 image needs no QEMU compile, and each release builds
once. It runs inside the existing `goreleaser release --clean` step, so the retry input
republishes the image with the assets.

Alternatives: `docker/build-push-action` with the existing `Dockerfile` recompiles under
QEMU, needs the ldflags passed as build args, and yields a binary that differs from the
archive's. `ko` drops the alpine base that the docs rely on for `wget` and a shell, and
adds a tool to `mise.toml`.

### A second Dockerfile, `Dockerfile.release`

`Dockerfile.release` is `alpine:3.24` plus the user, state directory and a `COPY` of the
prebuilt binary from the per-platform build context. The multi-stage `Dockerfile` stays:
`kind-stack.sh`, `live-stack.sh`, `examples/compose.yaml` (which builds from source and is the base the live tests merge over) and anyone without a release
still need a from-source image.

The two files now repeat the `adduser`, `mkdir`, `chown`, `USER` and `ENTRYPOINT` lines.
A small test, in the style of `TestSidecarCopiesAgree`, compares the final stage of both
files so they cannot drift. Alternative: one Dockerfile with a `--target`; rejected
because the release build has no Go toolchain stage and GoReleaser passes the binary in
the build context, not from a stage.

### Tags and labels

Templates: `{{ .Version }}` (GoReleaser strips the `v`), `{{ .Major }}.{{ .Minor }}`,
and `latest`. The release config already sets `prerelease: false`, so `latest` needs no
conditional. Labels include `org.opencontainers.image.source`, which links the GHCR
package to the repository so the workflow's `GITHUB_TOKEN` can push to it, plus
`version`, `revision` and `licenses`.

### Workflow permissions

Only `publish` gets `packages: write`; `release-please` and `checks` keep what they
have. The job adds `docker/setup-qemu-action` and `docker/setup-buildx-action`, and a
`docker/login-action` for `ghcr.io` with `github.actor` and `GITHUB_TOKEN`. No new
secret.

The CI `release-check` job gets Buildx and QEMU too, and its snapshot run builds the image
without pushing. If `dockers_v2` cannot build both platforms in `--snapshot` mode
(multi-platform images cannot be loaded into the local daemon), the fallback is a
`docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile.release` over the
snapshot's `dist/` layout, with no `--push`. Task 1.2 settles this before anything else
is built on it.

### No hand-written version

`.release-please-manifest.json` is the one place a version lives; release-please moves it
in the release PR. Everything else either derives from it or carries no version:

- **Prose and `docker run` snippets** in `deploy.md` and the how-to
  use a `<version>` placeholder that links to the releases page, so no page needs a
  release-time edit.
- **Deployable manifests** (the five sidecar copies) must name a tag, because Kubernetes
  rejects an empty one and `latest` defaults to `imagePullPolicy: Always`, which makes a
  running proxy pod's sidecar change on any restart. Each carries
  `ghcr.io/tailscale-dev/tail-watch:<version>` with an `x-release-please-version` marker,
  and release-please's `extra-files` (generic updater) rewrites it in the release PR. A
  person never types the number: the release PR ships the new tag and the examples that
  name it in one commit.
- **`kind-stack.sh`** holds no version. It reads the image reference from
  `examples/kubernetes/sidecar.yaml`, builds from source, tags the build with that name
  and `kind load`s it. The how-to's blocks run unchanged, and `IfNotPresent` uses the
  loaded image rather than pulling.
- **Drift guard:** `TestSidecarCopiesAgree` already requires all five copies to match;
  it also asserts that the image tag equals the version in
  `.release-please-manifest.json`, so a copy that loses its marker fails the test
  instead of going stale.

The only remaining literals are the five manifest tags, and they are machine-written.
Alternative: a floating `latest` there, rejected for the reason above.

Alternative: a floating `:latest` in the examples. Rejected because it makes a sidecar's
behavior change under a running proxy pod on any restart, and Kubernetes defaults
`imagePullPolicy` to `Always` for a `latest` tag.

### Boundaries

No `CONTEXT.md` boundary is crossed and no existing spec is contradicted. The image
adds no credential, shared state or tailscaled dependency. `proxygroup-sidecar`'s "no
injector image" concerns an admission webhook, not the sidecar container. `CONTEXT.md`
gets a line saying the image carries no `tailscaled`, so that stays a stated boundary.

## Risks / Trade-offs

- **The first release with an image is the next one.** `v0.1.0` is the newest tag and
  predates the image; rerunning its publish builds from a tree with no `dockers_v2`. The
  manifests name the manifest version (`0.1.0`) until the release PR rewrites them, and
  that tag does not exist in GHCR. Several `feat:` commits already sit past `v0.1.0`, so
  the next release should be `0.2.0` (`bump-minor-pre-major`). → Land this as a `feat:`
  PR title, merge the pending release PR to cut the first image, and state in the docs
  which version first has one.
- **The GHCR package starts private.** Anonymous pulls fail with `401` until an org
  admin sets visibility once. `gh` cannot reach this org. → Document the step in
  `docs/contributing/releases.md` and call it out in the task list as a manual action
  after the first publish.
- **`dockers_v2` snapshot behavior is unverified for multi-platform.** → Task 1.2 runs
  `mise run release-check` locally first, and the buildx fallback above is ready.
- **Two Dockerfiles can drift.** → The Dockerfile agreement test.
- **`extra-files` markers and `TestSidecarCopiesAgree`.** A marker comment in a YAML,
  CEL, Go or shell copy must not break the structural comparison or the "contains every
  key and value" check. → Tasks 4.2 and 4.3 run the test after adding the markers, and the
  marker is a trailing comment so values are untouched.
- **Rollback.** A bad image cannot be unpublished from the workflow, and moving `latest`
  back is a manual retag. → Fix forward with a patch release; the retry input republishes
  a tag's image if only the push failed.

## Migration Plan

1. Merge this change as one `feat:` PR. Nothing is published by the merge itself:
   release-please opens a release PR.
2. Merge the release PR. The `publish` job pushes the first image.
3. An org admin sets the package public (GHCR package settings → Change visibility) and
   confirms an anonymous `docker pull`.
4. Rollback is to revert the PR; published tags stay, which is harmless.
