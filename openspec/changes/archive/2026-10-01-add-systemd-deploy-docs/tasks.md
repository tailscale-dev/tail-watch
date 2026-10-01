# Tasks

## 1. Systemd unit

- [x] 1.1 Write `examples/tail-watch.service` with the shape in `design.md`; verify `systemd-analyze verify examples/tail-watch.service` reports nothing except the missing binary path
- [x] 1.2 Install the unit on a host with a kernel-mode `tailscaled`, create the `tail-watch` user, and start it with `examples/tail-watch.yaml`; verify `systemctl status` is active, `/healthz` answers `ok`, and no `tailscale set --operator` was run — verified by `TestVMSystemd/active` on a QEMU/KVM Ubuntu 24.04 guest
- [x] 1.3 On that host, configure one `icmp` and one `tsping` target; verify both run as the `tail-watch` user with only `AmbientCapabilities=CAP_NET_RAW`, and that a `queue: disk` sink writes under `/var/lib/tail-watch` — verified by `TestVMSystemd/icmp-needs-capability` and `TestVMSystemd/checks-and-queue`
- [x] 1.4 Stop the service with `systemctl stop`; verify it exits cleanly on `SIGTERM` and `journalctl` shows no error — verified by `TestVMSystemd/clean-stop` (`ExecMainStatus=0`, `Result=success`, `NRestarts=0`, no error-level journal lines)

## 2. Documentation

- [x] 2.1 Add a "Download a release" section to `docs/how-to/deploy.md` using `VERSION`, `OS` and `ARCH` variables, `curl`, `sha256sum -c --ignore-missing` and `install`; verify each command's syntax and the archive names against `.goreleaser.yaml`
- [x] 2.2 Add a "Run under systemd" section to `docs/how-to/deploy.md`: create the user, install the binary, config and unit, the environment file and `LoadCredential=` options for secrets, `ReadWritePaths=` for a different `state_dir`, and dropping `CAP_NET_RAW` when no `icmp` checks exist; verify each step against the run in 1.2 to 1.4 — verified by `TestVMSystemd/docs-commands`, which replays the section's bash blocks in the guest
- [x] 2.3 In the same section, state why no `tailscale set --operator` step is needed, and name the LocalAPI calls tail-watch makes; verify against `netenv.go`, `checks.go` and `main.go`
- [x] 2.4 Add a `docs/how-to/troubleshoot.md` row for a service that cannot reach `tailscaled` (`tailscaled is not reachable at <socket>`) and for a `state_dir` outside `StateDirectory`; verify the message text against `main.go`
- [x] 2.5 Link the new sections from `docs/README.md` and from the deploy lines in `README.md`; verify every link resolves

## 3. Packaging

- [x] 3.1 Add `examples/tail-watch.service` to the archive `files` in `.goreleaser.yaml`; verify `mise run release-check` succeeds and the linux archive contains it
- [x] 3.2 Update `docs/contributing/releases.md` where it lists the archive contents; verify it matches the archive from 3.1

## 4. Post-release check

- [ ] 4.1 After v0.1.0 is published, run the download commands from 2.1 against the real release on a clean host; verify the checksum passes and `tail-watch --version` prints `0.1.0`, and fix any placeholder that is wrong

## 5. Validation

- [x] 5.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test ./...`; verify all pass, confirming no Go code changed
- [x] 5.2 Run `mise run lint` (golangci-lint) and `govulncheck ./...`; verify no findings
- [x] 5.3 Run `markdownlint` over `README.md`, `docs/` and `openspec/changes/add-systemd-deploy-docs/`; verify zero findings
- [x] 5.4 Run a local link check over `docs/` and `README.md`; verify none are broken
- [x] 5.5 Run `mise exec -- openspec validate add-systemd-deploy-docs --strict`; verify it passes
