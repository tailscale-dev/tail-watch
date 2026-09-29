# Set up your first monitor

In this tutorial you run tail-watch beside a Tailscale node, watch one device on your
tailnet, make that device fail, and see the alert arrive in Slack. It takes about 20
minutes.

You need:

- Docker with Compose, on a machine that has `/dev/net/tun`.
- A tailnet where you can edit the policy file and create an auth key.
- A Slack workspace where you can add an incoming webhook.
- One device on your tailnet that you can reach by its MagicDNS name. This tutorial
  calls it `my-device`.

## 1. Allow the collector in your policy

tail-watch runs as a node tagged `tag:monitor`. In your tailnet policy file, own the tag
and let it reach `my-device`. Replace `tag:server` with the tag your device has, or use
its user or IP instead:

```jsonc
{
  "tagOwners": { "tag:monitor": ["autogroup:admin"] },
  "grants": [
    {
      "src": ["tag:monitor"],
      "dst": ["tag:server"],
      "ip": ["*"]
    }
  ]
}
```

[Write the ACLs](../how-to/acls.md) explains each field.

## 2. Create an auth key

In the admin console, open **Settings → Keys** and generate an auth key. Tick
**Pre-approved** if your tailnet requires device approval, and choose `tag:monitor` under
**Tags**. Copy the key and export it:

```bash
export TS_AUTHKEY=tskey-auth-...
```

## 3. Get a Slack webhook URL

Follow [Get an incoming webhook URL](../how-to/slack-sink.md#get-an-incoming-webhook-url),
then export the URL:

```bash
export SLACK_WEBHOOK_URL='https://hooks.slack.com/services/...'
```

## 4. Write a config

Create `first-monitor.yaml` next to `examples/compose.yaml`. It has one target, the
tailnet ping of `my-device`, and one Slack sink:

```yaml
collector:
  listen: 127.0.0.1:9109
  state_dir: /var/lib/tail-watch

defaults:
  interval: 10s
  timeout: 3s
  fail_threshold: 2
  recover_threshold: 1

targets:
  - name: my-device
    addr: my-device
    checks: [tsping]

sinks:
  - name: ops-slack
    type: slack
    url_env: SLACK_WEBHOOK_URL
    group_wait: 5s
```

Check it. This reads the file and does not contact Tailscale or Slack:

```bash
docker build -t tail-watch:local .
docker run --rm -v "$PWD/first-monitor.yaml:/first-monitor.yaml:ro" \
  -e SLACK_WEBHOOK_URL tail-watch:local \
  check-config --config /first-monitor.yaml
```

The output lists the target and the `ops-slack` sink. The shorter thresholds only make
the tutorial faster; the [defaults](../reference/config.md) are better for real use.

## 5. Pass the webhook to the container

`examples/compose.yaml` does not forward your environment to tail-watch. Add an override
file named `first-monitor.compose.yaml`:

```yaml
services:
  tail-watch:
    environment:
      SLACK_WEBHOOK_URL: ${SLACK_WEBHOOK_URL:?set SLACK_WEBHOOK_URL}
```

## 6. Start the stack

```bash
TAIL_WATCH_CONFIG_FILE=./first-monitor.yaml \
  docker compose -f examples/compose.yaml -f first-monitor.compose.yaml up -d --build
```

The `tail-watch` node appears in your admin console with the `tag:monitor` tag. Check
the logs:

```bash
docker compose -f examples/compose.yaml -f first-monitor.compose.yaml logs tail-watch
```

The last line is `listening` on `127.0.0.1:9109`. The `state_dir is readable by group
or others` warning is expected here, because the image creates that directory with mode
`0755`; it does not stop anything from working. Now send a test message through the real
Slack code:

```bash
docker compose -f examples/compose.yaml -f first-monitor.compose.yaml \
  exec tail-watch /usr/local/bin/tail-watch notify-test \
  --config /etc/tail-watch/tail-watch.yaml
```

Slack shows a test message, and the command prints `ops-slack slack 200` and exits 0.
If it does not, see [Troubleshoot](../how-to/troubleshoot.md).

You should also see `collector.started` arrive in the channel once every target has its
first result.

## 7. Watch it work

The status page runs on the node's loopback address, so open it from inside the
container namespace:

```bash
docker compose -f examples/compose.yaml exec tailscale wget -qO- http://127.0.0.1:9109/
```

`my-device` shows as up.

## 8. Break it

Stop Tailscale on `my-device`, or power the device off. Within about 30 seconds,
tail-watch marks the target `DOWN` and Slack shows a message that starts
`:red_circle: DOWN  my-device`, with the failure layer in brackets.

The layer says where the path broke. [Failure layers](../explanation/failure-layers.md)
explains why `control` and `tunnel` differ from `service` and `lan`.

## 9. Fix it

Start Tailscale on `my-device` again. After one successful check, tail-watch sends a
recovery message that names the original outage start and how long it lasted.

## Clean up

```bash
docker compose -f examples/compose.yaml -f first-monitor.compose.yaml down -v
```

Delete the `tail-watch` machine from the admin console if it does not expire on its own.

## Next steps

- [Deploy tail-watch](../how-to/deploy.md) on a host instead of Compose.
- [Monitor remote subnets](../how-to/remote-subnets.md) behind a router.
- [Configuration reference](../reference/config.md) for every key.
