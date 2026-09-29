package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui
var uiFiles embed.FS

const uiCSP = "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// uiHandler serves the embedded monitor page under /ui/. Every response
// carries a Content-Security-Policy that allows only same-origin resources.
func uiHandler() http.Handler {
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/ui/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
