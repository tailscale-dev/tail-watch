package main

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// callerIdentity returns the tailnet user login and client address that
// tailscale serve puts in Tailscale-User-Login and X-Forwarded-For. The headers are
// read only when the connection itself is from loopback, which is where serve
// connects from; on any other connection a client could have written them. A process
// on the collector host can still forge them, as it can reach the unauthenticated API.
func callerIdentity(r *http.Request) (string, string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", ""
	}
	if ip, perr := netip.ParseAddr(host); perr != nil || !ip.IsLoopback() {
		return "", ""
	}
	caller := ""
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
	if ip, perr := netip.ParseAddr(strings.TrimSpace(first)); perr == nil {
		caller = ip.String()
	}
	return r.Header.Get("Tailscale-User-Login"), caller
}

// callerAttrs returns the identity log attributes for r, omitting any that are empty.
func callerAttrs(r *http.Request) []slog.Attr {
	user, caller := callerIdentity(r)
	var attrs []slog.Attr
	if user != "" {
		attrs = append(attrs, slog.String("user", user))
	}
	if caller != "" {
		attrs = append(attrs, slog.String("caller", caller))
	}
	return attrs
}
