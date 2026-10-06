package main

import (
	"log/slog"
	"net/http"

	"tailscale.com/tsweb"
)

// accessLogged serves h and logs each request as one structured line. tsweb's own
// logging hands Logf a pre-encoded JSON object, which would land inside msg, so Logf
// stays nil (tsweb then discards it) and the record is logged from OnCompletion instead.
func accessLogged(log *slog.Logger, h tsweb.ReturnHandler) http.Handler {
	return tsweb.StdHandler(h, tsweb.HandlerOptions{
		Logf:                     nil,
		QuietLoggingIfSuccessful: false,
		Now:                      nil,
		StatusCodeCounters:       nil,
		StatusCodeCountersFull:   nil,
		BucketedStats:            nil,
		OnStart:                  nil,
		OnError:                  nil,
		OnCompletion: func(r *http.Request, rec tsweb.AccessLogRecord) {
			attrs := []slog.Attr{
				slog.String("method", rec.Method),
				slog.String("request_uri", rec.RequestURI),
				slog.Int("status", rec.Code),
				slog.Int("bytes", rec.Bytes),
				slog.Float64("duration", rec.Seconds),
				slog.String("remote_addr", rec.RemoteAddr),
			}
			if rec.Err != "" {
				attrs = append(attrs, slog.String("err", rec.Err))
			}
			log.LogAttrs(r.Context(), slog.LevelInfo, "http request", attrs...)
		},
	})
}
