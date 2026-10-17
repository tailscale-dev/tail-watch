#!/usr/bin/env bash
# Bring up (or tear down) only the tailnet side of the live notification test:
# the collector's tailscale node plus one fixture node per check. It does not
# start the tail-watch process or run any scenario. See
# docs/contributing/live-notification-test.md. Credentials come from the pulumi
# env in TAIL_WATCH_PULUMI_ENV (default tailscale-phase-2/tailscale).
#
# Set TAIL_WATCH_LIVE_SETEC=1 to add a setec server (--dev mode) as another
# tailnet node, tagged TAIL_WATCH_LIVE_SETEC_TAG (default tag:secrets), and to turn
# on MagicDNS in the collector's tailscale node so it can resolve the server's
# name. The policy needs the grants in cmd/tail-watch/testdata/snippet.hujson.
#
#   scripts/live-stack.sh up      create ephemeral nodes and print their addresses
#   scripts/live-stack.sh down    remove the containers and this stack's devices
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT="${TAIL_WATCH_LIVE_PROJECT:-tail-watch-live-stack}"
OUT="${TAIL_WATCH_LIVE_DIR:-${TMPDIR:-/tmp}/$PROJECT}"
ENVNAME="${TAIL_WATCH_PULUMI_ENV:-tailscale-phase-2/tailscale}"
FILES=(-f "$ROOT/examples/compose.yaml" -f "$ROOT/cmd/tail-watch/testdata/compose.live-notify.yaml")
SERVICES=(fx-icmp fx-tsping fx-tcp fx-http lan-router)

creds() {
  pulumi env open "$ENVNAME" --format json | python3 -c '
import json,sys
t=json.load(sys.stdin)["tailscale"]
print(t["apiKey"]); print(t["tailnet"])'
}

api() { curl -fsS -K <(printf 'user = "%s:"\n' "$APIKEY") -H 'Content-Type: application/json' "$@"; }

mint_key() {
  api -X POST "https://api.tailscale.com/api/v2/tailnet/$TAILNET/keys" \
    -d '{"capabilities":{"devices":{"create":{"reusable":false,"ephemeral":true,"preauthorized":true'"${1:+,\"tags\":[\"$1\"]}"'}}},"expirySeconds":900}' |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["key"])'
}

compose() {
  docker compose --project-name "$PROJECT" "${FILES[@]}" "$@"
}

devices() {
  api "https://api.tailscale.com/api/v2/tailnet/$TAILNET/devices?fields=all"
}

load_creds() {
  local out
  out="$(creds)"
  APIKEY="$(sed -n 1p <<<"$out")"
  TAILNET="$(sed -n 2p <<<"$out")"
}

up() {
  load_creds
  mkdir -p "$OUT"
  export TS_FX_PREFIX="$PROJECT"
  export TS_HOSTNAME="$PROJECT"
  export TS_AUTHKEY TS_KEY_ICMP TS_KEY_TSPING TS_KEY_TCP TS_KEY_HTTP TS_KEY_LAN
  local services=("${SERVICES[@]}")
  if [ -n "${TAIL_WATCH_LIVE_SETEC:-}" ]; then
    export TS_KEY_SETEC SETEC_BIN TS_LIVE_ACCEPT_DNS=true
    TS_KEY_SETEC="$(mint_key "${TAIL_WATCH_LIVE_SETEC_TAG:-tag:secrets}")"
    SETEC_BIN="$("$ROOT/scripts/build-setec.sh" "$OUT/bin")"
    services+=(setec)
  fi
  # TAIL_WATCH_LIVE_TAG defaults to untagged; set it to tag:monitor once the policy
  # in cmd/tail-watch/testdata/snippet.hujson is merged.
  TS_AUTHKEY="$(mint_key "${TAIL_WATCH_LIVE_TAG:-}")"
  TS_KEY_ICMP="$(mint_key)"
  TS_KEY_TSPING="$(mint_key)"
  TS_KEY_TCP="$(mint_key)"
  TS_KEY_HTTP="$(mint_key)"
  TS_KEY_LAN="$(mint_key)"
  export TAIL_WATCH_CONFIG_FILE="$OUT/tail-watch.yaml"
  : >"$TAIL_WATCH_CONFIG_FILE"
  compose up --detach --wait --wait-timeout 120 tailscale "${services[@]}"

  local id
  for _ in $(seq 1 30); do
    id="$(devices | python3 -c '
import json,sys
want=sys.argv[1].lower()
for d in json.load(sys.stdin)["devices"]:
    if d["hostname"].lower()==want: print(d["id"])' "$PROJECT-lan")"
    [ -n "$id" ] && break
    sleep 2
  done
  [ -n "$id" ] || { echo "lan router never registered" >&2; exit 1; }
  api -X POST "https://api.tailscale.com/api/v2/device/$id/routes" -d '{"routes":["10.10.53.0/24"]}' >/dev/null

  echo "tailnet nodes (project $PROJECT):"
  for s in tailscale "${services[@]}"; do
    printf '  %-10s %s\n' "$s" "$(compose exec --no-TTY "$s" tailscale ip -4 2>/dev/null | head -1)"
  done
  echo "LAN target behind lan-router: 10.10.53.1"
  if [ -n "${TAIL_WATCH_LIVE_SETEC:-}" ]; then
    echo "setec server: https://$PROJECT-setec.<tailnet>.ts.net (first request may take 30s for a certificate)"
    echo "seed a secret: docker compose --project-name $PROJECT ${FILES[*]} run --rm --no-deps setec-cli -c 'printf %s VALUE | /setec -s URL put NAME'"
  fi
  echo
  echo "Write a config at $TAIL_WATCH_CONFIG_FILE, then start the collector with:"
  echo "  TAIL_WATCH_CONFIG_FILE=$TAIL_WATCH_CONFIG_FILE docker compose --project-name $PROJECT ${FILES[*]} up -d --no-deps tail-watch"
  echo "Tear down with: scripts/live-stack.sh down"
}

down() {
  load_creds
  export TS_FX_PREFIX="$PROJECT" TS_KEY_SETEC=x TS_KEY_ICMP=x TS_KEY_TSPING=x TS_KEY_TCP=x TS_KEY_HTTP=x TS_KEY_LAN=x TS_AUTHKEY=x
  export TAIL_WATCH_CONFIG_FILE="$OUT/tail-watch.yaml"
  compose down --volumes --remove-orphans || true
  devices | python3 -c '
import json,sys
p=sys.argv[1].lower()
for d in json.load(sys.stdin)["devices"]:
    h=d["hostname"].lower()
    if h==p or h.startswith(p+"-"): print(d["id"])' "$PROJECT" |
    while read -r id; do api -X DELETE "https://api.tailscale.com/api/v2/device/$id" >/dev/null && echo "deleted device $id"; done
}

case "${1:-}" in
up) up ;;
down) down ;;
*) echo "usage: $0 up|down" >&2; exit 2 ;;
esac
