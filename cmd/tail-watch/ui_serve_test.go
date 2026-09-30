package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
