# kind ProxyGroup test

This page is for contributors who need to run the ProxyGroup sidecar test
against a real cluster and tailnet.

`TestKindProxyGroup` runs the documented sidecar recipes from
[Watch a Kubernetes ProxyGroup](../how-to/kubernetes-proxygroup.md) on a kind
cluster with the Tailscale Kubernetes operator and an ingress ProxyGroup. It is
off unless `TAIL_WATCH_KIND=1`, and it skips when `kind`, `kubectl`, `helm` or
`docker` is missing. For each injection method (MutatingAdmissionPolicy, Kyverno,
freeze-and-patch) it applies the how-to's bash blocks, then breaks hop 2 — a
NetworkPolicy denying the operator namespace, and a readiness probe on a port
other than `targetPort` — while the Service's endpoints stay `Ready` and the
operator keeps advertising, and expects the sidecar's `target.down` and
`target.recovered` at an in-cluster webhook receiver. Before each
admission-time method touches the proxy pods, a probe pod labelled as
ProxyGroup `probe` must come back with ConfigMap and Secret `tail-watch-probe`,
which proves the names follow the pod's ProxyGroup rather than `ingress`.

The unit test `TestSidecarCopiesAgree` needs no cluster: it checks every
embedded copy of the sidecar against `examples/kubernetes/sidecar.yaml`, so
change that file and the copies together.

```bash
TAIL_WATCH_KIND=1 go test -count=1 -timeout 30m -run TestKindProxyGroup -v ./cmd/tail-watch
```

## Prerequisites

- `kind`, `kubectl`, `helm` and a running Docker daemon.
- The cluster config is `cmd/tail-watch/testdata/kind-proxygroup.yaml`. The
  MutatingAdmissionPolicy API is v1beta1 and is not served by default on the
  kindest/node v1.35.0 image, so the config turns on the
  `MutatingAdmissionPolicy` feature gate and the
  `admissionregistration.k8s.io/v1beta1` runtime config.
- kindnet on the v1.35.0 node image enforces NetworkPolicy (verified: a
  deny-all ingress policy blocks pod-to-pod traffic), so no alternate CNI is
  installed. If a future node image changes that, the hop-2 NetworkPolicy
  break stops working and the test fails at the `target.down` assertion.
- The fixtures are in `cmd/tail-watch/testdata/kind/`: an nginx workload and
  its ClusterIP Service `web.apps.svc.cluster.local`, the L4 Service `web-ts`
  advertised as Tailscale Service `svc:tw-pg-web`, the ProxyGroups `ingress`
  (the test subject) and `plain` (the negative control), and the
  `alert-receiver` webhook receiver the sidecar's sink posts to.

## Tailnet policy and credentials

The operator needs the same policy as a production install: the
`tag:k8s-operator` and `tag:k8s` tagOwners and the Tailscale Services
auto-approver in `cmd/tail-watch/testdata/snippet.hujson`, merged into the
test tailnet policy.

Credentials come from the `tailscale-phase-2/tailscale` pulumi env
(`TAIL_WATCH_PULUMI_ENV` overrides it), next to `tailscale:apiKey`:

- `tailscale:operatorClientId` and `tailscale:operatorClientSecret`: an OAuth
  client with Devices Core, Auth Keys and Services write scopes, tagged
  `tag:k8s-operator`. The operator chart is pinned to the newest chart version
  at or below the `tailscale.com` version in `go.mod`.
- `tailscale:apiKey` and `tailscale:tailnet`: teardown deletes the Tailscale
  Service `svc:tw-pg-web` and every device whose hostname starts with `tw-pg`.

The test and the helper script never print or persist these values; the OAuth
secret reaches Helm through a mode-0600 values file that is deleted
afterwards.

## The harness by hand

`scripts/kind-stack.sh` does everything except run the scenarios:

```bash
scripts/kind-stack.sh up    # cluster, operator, fixtures, Ready ProxyGroup
scripts/kind-stack.sh down  # cluster, Tailscale Service and tw-pg devices gone
```

`TAIL_WATCH_KIND_CLUSTER` overrides the cluster name
(`tail-watch-proxygroup`), `TAIL_WATCH_KIND_IMAGE` the image built and loaded
(`tail-watch:0.1.0`). The test builds the same harness itself when the cluster
is missing, so a bare `go test` run works; `kind-stack.sh up` is for iterating
on the scenarios without paying cluster startup each time.

## Cleanup

The test leaves the harness running so the next run can reuse it. Tear it down
with `scripts/kind-stack.sh down`, and rerun it if a failed run leaves tailnet
state behind: it deletes the kind cluster, the Tailscale Service and any device
with the `tw-pg` hostname prefix, which covers the operator's proxies
(`tw-pg-ingress-*`, `tw-pg-plain-*`).
