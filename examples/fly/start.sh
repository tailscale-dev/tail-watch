#!/bin/sh
set -eu

: "${TS_AUTHKEY:?set TS_AUTHKEY with fly secrets set}"

mkdir -p /data/tailscale /data/tail-watch
chmod 700 /data/tail-watch

export TS_STATE_DIR=/data/tailscale
export TS_USERSPACE=false
export TS_ACCEPT_DNS=true
export TS_EXTRA_ARGS="--accept-routes --advertise-tags=${TS_TAGS:-tag:monitor}"
export TS_SOCKET=/var/run/tailscale/tailscaled.sock

containerboot &
boot=$!

tries=0
until tailscale --socket="$TS_SOCKET" status --json 2>/dev/null | grep -q '"BackendState": "Running"'; do
  tries=$((tries + 1))
  if [ "$tries" -gt 60 ] || ! kill -0 "$boot" 2>/dev/null; then
    echo "tailscaled did not reach Running" >&2
    exit 1
  fi
  sleep 1
done

if [ -n "${TS_SERVE_SERVICE:-}" ]; then
  tailscale --socket="$TS_SOCKET" serve --service="svc:$TS_SERVE_SERVICE" --https=443 --bg 127.0.0.1:9109 ||
    echo "tailscale serve failed; the UI is not exposed" >&2
fi

tail-watch --config /etc/tail-watch/tail-watch.yaml &
watch=$!

trap 'kill "$boot" "$watch" 2>/dev/null; exit 0' INT TERM

while kill -0 "$boot" 2>/dev/null && kill -0 "$watch" 2>/dev/null; do
  sleep 2
done

echo "tailscaled or tail-watch exited" >&2
kill "$boot" "$watch" 2>/dev/null || true
exit 1
