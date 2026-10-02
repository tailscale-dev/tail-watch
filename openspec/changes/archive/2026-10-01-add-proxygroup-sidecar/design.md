# Design

## Context

See proposal.md (Why) for the blind spot. These facts come from `tailscale.com@v1.102.5`,
the module version `go.mod` pins, and shape the approach:

- Containerboot's `/healthz` reports healthy when the node has tailnet addresses
  (`cmd/containerboot/main.go`, `healthCheck.Update(len(addrs) != 0)`).
- For an L4 Service on an ingress ProxyGroup, the operator advertises the Tailscale
  Service when any EndpointSlice endpoint is `Ready` (`checkEndpointsReady` in
  `cmd/k8s-operator/svc-for-pg.go`). It also needs the replica's forwarding config
  to be current, which it judges from the pod's IPs and the config status in the
  replica's Secret (`backendRoutesSetup`, `isCurrentStatus`). Pod readiness plays no
  part. The L7 Ingress path (`ingress-for-pg.go`) has no endpoint gate at all.
- The operator replaces the whole StatefulSet spec on every reconcile
  (`proxygroup.go`: `s.Spec = ss.Spec` inside `createOrUpdate`). A container added to
  the StatefulSet is gone on the next reconcile.
- ProxyClass can set pod labels, annotations, affinity, node selection, tolerations,
  and the `tailscale` and init containers, but not extra containers or volumes
  (`types_proxyclass.go`, type `Pod`).
- ProxyGroup pods carry `tailscale.com/managed: "true"`,
  `tailscale.com/parent-resource-type: proxygroup` and
  `tailscale.com/parent-resource: <name>` (`pgLabels`).
- Containerboot's tailscaled socket defaults to `/tmp/tailscaled.sock` inside the
  `tailscale` container, which other containers cannot reach without a shared volume.
- tail-watch refuses to start unless tailscaled is reachable, `Running` and in kernel
  mode. It already classifies `network: local` failures from device evidence alone,
  with no presence, route or disco data (failure-classification, "Layer decision
  order").

## Goals / Non-Goals

**Goals:**

- One sidecar contract (opt-in label, ConfigMap name, container shape) that every
  injection method produces, so the admin's choice of method changes nothing else.
- Opting in can never stop a proxy pod from starting or serving, or withdraw a Service.
- Every documented recipe runs in CI-like conditions on kind, so the docs cannot drift
  from what works.

**Non-Goals:**

- Changing the `tailscale` container, containerboot or the operator.
- Checks of hop 1 from inside the pod. The existing tailnet collector covers it from
  outside.
- An L7 Ingress-specific check type. An `http:` check against the Ingress backend
  Service covers it with today's checks.

## Decisions

### Local-only mode instead of sharing containerboot's tailscaled

The sidecar's hop-2 checks are `tcp:` and `http:` checks on cluster Services from the
pod's network namespace. They need no LocalAPI. `collector.local_only: true` skips the
LocalAPI entirely, so the sidecar needs nothing from the `tailscale` container.

Alternative: mount a shared `emptyDir` over the socket directory and set `TS_SOCKET`.
Rejected: it modifies the operator's `tailscale` container, which the isolation
requirement forbids. It would also require kernel-mode tailscaled in ProxyGroup pods,
which is unverified, and it buys nothing hop-2 checks use.

In local-only mode, a target without `network` defaults to `local`, so a sidecar
config does not repeat `network: local` on every target. `network: tailnet`, `tsping`
and the UI are rejected. The UI is refused because its address picker and dynamic
monitors assume a tailnet and a state directory, which are both meaningless here.
Identity is `os.Hostname()`, which is the pod name in Kubernetes. The payload already
marks `collector.tailscale_ip` "when known", so no schema change is needed.

### Inject into Pods, never the StatefulSet

Every method mutates Pod `CREATE` only. Mutating the StatefulSet would fight the
operator's full-spec overwrite. Pods are recreated from the operator's template and
re-injected, so injection survives reconciles and operator upgrades.

### Opt-in through a ProxyClass pod label

The selector is `tailscale.com/parent-resource-type: proxygroup` plus
`tail-watch/inject: "true"`. ProxyClass already sets pod labels, so the admin opts a
ProxyGroup in or out without any operator change, and only the ProxyGroups that use
that ProxyClass pay the resource cost. A cluster-wide namespace label was rejected
because it opts in every ProxyGroup at once.

### Per-ProxyGroup ConfigMap, mounted optional

The container mounts ConfigMap `tail-watch-<parent-resource>`, key `tail-watch.yaml`,
with `optional: true`. Every method builds the name from the pod's
`tailscale.com/parent-resource` label and only mutates pods that carry it, since a
name of `tail-watch-` alone is invalid and would fail the pod. A required ConfigMap volume that is missing blocks the whole pod
in `ContainerCreating`, `tailscale` container included. An optional one lets the pod
start, and tail-watch exits with its existing "config not found" error. Sink secrets
use the existing `*_env` keys filled from a Secret by `envFrom`, also optional.

### Container shape

The sidecar is a regular container appended to `spec.containers`, named `tail-watch`.
It uses the release image with a pinned tag, `--config /etc/tail-watch/tail-watch.yaml`,
and `--listen 127.0.0.1:9109` so the metrics and status page stay inside the pod. It
sets requests of 10m CPU and 32Mi memory and limits of 100m CPU and 64Mi memory. It runs as uid
65532, the image's user, with `readOnlyRootFilesystem` and no capabilities. TCP and HTTP
checks need none, and the docs tell the admin not to use `icmp`. A Kubernetes native
sidecar (restartable init container) was considered. It is rejected because the
ordering it buys does not matter here and it needs a newer minimum version.

### Four methods, one contract

| Method | Mechanism | Failure isolation | Ships |
| --- | --- | --- | --- |
| MutatingAdmissionPolicy | CEL mutation in the API server, `matchConstraints` on Pod `CREATE`, label `matchConditions` | No external component. `failurePolicy: Ignore` | manifest |
| Kyverno | `ClusterPolicy`, `mutate.patchStrategicMerge`, `pod-policies.kyverno.io/autogen-controllers: none` | `failurePolicy: Ignore`, Pod-only match | manifest |
| Custom webhook | `MutatingWebhookConfiguration` with `objectSelector` on both labels and `tailscale.com/parent-resource`, `sideEffects: None`, JSON patch response | `failurePolicy: Ignore`, and `objectSelector` keeps other pods from calling it | docs and example Go handler |
| Freeze and patch | Scale the operator to 0 and `kubectl patch` the StatefulSet | n/a, evaluation only | manifest patch |

Each recipe states its version floor. The MutatingAdmissionPolicy floor is the
Kubernetes release where the API is served. The task list checks it on the kind node
image instead of assuming it. Kyverno's autogen annotation is set explicitly, because
autogen would otherwise copy the rule onto the StatefulSet and fight the operator. The
custom webhook's example handler lives only in the how-to, as a teaching aid. It is not
built, released or tested, which matches the user's choice.

### kind live test runs the documented recipes

`TestKindProxyGroup` is gated on `TAIL_WATCH_KIND=1` and skips when `kind`, `kubectl`
or `helm` is missing.

1. Create a kind cluster from `cmd/tail-watch/testdata/kind-proxygroup.yaml`, which also
   turns on MutatingAdmissionPolicy if the node image's Kubernetes needs it.
2. Install the operator chart, pinned to the `tailscale.com` version in `go.mod`, with
   the OAuth client from the pulumi env.
3. `kind load` a locally built tail-watch image.
4. Create an ingress ProxyGroup, a workload, and an L4 Service on that ProxyGroup.
5. Deploy an in-cluster webhook receiver. The sidecar's sink posts to it and the test
   reads it, with no external secrets.
6. For each manifest method in turn (MutatingAdmissionPolicy, Kyverno,
   freeze-and-patch), apply the fenced blocks and manifests from the how-to. The block
   extraction follows `systemdDocBlocks` in `vm_systemd_test.go`.
7. Assert that the pods have the `tail-watch` container and the StatefulSet does not.
8. Break hop 2 with a NetworkPolicy, then with a readiness probe on the wrong port. Each
   time, assert that EndpointSlices stay `Ready`, that the operator condition reads
   "N/N proxy backends ready and advertising", and that `target.down` arrives, then
   `target.recovered` once the fix is applied.
9. Teardown deletes the cluster, the Tailscale Service and the operator's devices.

### Test tailnet prerequisites

The operator needs an OAuth client with device, auth key and services write scopes,
and policy for `tag:k8s-operator`, `tag:k8s` and Tailscale Service approval. The client
goes into the `tailscale-phase-2/tailscale` pulumi env next to `apiKey`, and the test
reads it the same way `live-stack.sh` reads credentials. The policy snippet joins
`cmd/tail-watch/testdata/snippet.hujson`. The test never writes or logs the secret.

## Risks / Trade-offs

- [A crash-looping sidecar marks the proxy pod NotReady, which can stall the
  StatefulSet's rolling updates during operator upgrades] → Advertising is unaffected,
  because the gate ignores pod readiness. The docs tell admins to run
  `tail-watch check-config` before applying the ConfigMap. The kind test measures
  whether an exiting sidecar changes the ProxyGroup's `Ready` condition or blocks a
  rollout, and the docs state the result.
- [The sidecar's own connections traverse OUTPUT, not the PREROUTING DNAT that
  forwarded traffic takes, so a broken containerboot rule is not seen] → Documented as
  the residual gap. Hop 1 and the DNAT are covered end to end by a tailnet collector
  probing the Service's tailnet name.
- [A webhook with `failurePolicy: Fail` would block proxy pods while it is down] →
  Every recipe uses `Ignore`, and the docs explain why and what is lost: pods start
  without the sidecar.
- [Operator changes rename labels or change reconcile behavior] → The chart is pinned
  to the module version, and the kind test fails loudly when the selector or the
  "StatefulSet does not contain it" assertion breaks.
- [Each replica probes every Service, so cost scales with replicas × Services ÷
  interval] → Documented. Requests and limits are explicit, and the default interval
  stays 30s.
- [The kind test creates real devices and a Tailscale Service on the test tailnet] →
  Teardown deletes them, and on failure the devices are cleaned up by their tag or
  hostname prefix.

## Deviations from the design doc

- "Runtime: kernel-mode tailscaled" requires kernel-mode tailscaled. Local-only mode
  needs no tailscaled, because hop-2 checks never cross the tailnet. The kernel-mode
  rule still holds whenever any target is `network: tailnet`.
- "Placement" says not to run the collector on the device it monitors, because it
  cannot see tunnel failures and goes down with the device. The sidecar runs on the
  proxy deliberately. Its subject is hop 2, which only the proxy's namespace sees, and
  hop 1 stays with a tailnet collector.

## Migration Plan

Additive. `local_only` defaults to `false`, so existing configs and deployments are
unchanged. Admins opt in per ProxyGroup and roll back by removing the ProxyClass label
and the policy. Existing pods keep the sidecar until they are recreated.

## Open Questions

- The exact Kubernetes release where MutatingAdmissionPolicy is served by default, for
  the version floor in the docs. The test's kind config enables it either way, so the
  answer only changes one sentence in the how-to.
- Whether kindnet enforces NetworkPolicy on the kind node image. If it does not, the
  test installs Calico, which is a setup detail with the same assertions.
