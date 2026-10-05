# Send alerts to Microsoft Teams

Post alert transitions to a Teams channel through a Workflows webhook, as an
Adaptive Card. Office 365 Connectors, the old channel Incoming Webhook, were
switched off in May 2026; the Workflows template below is the replacement.

Prerequisites:

- A Teams team where you can create a workflow.
- A collector that can reach the workflow host (such as `logic.azure.com`;
  see the egress note in [Send alerts to a webhook](webhook-sink.md)).

## Get a Workflows webhook URL

tail-watch posts to the stock **When a Teams webhook request is received**
template. It posts an Adaptive Card envelope, which that template renders as
the card; a flow you design yourself can take the raw event through a
[webhook sink](webhook-sink.md) instead.

1. In Teams, open the channel's menu and choose **Workflows**.
2. Create a flow from the **Post to a channel when a webhook request is
   received** template (the "When a Teams webhook request is received"
   trigger), pick the team and channel, and finish the wizard.
3. Copy the webhook URL the flow shows. It contains a `sig` query parameter
   and is a bearer secret: anyone holding it can post to the channel.

Add a co-owner to the flow (in Power Automate, open the flow and share it).
The flow runs as its creator and goes silent when that account leaves; the
collector cannot see ownership, and failed delivery only shows up in the
drop and failure metrics.

Store the URL as a secret in your environment, secret manager or a file
outside the config. Never put it in the YAML.

## Add the sink

1. Add a sink to the config. `url_env` names the environment variable that
   holds the URL; use `url_file` for a file instead:

   ```yaml
   sinks:
     - name: ops-teams
       type: teams
       url_env: TEAMS_WEBHOOK_URL
       min_severity: warn
       status_url: https://monitor-nyc.example.ts.net:9109
       group_wait: 10s
       queue: disk
       match:
         sites: [nyc, lab]
   ```

   - `status_url` turns each target's name into a link to
     `<status_url>/#<target>`.
   - `group_wait` batches events that arrive together into one card, at most
     ten events and an "and N more" line. Cards are sent at most one per
     second.
   - `queue: disk` needs `collector.state_dir`. With the default
     `queue: memory`, an event still waiting in the group window is lost when
     tail-watch restarts. Use `disk` if an alert must survive a restart.
   - A Teams sink rejects `batch`, `max_batch`, `secret_*`, `headers_env` and
     `mention_on_page`. A route's `mention` is not printed on a card; page
     severity shows as the red `Attention` headline instead.
2. Validate the file. `check-config` prints each sink's name, type, filters
   and queue mode, never a secret. It does not read the secret variables:

   ```bash
   ./bin/tail-watch check-config --config /etc/tail-watch/tail-watch.yaml
   ```

3. Send a test message:

   ```bash
   ./bin/tail-watch notify-test --sink ops-teams --timeout 10s
   ```

Verify: `notify-test` prints `ops-teams teams 202` and exits 0 (a workflow
trigger answers `202 Accepted`, which tail-watch counts as delivered), and the card
appears in the channel, posted by the Workflows bot with a "via Workflows"
footer. A missing variable prints
`environment variable TEAMS_WEBHOOK_URL is unset or empty`; see
[Troubleshoot](troubleshoot.md). Run `notify-test` after every config change.

For the event fields behind the card see the
[payload reference](../reference/payload.md). For sink options see the
[configuration reference](../reference/config.md).
