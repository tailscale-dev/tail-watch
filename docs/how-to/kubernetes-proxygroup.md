# Watch a Kubernetes ProxyGroup from inside the proxy pod

A Tailscale Kubernetes operator ingress ProxyGroup can advertise a Tailscale
Service while traffic from the proxy pod to the workload is broken, and
nothing notices. This page runs tail-watch as an opt-in sidecar in the proxy
pods to close that gap, with four injection methods to choose from.

It assumes the operator is already installed in the `tailscale` namespace and
that you have an ingress ProxyGroup, called `ingress` here, forwarding to a
ClusterIP Service in the `apps` namespace.

Why Kubernetes misses this, and what the sidecar cannot see, is in
[ProxyGroup sidecar](../explanation/proxygroup-sidecar.md).

## The contract

Every injection method below produces the same pod, selected the same way:

- **Opt-in selector.** The sidecar is added only to pods carrying both
  `tailscale.com/parent-resource-type: proxygroup`, which the operator sets,
  and `tail-watch/inject: "true"`, which you set through the ProxyGroup's
  ProxyClass pod labels. All other pods are admitted unchanged. Removing the
  label from the ProxyClass stops injection for pods created afterwards.
- **Per-ProxyGroup config.** The sidecar reads `tail-watch.yaml` from the
  ConfigMap named `tail-watch-<proxygroup-name>` in the operator's namespace,
  mounted read-only at `/etc/tail-watch`. Each method builds the name from the
  pod's `tailscale.com/parent-resource` label, which the operator sets, so one
  policy serves every opted-in ProxyGroup. Sink secrets come from the Secret of
  the same name through `envFrom` and the existing `*_env` keys, never from the
  ConfigMap.
- **Local-only mode.** The config sets
  [`collector.local_only`](../reference/config.md#local-only-mode): no
  tailscaled, targets default to `network: local`, and the payload identifies
  the collector by pod hostname. Do not use `icmp` checks in the sidecar —
  the container drops all capabilities, so there is no `NET_RAW`. Use `tcp:`
  and `http:` checks only.
- **Container shape.**
  [`examples/kubernetes/sidecar.yaml`](../../examples/kubernetes/sidecar.yaml)
  is the exact fragment every method embeds: image, args, CPU and memory
  requests and limits, a non-root user, a read-only root filesystem, and all
  capabilities dropped.
- **Never blocks a proxy pod.** Both volumes and every method's failure
  policy are `Ignore` or optional, so opting in can never stop a proxy pod
  from starting or withdraw a Service. If the injector fails or the ConfigMap
  is missing, the pod starts without monitoring and the `tailscale` container
  serves as before; it gets the sidecar when it is next recreated after the
  injector recovers. That recreation can take a few tries right after the
  injector comes back, while its webhook is still being registered.

A sidecar that crash-loops on a bad config stalls the ProxyGroup's rolling
updates (see [what a crash-looping sidecar
does](../explanation/proxygroup-sidecar.md#what-a-crash-looping-sidecar-does)),
so run `tail-watch check-config` on the ConfigMap contents before applying:

```bash
kubectl get configmap tail-watch-ingress -n tailscale \
  -o jsonpath='{.data.tail-watch\.yaml}' | tail-watch check-config --config /dev/stdin
```

## Build the image

The project does not publish to a registry yet. Build the image from the
repository at the release tag and push it somewhere the cluster can pull:

```bash
git checkout v0.1.0
docker build -t tail-watch:0.1.0 .
```

For a local kind cluster, `kind load docker-image tail-watch:0.1.0` is
enough. The manifests use `imagePullPolicy: IfNotPresent`.

## MutatingAdmissionPolicy

Version floor: `admissionregistration.k8s.io/v1beta1` MutatingAdmissionPolicy
exists from Kubernetes 1.34 (beta). It is not served by default everywhere —
on the kindest/node v1.35.0 image the API server must start with the
MutatingAdmissionPolicy feature gate and the
`admissionregistration.k8s.io/v1beta1` runtime config (the kind config in this
repository enables both). On 1.32–1.33, enable the same feature gate and the
v1alpha1 (1.32) or v1beta1 (1.33) API, and change the manifest's `apiVersion`
to match. No external component runs, and
`failurePolicy: Ignore` means a failed mutation admits the pod unchanged, so
proxy pods always start.

The policy matches Pod CREATE and its `matchConditions` select only opted-in
ProxyGroup pods that carry the `tailscale.com/parent-resource` label. Apply
the manifests and opt the ProxyGroup in:

```bash
kubectl apply -f examples/kubernetes/proxyclass.yaml
kubectl apply -f examples/kubernetes/secret.yaml
kubectl apply -f examples/kubernetes/configmap.yaml
kubectl apply -f examples/kubernetes/mutatingadmissionpolicy.yaml
kubectl patch proxygroup ingress --type merge \
  -p '{"spec":{"proxyClass":"tail-watch"}}'
kubectl rollout restart statefulset ingress -n tailscale
kubectl rollout status statefulset ingress -n tailscale
```

Verify the sidecar is there and the StatefulSet template is not:

```bash
kubectl get pod ingress-0 -n tailscale -o jsonpath='{.spec.containers[*].name}'
# tailscale tail-watch
kubectl get statefulset ingress -n tailscale \
  -o jsonpath='{.spec.template.spec.containers[*].name}'
# tailscale
```

## Kyverno

Version floor: any supported Kyverno v1 (`kyverno.io/v1` ClusterPolicy; the
recipe is tested with chart kyverno-3.9.1, which deprecates the API in favour
of the CEL-based `policies.kyverno.io` MutatingPolicy but still enforces it).
`failurePolicy: Ignore` admits pods unchanged when Kyverno fails, and
`background: false` keeps the rule on the admission path only.

The annotation `pod-policies.kyverno.io/autogen-controllers: "none"` is
required: Kyverno's autogen would otherwise generate a rule that mutates the
ProxyGroup's StatefulSet, which the operator overwrites on every reconcile.
The rule mutates Pod CREATE only:

```bash
helm repo add kyverno https://kyverno.github.io/kyverno/ --force-update
helm upgrade --install kyverno kyverno/kyverno -n kyverno --create-namespace --wait
kubectl apply -f examples/kubernetes/proxyclass.yaml
kubectl apply -f examples/kubernetes/secret.yaml
kubectl apply -f examples/kubernetes/configmap.yaml
kubectl apply -f examples/kubernetes/kyverno-clusterpolicy.yaml
kubectl patch proxygroup ingress --type merge \
  -p '{"spec":{"proxyClass":"tail-watch"}}'
kubectl rollout restart statefulset ingress -n tailscale
kubectl rollout status statefulset ingress -n tailscale
```

Verify as above:

```bash
kubectl get pod ingress-0 -n tailscale -o jsonpath='{.spec.containers[*].name}'
# tailscale tail-watch
```

## Custom webhook

Version floor: any cluster with `admissionregistration.k8s.io/v1`, which is
every supported Kubernetes release. No injector image ships and this project
does not build or test one; the code below is a teaching aid for writing your
own.

The webhook answers an AdmissionReview request for a Pod CREATE with an
`allowed: true` response carrying a base64 JSON patch that appends the
sidecar container and volume. Two guards keep proxy pods starting: the
`objectSelector` restricts calls to pods with both opt-in labels, and
`failurePolicy: Ignore` admits the pod unchanged if the webhook is down.

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: tail-watch-sidecar
webhooks:
  - name: tail-watch-sidecar.example.com
    admissionReviewVersions: ["v1"]
    sideEffects: None
    failurePolicy: Ignore
    reinvocationPolicy: Never
    objectSelector:
      matchLabels:
        tailscale.com/parent-resource-type: proxygroup
        tail-watch/inject: "true"
      matchExpressions:
        - key: tailscale.com/parent-resource
          operator: Exists
    clientConfig:
      service:
        name: tail-watch-injector
        namespace: tailscale
        path: /mutate
        port: 443
      caBundle: "" # PEM of the CA that signed the serving certificate
    rules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["pods"]
```

TLS is your responsibility. With cert-manager, issue a `Certificate` for
`tail-watch-injector.tailscale.svc` and copy its CA into `caBundle` (or use a
`caInjector` annotation). Manually, create a serving certificate signed by a
CA you control — a `CertificateSigningRequest` with signer
`kubernetes.io/kubelet-serving` does not work here; generate one with your
own CA or the cluster's — and base64 the CA into `caBundle`.

The handler, listening on 8443 behind that Service:

```go
// Teaching aid for docs/how-to/kubernetes-proxygroup.md. Not shipped.
package main

import (
	"encoding/json"
	"log"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
)

// sidecar and volume are examples/kubernetes/sidecar.yaml with the ConfigMap
// and Secret named tail-watch-<proxygroup-name>.
func sidecar(name string) map[string]any {
	return map[string]any{
		"name":            "tail-watch",
		"image":           "tail-watch:0.1.0",
		"imagePullPolicy": "IfNotPresent",
		"args": []string{"--config", "/etc/tail-watch/tail-watch.yaml",
			"--listen", "127.0.0.1:9109"},
		"envFrom": []any{map[string]any{
			"secretRef": map[string]any{"name": name, "optional": true}}},
		"volumeMounts": []any{map[string]any{
			"name": "tail-watch-config", "mountPath": "/etc/tail-watch", "readOnly": true}},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "10m", "memory": "32Mi"},
			"limits":   map[string]any{"cpu": "100m", "memory": "64Mi"}},
		"securityContext": map[string]any{
			"runAsUser": 65532, "runAsGroup": 65532,
			"readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false,
			"capabilities": map[string]any{"drop": []string{"ALL"}}},
	}
}

func volume(name string) map[string]any {
	return map[string]any{
		"name":      "tail-watch-config",
		"configMap": map[string]any{"name": name, "optional": true},
	}
}

func mutate(w http.ResponseWriter, r *http.Request) {
	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := &admissionv1.AdmissionResponse{UID: review.Request.UID, Allowed: true}

	var pod corev1.Pod
	if err := json.Unmarshal(review.Request.Object.Raw, &pod); err == nil {
		name := "tail-watch-" + pod.Labels["tailscale.com/parent-resource"]
		ops := []map[string]any{
			{"op": "add", "path": "/spec/containers/-", "value": sidecar(name)},
		}
		if len(pod.Spec.Volumes) == 0 {
			ops = append(ops, map[string]any{
				"op": "add", "path": "/spec/volumes", "value": []any{volume(name)}})
		} else {
			ops = append(ops, map[string]any{
				"op": "add", "path": "/spec/volumes/-", "value": volume(name)})
		}
		patch, err := json.Marshal(ops)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Patch is []byte, so encoding/json base64-encodes it on the wire.
		resp.Patch = patch
		pt := admissionv1.PatchTypeJSONPatch
		resp.PatchType = &pt
	}

	review.Response = resp
	review.Request = nil
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(review)
}

func main() {
	http.HandleFunc("/mutate", mutate)
	log.Fatal(http.ListenAndServeTLS(":8443", "/tls/tls.crt", "/tls/tls.key", nil))
}
```

The `objectSelector` already restricts calls to opted-in ProxyGroup pods that
carry `tailscale.com/parent-resource`, so the handler can trust that every
request it sees wants the sidecar and names its ProxyGroup. Deploy it
with the same opt-in, ConfigMap and rollback contract as the other methods.

## Freeze and patch (evaluation only)

No version floor. This method scales the operator to zero and patches the
ProxyGroup's StatefulSet directly — the one thing the other methods avoid —
which works only because nothing reconciles while the operator is down. That
is also why it is evaluation only: while the operator is scaled to zero,
ProxyGroups, Services and Ingresses stop reacting to changes and the proxy
pods run unmanaged. Use it to try the sidecar on a test cluster:

```bash
kubectl apply -f examples/kubernetes/proxyclass.yaml
kubectl apply -f examples/kubernetes/secret.yaml
kubectl apply -f examples/kubernetes/configmap.yaml
examples/kubernetes/freeze-and-patch.sh ingress
kubectl rollout status statefulset ingress -n tailscale
kubectl get pod ingress-0 -n tailscale -o jsonpath='{.spec.containers[*].name}'
# tailscale tail-watch
```

The script prints its own undo: scale the operator back to 1. The next
reconcile overwrites the whole StatefulSet spec, which removes the sidecar
(see [Roll back](#roll-back)).

## What it costs

Each replica probes every configured Service, so check load scales with
replicas × Services ÷ interval. The default interval is 30s, and the requests
and limits in
[`examples/kubernetes/sidecar.yaml`](../../examples/kubernetes/sidecar.yaml)
bound what opting in costs per pod. Only the ProxyGroups that use the opt-in
ProxyClass pay it. The sidecar does not cover hop 1; see [what the sidecar
cannot see](../explanation/proxygroup-sidecar.md#what-the-sidecar-cannot-see).

## Roll back

Remove the label from the ProxyClass (or point the ProxyGroup at another
ProxyClass) and delete the policy you applied. Existing pods keep the
sidecar until they are recreated; new pods are admitted unchanged:

```bash
kubectl patch proxygroup ingress --type merge \
  -p '{"spec":{"proxyClass":null}}'
kubectl delete -f examples/kubernetes/mutatingadmissionpolicy.yaml
kubectl rollout restart statefulset ingress -n tailscale
```

For freeze-and-patch, scaling the operator back to 1 both unfreezes it and
removes the sidecar at the next reconcile:

```bash
kubectl scale deployment operator -n tailscale --replicas=1
```
