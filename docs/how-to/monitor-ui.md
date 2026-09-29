# Manage monitors in the UI

The monitor UI adds, edits and deletes monitors while tail-watch runs, with
no restart and no lost grace timers. It is off by default.

Read [Monitor UI risks](../explanation/monitor-ui-risks.md) first: there is
no login, and whoever can reach the port can make the collector probe any
address it can route to.

## Turn the UI on

Prerequisites: a writable `collector.state_dir`. Monitors created in the UI
are stored in `state_dir/targets.json`.

Steps:

1. Set `collector.state_dir` in the config:

   ```yaml
   collector:
     listen: 127.0.0.1:9109
     state_dir: /var/lib/tail-watch
     ui: true
   targets:
     - name: nyc-subnet-router
       addr: nyc-router
       checks: [tsping]
   ```

2. Enable the UI with `collector.ui: true` (above) or the flag:

   ```bash
   tail-watch --config /etc/tail-watch/tail-watch.yaml --ui
   ```

   The flag wins over the file, so `--ui=false` turns off a config that
   enables it. Under `--once` the UI is ignored.
3. Restart tail-watch.

Verify: the log has `monitor write API enabled`, and
`http://127.0.0.1:9109/ui/` shows the monitor list. If tail-watch exits with
`the UI needs collector.state_dir to store monitors`, set `state_dir`.

## Reach the UI from another device

The default `listen` address, `127.0.0.1:9109`, is reachable only from the
host. To open the UI from another device on the tailnet, bind an address that
device can reach and grant it access.

Steps:

1. Bind the node's tailnet IP with `--listen` or `collector.listen`, for
   example `--listen 100.101.102.103:9109`. tail-watch then warns
   `monitor write API is reachable beyond loopback; access is controlled only
   by the network`.
2. Grant the device access to the port:

   ```hujson
   {
     "grants": [
       {
         "src": ["autogroup:admin"],
         "dst": ["tag:monitor"],
         "ip": ["tcp:9109"]
       }
     ]
   }
   ```

   A grant covers the whole port, so it also exposes `/metrics`.
3. Open `http://<tailnet-ip>:9109/ui/` from that device.

In the Compose layout tail-watch shares the `tailscale` container's network
namespace, so every process in that namespace can reach a loopback listener.

## Add, edit and delete a monitor

Steps:

1. Click **Add monitor**. Fill in name, address, checks and the other fields.
   The `defaults` block of the YAML file applies to UI monitors.
2. Click **Check** to validate without saving, then **Save**. Errors appear
   next to the field they belong to, and nothing is created until they are
   fixed. They come from the same rules the YAML loader uses.

   ![The add monitor form with an invalid TCP port reported under Checks.](../images/monitor-ui-form-error.png)

3. The list shows every monitor. YAML monitors are read-only; only monitors
   created in the UI have **Edit** and **Delete**. Monitors behind a down
   parent show `SUPPRESSED`.

   ![The monitor list: YAML monitors are read-only, UI monitors have Edit and Delete, and monitors behind a down parent show SUPPRESSED.](../images/monitor-ui-list.png)

4. To delete, click **Delete** and confirm. Deleting a `DOWN` monitor asks
   first, and says its open incident closes.

   ![The delete confirmation for a down monitor.](../images/monitor-ui-delete-down.png)

Behavior to expect:

- An edit is a delete followed by an add. Editing or deleting a `DOWN`
  monitor closes its open incident with `reason: removed`. A new incident
  opens only if the new definition fails `fail_threshold` checks.
- A monitor that other monitors name as `parent` cannot be deleted until
  they are changed. The API answers with `"<name>" is the parent of [...];
  delete or change those first`.
- Editing a parent briefly releases its suppressed dependents, which can page
  while the parent is down.
- A monitor whose name matches a YAML target is skipped at startup with a
  warning, and the YAML target wins.
- `targets.json` uses the YAML field names, is written atomically, and has
  mode 0600.
- With the UI off, `targets.json` is left alone and its monitors do not run.
  A startup warning, `dynamic monitors are inactive: the UI is off`, gives
  the count.

Every write is logged with the caller's address as a `monitor write` line
with `op`, `target`, `status`, `outcome` and `remote`. To find writes, filter
the JSON log on that message.
