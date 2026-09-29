# Send alerts to a webhook

Deliver each alert event as a signed JSON `POST` to your own receiver.

Prerequisites:

- A receiver that accepts `POST` with `Content-Type: application/json` and
  answers `2xx`.
- The receiver must deduplicate on the event `id`, because delivery is at
  least once. See the [payload reference](../reference/payload.md).

## Add the sink

1. Add a sink. Secrets come only from environment variables or files:

   ```yaml
   sinks:
     - name: ops-webhook
       type: webhook
       url_env: OPS_WEBHOOK_URL
       secret_env: OPS_WEBHOOK_SECRET
       headers_env:
         Authorization: OPS_WEBHOOK_AUTH
       min_severity: page
       events: [target.down, target.recovered]
       max_age: 1h
       queue: disk
       queue_size: 1000
       batch: true
       group_wait: 10s
       max_batch: 100
   ```

   - `secret_env` (or `secret_file`) is the HMAC key. Without it requests are
     unsigned, and tail-watch logs `webhook sink is unsigned: no secret_env
     or secret_file`.
   - `headers_env` maps a header name to the environment variable holding its
     value.
   - `queue: disk` needs `collector.state_dir`.
   - `batch: true` waits `group_wait` after the first queued event, or until
     `max_batch` events are waiting, and sends one request.
2. Validate the file. `check-config` does not read the secret variables:

   ```bash
   ./bin/tail-watch check-config --config /etc/tail-watch/tail-watch.yaml
   ```

   Export the variables in the collector's environment before you run
   `notify-test` or start tail-watch.

3. Send a test event through the real signing and retry code:

   ```bash
   ./bin/tail-watch notify-test --sink ops-webhook
   ```

Verify: `notify-test` prints `ops-webhook webhook 200` and exits 0 when the
receiver answers `2xx` within `--timeout` (default 30s). It bypasses filters,
queues and `state_dir`, and works when `tailscaled` is down.

## What the receiver sees

Each event is one `POST` with these headers:

- `Content-Type: application/json`
- `User-Agent: tail-watch/<version>`
- `Tail-Watch-Event-Id`, or `Tail-Watch-Batch-Id` for a batched sink
- `Tail-Watch-Signature: t=<unix seconds>,v1=<hex>` when a secret is set
- any `headers_env` headers

Redirects are not followed and each request times out after 10s. A batched
request has the body `{"version": 1, "batch_id": "bat_...", "events": [...]}`.

| Response | What tail-watch does |
| --- | --- |
| `2xx` | delivered |
| `408`, `5xx`, timeout, network error | retry with growing delay |
| `429` | retry, waiting at least `Retry-After` |
| anything else, including `3xx` and other `4xx` | drop, log once, count `permanent` |

The delay is 1s, 2s, 4s and so on with 0.5 to 1.5 times jitter, never more
than 5 minutes. An event older than `max_age` when an attempt is due is
dropped and counted as `expired`. A retry sends identical body bytes with a
fresh `t`.

## Verify the signature in the receiver

`v1` is the lowercase hex HMAC-SHA256 of `<t>.<body>` under the secret. The
signature covers the whole body, including a batch. Reject stale
timestamps:

```go
func verify(secret, header string, body []byte, tolerance time.Duration) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(secs, 0)).Abs() > tolerance {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s.%s", ts, body)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig))
}
```

To check your implementation, the repository's test vector uses the secret
`whsec_test_key`, the timestamp `1700000000` and the body
`{"id":"evt_X","type":"test"}`. The header must be:

```text
t=1700000000,v1=1419cf10ea0dc484e3a06e7df47e222718af9df5f1bb5180b9c3bb9cde19722f
```

You can reproduce the digest with:

```bash
printf '%s' '1700000000.{"id":"evt_X","type":"test"}' \
  | openssl dgst -sha256 -hmac whsec_test_key
```

## Make sure the collector can reach the receiver

Sinks use the host's ordinary routing table. tail-watch warns at startup with
`exit node is active; notifications will route through the exit node` when an
exit node is set. A receiver on your own tailnet needs a grant from
`tag:monitor`; see [Write the ACLs](acls.md).

For how queues, retries and drops behave, see
[Incidents and queues](../explanation/incidents-and-queues.md).
