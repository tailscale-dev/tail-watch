#!/usr/bin/env bash
# EVALUATION ONLY.
#
# This freezes the Tailscale operator (scales it to zero) and patches the
# ProxyGroup's StatefulSet directly with the tail-watch sidecar. While the
# operator is scaled to zero it reconciles nothing: ProxyGroups, Services and
# Ingresses stop reacting to changes, and existing proxy pods keep running
# unmanaged. Use this to evaluate the sidecar on a test cluster, not in
# production. The manifest-driven methods in
# docs/how-to/kubernetes-proxygroup.md mutate Pod CREATE instead and leave
# the operator running.
#
# Usage: freeze-and-patch.sh <proxygroup-name>
#
# Undo: scale the operator back to 1. Its next reconcile overwrites the whole
# StatefulSet spec, which removes the sidecar:
#   kubectl scale deployment operator -n tailscale --replicas=1
set -euo pipefail

usage() {
  echo "usage: $0 <proxygroup-name>" >&2
  echo "example: $0 ingress" >&2
}

if [ "$#" -ne 1 ]; then
  usage
  exit 2
fi

PG="$1"
NS="tailscale"
OPERATOR_DEPLOYMENT="operator"
STS="${PG}"
IMAGE="ghcr.io/tailscale-dev/tail-watch:0.2.0" # x-release-please-version

# The same container and volume as examples/kubernetes/sidecar.yaml, as a
# strategic merge patch. Containers and volumes merge by name.
read -r -d '' PATCH <<EOF || true
{
  "spec": {
    "template": {
      "spec": {
        "containers": [
          {
            "name": "tail-watch",
            "image": "${IMAGE}",
            "imagePullPolicy": "IfNotPresent",
            "args": ["--config", "/etc/tail-watch/tail-watch.yaml",
                     "--listen", "127.0.0.1:9109"],
            "envFrom": [
              {"secretRef": {"name": "tail-watch-${PG}", "optional": true}}
            ],
            "volumeMounts": [
              {"name": "tail-watch-config", "mountPath": "/etc/tail-watch",
               "readOnly": true}
            ],
            "resources": {
              "requests": {"cpu": "10m", "memory": "32Mi"},
              "limits": {"cpu": "100m", "memory": "64Mi"}
            },
            "securityContext": {
              "runAsUser": 65532,
              "runAsGroup": 65532,
              "readOnlyRootFilesystem": true,
              "allowPrivilegeEscalation": false,
              "capabilities": {"drop": ["ALL"]}
            }
          }
        ],
        "volumes": [
          {"name": "tail-watch-config",
           "configMap": {"name": "tail-watch-${PG}", "optional": true}}
        ]
      }
    }
  }
}
EOF

echo "Scaling the operator to 0. Nothing reconciles while it is down."
kubectl scale deployment "${OPERATOR_DEPLOYMENT}" -n "${NS}" --replicas=0

echo "Patching StatefulSet ${STS} in namespace ${NS} with the tail-watch sidecar."
kubectl patch statefulset "${STS}" -n "${NS}" --type strategic --patch "${PATCH}"

echo "Restarting the ProxyGroup pods so they pick up the sidecar."
kubectl rollout restart statefulset "${STS}" -n "${NS}"

cat <<EOF

Done. Verify with:
  kubectl get pod ${STS}-0 -n ${NS} -o jsonpath='{.spec.containers[*].name}'

To undo (the operator's reconcile removes the sidecar from the StatefulSet):
  kubectl scale deployment ${OPERATOR_DEPLOYMENT} -n ${NS} --replicas=1
EOF
