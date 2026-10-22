ARG TAIL_WATCH_VERSION=latest
FROM ghcr.io/tailscale-dev/tail-watch:${TAIL_WATCH_VERSION} AS tail-watch

FROM tailscale/tailscale:v1.102.5

COPY --from=tail-watch /usr/local/bin/tail-watch /usr/local/bin/tail-watch
COPY tail-watch.yaml /etc/tail-watch/tail-watch.yaml
COPY start.sh /usr/local/bin/start.sh

ENTRYPOINT ["/usr/local/bin/start.sh"]
