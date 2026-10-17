# Read secrets from setec

Read a sink URL, a webhook HMAC key, a webhook header, or the heartbeat URL
from a [setec](https://github.com/tailscale/setec) server on the tailnet.
tail-watch sends no setec credential. The server allows the request because
the collector's tailnet identity is allowed to `get` that secret.

`*_env` and `*_file` stay available. A secret has one source. There is no
fallback: if the setec server is down at startup, tail-watch exits.

This page was checked against setec commit `bc168fdb2362`, the module in
`go.mod`. The server guide in that tree is `docs/server.md`, and the project
README is `docs/README.md` (the repository root has no README at that
commit). When you bump the module, compare the grant below with `docs/server.md`
again.

## Prerequisites

- A collector joined to the tailnet with `tag:monitor`. `collector.local_only`
  cannot use setec, because there is no tailscaled to reach the server through.
- A setec server you can reach over the tailnet. The server encrypts its store
  with AWS KMS or a key sealed to a TPM. Follow `docs/server.md` in the pinned
  tree to run one, or use the server your tailnet already has.
- The server's HTTPS name resolves on the collector (MagicDNS is the usual way).

## Grant `tag:monitor` read access

The pinned server guide uses two grants: one so the client can open a
connection, and one `tailscale.com/cap/secrets` grant for the API. For
tail-watch, `action` is only `get`, and `secret` lists the names the config
uses. `tag:secrets` is the tag the server guide suggests for the setec node.
Replace the destination if your server uses an IP or a different tag.

```hujson
"grants": [
    {
        "src": ["tag:monitor"],
        "dst": ["tag:secrets"],
        "ip":  ["*"],
    },
    {
        "src": ["tag:monitor"],
        "dst": ["tag:secrets"],
        "app": {
            "tailscale.com/cap/secrets": [
                {
                    "action": ["get"],
                    "secret": [
                        "prod/tail-watch/webhook-url",
                        "prod/tail-watch/webhook-hmac",
                    ],
                },
            ],
        },
    },
],
```

Put a secret with the `setec` CLI from that same tree, for example
`setec -s https://secrets.example.ts.net put prod/tail-watch/webhook-url`.
The value is the URL, key, or header the sink would have read from the
environment. Do not put the value in the tail-watch config.

## Point tail-watch at the server

`server` is an `https` URL with a host. `poll_interval` defaults to `5m` and
must be at least `1m`. A `collector.setec` section that no key uses is
accepted and does not open a client.

```yaml
collector:
  setec:
    server: https://secrets.example.ts.net
    poll_interval: 5m

sinks:
  - name: ops-webhook
    type: webhook
    url_setec: prod/tail-watch/webhook-url
    secret_setec: prod/tail-watch/webhook-hmac
    headers_setec:
      Authorization: prod/tail-watch/webhook-auth

heartbeat:
  url_setec: prod/tail-watch/heartbeat-url
```

Slack and Teams accept `url_setec` only. `secret_setec` and `headers_setec`
are webhook keys. A header name cannot be in both `headers_env` and
`headers_setec`.

`check-config` checks the shape and does not contact the server. On success
a setec URL is printed as `url=setec:<name>`, never the value:

```bash
tail-watch check-config --config /etc/tail-watch/tail-watch.yaml
```

## Move one secret at a time

1. Put the current value into setec under a new name.
2. On one sink, replace `url_env` or `url_file` with `url_setec`. Leave the
   other secrets on `*_env` or `*_file`.
3. Run `notify-test --sink <name>`. It opens the same store a normal start
   does and posts to the URL from setec. A failure is one line naming the
   sink and the secret. The value is not printed.
4. Start tail-watch. Repeat for the next secret.

Startup waits up to 30 seconds for every declared name. It exits 1 if the
server is unreachable or a secret is missing or empty. The error names the
secret and the server host. Whitespace around a value is trimmed.

After startup, tail-watch reads the current value on every send and polls
setec at `poll_interval`. A value changed on the server is used on the next
delivery after the next poll, with no restart. A failed poll keeps the last
good value and logs a warning. Secrets stay in memory. They are not written
to `notification-state.json`, a queue file, a log line, or an error. A URL
taken from setec is logged as scheme and host only.

`--once` does not open a store and does not read setec secrets.

Keep a secret on `*_env` or `*_file` when that process must start while setec
is down. Those sources are not tried when the `*_setec` key is set.
