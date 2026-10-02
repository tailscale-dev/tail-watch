# proxygroup-sidecar Specification

## Purpose

Runs tail-watch as an opt-in sidecar in Tailscale Kubernetes operator ProxyGroup pods,
so a broken path from the proxy to the workload is detected and alerted on even while
the operator and control report the Tailscale Service as advertised.

## Requirements

### Requirement: Opt-in sidecar selection

The documented injection SHALL add the tail-watch sidecar only to pods that carry both
`tailscale.com/parent-resource-type: proxygroup`, which the operator sets, and
`tail-watch/inject: "true"`, which the admin sets through the ProxyGroup's ProxyClass
pod labels. Any other pod, including a ProxyGroup pod without the opt-in label, SHALL
be admitted unchanged. Removing the label from the ProxyClass SHALL stop injection
for pods created afterwards.

#### Scenario: Opted-in ProxyGroup gets the sidecar

- **WHEN** a ProxyGroup uses a ProxyClass that sets the pod label
  `tail-watch/inject: "true"` and its pods are created
- **THEN** each proxy pod runs a `tail-watch` container next to the `tailscale`
  container

#### Scenario: Other pods are untouched

- **WHEN** a ProxyGroup pod without the opt-in label, or any pod that is not a
  ProxyGroup pod, is created
- **THEN** the pod is admitted exactly as submitted

### Requirement: Per-ProxyGroup configuration

The injected container SHALL run tail-watch in local-only mode with its config read
from the ConfigMap `tail-watch-<name>` in the pod's namespace, key `tail-watch.yaml`,
where `<name>` is the pod's `tailscale.com/parent-resource` label, mounted read-only.
Targets in that config SHALL name the cluster Services the ProxyGroup forwards to,
for example `web.apps.svc.cluster.local` with `tcp:` or `http:` checks. Secrets for
notification sinks SHALL come from a Kubernetes Secret through environment variables,
using the existing `*_env` keys, and SHALL NOT appear in the ConfigMap.

#### Scenario: Config follows the ProxyGroup name

- **WHEN** a pod of ProxyGroup `ingress-a` is injected
- **THEN** its `tail-watch` container reads `tail-watch.yaml` from ConfigMap
  `tail-watch-ingress-a`

#### Scenario: Missing ConfigMap

- **WHEN** the ConfigMap for an opted-in ProxyGroup does not exist
- **THEN** the pod is still created, the `tail-watch` container reports the missing
  config, and the `tailscale` container runs, serves traffic and keeps the Service
  advertised as it would without the sidecar

### Requirement: Proxy isolation

Injection SHALL change only the pod: it SHALL add the `tail-watch` container and its
config volume, and SHALL NOT modify the `tailscale` container, the pod's other
settings, or the operator-owned StatefulSet. A failure of the injector SHALL NOT stop
proxy pods from being created; such a pod SHALL start without the sidecar. The
documented container SHALL set CPU and memory requests and limits and run as a
non-root user, so the admin can see and bound what opting in costs.

#### Scenario: Sidecar failure keeps the Service advertised

- **WHEN** the `tail-watch` container exits or restarts repeatedly
- **THEN** the `tailscale` container keeps serving and the operator keeps the
  Service advertised from that pod

#### Scenario: Injector unavailable

- **WHEN** the injector of a method that relies on an admission webhook is down and
  a ProxyGroup pod is created
- **THEN** the pod is created without the `tail-watch` container

#### Scenario: Operator reconcile keeps the sidecar

- **WHEN** the operator reconciles an opted-in ProxyGroup and its pods are recreated
- **THEN** the new pods have the `tail-watch` container again and the StatefulSet
  spec does not contain it

### Requirement: Documented injection methods

The documentation SHALL describe, with the same opt-in label and ConfigMap contract,
four ways to inject the sidecar so the admin can choose: a MutatingAdmissionPolicy;
a Kyverno ClusterPolicy that mutates Pods on create with policy autogeneration for
pod controllers turned off; a custom mutating admission webhook, giving the
AdmissionReview contract, the JSON patch, the webhook configuration and TLS setup, and
example handler code; and freeze-and-patch, marked as evaluation only because the
operator reconciles nothing while it is scaled to zero. Each method SHALL state the
Kubernetes and component versions it needs and how its failure policy keeps proxy pods
starting. The repository SHALL ship example manifests for the MutatingAdmissionPolicy,
Kyverno and freeze-and-patch methods, and no injector image.

#### Scenario: Admin picks Kyverno

- **WHEN** an admin with Kyverno applies the documented ClusterPolicy and opts a
  ProxyGroup in
- **THEN** the ProxyGroup's pods get the sidecar and the StatefulSet is not mutated

#### Scenario: Admin picks a custom webhook

- **WHEN** an admin builds an injector from the documented contract and example code
- **THEN** it produces the same pod as the other methods for the same opt-in label
  and ConfigMap name

### Requirement: Detect a broken proxy-to-workload hop

With the sidecar running and the Service still advertised, a target for a cluster
Service SHALL go `DOWN` and send `target.down` when traffic from the proxy pod to the
workload fails, even while the Service's EndpointSlice endpoints are `Ready`. It SHALL
send `target.recovered` when the path works again. Detection SHALL rely only on the
sidecar's own checks, not on operator status, endpoint readiness or the LocalAPI.

#### Scenario: NetworkPolicy blocks the proxy

- **WHEN** a NetworkPolicy denies ingress from the operator's namespace to the
  workload, its endpoints stay `Ready`, and the operator still reports the Service as
  advertised
- **THEN** the sidecar's check for that Service fails and `target.down` is sent

#### Scenario: Readiness probe on another port

- **WHEN** the workload's readiness probe passes on one port while nothing listens on
  the Service's `targetPort`
- **THEN** the sidecar's check for that Service fails and `target.down` is sent

#### Scenario: Path restored

- **WHEN** the blocking NetworkPolicy is removed
- **THEN** the target recovers and `target.recovered` is sent
