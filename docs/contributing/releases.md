# Development and releases

This page is for contributors who build, check, and release tail-watch.

`mise run check` builds, tests, and lints with the pinned tools. The lint
configuration is based on
[maratori/golangci-lint-config](https://github.com/maratori/golangci-lint-config),
with this module's import prefix. CI also runs race tests and builds release
archives for Linux, macOS, and Windows on amd64 and arm64.

## Releasing

Use Conventional Commits in squash-merge titles (`fix: ...`, `feat: ...`,
or `feat!: ...` for breaking changes). After checks pass on `main`,
release-please opens or updates a release PR containing the version and
changelog. Merge that PR to create a GitHub release; GoReleaser then attaches
the archives and `checksums.txt`. Each archive includes `tail-watch`, the license,
the README, the `docs/` tree, and `examples/tail-watch.yaml`. The binary's
`--version` reports the release version.

release-please owns `CHANGELOG.md`; do not edit it by hand. It lists `feat`, `fix`,
`perf`, `revert` and `docs` commits, so a documentation change appears under
**Documentation** when its squash-merge title starts with `docs:`.

## Repository setup

In **Settings → Actions → General → Workflow permissions**,
enable **Allow GitHub Actions to create and approve pull requests**.
The workflow uses the built-in `GITHUB_TOKEN`. PRs created with that token
do not automatically start CI; close and reopen the release PR to trigger
its checks. Publishing runs in the release workflow itself because tags
created with that token also do not trigger a separate workflow.

## Validating packaging locally

To validate packaging locally without publishing:

```bash
mise run release-check
```

Snapshot archives appear in `dist/`. If publishing fails after the release
has been created, rerun the failed job or manually run the **Release** workflow
with its existing tag (for example, `v0.1.0`).
