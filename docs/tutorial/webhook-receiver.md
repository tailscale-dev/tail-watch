# Receive your first webhook

In this tutorial you write a small HTTP receiver, point a tail-watch `webhook` sink at
it, make a monitored service fail and recover, and watch the receiver verify each
signature and ignore a duplicate event. It takes about 15 minutes and needs no tailnet.

You need:

- A tail-watch binary. From a checkout, `mise run build` writes `bin/tail-watch`; the
  commands below call it `tail-watch`, so put it on your `PATH` or use the full path.
- `python3`, `curl` and `openssl`.
- Four terminal windows, all in one empty working directory.

## 1. Write the receiver

A webhook receiver is any server that accepts a `POST` and answers `2xx`. Save this as
`receiver.py`. It checks the signature, drops events whose `id` it has already seen,
and prints one line per new event:

```python
import hashlib
import hmac
import json
import os
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = os.environ["WEBHOOK_SECRET"].encode()
TOLERANCE = 300
seen = set()


def signature_ok(header, body):
    parts = dict(p.split("=", 1) for p in header.split(",") if "=" in p)
    try:
        ts = int(parts["t"])
    except (KeyError, ValueError):
        return False
    if abs(time.time() - ts) > TOLERANCE:
        return False
    signed = f"{ts}.".encode() + body
    want = hmac.new(SECRET, signed, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, parts.get("v1", ""))


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        if not signature_ok(self.headers.get("Tail-Watch-Signature", ""), body):
            self.send_response(401)
            self.end_headers()
            print("rejected: bad signature", flush=True)
            return
        event = json.loads(body)
        if event["id"] in seen:
            print(f"duplicate {event['id']}, ignored", flush=True)
        else:
            seen.add(event["id"])
            target = event.get("target", {}).get("name", "-")
            print(f"{event['type']} severity={event['severity']} target={target} id={event['id']}", flush=True)
        self.send_response(204)
        self.end_headers()

    def log_message(self, *args):
        pass


HTTPServer(("127.0.0.1", 8080), Handler).serve_forever()
```

Two parts matter. `signature_ok` recomputes the HMAC over `<t>.<raw body>` and rejects
an old `t`. The `seen` set implements the rule that delivery is at least once, so the
receiver must deduplicate on `id`. A real receiver would keep `seen` in a database.

Start it with a secret of your choosing, in terminal 1:

```bash
WEBHOOK_SECRET=whsec_tutorial python3 receiver.py
```

## 2. Start something to monitor

In terminal 2, serve a plain TCP port. This stands in for the service you want to
watch:

```bash
python3 -m http.server 9000 --bind 127.0.0.1
```

## 3. Configure the sink

Save this as `tutorial.yaml`. `local_only` runs tail-watch without tailscaled, so the
target is a port on this machine. The short interval and thresholds only speed up the
tutorial; the [defaults](../reference/config.md#defaults) are better for real use.

```yaml
collector:
  local_only: true
  listen: 127.0.0.1:9109

defaults:
  interval: 2s
  timeout: 1s
  fail_threshold: 2
  recover_threshold: 1

targets:
  - name: demo-service
    addr: 127.0.0.1
    checks: [tcp:9000]

sinks:
  - name: tutorial-hook
    type: webhook
    url_env: TUTORIAL_WEBHOOK_URL
    secret_env: TUTORIAL_WEBHOOK_SECRET
    group_wait: 1s
```

The sink has `type: webhook`. Its URL and HMAC key come from environment variables,
never from the file. In terminal 3, export them and check the file. `check-config` does
not read the variables, so it passes even when they are unset:

```bash
export TUTORIAL_WEBHOOK_URL=http://127.0.0.1:8080/hook
export TUTORIAL_WEBHOOK_SECRET=whsec_tutorial
tail-watch check-config --config tutorial.yaml
```

The output lists `demo-service` and the `tutorial-hook` sink. The secret must equal the
receiver's `WEBHOOK_SECRET`.

## 4. Send a test event

```bash
tail-watch notify-test --config tutorial.yaml
```

The command prints `tutorial-hook webhook 204` and exits 0. Terminal 1 prints a line
that starts `test severity=info`. That request went through the real signing and
delivery code, and your receiver accepted its signature.

## 5. Start the collector

Still in terminal 3, with the variables exported:

```bash
tail-watch --config tutorial.yaml
```

Within a few seconds terminal 1 prints a `collector.started` line.

## 6. Break the service

In terminal 2, press Ctrl+C to stop the Python server. After two failed checks, about
four seconds later, terminal 1 prints:

```text
target.down severity=page target=demo-service id=evt_...
```

The request body is the `target.down` event. The collector log in terminal 3 says why:
`layer` is `service`, because the host answered but nothing was listening. See
[Event payload](../reference/payload.md#fields) for every field.

## 7. Fix it

Start the server in terminal 2 again. After one successful check, terminal 1 prints a
`target.recovered` line. That event carries `down_event_id`, the `id` of the down it
resolves, and `duration_seconds`.

## 8. Replay an event

Stop the collector with Ctrl+C in terminal 3. In terminal 4, build a request by hand and
send it twice. The shell does what tail-watch does: it signs `<t>.<body>` with the
secret.

```bash
BODY='{"version":1,"id":"evt_REPLAY","type":"test","severity":"info","time":"2026-01-01T00:00:00Z","collector":{"name":"demo"}}'
T=$(date +%s)
SIG=$(printf '%s.%s' "$T" "$BODY" | openssl dgst -sha256 -hmac whsec_tutorial -hex | awk '{print $NF}')
for i in 1 2; do
  curl -s -o /dev/null -w '%{http_code}\n' -X POST \
    -H "Tail-Watch-Signature: t=$T,v1=$SIG" -d "$BODY" http://127.0.0.1:8080/hook
done
```

Both requests print `204`, but the receiver acts on only the first. Terminal 1 shows
`test severity=info ... id=evt_REPLAY` once, then `duplicate evt_REPLAY, ignored`. The
`204` on the duplicate matters: if a receiver answered an error for an event it
already handled, tail-watch would retry it. See
[the response table](../how-to/webhook-sink.md#what-the-receiver-sees).

Now change the body but reuse the signature:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST \
  -H "Tail-Watch-Signature: t=$T,v1=$SIG" -d "${BODY/demo/evil}" http://127.0.0.1:8080/hook
```

It prints `401`, and the receiver logs `rejected: bad signature`. A tail-watch sink treats
`401` as permanent and drops the event, so a receiver that mis-verifies loses alerts
instead of seeing them retried.

## Clean up

Press Ctrl+C in terminals 1 and 2, and delete `receiver.py` and `tutorial.yaml`.

## Next steps

- [Send alerts to a webhook](../how-to/webhook-sink.md) covers extra headers, batching,
  disk queues, retries and a Go verifier.
- [Event payload](../reference/payload.md) lists every field, the signature format and
  the batch envelope.
- [Route notifications](../how-to/notification-routes.md) chooses which monitors reach
  this sink.
- [Incidents and queues](../explanation/incidents-and-queues.md) explains why delivery is
  at least once.
