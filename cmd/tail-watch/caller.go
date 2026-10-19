package main

import (
	"log/slog"
	"net/http"
	"strings"
)

// callerAttrs returns the verified identity attributes for r. The login, node
// and tags come from tailscaled through rs, never from a request header. role
// is included only when authorization is on. A Tailscale-User-Login that
// disagrees with the login tailscaled reports sets identity_mismatch, and the
// user attribute stays the login tailscaled reports. When rs is nil or the
// caller cannot be resolved, the result is empty: the audit line then carries
// only the connection's remote address. X-Forwarded-For is read only for a
// loopback connection, inside rs.
func callerAttrs(r *http.Request, rs *resolver) []slog.Attr {
	if rs == nil || r == nil {
		return nil
	}
	c, err := rs.resolve(r)
	if err != nil {
		return nil
	}
	var attrs []slog.Attr
	if c.User != "" {
		attrs = append(attrs, slog.String("user", c.User))
	}
	if c.Node != "" {
		attrs = append(attrs, slog.String("node", c.Node))
	}
	if len(c.Tags) > 0 {
		attrs = append(attrs, slog.Any("tags", c.Tags))
	}
	if rs.enabled {
		attrs = append(attrs, slog.String("role", string(c.Role)))
	}
	header := strings.TrimSpace(r.Header.Get("Tailscale-User-Login"))
	if header != "" && header != c.User {
		attrs = append(attrs, slog.Bool("identity_mismatch", true))
	}
	return attrs
}
