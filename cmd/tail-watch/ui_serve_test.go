package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TestUIServedPeerPicker checks the embedded page wires the address field to
// the peer suggestions and that the script never builds markup from peer
// data with innerHTML.
func TestUIServedPeerPicker(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle("/ui/", uiHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	get := func(path string) string {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	html := get("/ui/")
	for _, want := range []string{`<datalist id="peers">`, `list="peers"`, `id="addr-route"`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %s", want)
		}
	}
	js := get("/ui/app.js")
	if strings.Contains(js, "innerHTML") {
		t.Error("app.js uses innerHTML; peer names must only ever become text")
	}
}

// TestAuthorizationLeavesOpenEndpoints requests the paths that stay outside
// the role check, with authorization on and no caller identity.
func TestAuthorizationLeavesOpenEndpoints(t *testing.T) {
	t.Parallel()
	e := newAPIEnv(t)
	e.mgr.cfg.Collector.Authorization = Authorization{Capability: "example.com/cap/tail-watch", Service: ""}
	e.mgr.authz = roleResolver(ident(nil, errors.New("tailscaled down")), "", true)
	e.mgr.authorize = e.mgr.authorizeRequest
	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "status page")
	}))
	mux.Handle("/metrics", promhttp.HandlerFor(prometheus.NewRegistry(), promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.Handle("/ui/", uiHandler())
	e.mgr.routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for _, path := range []string{"/", "/metrics", "/healthz", "/ui/"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s", path, resp.StatusCode, b)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/targets", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("targets: %d, want 403", resp.StatusCode)
	}
}
