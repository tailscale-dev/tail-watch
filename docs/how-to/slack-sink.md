# Send alerts to Slack

Post alert transitions to a Slack channel through an incoming webhook.

Prerequisites:

- A Slack workspace where you can create an app.
- A collector that can reach `hooks.slack.com` (see the egress note in
  [Send alerts to a webhook](webhook-sink.md)).

## Get an incoming webhook URL

tail-watch posts to an incoming webhook URL. It does not use bot or app-level
tokens (`xoxb-`, `xapp-`), and a bot token cannot produce a webhook URL.
Slack's guide,
[Sending messages using incoming webhooks](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/),
is the reference.

1. Create an app at <https://api.slack.com/apps?new_app=1>, or open an
   existing one.
2. In the app's settings sidebar choose **Incoming Webhooks** and switch
   **Activate Incoming Webhooks** on.
3. Click **Add New Webhook to Workspace**, pick the channel, and click
   **Allow**.
4. Copy the URL from **Webhook URLs for Your Workspace**. It starts with
   `https://hooks.slack.com/services/`.

If the sidebar has no **Incoming Webhooks** entry, Slack's `incoming-webhook`
scope produces a webhook during an OAuth install. Slack changes these pages
over time; if the steps do not match, follow Slack's guide.

Check the URL. Slack answers `ok`:

```bash
curl -X POST -H 'Content-type: application/json' \
  --data '{"text":"hello"}' "$SLACK_WEBHOOK_URL"
```

Store the URL as a secret in your environment, secret manager or a file
outside the config. Never put it in the YAML.

## Add the sink

1. Add a sink to the config. `url_env` names the environment variable that
   holds the URL; use `url_file` for a file instead:

   ```yaml
   sinks:
     - name: ops-slack
       type: slack
       url_env: SLACK_WEBHOOK_URL
       min_severity: warn
       mention_on_page: "<!subteam^S0123ABC>"
       status_url: https://monitor-nyc.example.ts.net:9109
       group_wait: 10s
       queue: disk
       match:
         sites: [nyc, lab]
   ```

   - `mention_on_page` is prepended only to a `target.down` of severity
     `page`, never to recoveries, flapping notices or warnings.
   - `status_url` turns each target line into a link to
     `<status_url>/#<target>`.
   - `group_wait` batches events that arrive together into one message.
   - `queue: disk` needs `collector.state_dir`. With the default
     `queue: memory`, an event still waiting in the group window is lost when
     tail-watch restarts. Use `disk` if an alert must survive a restart.
   - A Slack sink rejects `batch`, `max_batch`, `secret_*` and `headers_env`.
2. Validate the file. `check-config` prints each sink's name, type, filters
   and queue mode, never a secret. It does not read the secret variables:

   ```bash
   ./bin/tail-watch check-config --config /etc/tail-watch/tail-watch.yaml
   ```

3. Send a test message:

   ```bash
   ./bin/tail-watch notify-test --sink ops-slack --timeout 10s
   ```

Verify: `notify-test` prints `ops-slack slack 200` and exits 0, and the message
appears in the channel. A missing variable prints
`environment variable SLACK_WEBHOOK_URL is unset or empty`; see
[Troubleshoot](troubleshoot.md). Run `notify-test` after every config change.

For the message fields see the [payload reference](../reference/payload.md).
For sink options see the [configuration reference](../reference/config.md).
