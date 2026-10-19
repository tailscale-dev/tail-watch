package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tailscale.com/client/tailscale/apitype"
)

func TestCallerAttrs(t *testing.T) {
	t.Parallel()
	alice := whoisCaller("alice@example.com", "alice-phone", nil, `{"role":"editor"}`)
	tagged := whoisCaller("ignored@example.com", "db", []string{"tag:db"}, `{"role":"viewer"}`)
	tests := []struct {
		name    string
		enabled bool
		who     *apitype.WhoIsResponse
		remote  string
		headers map[string]string
		want    []string
		absent  []string
	}{
		{
			name:    "matching login",
			enabled: true,
			who:     alice,
			remote:  "127.0.0.1:9",
			headers: map[string]string{
				"X-Forwarded-For":      "100.64.0.7",
				"Tailscale-User-Login": "alice@example.com",
			},
			want:   []string{`"user":"alice@example.com"`, `"node":"alice-phone"`, `"role":"editor"`},
			absent: []string{`"identity_mismatch"`},
		},
		{
			name:    "forged login",
			enabled: true,
			who:     alice,
			remote:  "127.0.0.1:9",
			headers: map[string]string{
				"X-Forwarded-For":      "100.64.0.7",
				"Tailscale-User-Login": "admin@example.com",
			},
			want:   []string{`"user":"alice@example.com"`, `"identity_mismatch":true`},
			absent: []string{`"user":"admin@example.com"`},
		},
		{
			name:    "unresolved caller",
			enabled: true,
			who:     alice,
			remote:  "127.0.0.1:9",
			headers: map[string]string{"Tailscale-User-Login": "admin@example.com"},
			want:    nil,
			absent:  []string{`"user"`, `"node"`, `"tags"`, `"role"`},
		},
		{
			name:    "tagged caller",
			enabled: true,
			who:     tagged,
			remote:  "127.0.0.1:9",
			headers: map[string]string{"X-Forwarded-For": "100.64.0.8"},
			want:    []string{`"node":"db"`, `"tag:db"`, `"role":"viewer"`},
			absent:  []string{`"user"`},
		},
		{
			name:    "no role",
			enabled: true,
			who:     whoisCaller("alice@example.com", "alice-phone", nil, `{"role":"owner"}`),
			remote:  "127.0.0.1:9",
			headers: map[string]string{"X-Forwarded-For": "100.64.0.7"},
			want:    []string{`"user":"alice@example.com"`, `"role":""`},
			absent:  nil,
		},
		{
			name:    "authorization off",
			enabled: false,
			who:     alice,
			remote:  "127.0.0.1:9",
			headers: map[string]string{"X-Forwarded-For": "100.64.0.7"},
			want:    []string{`"user":"alice@example.com"`, `"node":"alice-phone"`},
			absent:  []string{`"role"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rs := roleResolver(ident(tt.who, nil), "", tt.enabled)
			r := httptest.NewRequest(http.MethodPost, "/api/v1/targets", nil)
			r.RemoteAddr = tt.remote
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			text := renderAttrs(callerAttrs(r, rs))
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Errorf("missing %s in %s", want, text)
				}
			}
			for _, abs := range tt.absent {
				if strings.Contains(text, abs) {
					t.Errorf("unexpected %s in %s", abs, text)
				}
			}
		})
	}
}

func renderAttrs(attrs []slog.Attr) string {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.LogAttrs(context.Background(), slog.LevelInfo, "audit", attrs...)
	return buf.String()
}

func TestAuditLineCarriesResolvedIdentity(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	e.mgr.authz = roleResolver(
		ident(whoisCaller("alice@example.com", "alice-phone", nil, `{"role":"editor"}`), nil), "", true,
	)
	e.do(t, http.MethodDelete, "/api/v1/routes/nope", "", func(r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "admin@example.com")
		r.Header.Set("X-Forwarded-For", "100.64.0.7")
	})
	out := e.logs.String()
	for _, want := range []string{
		`"msg":"route write"`, `"user":"alice@example.com"`, `"node":"alice-phone"`,
		`"role":"editor"`, `"identity_mismatch":true`, `"remote":"127.0.0.1:`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit line missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"user":"admin@example.com"`) {
		t.Errorf("audit line took user from the header:\n%s", out)
	}

	e.logs.Reset()
	e.mgr.authz = nil
	e.do(t, http.MethodDelete, "/api/v1/routes/nope", "", func(r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "alice@example.com")
		r.Header.Set("X-Forwarded-For", "100.64.0.7")
	})
	if plain := e.logs.String(); strings.Contains(plain, `"user"`) {
		t.Errorf("an unresolved caller carries a user:\n%s", plain)
	}
}
