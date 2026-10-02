package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestKindProxyGroup runs the ProxyGroup sidecar recipes from
// docs/how-to/kubernetes-proxygroup.md on a kind cluster with the Tailscale
// operator and an ingress ProxyGroup, then breaks hop 2 while the operator
// keeps the Service advertised and expects the sidecar to notice. It is off
// unless TAIL_WATCH_KIND=1. The harness (cluster, operator, fixtures) comes
// from scripts/kind-stack.sh; see docs/contributing/kind-proxygroup-test.md.
//
//nolint:paralleltest // The subtests mutate one shared cluster in a fixed order.
func TestKindProxyGroup(t *testing.T) {
	if os.Getenv("TAIL_WATCH_KIND") != "1" {
		t.Skip("set TAIL_WATCH_KIND=1 to run the kind ProxyGroup test")
	}
	for _, tool := range []string{"kind", "kubectl", "helm", "docker"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	h := newKindHarness(t)

	t.Run("MutatingAdmissionPolicy", func(t *testing.T) {
		h.runDocBlocks(t, "MutatingAdmissionPolicy")
		h.waitMutationLive(t)
		h.configureSidecar(t)
		h.ensureInjection(t, "ingress", true, 0)
		h.ensureInjection(t, "plain", false, 0)
		if got := h.stsContainers(t); !slices.Equal(got, []string{"tailscale"}) {
			t.Fatalf("StatefulSet template containers %v, want [tailscale]", got)
		}
		h.assertSidecarLive(t)

		t.Run("NetworkPolicy break", func(t *testing.T) {
			since := len(h.receiverEvents(t))
			h.applyStdin(t, networkPolicyDenyHop2)
			t.Cleanup(func() { h.kubectlDelete(t, "networkpolicy", "deny-hop2", "-n", "apps") })
			h.waitEvent(t, since, "target.down")
			h.assertStillAdvertising(t)
			h.kubectlDelete(t, "networkpolicy", "deny-hop2", "-n", "apps")
			h.waitEvent(t, since, "target.recovered")
		})
		t.Run("readiness probe break", func(t *testing.T) {
			since := len(h.receiverEvents(t))
			h.patchWorkloadWrongPort(t)
			restored := false
			t.Cleanup(func() {
				if !restored {
					h.restoreWorkload(t)
				}
			})
			h.waitEvent(t, since, "target.down")
			h.assertStillAdvertising(t)
			h.restoreWorkload(t)
			restored = true
			h.waitEvent(t, since, "target.recovered")
		})
		t.Run("sidecar exit keeps the Service advertised", func(t *testing.T) {
			h.kubectlDelete(t, "configmap", "tail-watch-ingress", "-n", "tailscale")
			h.kubectl(t, "delete", "pod", "ingress-0", "-n", "tailscale", "--wait=false")
			h.waitPodContainers(t, "ingress-0", "tail-watch")
			// The sidecar exits on the missing config and crash-loops; the
			// pod goes NotReady. Advertising ignores pod readiness.
			h.waitFor(t, 3*time.Minute, "sidecar crash-looping", func() bool {
				out := h.kubectl(
					t,
					"get",
					"pod",
					"ingress-0",
					"-n",
					"tailscale",
					"-o",
					"jsonpath={.status.containerStatuses[?(@.name=='tail-watch')].restartCount}",
				)
				return out != "" && out != "0"
			})
			h.assertStillAdvertising(t)
			out := h.kubectl(t, "get", "proxygroup", "ingress",
				"-o", "jsonpath={.status.conditions[?(@.type=='ProxyGroupReady')].status}")
			rollout := h.kubectlErr(
				"rollout",
				"status",
				"statefulset",
				"ingress",
				"-n",
				"tailscale",
				"--timeout=20s",
			)
			t.Logf(
				"with the sidecar crash-looping: ProxyGroupReady=%s, rollout status err=%v",
				out,
				rollout,
			)
			h.configureSidecar(t)
			h.kubectl(
				t,
				"rollout",
				"status",
				"statefulset",
				"ingress",
				"-n",
				"tailscale",
				"--timeout=180s",
			)
		})

		h.rollbackMethod(t, "mutatingadmissionpolicy.yaml")
	})

	t.Run("Kyverno", func(t *testing.T) {
		h.runDocBlocks(t, "Kyverno")
		h.waitMutationLive(t)
		h.configureSidecar(t)
		h.ensureInjection(t, "ingress", true, 0)
		h.ensureInjection(t, "plain", false, 0)
		if got := h.stsContainers(t); !slices.Equal(got, []string{"tailscale"}) {
			t.Fatalf("StatefulSet template containers %v, want [tailscale]", got)
		}

		t.Run("injector outage admits pods without the sidecar", func(t *testing.T) {
			deps := h.kubectl(t, "get", "deploy", "-n", "kyverno",
				"-o", "jsonpath={.items[*].metadata.name}")
			h.scaleKyverno(t, deps, 0)
			t.Cleanup(func() { h.scaleKyverno(t, deps, 1) })
			h.kubectl(
				t,
				"delete",
				"pod",
				"ingress-0",
				"-n",
				"tailscale",
				"--wait=true",
				"--timeout=180s",
			)
			h.waitFor(t, 3*time.Minute, "pod recreated without the sidecar", func() bool {
				out := h.kubectl(t, "get", "pod", "ingress-0", "-n", "tailscale",
					"-o", "jsonpath={.spec.containers[*].name}")
				return strings.TrimSpace(out) == "tailscale"
			})
			h.scaleKyverno(t, deps, 1)
			// The webhook must be serving before the pods are recreated, or
			// admission falls back to Ignore and they start without the
			// sidecar. Available is not enough: the webhook configuration is
			// reconciled after the controller is up, so prove the mutation is
			// live with a probe pod first.
			h.waitMutationLive(t)
			h.kubectl(t, "rollout", "restart", "statefulset", "ingress", "-n", "tailscale")
			h.ensureInjection(t, "ingress", true, 3)
		})

		h.rollbackMethod(t, "kyverno-clusterpolicy.yaml")
	})

	t.Run("Freeze and patch (evaluation only)", func(t *testing.T) {
		h.runDocBlocks(t, "Freeze and patch (evaluation only)")
		h.configureSidecar(t)
		h.ensureInjection(t, "ingress", true, 0)
		h.ensureInjection(t, "plain", false, 0)
		// This method is the exception: the sidecar is in the StatefulSet
		// until the operator reconciles again.
		if got := h.stsContainers(t); !slices.Contains(got, "tail-watch") {
			t.Fatalf("StatefulSet template containers %v, want the sidecar present", got)
		}

		h.kubectl(t, "scale", "deployment", "operator", "-n", "tailscale", "--replicas=1")
		h.kubectl(
			t,
			"-n",
			"tailscale",
			"wait",
			"--for=condition=Available",
			"deploy/operator",
			"--timeout=180s",
		)
		h.waitFor(t, 3*time.Minute, "operator reconcile removes the sidecar", func() bool {
			return slices.Equal(h.stsContainers(t), []string{"tailscale"})
		})
	})
}

const networkPolicyDenyHop2 = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-hop2
  namespace: apps
spec:
  podSelector:
    matchLabels:
      app: web
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: apps
`

// sidecarConfig is examples/kubernetes/configmap.yaml with the timing turned
// up so detection fits a test: the mechanics (local-only, tcp check, webhook
// sink) are the documented ones.
const sidecarConfig = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: tail-watch-ingress
  namespace: tailscale
data:
  tail-watch.yaml: |
    collector:
      local_only: true
    defaults:
      interval: 5s
      timeout: 2s
      fail_threshold: 1
      recover_threshold: 1
    targets:
      - name: web
        addr: web.apps.svc.cluster.local
        site: apps
        checks: [tcp:80]
    sinks:
      - name: ops-webhook
        type: webhook
        url_env: OPS_WEBHOOK_URL
        secret_env: OPS_WEBHOOK_SECRET
        group_wait: 2s
`

// kindHarness drives kubectl and the doc blocks against the kind cluster.
type kindHarness struct {
	kubeconfig string
	root       string
}

func newKindHarness(t *testing.T) *kindHarness {
	t.Helper()
	cluster := os.Getenv("TAIL_WATCH_KIND_CLUSTER")
	if cluster == "" {
		cluster = "tail-watch-proxygroup"
	}
	root := repoRoot(t)
	up := func() {
		t.Log("bringing the harness up with scripts/kind-stack.sh up")
		cmd := exec.Command(filepath.Join(root, "scripts", "kind-stack.sh"), "up")
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("kind-stack.sh up: %v\n%s", err, out)
		}
	}
	clusters, err := exec.Command("kind", "get", "clusters").Output()
	if err != nil {
		t.Fatalf("kind get clusters: %v", err)
	}
	running := false
	for line := range strings.Lines(string(clusters)) {
		if strings.TrimSpace(line) == cluster {
			running = true
		}
	}
	if !running {
		up()
	} else if pgOut, pgErr := exec.Command(
		"kubectl", "--context", "kind-"+cluster, "get", "proxygroup", "ingress",
	).CombinedOutput(); pgErr != nil {
		t.Logf("proxygroup ingress not found (%v: %s)", pgErr, pgOut)
		up()
	}
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	kc, kcErr := exec.Command("kind", "get", "kubeconfig", "--name", cluster).Output()
	if kcErr != nil {
		t.Fatalf("kind get kubeconfig: %v", kcErr)
	}
	if writeErr := os.WriteFile(kubeconfig, kc, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	return &kindHarness{kubeconfig: kubeconfig, root: root}
}

func (h *kindHarness) command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = h.root
	cmd.Env = append(os.Environ(), "KUBECONFIG="+h.kubeconfig)
	return cmd
}

func (h *kindHarness) kubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := h.command("kubectl", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (h *kindHarness) kubectlErr(args ...string) error {
	out, err := h.command("kubectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func (h *kindHarness) kubectlDelete(t *testing.T, args ...string) {
	t.Helper()
	out, err := h.command("kubectl", append([]string{"delete", "--ignore-not-found"}, args...)...).
		CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl delete %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (h *kindHarness) applyStdin(t *testing.T, manifest string) {
	t.Helper()
	cmd := h.command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl apply: %v\n%s", err, out)
	}
}

// runDocBlocks extracts the fenced bash blocks under the given "## " heading
// of docs/how-to/kubernetes-proxygroup.md, in the style of systemdDocBlocks,
// and runs each one. The how-to is what the test executes, so the docs cannot
// drift from what works.
func (h *kindHarness) runDocBlocks(t *testing.T, heading string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.root, "docs", "how-to", "kubernetes-proxygroup.md"))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	var cur strings.Builder
	inSection, inBlock := false, false
	for line := range strings.Lines(string(raw)) {
		if strings.HasPrefix(line, "## ") {
			inSection = strings.HasPrefix(line, "## "+heading)
		}
		if !inSection {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```bash"):
			inBlock = true
			cur.Reset()
		case inBlock && strings.HasPrefix(trimmed, "```"):
			inBlock = false
			blocks = append(blocks, cur.String())
		case inBlock:
			cur.WriteString(line)
		}
	}
	if len(blocks) == 0 {
		t.Fatalf("docs/how-to/kubernetes-proxygroup.md has no bash blocks under ## %s", heading)
	}
	for i, block := range blocks {
		t.Logf("running %s block %d", heading, i+1)
		cmd := h.command("bash", "-euo", "pipefail", "-s")
		cmd.Stdin = strings.NewReader(block)
		blockOut, blockErr := cmd.CombinedOutput()
		if blockErr != nil {
			t.Fatalf("%s block %d: %v\n%s", heading, i+1, blockErr, blockOut)
		}
	}
}

// configureSidecar points the sink Secret at the in-cluster receiver (the doc
// blocks apply the placeholder), applies the fast-timing config, and restarts
// the proxy pods so the sidecar picks both up.
func (h *kindHarness) configureSidecar(t *testing.T) {
	t.Helper()
	render := h.command(
		"kubectl",
		"-n",
		"tailscale",
		"create",
		"secret",
		"generic",
		"tail-watch-ingress",
		"--from-literal=OPS_WEBHOOK_URL=http://alert-receiver.apps.svc.cluster.local:8080/events",
		"--from-literal=OPS_WEBHOOK_SECRET=kind-test-hmac-key",
		"--dry-run=client",
		"-o",
		"yaml",
	)
	rendered, err := render.Output()
	if err != nil {
		t.Fatalf("render sink secret: %v", err)
	}
	apply := h.command("kubectl", "apply", "-f", "-")
	apply.Stdin = bytes.NewReader(rendered)
	if applied, applyErr := apply.CombinedOutput(); applyErr != nil {
		t.Fatalf("apply sink secret: %v\n%s", applyErr, applied)
	}
	h.applyStdin(t, sidecarConfig)
	h.restartIngress(t)
}

func (h *kindHarness) restartIngress(t *testing.T) {
	t.Helper()
	h.kubectl(t, "rollout", "restart", "statefulset", "ingress", "-n", "tailscale")
	h.kubectl(t, "rollout", "status", "statefulset", "ingress", "-n", "tailscale", "--timeout=300s")
}

// scaleKyverno scales every Kyverno deployment and, when bringing them back,
// waits for each to be Available.
func (h *kindHarness) scaleKyverno(t *testing.T, deps string, replicas int) {
	t.Helper()
	for d := range strings.FieldsSeq(deps) {
		h.kubectl(t, "scale", "deploy", d, "-n", "kyverno", fmt.Sprintf("--replicas=%d", replicas))
	}
	if replicas == 0 {
		return
	}
	for d := range strings.FieldsSeq(deps) {
		h.kubectl(t, "-n", "kyverno", "wait", "--for=condition=Available", "deploy/"+d, "--timeout=300s")
	}
}

// ensureInjection waits for every pod of the ProxyGroup to reach the wanted
// injection state. A Ready pod that came back wrong raced the injector, so it
// is deleted (up to maxDelete times) and the StatefulSet recreates it through
// the now-live injector, which is how the cluster converges in practice.
func (h *kindHarness) ensureInjection(t *testing.T, pg string, want bool, maxDelete int) {
	t.Helper()
	deleted := 0
	h.waitFor(t, 5*time.Minute, fmt.Sprintf("pods of %s injected=%v", pg, want), func() bool {
		out := h.kubectl(
			t,
			"get",
			"pods",
			"-n",
			"tailscale",
			"-l",
			"tailscale.com/parent-resource="+pg,
			"-o",
			`jsonpath={range .items[*]}{.metadata.name} {.status.conditions[?(@.type=="Ready")].status} {.spec.containers[*].name}{"\n"}{end}`,
		)
		pods := strings.Split(strings.TrimSpace(out), "\n")
		if len(pods) == 0 || (len(pods) == 1 && strings.TrimSpace(pods[0]) == "") {
			return false
		}
		for _, line := range pods {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				return false
			}
			name, ready, containers := fields[0], fields[1], fields[2:]
			if slices.Contains(containers, "tail-watch") == want {
				continue
			}
			if ready != "True" || deleted >= maxDelete {
				return false
			}
			deleted++
			t.Logf("pod %s recreated injected=%v, deleting it again", name, !want)
			h.kubectl(t, "delete", "pod", name, "-n", "tailscale", "--wait=false")
			return false
		}
		return true
	})
}

// rollbackMethod removes one method's injection and returns the ProxyGroup to
// its operator-managed state for the next method.
func (h *kindHarness) rollbackMethod(t *testing.T, manifest string) {
	t.Helper()
	h.kubectlDelete(t, "-f", filepath.Join(h.root, "examples", "kubernetes", manifest))
	h.kubectl(t, "patch", "proxygroup", "ingress", "--type", "merge",
		"-p", `{"spec":{"proxyClass":null}}`)
	h.restartIngress(t)
	h.ensureInjection(t, "ingress", false, 0)
}

func (h *kindHarness) stsContainers(t *testing.T) []string {
	t.Helper()
	out := h.kubectl(t, "get", "statefulset", "ingress", "-n", "tailscale",
		"-o", "jsonpath={.spec.template.spec.containers[*].name}")
	return strings.Fields(out)
}

// assertSidecarLive waits for the sidecar's collector.started at the
// receiver, which proves it runs and its sink reaches the receiver.
func (h *kindHarness) assertSidecarLive(t *testing.T) {
	t.Helper()
	h.waitFor(t, 3*time.Minute, "collector.started at the receiver", func() bool {
		return strings.Contains(h.receiverEvents(t), `"type":"collector.started"`)
	})
}

func (h *kindHarness) waitPodContainers(t *testing.T, pod, container string) {
	t.Helper()
	h.waitFor(t, 3*time.Minute, pod+" recreated with "+container, func() bool {
		out := h.kubectl(t, "get", "pod", pod, "-n", "tailscale",
			"-o", "jsonpath={.spec.containers[*].name}")
		return slices.Contains(strings.Fields(out), container)
	})
}

// mutationProbePod carries the opt-in labels so the admission chain under
// test mutates it exactly like a pod of a ProxyGroup named probe.
const mutationProbePod = `
apiVersion: v1
kind: Pod
metadata:
  name: mutation-probe
  namespace: tailscale
  labels:
    tailscale.com/parent-resource-type: proxygroup
    tailscale.com/parent-resource: probe
    tail-watch/inject: "true"
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.10
`

// waitMutationLive recreates a probe pod until the admission chain adds the
// sidecar to it, proving the injector is serving before the ProxyGroup pods
// are recreated. A pod created while the injector is still registering its
// webhook is admitted unchanged and never re-mutated. The probe's ConfigMap
// and Secret must be named after its own ProxyGroup, not ingress.
func (h *kindHarness) waitMutationLive(t *testing.T) {
	t.Helper()
	h.waitFor(t, 3*time.Minute, "admission mutation live", func() bool {
		h.kubectlDelete(t, "pod", "mutation-probe", "-n", "tailscale")
		h.applyStdin(t, mutationProbePod)
		out := h.kubectl(t, "get", "pod", "mutation-probe", "-n", "tailscale",
			"-o", "jsonpath={.spec.containers[*].name}")
		return slices.Contains(strings.Fields(out), "tail-watch")
	})
	names := h.kubectl(t, "get", "pod", "mutation-probe", "-n", "tailscale", "-o",
		"jsonpath={.spec.volumes[?(@.name=='tail-watch-config')].configMap.name} "+
			"{.spec.containers[?(@.name=='tail-watch')].envFrom[0].secretRef.name}")
	if names != "tail-watch-probe tail-watch-probe" {
		t.Fatalf("probe pod ConfigMap and Secret names %q, want tail-watch-probe for both", names)
	}
	h.kubectlDelete(t, "pod", "mutation-probe", "-n", "tailscale")
}

// assertStillAdvertising checks the two signals that stay green while hop 2
// is broken: the workload has Ready endpoints and the operator reports the
// Service advertised. Terminating endpoints, left over from a workload that
// was just recreated, are not part of the claim; an empty slice fails it.
func (h *kindHarness) assertStillAdvertising(t *testing.T) {
	t.Helper()
	conds := h.kubectl(t, "get", "endpointslice", "-n", "apps",
		"-l", "kubernetes.io/service-name=web", "-o",
		`jsonpath={range .items[*].endpoints[*]}{.conditions.ready} {.conditions.terminating}{"\n"}{end}`)
	ready := 0
	for line := range strings.Lines(conds) {
		fields := strings.Fields(line)
		if len(fields) == 0 || (len(fields) > 1 && fields[1] == "true") {
			continue
		}
		if fields[0] != "true" {
			t.Fatalf("workload endpoint not Ready while hop 2 is broken:\n%s", conds)
		}
		ready++
	}
	if ready == 0 {
		t.Fatalf("workload has no Ready endpoints, so hop 2 is not what is broken:\n%s", conds)
	}
	status := h.kubectl(t, "get", "svc", "web-ts", "-n", "apps",
		"-o", "jsonpath={.status.conditions[?(@.type=='TailscaleIngressSvcConfigured')].status}")
	msg := h.kubectl(t, "get", "svc", "web-ts", "-n", "apps",
		"-o", "jsonpath={.status.conditions[?(@.type=='TailscaleIngressSvcConfigured')].message}")
	if status != "True" || !strings.HasSuffix(msg, "proxy backends ready and advertising") {
		t.Fatalf("operator condition status=%s message=%q", status, msg)
	}
}

// patchWorkloadWrongPort runs the workload on 8080 with a readiness probe on
// 8080 while the Service's targetPort stays 80: the endpoints stay Ready and
// nothing listens where the Service points.
func (h *kindHarness) patchWorkloadWrongPort(t *testing.T) {
	t.Helper()
	h.kubectl(t, "patch", "deploy", "web", "-n", "apps", "--type", "strategic", "-p", `{
  "spec": {"template": {"spec": {"containers": [{
    "name": "web",
    "image": "python:3.13-alpine",
    "command": ["python", "-m", "http.server", "8080"],
    "ports": [{"containerPort": 8080}],
    "readinessProbe": {"tcpSocket": {"port": 8080}}
  }]}}}
}`)
	h.kubectl(t, "-n", "apps", "rollout", "status", "deploy/web", "--timeout=600s")
}

// restoreWorkload recreates the documented workload. A kubectl apply is not
// enough: the strategic-merge patch added command and readinessProbe fields
// the example file does not own, and apply would leave them behind (an nginx
// image with a python command crash-loops).
func (h *kindHarness) restoreWorkload(t *testing.T) {
	t.Helper()
	h.kubectlDelete(t, "deploy", "web", "-n", "apps")
	h.kubectl(
		t,
		"apply",
		"-f",
		filepath.Join(h.root, "cmd", "tail-watch", "testdata", "kind", "workload.yaml"),
	)
	h.kubectl(t, "-n", "apps", "rollout", "status", "deploy/web", "--timeout=600s")
}

// receiverEvents port-forwards to the in-cluster receiver and returns every
// event body it has collected.
func (h *kindHarness) receiverEvents(t *testing.T) string {
	t.Helper()
	port := freePort(t)
	cmd := h.command("kubectl", "-n", "apps", "port-forward", "svc/alert-receiver",
		fmt.Sprintf("%d:8080", port))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("port-forward: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	client := &http.Client{Timeout: 2 * time.Second}
	var body string
	for range 40 {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/events", port))
		if err == nil {
			b, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK {
				body = string(b)
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return body
}

// waitEvent waits until the receiver holds an event of eventType beyond the
// first since bytes it already held, so an event from an earlier subtest does
// not satisfy a later one.
func (h *kindHarness) waitEvent(t *testing.T, since int, eventType string) {
	t.Helper()
	h.waitFor(t, 3*time.Minute, eventType+" at the receiver", func() bool {
		events := h.receiverEvents(t)
		if len(events) < since {
			return false
		}
		return strings.Contains(events[since:], `"type":"`+eventType+`"`)
	})
}

func (h *kindHarness) waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for %s", what)
}
