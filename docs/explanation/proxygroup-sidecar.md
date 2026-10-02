# ProxyGroup sidecar

A Tailscale Kubernetes operator ingress ProxyGroup can advertise a Tailscale
Service while traffic from the proxy pod to the workload is broken. This page
explains why Kubernetes does not notice, what a tail-watch sidecar in the
proxy pod adds, and what it still cannot see.

To install the sidecar, see
[Watch a Kubernetes ProxyGroup](../how-to/kubernetes-proxygroup.md). The
`collector.local_only` key it relies on is in the
[configuration reference](../reference/config.md#local-only-mode).

## Why Kubernetes does not notice hop 2

An ingress ProxyGroup has two hops: hop 1 from the tailnet client to the
proxy pod, and hop 2 from the proxy pod to the workload Service. Kubernetes
health signals do not cover hop 2:

- Containerboot's `/healthz` reports healthy when the node has tailnet
  addresses. It says nothing about the path to the workload.
- The operator advertises an L4 Tailscale Service as soon as any
  EndpointSlice endpoint is `Ready`. Readiness is the kubelet's view from the
  workload's node, not reachability from the proxy pod.

So a NetworkPolicy that drops the proxy's traffic, a readiness probe on a
different port than the Service's `targetPort`, or a broken CNI path on one
proxy's node all leave the Service advertised while clients cannot use it.

A tail-watch sidecar in the proxy pod probes the workload Services from the
pod's own network namespace, which is exactly the vantage point hop 2 needs.

## What a crash-looping sidecar does

A sidecar that keeps exiting marks its proxy pod NotReady. The kind test
measured what follows: the Service stays advertised, because the operator's
advertising gate ignores pod readiness and `ProxyGroupReady` stays `True`. A
StatefulSet rolling update, though, stalls at the affected pod until the
sidecar runs again, because the update waits for each pod to be Ready. That is
why the how-to validates the config before applying it.

## What the sidecar cannot see

The sidecar's own connections leave the pod through OUTPUT, not through the
PREROUTING DNAT that forwarded tailnet traffic takes. A broken containerboot
forwarding rule is invisible to it. Cover hop 1 — tailnet client to proxy,
including the DNAT — end to end with a regular tail-watch collector probing
the Service's tailnet name, as in
[Monitor remote subnets and local LANs](../how-to/remote-subnets.md).
