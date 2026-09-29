package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"tailscale.com/tstime"
)

const heartbeatTimeout = 10 * time.Second

// heartbeat pings a dead-man's-switch URL every interval once the first round
// of checks is complete. A failed ping is counted and not retried early.
type heartbeat struct {
	cfg      HeartbeatConfig
	clock    tstime.Clock
	log      *slog.Logger
	client   *http.Client
	first    <-chan struct{}
	failures prometheus.Counter
}

func newHeartbeat(
	cfg HeartbeatConfig,
	clock tstime.Clock,
	log *slog.Logger,
	env netEnv,
	first <-chan struct{},
	reg prometheus.Registerer,
) *heartbeat {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, ForceAttemptHTTP2: true}
	if env != nil {
		tr.DialContext = env.Dial
	}
	failures := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsPrefix, Name: "heartbeat_failures_total",
		Help: "Heartbeat pings that failed.",
	})
	if reg != nil {
		reg.MustRegister(failures)
	}
	return &heartbeat{
		cfg: cfg, clock: clock, log: log, first: first, failures: failures,
		client: &http.Client{Transport: tr, Timeout: heartbeatTimeout},
	}
}

// run blocks until ctx ends. It pings on every tick after first closes.
func (h *heartbeat) run(ctx context.Context) {
	select {
	case <-h.first:
	case <-ctx.Done():
		return
	}
	tk, tick := h.clock.NewTicker(h.cfg.Interval)
	defer tk.Stop()
	for {
		select {
		case <-tick:
			h.ping(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (h *heartbeat) ping(ctx context.Context) {
	url, err := h.cfg.URL.resolve()
	if err != nil {
		h.fail("heartbeat url unavailable", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.fail("heartbeat request", errBadURL(url))
		return
	}
	req.Header.Set("User-Agent", "tail-watch/"+versionString())
	resp, err := h.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		h.fail("heartbeat ping failed", classifyError(err, url).Err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		h.failures.Inc()
		h.log.WarnContext(
			context.Background(),
			"heartbeat ping rejected",
			"url",
			redactURL(url),
			"status",
			resp.StatusCode,
		)
	}
}

func (h *heartbeat) fail(msg string, err error) {
	h.failures.Inc()
	h.log.Warn(msg, "err", err)
}

type badURLError string

func (e badURLError) Error() string { return "invalid URL " + string(e) }

func errBadURL(raw string) error { return badURLError(redactURL(raw)) }
