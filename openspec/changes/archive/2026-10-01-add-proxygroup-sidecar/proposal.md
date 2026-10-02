# Proposal

## Why

A Tailscale Kubernetes operator ingress ProxyGroup advertises a Tailscale Service
while traffic from the proxy to the workload (hop 2) is broken, and nothing notices.
The proxy's `/healthz` only says the node has tailnet addresses. The operator
advertises an L4 Service once any EndpointSlice endpoint is `Ready`. Readiness is
the kubelet's view from the workload's node, not reachability from the proxy pod.
A NetworkPolicy that drops the proxy's traffic, a readiness probe on a different
port from `targetPort`, or a broken CNI path on one proxy's node all leave control
advertising a Service that clients cannot use. tail-watch already runs TCP and HTTP
checks on `network: local` targets and alerts on them. Running it next to the proxy
covers that blind spot without operator changes. A Kubernetes operator maintainer
asked for this, as a step toward a containerboot companion.

## What Changes

- Add `collector.local_only`. With it on, tail-watch starts without tailscaled:
  it skips the LocalAPI preflight, the peer presence view, dependency learning and
  disco probes. It accepts only `network: local` targets and refuses `tsping`
  checks and the monitor UI. Alerts carry the hostname as `collector.name` and omit
  `collector.tailscale_ip`, which the payload already allows. The default keeps
  today's behavior.
- Document running tail-watch as an opt-in sidecar in ProxyGroup proxy pods. The
  admin opts a ProxyGroup in by adding a pod label through its ProxyClass. Each
  ProxyGroup reads its own ConfigMap, named from the `tailscale.com/parent-resource`
  pod label. The sidecar probes the ClusterIP Services the proxy forwards to.
- Document four injection methods for the admin to choose from, because the
  operator overwrites its StatefulSet spec on every reconcile and ProxyClass cannot
  add containers:
  - MutatingAdmissionPolicy, for clusters where it is enabled.
  - A Kyverno ClusterPolicy that mutates Pods on create, with autogen off.
  - A custom mutating webhook: the AdmissionReview contract, the JSON patch, the
    webhook configuration and TLS, with example handler code in the docs. No
    injector image ships.
  - Freeze and patch: scale the operator to zero and patch the StatefulSet. This is
    for evaluation only, because nothing reconciles while the operator is down.

  Every method uses the same opt-in label and ConfigMap name and must not stop
  proxy pods from starting when the injector fails.
- Ship example manifests under `examples/kubernetes/` for the methods that apply
  manifests.
- Add a kind-based live test, gated like the other live tests. It brings up the
  operator and an ingress ProxyGroup. It injects the sidecar with each manifest
  method from the docs. It breaks hop 2 in two ways while the Service's endpoints
  stay `Ready` and the operator still reports the Service as advertised. It expects
  the sidecar to alert on each break.

## Capabilities

### New Capabilities

- `proxygroup-sidecar`: tail-watch as an opt-in sidecar in operator ProxyGroup pods.
  Covers opt-in selection, the per-ProxyGroup config, the injection methods and
  their failure isolation, and detecting a broken proxy-to-workload hop while the
  Service is advertised.

### Modified Capabilities

- `collector-config`: a `collector.local_only` key, and validation that limits a
  local-only config to `network: local` targets with no `tsping` and no UI.
- `collector-runtime`: in local-only mode, startup no longer requires a reachable,
  `Running` tailscaled, and the collector identity comes from the hostname.

## Impact

- Design doc sections: "Placement" gains a Kubernetes sidecar placement, and
  "Runtime: kernel-mode tailscaled" with "Network access" gain the local-only
  exception. "What a failed check means" already classifies `network: local`
  failures without tailnet evidence and is unchanged.
- Phase: a phase 1 follow-up. Non-goals, deferred to later phases or other work:
  - `tsnetEnv`, `target.degraded`, PagerDuty and other phase 2 items, and phase 3
    quorum.
  - Acting on a failure, such as unadvertising the Service from a bad replica.
  - An operator setting for sidecars. Both of these are tracked in a separate
    issue for the operator.
  - A shipped injector image.
  - Probing hop 1 (tailnet client to proxy) from the sidecar.
  - Deriving targets from containerboot's ingress config file.
- Code: `cmd/tail-watch/config.go` (key and validation), `main.go` (startup,
  identity, preflight), plus tests. Checks, the payload schema and metric names do
  not change.
- New: `docs/how-to/kubernetes-proxygroup.md`, `examples/kubernetes/`, a kind live
  test with its compose-free helper script, and doc updates in `reference/config.md`
  and `reference/cli.md`.
- Test prerequisites: kind and Helm, which are installed. The test tailnet also
  needs an OAuth client for the operator, which the current pulumi env does not
  hold, and policy for the operator's tags and Tailscale Services.
- No new Go dependency.
