package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCallerIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		wantUser   string
		wantCaller string
	}{
		{
			name:       "behind serve on loopback",
			remoteAddr: "127.0.0.1:43322",
			headers:    map[string]string{"Tailscale-User-Login": "alice@example.com", "X-Forwarded-For": "100.64.0.7"},
			wantUser:   "alice@example.com",
			wantCaller: "100.64.0.7",
		},
		{
			name:       "ipv6 loopback",
			remoteAddr: "[::1]:43322",
			headers:    map[string]string{"Tailscale-User-Login": "alice@example.com"},
			wantUser:   "alice@example.com",
			wantCaller: "",
		},
		{
			name:       "forged by a remote client",
			remoteAddr: "100.64.0.9:5555",
			headers:    map[string]string{"Tailscale-User-Login": "root@example.com", "X-Forwarded-For": "100.64.0.1"},
			wantUser:   "",
			wantCaller: "",
		},
		{
			name:       "no headers",
			remoteAddr: "127.0.0.1:43322",
			headers:    map[string]string{},
			wantUser:   "",
			wantCaller: "",
		},
		{
			name:       "forwarded chain keeps the first address",
			remoteAddr: "127.0.0.1:43322",
			headers:    map[string]string{"X-Forwarded-For": "100.64.0.7, 10.0.0.1"},
			wantUser:   "",
			wantCaller: "100.64.0.7",
		},
		{
			name:       "forwarded address that is not an address",
			remoteAddr: "127.0.0.1:43322",
			headers:    map[string]string{"X-Forwarded-For": "not-an-ip"},
			wantUser:   "",
			wantCaller: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/targets", nil)
			r.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			user, caller := callerIdentity(r)
			if user != tt.wantUser || caller != tt.wantCaller {
				t.Errorf("callerIdentity = (%q, %q), want (%q, %q)", user, caller, tt.wantUser, tt.wantCaller)
			}
		})
	}
}

func TestAuditLineCarriesServeIdentity(t *testing.T) {
	t.Parallel()
	e := newRouteEnv(t)
	e.do(t, http.MethodDelete, "/api/v1/routes/nope", "", func(r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "alice@example.com")
		r.Header.Set("X-Forwarded-For", "100.64.0.7")
	})
	out := e.logs.String()
	for _, want := range []string{`"msg":"route write"`, `"user":"alice@example.com"`, `"caller":"100.64.0.7"`} {
		if !strings.Contains(out, want) {
			t.Errorf("audit line missing %s:\n%s", want, out)
		}
	}

	e.logs.Reset()
	e.do(t, http.MethodDelete, "/api/v1/routes/nope", "")
	if plain := e.logs.String(); strings.Contains(plain, `"user"`) || strings.Contains(plain, `"caller"`) {
		t.Errorf("a request without serve headers carries an identity:\n%s", plain)
	}
}
