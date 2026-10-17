#!/usr/bin/env bash
# Build the setec binary at the version pinned in go.mod, for the live tests'
# setec node. It builds outside this module (go.mod must not pick up the
# server's AWS and Tink dependencies) and prints the binary's path.
#
#   scripts/build-setec.sh OUTDIR
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTDIR="${1:?usage: $0 OUTDIR}"
VERSION="$(cd "$ROOT" && go list -m -f '{{.Version}}' github.com/tailscale/setec)"

mkdir -p "$OUTDIR"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
(cd "$WORK" && GOBIN="$(cd "$OUTDIR" && pwd)" CGO_ENABLED=0 go install "github.com/tailscale/setec/cmd/setec@$VERSION") >&2
echo "$(cd "$OUTDIR" && pwd)/setec"
