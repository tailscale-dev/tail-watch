# Tasks

## 1. Local-only config

- [x] 1.1 Add `local_only` to `CollectorConfig` in `cmd/tail-watch/config.go`: when true,
  a target without `network` defaults to `local`, and validation reports every
  `network: tailnet` target, every `tsping` check, and `collector.ui:
  true`, each naming its field; verify table-driven cases in
  `cmd/tail-watch/config_test.go` cover the default-to-local, each rejection, several
  problems reported together, and `local_only: false` leaving today's behavior unchanged
- [x] 1.2 Make `tail-watch check-config` apply the same rules and reject `--ui` with
  `local_only` at flag resolution in `cmd/tail-watch/main.go`; verify tests in
  `cmd/tail-watch/main_test.go` show `check-config` failing on a local-only config with
  a `tsping` check and `--ui` failing with the local-only message
- [x] 1.3 Document `collector.local_only` in `docs/reference/config.md` (key table,
  defaulting rule, what it rejects) and check the new snippet with
  `tail-watch check-config`; verify markdownlint passes on the file

## 2. Local-only runtime

- [x] 2.1 In `cmd/tail-watch/main.go`, skip the LocalAPI preflight, presence view,
  dependency learner and resolver binding when `local_only` is set; take the collector
  identity from `os.Hostname()` with no Tailscale IP; log hostname and local-only mode;
  verify a test in `cmd/tail-watch/main_test.go` starts the app with a socket path that
  does not exist and gets a scheduled `tcp:` result against an `httptest` listener, and
  that no LocalAPI call is made (fake env counts calls)
- [x] 2.2 Make `--once` work in local-only mode without tailscaled; verify a `--once` test
  with a nonexistent socket exits 0 for a passing local check and 1 for a failing one
- [x] 2.3 Verify the payload identity: a test in `cmd/tail-watch/event_test.go` (or the
  notifier tests) shows `collector.started` from a local-only collector carries
  `collector.name` and omits `collector.tailscale_ip`, with no golden-file change
- [x] 2.4 Update `docs/reference/cli.md` (`--socket` ignored in local-only mode) and
  `docs/how-to/troubleshoot.md` (local-only startup log line and its rejections);
  verify markdownlint passes

## 3. Test tailnet prerequisites

- [x] 3.1 Ask the user to add an operator OAuth client (device, auth key and services
  write scopes) to the `tailscale-phase-2/tailscale` pulumi env; verify
  `pulumi env open` lists the new keys by name only, never printing values
- [x] 3.2 Add the operator's `tagOwners` (`tag:k8s-operator`, `tag:k8s`) and Tailscale
  Service approval to `cmd/tail-watch/testdata/snippet.hujson`, and confirm with the
  user that the test tailnet policy contains them; verify the file still parses with
  the existing snippet test, if any, or `hujson` standardization

## 4. Sidecar recipes and examples

- [x] 4.1 Add `examples/kubernetes/`: `proxyclass.yaml` (opt-in label
  `tail-watch/inject: "true"`), `configmap.yaml` (a local-only config probing a Service
  by cluster DNS name), `secret.yaml` (sink env, placeholders only), and the shared
  container spec as used by all methods (name `tail-watch`, pinned image tag,
  `--listen 127.0.0.1:9109`, requests and limits, uid 65532, read-only root, optional
  ConfigMap and Secret); verify the ConfigMap's config passes `tail-watch check-config`
  and the manifests pass `kubectl apply --dry-run=client`
- [x] 4.2 Add `examples/kubernetes/mutatingadmissionpolicy.yaml` (Pod `CREATE`, both
  labels in `matchConditions`, `failurePolicy: Ignore`) and
  `examples/kubernetes/kyverno-clusterpolicy.yaml` (`patchStrategicMerge`,
  `pod-policies.kyverno.io/autogen-controllers: none`, `failurePolicy: Ignore`), each
  producing the same container and volumes; verify `kubectl apply --dry-run=server`
  against a kind cluster with Kyverno installed
- [x] 4.3 Add `examples/kubernetes/freeze-and-patch.sh` (scale the operator to 0, patch
  the ProxyGroup StatefulSet, with an "evaluation only" header); verify `bash -n` and
  that it refuses to run without the ProxyGroup name
- [x] 4.4 Write `docs/how-to/kubernetes-proxygroup.md`: what hop 2 is and why
  readiness misses it, the opt-in label and ConfigMap contract, the four methods with
  version floors and failure policy, the custom webhook section (AdmissionReview
  contract, JSON patch, `objectSelector`, `sideEffects: None`, TLS, example Go handler),
  cost guidance, the residual DNAT gap, and fenced bash blocks under a fixed heading
  per method for the test to run; verify markdownlint and that every config snippet
  passes `tail-watch check-config`
- [x] 4.5 Link the page from `docs/README.md` and `README.md`, and add the Kubernetes
  placement to `docs/explanation/` where placement is explained; verify markdownlint

## 5. kind harness

- [x] 5.1 Add `cmd/tail-watch/testdata/kind-proxygroup.yaml` (cluster config, enabling
  MutatingAdmissionPolicy when the node image needs it) and testdata manifests for the
  workload, its L4 Service on the ProxyGroup, the ProxyGroup, and an in-cluster webhook
  receiver; verify `kind create cluster --config` succeeds and
  `kubectl api-resources | grep -i mutatingadmission` lists the API
- [x] 5.2 Add `scripts/kind-stack.sh up|down` (cluster, operator chart pinned to the
  `tailscale.com` version in `go.mod`, OAuth client read from the pulumi env, `kind
  load` of a locally built image; `down` deletes the cluster, the Tailscale Service and
  the operator's devices); verify `up` reaches a `Ready` ProxyGroup and `down` leaves no
  devices with the stack's hostname prefix
- [x] 5.3 Determine whether kindnet enforces NetworkPolicy on the node image; if not, add
  Calico to `kind-stack.sh`; verify a deny-all policy blocks a test `curl` between pods
- [x] 5.4 Document the harness in `docs/contributing/kind-proxygroup-test.md`
  (prerequisites, env vars, cleanup); verify markdownlint

## 6. kind live test

- [x] 6.1 Add `TestKindProxyGroup` in `cmd/tail-watch/kind_proxygroup_test.go`, gated on
  `TAIL_WATCH_KIND=1` and skipping when `kind`, `kubectl` or `helm` is missing, with
  doc-block extraction in the style of `systemdDocBlocks`; verify it skips cleanly
  without the gate and `go vet` passes
- [x] 6.2 For each of MutatingAdmissionPolicy, Kyverno and freeze-and-patch, run the
  how-to's blocks, then assert every proxy pod has the `tail-watch` container, the
  StatefulSet does not (except freeze-and-patch), and pods of a ProxyGroup without the
  label are unchanged; verify the test passes for all three
- [x] 6.3 Break hop 2 with a NetworkPolicy denying the operator namespace, assert
  EndpointSlices stay `Ready` and the operator condition reads "N/N proxy backends ready
  and advertising", then assert `target.down` reaches the receiver; remove the policy
  and assert `target.recovered`; verify the test passes
- [x] 6.4 Repeat 6.3 with a readiness probe on a port other than `targetPort` and
  nothing listening on `targetPort`; verify the test passes
- [x] 6.5 Make the sidecar exit (delete its ConfigMap, recreate a pod) and assert the
  Service stays advertised from that pod; record whether the ProxyGroup `Ready`
  condition or a StatefulSet rollout is affected, and state the result in
  `docs/how-to/kubernetes-proxygroup.md`; verify the test passes and the doc names the
  observed behavior
- [x] 6.6 Assert an injector outage leaves proxy pods creatable: scale Kyverno to 0,
  recreate a proxy pod, and assert it starts without the sidecar; verify the test passes

## 7. Follow-up and project notes

- [x] 7.1 Draft the operator issue (sidecar setting on ProxyClass/ProxyGroup, and
  unadvertising a replica whose hop 2 is broken), citing this change's evidence; show
  it to the user and file it only after approval; verify the user approved the text.
  Outcome: drafted and shown; the user decided not to file the issue — the
  admission-mutation methods documented in this change (MutatingAdmissionPolicy,
  Kyverno, custom webhook, freeze-and-patch) are the accepted way to run the sidecar,
  so no operator feature request is needed.
- [x] 7.2 Add the local-only mode, the sidecar contract and the kind test to `CLAUDE.md`
  (Layout and a "kind ProxyGroup test" section); verify markdownlint

## 8. Validation

- [x] 8.1 Run `mise exec -- go build ./...`, `go vet ./...` and `go test ./...`; verify
  all pass
- [x] 8.2 Run `mise run lint` and `govulncheck ./...`; verify no findings
- [x] 8.3 Run `markdownlint-cli2` over the changed docs and
  `openspec/changes/add-proxygroup-sidecar/`; verify zero findings
- [x] 8.4 Run `mise exec -- openspec validate add-proxygroup-sidecar --strict`; verify
  it passes
- [x] 8.5 Run `TAIL_WATCH_KIND=1 go test -run TestKindProxyGroup ./cmd/tail-watch`
  end to end and then `scripts/kind-stack.sh down`; verify it passes and the test
  tailnet has no leftover devices or Tailscale Service
