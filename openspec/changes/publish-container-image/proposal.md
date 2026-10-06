# Proposal

## Why

The only way to run tail-watch in a container is to clone the repository and
`docker build` it. The Kubernetes sidecar examples pin `tail-watch:0.1.0`, a tag that
exists in no registry, and `docs/how-to/kubernetes-proxygroup.md` tells operators to
build and push their own copy. The release workflow already builds and uploads archives
for every release; the image is the one artifact it does not publish. The
`add-systemd-deploy-docs` change deferred it as "a separate change", and this is that
change.

## What Changes

- Publish a multi-arch image (`linux/amd64`, `linux/arm64`) to
  `ghcr.io/tailscale-dev/tail-watch` from the existing `publish` job in
  `.github/workflows/release.yml`, built by GoReleaser `dockers_v2` from the same
  binaries as the archives.
- Tag each release `X.Y.Z` (the git tag without its `v`), plus `X.Y` and `latest`.
- Add `Dockerfile.release`, which copies the prebuilt binary into `alpine:3.24` with the
  same uid 65532 user and `/var/lib/tail-watch` directory as today. The existing
  `Dockerfile` stays as the from-source build.
- Give the image OCI labels and make `tail-watch --version` print the release version.
- Extend the CI `release-check` job to build the release image from the snapshot
  without pushing it.
- Replace the hard-coded `tail-watch:0.1.0` in the five sidecar copies with
  `ghcr.io/tailscale-dev/tail-watch:<version>`, written by release-please from
  `.release-please-manifest.json` on each release; `scripts/kind-stack.sh` reads the image
  from `sidecar.yaml` and holds no version, and prose uses a `<version>` placeholder.
- Document `docker pull` in `docs/how-to/deploy.md` and
  `docs/how-to/kubernetes-proxygroup.md`, and the one-time manual step to make the
  GHCR package public. `examples/compose.yaml` and the tutorial that runs it keep
  building from source, because the live tests merge their override files over that file.
- Update `docs/contributing/releases.md`, `CHANGELOG.md` (release-please owns it, so no
  hand edit), `CONTEXT.md` and `ROADMAP.md`.

No Go code in `cmd/tail-watch/` changes. No config key, flag, metric, event field or log
message changes, so `docs/reference/` is untouched.

## Capabilities

### New Capabilities

- `container-image`: the published image's location, tags, platforms, runtime user,
  entrypoint, version output, and what a failed or partial publish leaves behind.

### Modified Capabilities

None. `proxygroup-sidecar` mentions "no injector image", which concerns an admission
webhook image and stays true. The sidecar container's image reference is an example
detail, not a spec requirement.

## Impact

- **Delivers:** the ROADMAP.md item "Publish a container image [M]".
- **Release pipeline:** `.github/workflows/release.yml` (`packages: write`, QEMU, Buildx
  and registry login steps), `.github/workflows/ci.yml` (snapshot image build),
  `.goreleaser.yaml` (`dockers_v2`), `release-please-config.json` (`extra-files`).
- **New file:** `Dockerfile.release`.
- **Examples and docs:** `examples/kubernetes/sidecar.yaml`,
  `kyverno-clusterpolicy.yaml`, `mutatingadmissionpolicy.yaml`, `freeze-and-patch.sh`,
  the webhook handler in `docs/how-to/kubernetes-proxygroup.md`, `scripts/kind-stack.sh`,
  `docs/contributing/kind-proxygroup-test.md`.
- **Tests:** `TestSidecarCopiesAgree` must keep passing; the kind harness must still run
  the how-to blocks against a locally built image.
- **Manual step:** the first push creates the GHCR package private; an org admin makes it
  public once in the web UI.

## Non-Goals

- Image signing (cosign), SBOMs and provenance attestations.
- A Docker Hub or other registry mirror.
- An embedded `tailscaled` or any change to the "separate process beside a kernel-mode
  tailscaled" model.
- A Helm chart, an injector webhook image, or an operator.
- Other ROADMAP.md items: PagerDuty sink, TLS expiry check, `target.degraded`,
  `tsnetEnv`, tag discovery and Slack bot-token mode.
