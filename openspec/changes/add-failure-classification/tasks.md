# Tasks

## 1. Peer presence (`cmd/tsmon/presence.go`)

- [ ] 1.1 Define `peerInfo` (NodeID, Name, DNSName, HostName, IPs, Routes, AllowedIPs, Tags, `Online opt.Bool`) and the `presence` view with `Online`, `ByIP`, `Route` (longest prefix), `ByName`, `Peers`, guarded by an RWMutex; verify table tests in `presence_test.go` for owner-IP lookup, longest-prefix route lookup, name lookup precedence (DNSName, short name, HostName) and no-match
- [ ] 1.2 Implement `apply(ipn.Notify)`: seed from `InitialStatus.Peer` using `PeerStatus.NodeID`, apply `PeerChangedPatch` Online flips, upsert `PeersChanged` (nil `Online` → unknown), delete `PeersRemoved`, mark unseeded on non-`Running` `State`; verify `presence_test.go` feeds synthetic `ipn.Notify` sequences covering seed, patch flip, full replace with new routes, removal, and state leaving Running
- [ ] 1.3 Add a guard test asserting the exact `tailcfg.PeerChange` field set (NodeID, DERPRegion, Cap, CapMap, Endpoints, Key, KeySignature, DiscoKey, Online, LastSeen, KeyExpiry) via reflection, so a tailscale.com upgrade that adds fields fails loudly; verify it passes on v1.102.5
- [ ] 1.4 Implement `presence.run(ctx)` with `WatchIPNBus(ipn.NotifyInitialStatus|ipn.NotifyPeerPatches)`, clear-on-reconnect, unknown-while-disconnected, and `util/backoff` capped at 30s, logging each disconnect once; verify a test driving `run` through a fake watcher source that errors then reseeds, asserting `known=false` in between
- [ ] 1.5 Implement `presence.TagsFor(target string) []string` using a handler-resolver hook installed by the classifier (Tailscale-node target → own node's tags; LAN target → handling node's tags; unknown → empty); verify table tests for node target, LAN target inheriting router tags, implicit target, and unknown handler
- [ ] 1.6 Switch `tsping` name resolution in `cmd/tsmon/checks.go` to `presence.ByName`, keeping the `Status()` cache only as the unseeded fallback; verify the existing resolution tests from change 1 still pass plus one using a seeded presence

## 2. Classification core (`cmd/tsmon/classify.go`)

- [ ] 2.1 Define `layer`, `pathInfo`, `Classified`, `classifiedConsumer`; implement `pathFromPing(*ipnstate.PingResult)` (Endpoint → direct, PeerRelay → peer_relay, DERPRegionCode → derp); verify table tests for direct, DERP, peer relay, and `Err` set
- [ ] 2.2 Implement error classification (`refused`, `timeout`, `http`, `tls`, `other`) with `errors.Is`/`errors.As` over the check error types from change 1; verify a table test with real errors from a refused loopback dial, a context deadline, the HTTP check's status/body error, and an x509 error
- [ ] 2.3 Implement the pure `decide(classifyInput) layer` in the spec's order (unrouted, control, tunnel, service, lan; node targets map lan → service; failed tsping never re-pings); verify an exhaustive table test covering every spec scenario, including presence unknown, offline router, missing handling node, and a missing `NodeIP`
- [ ] 2.4 Implement actual handling-node lookup in `classify.go` via peer identity, learned handler and current primary route, independently of explicit suppression parents; local targets bypass lookup; verify a switch parent does not replace a router handler and no recursion follows mixed graph edges.

## 3. Supplemental probes (`cmd/tsmon/classify.go`)

- [ ] 3.1 Implement `probeCache` keyed by NodeID (disco) and `netip.Addr` (icmp) using `tailscale.com/util/singleflight` and a TTL of `min(10s, interval)` on `tstime.Clock`; verify with `tstest.Clock` that entries expire at the TTL and that 40 concurrent callers trigger exactly one probe func call
- [ ] 3.2 Seed the cache from targets' own passing `icmp`/`tsping` results and run supplemental disco/ICMP probes via change 1's check implementations over `netEnv` with the target's timeout, bypassing prober; verify a test with a counting fake env that a fresh own-check pass prevents a supplemental probe
- [ ] 3.3 Implement `classifier.Observe(Result)`: lazily fill `classifyInput`, call `decide`, attach parent and path, fan out to `classifiedConsumer`s; verify tests for each layer end-to-end with fake presence and a fake netEnv/LocalClient ping source, and that passing results carry an empty layer

## 4. Dependency learning (`cmd/tsmon/classify.go`)

- [ ] 4.1 Implement remote-only learning in `classify.go`: one disco ping per remote LAN target, max eight concurrent, retain learned handler on failure unless current primary-route change supersedes it, explicit suppression override independent of handler; verify startup failure fallback, HA primary change and local-target exclusion.
- [ ] 4.2 Implement reachability-only implicit targets in `classify.go`/`config.go` with stable node identity, unique short-name/ID/suffix allocation and shared site; reuse only configured `[tsping]` targets; verify mixed service router creates a separate parent, name collision and deferred removal while down.
- [ ] 4.3 Implement the missing-grant warning state machine (log once on entering unhandled, silent while it persists, log again only after resolved-then-lost); verify a test across three rounds using a captured slog handler
- [ ] 4.4 Implement `learner.run(ctx)` scheduling (first round after presence seeds or 10s, then every 10m on the clock); verify with `tstest.Clock` that rounds fire at 10s and 10m10s
- [ ] 4.5 Implement `learner.FirstPassDone()` as attempt completion only and separate authoritative reconciliation in `classify.go`; require continuously seeded presence and resolved handlers, retry on reseed/route changes/periodic rounds; test all-timeout first pass retains unresolved incidents, later complete reconciliation can establish removal, and no remote LAN targets completes immediately.
- [ ] 4.6 Wire presence, learner, classifier and a debug-logging `classifiedConsumer` into `cmd/tsmon/tsmon.go`, with `targetRegistry` fanning out to `Prober.Run`/`Probe.Close` and to `alerter.AddTarget`/`RemoveTarget`/`IsDown` (a no-op stub returning false until change 3 lands); verify `go build ./cmd/tsmon` and that `tsmon --once` still prints results for a loopback-only config

## 5. Metrics and docs

- [ ] 5.1 Register `tsmon_failure_layer_total{target,layer}`, `tsmon_path{target,kind}` (all three kinds written each tsping result) and `tsmon_target_parent{target,parent}` (old pair deleted on change) in `cmd/tsmon/classify.go`; verify tests using `prometheus/testutil` on a private registry passed into the classifier
- [ ] 5.2 Document failure layers, learned vs. configured parents, implicit targets, the missing-grant log, and the three metrics in `README.md`; verify markdownlint passes

## 6. Dependency graph integration

- [ ] 6.1 In `classify.go`, bypass tailnet learning/classification for local targets; verify local LAN checks remain independent of a failed Tailscale router and do not inherit its tags.
- [ ] 6.2 In `classify.go`, reuse only configured `[tsping]` parents, create separate reachability targets for service-monitored routers, persist stable identity and allocate collision-safe names; verify HTTP-only and mixed-check router targets cannot suppress LAN devices.
- [ ] 6.3 In `classify.go` and `tsmon.go`, validate the combined dependency graph and apply atomic `UpdateParents`; register/restore before scheduling; verify mixed-edge cycle rejection and HA failover release without a router recovery transition.

## 7. Validation

- [ ] 7.1 Run `go build ./...`, `go vet ./...`, `go test -race ./...`; all pass
- [ ] 7.2 Run `golangci-lint run ./...` and `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`; zero issues and no reachable vulnerabilities
- [ ] 7.3 Run markdownlint on changed Markdown and `openspec validate add-failure-classification --strict`; both pass
- [ ] 7.4 On a real tailnet with a subnet router: unplug a device behind it and confirm layer `lan`; stop tailscaled on the router and confirm the router's target reports `control` (or `tunnel` within the ~15s before control notices) and its dependents classify `control`; record the `tsmon_failure_layer_total` output in the PR description
