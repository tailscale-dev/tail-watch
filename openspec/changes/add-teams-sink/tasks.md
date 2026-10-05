# Tasks

## 1. Config (`cmd/tail-watch/config_sinks.go`)

- [x] 1.1 Accept `type: teams` in `cmd/tail-watch/config_sinks.go`: require exactly one of `url_env` or `url_file`, default `group_wait` to `10s`, allow `status_url` and the shared routing and queue keys, and reject `batch`, `max_batch`, `secret_env`, `secret_file`, `headers_env` and `mention_on_page`. Change the unknown-type error to `must be webhook, slack or teams`. Verify new rows in `cmd/tail-watch/config_sinks_test.go` for a valid Teams sink, `batch: true`, `mention_on_page`, both URL keys, and that the unknown-type row still fails.
- [x] 1.2 Verify `describeSink` in `cmd/tail-watch/config_sinks.go` prints `type=teams` and never prints the URL: extend the `check-config` test in `cmd/tail-watch/config_sinks_test.go` with a Teams sink whose `url_env` value must be absent from stdout.

## 2. Card (`cmd/tail-watch/teams.go`)

- [x] 2.1 Render the Workflows envelope and card in `cmd/tail-watch/teams.go` as design D2 and D3: Adaptive Card 1.4, headline colors `Attention`, `Good` and `Warning`, detail lines, `(learned)`, at most 10 dependent names, `teamsEscape` on error, check, address, site, parent and collector name, 300-byte error truncation, and a `status_url` markdown link. Do not implement `mentionRenderer`. Verify `TestTeamsGolden` golden files in `cmd/tail-watch/testdata/teams/` for down at layer `service`, recovered, an implicit router with 14 dependents, flapping, the phishing-error escape, a 301-byte error, a status link to `https://monitor-nyc.example.ts.net:9109/#nyc-nas`, and a route mention `<at>Jane</at>` that is absent. Regenerate with `go test ./cmd/tail-watch -run 'TestTeamsGolden' -update`.

## 3. Delivery (`cmd/tail-watch/teams.go`, `cmd/tail-watch/sink.go`, `cmd/tail-watch/notify.go`, `cmd/tail-watch/notifytest.go`)

- [x] 3.1 Construct the sink from `newSinkWithEnv` in `cmd/tail-watch/sink.go` and send through `httpPost` with `Content-Type: application/json`, `User-Agent: tail-watch/<version>`, and no `Tail-Watch-Signature`. Rename the one-second floor to `messageMinInterval` and use it from `cmd/tail-watch/slack.go` and `cmd/tail-watch/teams.go`. Verify an `httptest` test in `cmd/tail-watch/teams_test.go` that a `400` is not retried and counts `tailwatch_notify_dropped_total{reason="permanent"}`, a `429` with `Retry-After: 2` waits at least 2 seconds and resends the same body, and a failed POST whose URL contains `sig=sekret` omits `sekret` from the error.
- [x] 3.2 Add `SinkConfig.groupsMessages` and use it from `worker.grouped`, `worker.groupCap` and `sendTest` in `cmd/tail-watch/notify.go` and `cmd/tail-watch/notifytest.go`. Teams and Slack cap a group at `queue_size`; a Teams `notify-test` uses a `bat_` id. Verify under `tstest.Clock` in `cmd/tail-watch/teams_test.go` that 25 events inside `group_wait` become one card with 10 events and "and 15 more", and that two immediate `200`s are at least one second apart. Verify the existing Slack grouping test still passes.
- [x] 3.3 Extend `redactSecrets` in `cmd/tail-watch/live_tailnet_test.go` so a `logic.azure.com` workflow URL and a `sig=` query value become `redacted`. Verify a table test next to `TestRedactURL` in `cmd/tail-watch/config_sinks_test.go`.

- [x] 3.4 Add `SinkConfig.messageSink` (Slack or Teams) and use it from `groupsMessages` and `worker.groupCap`. Verify `TestSinkGroupsMessages` covers both helpers and `TestNotifyTestBatchedWebhookUsesBatchID` sees a `bat_` `Tail-Watch-Batch-Id` from a batched webhook `notify-test`.
- [x] 3.5 Add the opt-in `TestLiveTeams` in `cmd/tail-watch/live_teams_test.go` and a Teams sink in `TestLiveNotifications`, both reading `TEAMS_WEBHOOK_URL` or `TAIL_WATCH_TEAMS_PULUMI_ENV` through `liveSinkURL`. Extend `redactSecrets` to `powerplatform.com` hosts. Document both in `docs/contributing/live-notification-test.md`. Verify `TestLiveTeams` passes against a local receiver that answers `202`.
- [x] 3.6 Add `TestTeamsCardRender` in `cmd/tail-watch/teams_render_test.go`: vendor `adaptivecards` 3.0.6, `markdown-it` 15.0.2 and the `adaptivecards-designer` 2.4.4 Teams light host config, with their MIT licenses, under `cmd/tail-watch/testdata/teams/render/`, and render every golden card in Chromium through `browserPage`. Join detail lines with `\n\n` in `teamsDetail`. Verify the test fails on the single-`\n` goldens and passes after regenerating them.

## 4. Docs

- [x] 4.1 Add `docs/how-to/teams-sink.md` for the stock "When a Teams webhook request is received" template: paste the URL into `url_env` or `url_file`, the `type: teams` snippet, `notify-test`, co-owners, the Workflows bot footer, and that route mentions are not printed. Link it from `docs/README.md`, `docs/reference/config.md`, `docs/explanation/incidents-and-queues.md` and `docs/how-to/troubleshoot.md`. Verify `markdownlint-cli2` passes on those files.
- [x] 4.2 Update `docs/reference/config.md`: `type` includes `teams`, `group_wait` groups Teams the way it groups Slack, and a Teams-only section lists `status_url` and the rejected keys. Update the sink list in `CLAUDE.md` to name `teams.go`. Verify `markdownlint-cli2` passes on the config page.
- [x] 4.3 Add a Teams subsection after "Slack" in `tailnet-reachability-monitor-design.md` and one sentence that a Workflows webhook sits beside the Slack incoming webhook. Leave the historical "two sink types" sentence and the phase 2 list (PagerDuty, Slack bot) unchanged. Add an `ops-teams` sink to `examples/tail-watch.yaml`. Verify `markdownlint-cli2` passes on the design doc and `mise exec -- go run ./cmd/tail-watch check-config --config examples/tail-watch.yaml` exits 0.

## 5. Validation

- [x] 5.1 Run `mise run build`, `mise exec -- go vet ./...` and `mise run test`. Verify all three succeed.
- [x] 5.2 Run `mise run lint` and `mise exec -- go run golang.org/x/vuln/cmd/govulncheck@latest ./...`. Verify zero lint issues and no reachable vulnerabilities.
- [x] 5.3 Run `markdownlint-cli2` on `docs/`, `README.md`, `CLAUDE.md`, `tailnet-reachability-monitor-design.md` and `openspec/changes/add-teams-sink/`, and `mise exec -- openspec validate add-teams-sink --strict`. Verify both pass.
