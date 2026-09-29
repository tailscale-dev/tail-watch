# Watch the watcher with a heartbeat

A dead collector cannot report itself. Point `heartbeat` at a dead-man's-switch
service that alerts when the pings stop.

Prerequisites: an account with a dead-man's-switch or cron-monitoring service
that gives you a ping URL and alerts when it stops receiving pings.

Steps:

1. Put the ping URL in an environment variable, or in a file for `url_file`.
   Do not write it into the config.
2. Add the block to the config. Set exactly one of `url_env` or `url_file`:

   ```yaml
   heartbeat:
     url_env: HEARTBEAT_URL
     interval: 1m
   ```

3. Set the interval below the service's grace period.
4. Validate the file, then restart. `check-config` does not read the URL
   variable, but startup fails if it is unset:

   ```bash
   ./bin/tail-watch check-config --config /etc/tail-watch/tail-watch.yaml
   ```

Verify: the service shows pings arriving each `interval`. tail-watch sends a
`GET` starting once every target and check has a first result, with a 10s
timeout. When the process exits the pings stop and the service alerts.

A failed ping is logged, counted in `tailwatch_heartbeat_failures_total`, and
not retried before the next interval. Alert on that counter; see the
[metrics reference](../reference/metrics.md).

Exit-node egress warning: the heartbeat leaves through the host's ordinary
routing table, like the notification sinks. tail-watch warns at startup with
`exit node is active; notifications will route through the exit node` when
an exit node is set, and the pings would then come from the exit node's
address. Clear the exit node on the collector or make sure the service does not
filter by source address.

`tail-watch --once` never sends a heartbeat. If the URL variable is unset,
startup fails; see [Troubleshoot](troubleshoot.md).
