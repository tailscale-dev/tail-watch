package main

import (
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// auditWriter records the status and monitor name of a write for the audit log.
type auditWriter struct {
	http.ResponseWriter

	status int
	name   string
}

func (w *auditWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func setAuditName(w http.ResponseWriter, name string) {
	if aw, ok := w.(*auditWriter); ok {
		aw.name = name
	}
}

// guard wraps a handler that changes a monitor. The order is: audit the
// outcome, require JSON (415), reject a foreign Origin (403), then ask
// authorize (403). Nothing changes unless all of them pass. op is the audit
// operation. The dry-run validation call passes an empty op: it changes
// nothing, so it is neither audited nor authorized. The probe passes "probe"
// even though it persists nothing: it emits network traffic toward an address
// the caller names, so it is authorized and audited like a write.
func (m *monitorManager) guard(next http.HandlerFunc, op string) http.HandlerFunc {
	write := op != ""
	return func(rw http.ResponseWriter, r *http.Request) {
		w := &auditWriter{ResponseWriter: rw, status: 0, name: r.PathValue("name")}
		defer func() {
			if !write {
				return
			}
			m.log.LogAttrs(r.Context(), slog.LevelInfo, "monitor write",
				slog.String("op", op),
				slog.String("target", w.name),
				slog.Int("status", w.status),
				slog.String("outcome", outcomeOf(w.status)),
				slog.String("remote", r.RemoteAddr))
		}()
		if !isJSON(r.Header.Get("Content-Type")) {
			writeAPIError(w, fail(http.StatusUnsupportedMediaType, "Content-Type must be application/json"))
			return
		}
		if !sameOrigin(r) {
			writeAPIError(w, fail(http.StatusForbidden, "cross-origin request refused"))
			return
		}
		if !write {
			next(w, r)
			return
		}
		if err := m.authorize(r); err != nil {
			writeAPIError(w, fail(http.StatusForbidden, err.Error()))
			return
		}
		next(w, r)
	}
}

func outcomeOf(status int) string {
	if status >= 200 && status < 300 {
		return "accepted"
	}
	return "refused"
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == contentTypeJSON
}

// sameOrigin is true when the request has no Origin header or names its own Host.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// routes registers the monitor API on mux.
func (m *monitorManager) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/targets", m.handleList)
	mux.HandleFunc("GET /api/v1/peers", m.handlePeers)
	mux.HandleFunc("POST /api/v1/targets", m.guard(m.handleCreate, "create"))
	mux.HandleFunc("PUT /api/v1/targets/{name}", m.guard(m.handleReplace, "replace"))
	mux.HandleFunc("DELETE /api/v1/targets/{name}", m.guard(m.handleDelete, "delete"))
	mux.HandleFunc("POST /api/v1/validate", m.guard(m.handleValidate, ""))
	mux.HandleFunc("POST /api/v1/probe", m.guard(m.handleProbe, "probe"))
	mux.HandleFunc("/api/v1/", notFoundJSON)
}

func notFoundJSON(w http.ResponseWriter, _ *http.Request) {
	writeAPIError(w, fail(http.StatusNotFound, "not found"))
}

// loopbackListen reports whether a listen address binds only to loopback.
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
