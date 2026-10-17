# Live notification test

This page is for contributors who need to run the notification path against a
real tailnet.

`TestLiveNotifications` runs the whole notification path on a real tailnet. It is off
unless `TAIL_WATCH_LIVE_TAILNET=1`. It starts the collector and one fixture node per
check (`icmp`, `tsping`, `tcp`, `http`, and a LAN target behind a subnet router), fails and
heals each one, restarts the collector mid-outage with `queue: disk`, and runs
`notify-test`. A signature-verifying webhook receiver in the test process checks every
event and heartbeat. The compose fixtures are in
`cmd/tail-watch/testdata/compose.live-notify.yaml`. Devices are ephemeral, named
`tail-watch-live-notify-*`, and deleted afterwards.

```bash
TAIL_WATCH_LIVE_TAILNET=1 TAIL_WATCH_LIVE_TAG= \
  TAIL_WATCH_SLACK_PULUMI_ENV=<org>/tail-watch-live/slack \
  go test -count=1 -timeout 18m -run TestLiveNotifications -v ./cmd/tail-watch
```

## Tailnet policy and tags

With `TAIL_WATCH_LIVE_TAG` unset the collector joins as `tag:monitor`, so the tailnet
policy must own that tag and grant it the fixtures: merge
`cmd/tail-watch/testdata/snippet.hujson`. The grants that matter are `tcp:9000` and
`tcp:8080` to members (the `tcp` and `http` fixtures) and `icmp` to `10.10.0.0/16` (the LAN
target). Without the two TCP grants those fixtures are down from the start, with
`i/o timeout` errors because the packets are dropped. Setting `TAIL_WATCH_LIVE_TAG=` to
empty runs the collector as an untagged member node, which needs no policy change.

## Credentials

Tailnet credentials come from the `tailscale-phase-2/tailscale` pulumi env
(`TAIL_WATCH_PULUMI_ENV` overrides it). Slack is optional: without a webhook the test
runs the webhook sink only. Slack is one-way, so the test checks tail-watch's own sent and
failed counters for that sink; look in the channel for the messages.
`TAIL_WATCH_LIVE_MENTION` sets `mention_on_page` for the Slack sink.

The test reads the webhook from `SLACK_WEBHOOK_URL`, or from the pulumi env named by
`TAIL_WATCH_SLACK_PULUMI_ENV`. Never write it to a file or a log. To get a webhook URL,
see the [Slack sink how-to](../how-to/slack-sink.md).

The webhook can live in a pulumi env such as `<org>/tail-watch-live/slack`; pass its name
as `TAIL_WATCH_SLACK_PULUMI_ENV`. To rotate it, run:

```bash
pulumi env set <env> environmentVariables.SLACK_WEBHOOK_URL '<new url>' --secret
```

## Teams

Teams is optional in the same way. `TestLiveNotifications` adds an `ops-teams` sink when
`TEAMS_WEBHOOK_URL` is set or `TAIL_WATCH_TEAMS_PULUMI_ENV` names a pulumi env with
`environmentVariables.TEAMS_WEBHOOK_URL`, and checks its counters as it does for Slack.
To get a URL, see [Send alerts to Microsoft Teams](../how-to/teams-sink.md).

To check only the Teams cards, with no tailnet or Docker, run `TestLiveTeams`:

```bash
TAIL_WATCH_LIVE_TEAMS=1 TAIL_WATCH_TEAMS_PULUMI_ENV=<org>/tail-watch-live/teams \
  go test -count=1 -run TestLiveTeams -v ./cmd/tail-watch
```

It sends three cards: the `notify-test` card, one grouped card with a down, a learned router
with 14 dependents, an escaped error, a cut error, a flap and a recovery, and a 12-event
card that ends in "and 2 more". It needs its own gate because operators export
`TEAMS_WEBHOOK_URL` to run the collector, and a plain test run must not post to their
channel. The workflow only answers `202`, so look in the channel to check that the cards
render. To store the URL, run:

```bash
pulumi env set <env> environmentVariables.TEAMS_WEBHOOK_URL '<workflow url>' --secret
```

The URL's `sig` query is a bearer secret. The test logs scrub `logic.azure.com` and
`powerplatform.com` URLs and any `sig=` value.

## Setec

`TestLiveSetec` runs the collector with every sink and heartbeat secret in a setec
server, under the same `TAIL_WATCH_LIVE_TAILNET=1` gate. The server runs in `--dev`
mode (a dummy KMS, not for production) as its own tsnet node, built by
`scripts/build-setec.sh` at the version in `go.mod`. The test seeds three secrets from
the collector's network namespace, checks signed webhook posts, a heartbeat and
`notify-test`, then rotates the heartbeat URL and waits up to three minutes for pings to
reach a second receiver without a restart.

When a Slack webhook is configured (see above), the test also seeds it as
`live/slack-url`, adds a `url_setec` Slack sink, and expects `notify-test` to report
`ops-slack slack 200`, so one real message reaches the channel per run.

```bash
TAIL_WATCH_SLACK_PULUMI_ENV=<org>/tail-watch-live/slack \
TAIL_WATCH_LIVE_TAILNET=1 go test -count=1 -timeout 18m -run 'TestLiveSetec$' -v ./cmd/tail-watch
```

The policy needs `tag:secrets` and the two `tag:monitor` grants in
`cmd/tail-watch/testdata/snippet.hujson` (the grant's `put` and `activate` actions are for
the test's own seeding, not for the collector). `TAIL_WATCH_LIVE_SETEC_TAG` changes the
server's tag. The test turns on MagicDNS in the collector's node through
`TS_LIVE_ACCEPT_DNS`, because the collector must resolve the server's name, and the first
request waits while the server obtains a certificate.

## Restart behavior

The Slack sink uses a memory queue, so an event still inside its `group_wait` window when
the test restarts the collector is lost and counted as `dropped{reason="restart"}`. The
test accepts that reason and fails on any other drop or failure. The webhook sink uses
`queue: disk` and must survive the restart without paging again.

Set `queue: disk` on the Slack sink if an alert must survive a collector restart. The
default `queue: memory` loses events still waiting in the group window.

## Bringing up only the tailnet side

To bring up only the tailnet side, without the collector process or any scenario, use
`scripts/live-stack.sh up`. It mints ephemeral keys from the pulumi env, starts the
collector's tailscale node, the four fixture nodes, and the LAN router, approves the LAN
route, and prints each node's address. `scripts/live-stack.sh down` removes the containers
and deletes that stack's devices.

Set `TAIL_WATCH_LIVE_SETEC=1` to add the setec server to that stack. `up` then also prints
the server's name and how to seed a secret through the `setec-cli` service.
