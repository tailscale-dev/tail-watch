#!/usr/bin/env bash
# Bring up (or tear down) the kind cluster for TestKindProxyGroup: the
# Tailscale operator pinned to the tailscale.com version in go.mod, the
# fixtures in cmd/tail-watch/testdata/kind, and a locally built tail-watch
# image loaded into the cluster. It does not inject the sidecar or run any
# scenario; the test does that with the doc blocks from
# docs/how-to/kubernetes-proxygroup.md. See
# docs/contributing/kind-proxygroup-test.md. Credentials come from the pulumi
# env in TAIL_WATCH_PULUMI_ENV (default tailscale-phase-2/tailscale): the
# operator's OAuth client as tailscale:operatorClientId and
# tailscale:operatorClientSecret, and tailscale:apiKey plus tailscale:tailnet
# for teardown.
#
#   scripts/kind-stack.sh up      create the cluster and reach a Ready ProxyGroup
#   scripts/kind-stack.sh down    delete the cluster, the Tailscale Service and
#                                 the stack's devices
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${TAIL_WATCH_KIND_CLUSTER:-tail-watch-proxygroup}"
ENVNAME="${TAIL_WATCH_PULUMI_ENV:-tailscale-phase-2/tailscale}"
IMAGE="${TAIL_WATCH_KIND_IMAGE:-tail-watch:0.1.0}"
SERVICE_NAME="svc:tw-pg-web"
DEVICE_PREFIX="tw-pg"

kube() { kubectl --context "kind-$CLUSTER" "$@"; }

creds() {
  pulumi env open "$ENVNAME" --format json | python3 -c '
import json,sys
t=json.load(sys.stdin)["tailscale"]
for k in ("apiKey","tailnet","operatorClientId","operatorClientSecret"):
    print(t.get(k,""))'
}

load_creds() {
  local out
  out="$(creds)"
  APIKEY="$(sed -n 1p <<<"$out")"
  TAILNET="$(sed -n 2p <<<"$out")"
  CLIENT_ID="$(sed -n 3p <<<"$out")"
  CLIENT_SECRET="$(sed -n 4p <<<"$out")"
}

# chart_version pins the operator chart to the tailscale.com module version in
# go.mod. Not every module release ships a chart (v1.102.5 has none), so pick
# the newest chart version that is not newer than the module version.
chart_version() {
  local mod
  mod="$(cd "$ROOT" && mise exec -- go list -m -f '{{.Version}}' tailscale.com | sed 's/^v//')"
  helm search repo tailscale/tailscale-operator --versions --output json |
    python3 -c '
import json,sys
want=sys.argv[1]
def key(v):
    return [int(p) for p in v.split(".")]
versions=[e["version"] for e in json.load(sys.stdin)]
ok=[v for v in versions if key(v)<=key(want)]
if not ok:
    sys.exit("no tailscale-operator chart at or below "+want)
print(sorted(ok,key=key)[-1])' "$mod"
}

api() { curl -fsS -K <(printf 'user = "%s:"\n' "$APIKEY") -H 'Content-Type: application/json' "$@"; }

up() {
  load_creds
  if [ -z "$CLIENT_ID" ] || [ "$CLIENT_ID" = "None" ] || [ -z "$CLIENT_SECRET" ] || [ "$CLIENT_SECRET" = "None" ]; then
    echo "pulumi env $ENVNAME has no tailscale:operatorClientId/operatorClientSecret;" >&2
    echo "add an OAuth client with Devices Core, Auth Keys and Services write scopes, tagged tag:k8s-operator" >&2
    exit 1
  fi
  helm repo add tailscale https://pkgs.tailscale.com/helmcharts >/dev/null
  helm repo update tailscale >/dev/null
  local version
  version="$(chart_version)"
  if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    kind create cluster --name "$CLUSTER" --config "$ROOT/cmd/tail-watch/testdata/kind-proxygroup.yaml" --wait 120s
  fi
  # Secrets go through a values file, never the command line.
  local values
  values="$(mktemp)"
  chmod 600 "$values"
  trap 'rm -f "$values"' EXIT
  cat >"$values" <<EOF
oauth:
  clientId: "$CLIENT_ID"
  clientSecret: "$CLIENT_SECRET"
EOF
  helm upgrade --install tailscale-operator tailscale/tailscale-operator \
    --version "$version" \
    --namespace tailscale --create-namespace \
    --kube-context "kind-$CLUSTER" \
    --values "$values" \
    --wait --timeout 240s
  rm -f "$values"
  trap - EXIT

  docker build -q -t "$IMAGE" "$ROOT" >/dev/null
  kind load docker-image "$IMAGE" --name "$CLUSTER"

  kube apply -f "$ROOT/cmd/tail-watch/testdata/kind/workload.yaml"
  kube apply -f "$ROOT/cmd/tail-watch/testdata/kind/proxygroup.yaml"
  # The sink Secret (tail-watch-ingress in namespace tailscale) is left to the
  # test: the how-to's blocks apply the placeholder from
  # examples/kubernetes/secret.yaml, and the test then points OPS_WEBHOOK_URL
  # at the in-cluster alert-receiver.
  kube -n apps wait --for=condition=Available deploy/web deploy/alert-receiver --timeout=240s
  kube wait --for=condition=ProxyGroupReady proxygroup/ingress proxygroup/plain --timeout=600s
  echo "kind cluster $CLUSTER is up; ProxyGroup ingress and plain are Ready"
  echo "Run the test with: TAIL_WATCH_KIND=1 go test -run TestKindProxyGroup ./cmd/tail-watch"
}

down() {
  load_creds
  kind delete cluster --name "$CLUSTER" || true
  api -X DELETE "https://api.tailscale.com/api/v2/tailnet/$TAILNET/services/$SERVICE_NAME" >/dev/null 2>&1 &&
    echo "deleted Tailscale Service $SERVICE_NAME" || true
  api "https://api.tailscale.com/api/v2/tailnet/$TAILNET/devices?fields=all" | python3 -c '
import json,sys
p=sys.argv[1].lower()
for d in json.load(sys.stdin)["devices"]:
    if d["hostname"].lower().startswith(p): print(d["id"])' "$DEVICE_PREFIX" |
    while read -r id; do api -X DELETE "https://api.tailscale.com/api/v2/device/$id" >/dev/null && echo "deleted device $id"; done
}

case "${1:-}" in
up) up ;;
down) down ;;
*) echo "usage: $0 up|down" >&2; exit 2 ;;
esac
