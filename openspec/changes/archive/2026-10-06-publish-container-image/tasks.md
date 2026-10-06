# Tasks

## 1. Release image definition

- [x] 1.1 Add `Dockerfile.release` (`alpine:3.24`, uid/gid 65532 user `tail-watch`, `/var/lib/tail-watch` owned by it, `COPY` of the prebuilt binary, `USER`, `ENTRYPOINT ["/usr/local/bin/tail-watch"]`); verify `docker buildx build -f Dockerfile.release` succeeds over a staged `linux/amd64/tail-watch` binary
- [x] 1.2 Add a `dockers_v2` section to `.goreleaser.yaml` (`images: ghcr.io/tailscale-dev/tail-watch`, tags `{{ .Version }}`, `{{ .Major }}.{{ .Minor }}`, `latest`, platforms `linux/amd64` and `linux/arm64`, OCI labels including `org.opencontainers.image.source`, `sbom` off); verify `mise run release-check` builds the image locally without pushing, or record the `docker buildx build --platform` fallback from design.md and use it in task 2.1
- [x] 1.3 Write `TestDockerfilesAgree` in `cmd/tail-watch/` comparing the user, state directory, `USER` and `ENTRYPOINT` lines of the final stage of `Dockerfile` and `Dockerfile.release`; verify it passes and fails when one line is edited in either file
- [x] 1.4 Verify the staged image's binary: `docker run --rm` the locally built image with `--version` prints the snapshot version, `id -u` shows 65532, and a write to `/var/lib/tail-watch` succeeds

## 2. Workflows

- [ ] 2.1 Extend the `release-check` job in `.github/workflows/ci.yml` with QEMU and Buildx setup so the snapshot run builds the release image without pushing; verify the job passes on a PR, and fails when `Dockerfile.release` is broken on a scratch commit
- [x] 2.2 In `.github/workflows/release.yml` give only the `publish` job `packages: write`, and add QEMU, Buildx and `docker/login-action` for `ghcr.io` (`github.actor`, `GITHUB_TOKEN`) before the GoReleaser step; verify with `actionlint` or a `workflow_dispatch` dry run that the file parses and the permissions block is scoped to `publish`
- [x] 2.3 Pin the new actions the way the existing ones are pinned (major versions), and confirm none needs a secret beyond `GITHUB_TOKEN`; verify by reading the workflow diff

## 3. Versioned references

- [x] 3.1 Add `extra-files` (generic updater) to `release-please-config.json` for the files in groups 3 and 4, and an `x-release-please-version` marker on each image line; verify with `release-please` dry run or by reading its config schema that the paths and markers resolve
- [x] 3.2 Replace `tail-watch:0.1.0` with `ghcr.io/tailscale-dev/tail-watch:<manifest version>` in `examples/kubernetes/sidecar.yaml`, `kyverno-clusterpolicy.yaml`, `mutatingadmissionpolicy.yaml`, `freeze-and-patch.sh` and the webhook handler in `docs/how-to/kubernetes-proxygroup.md`, remove the "does not publish to a registry yet" comment from `sidecar.yaml`, and add the marker; verify `go test ./cmd/tail-watch -run TestSidecarCopiesAgree` passes
- [x] 3.3 Extend `TestSidecarCopiesAgree` in `cmd/tail-watch/examples_kubernetes_test.go` to assert the image tag in each copy equals the version in `.release-please-manifest.json`; verify it passes, and fails when one copy's tag is edited
- [x] 3.4 Change `scripts/kind-stack.sh` to hold no version: read the image from `examples/kubernetes/sidecar.yaml`, build from source, tag the build with that name before `kind load`; verify `bash -n scripts/kind-stack.sh` and `shellcheck`, and run `TAIL_WATCH_KIND=1 go test ./cmd/tail-watch -run TestKindProxyGroup` if kind is available, since the how-to blocks now name the GHCR tag
- [x] 3.5 Update `docs/contributing/kind-proxygroup-test.md` for the new `TAIL_WATCH_KIND_IMAGE` default; verify the page names the same default as the script

## 4. User documentation

- [x] 4.1 In `docs/how-to/deploy.md` "Run in Docker", lead with `docker pull ghcr.io/tailscale-dev/tail-watch:<version>` and `docker run`, where `<version>` is a placeholder linking to the releases page and not a number, keep `docker build -t tail-watch .` as a from-source option, and say which release first has an image; verify the fenced blocks still match the `TestVMSystemd` extraction and the VM-test doc if that test runs those blocks
- [x] 4.2 Update the "Build the image" section of `docs/how-to/kubernetes-proxygroup.md` to pull from GHCR and keep the build-and-`kind load` path for air-gapped and local use; verify the section's bash blocks are not extracted by `TestKindProxyGroup` as a method heading, or are still valid there
- [x] 4.3 Leave `docs/tutorial/first-monitor.md` and `examples/compose.yaml` unchanged: the Compose file builds from source and the live tests merge override files over it; verify `git diff` shows neither file touched
- [x] 4.4 Add the image, its tags, and the one-time make-the-package-public step to `docs/contributing/releases.md`, and note that `CHANGELOG.md` is release-please's; verify the page names the tags in the spec
- [x] 4.5 Add a "no `tailscaled` in the image" line to `CONTEXT.md` and move "Publish a container image" to done in `ROADMAP.md`; verify both render under `markdownlint`
- [x] 4.6 Update `CLAUDE.md`: the layout note for `Dockerfile.release`, the `extra-files` markers beside the "edit all five together" rule, and `kind-stack.sh`'s new image default; verify against the final files

## 5. Validation

- [x] 5.1 `openspec validate publish-container-image --strict`
- [x] 5.2 `go build ./...`, `go vet ./...`, `go test ./...`, `mise run lint` (extended timeout) and `govulncheck ./...`
- [x] 5.3 `markdownlint` on the changed `docs/`, `CONTEXT.md`, `ROADMAP.md`, `CLAUDE.md` and change files
- [x] 5.4 `mise run release-check` and `docker build .` still succeed
- [ ] 5.5 After merge and the first release: confirm the `publish` job pushed `X.Y.Z`, `X.Y` and `latest`, the package was made public, an anonymous `docker pull` works on amd64 and arm64, and `docker run --rm ghcr.io/tailscale-dev/tail-watch:<version> --version` prints the release version
