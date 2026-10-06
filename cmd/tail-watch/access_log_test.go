package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tailscale.com/tsweb"
)

func TestAccessLoggedUsesStructuredFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler tsweb.ReturnHandlerFunc
		want    map[string]any
		wantErr bool
	}{
		{
			name: "success",
			handler: func(w http.ResponseWriter, _ *http.Request) error {
				_, err := w.Write([]byte("ok"))
				return err
			},
			want:    map[string]any{"method": "GET", "request_uri": "/status?x=1", "status": 200.0},
			wantErr: false,
		},
		{
			name: "handler error",
			handler: func(http.ResponseWriter, *http.Request) error {
				return tsweb.Error(http.StatusNotFound, "no such page", nil)
			},
			want:    map[string]any{"method": "GET", "request_uri": "/status?x=1", "status": 404.0},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			srv := accessLogged(slog.New(slog.NewJSONHandler(&logs, nil)), tt.handler)
			srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status?x=1", nil))

			line := decodeLogLine(t, logs.Bytes())
			for k, want := range tt.want {
				if line[k] != want {
					t.Errorf("%s = %v, want %v", k, line[k], want)
				}
			}
			if msg, _ := line["msg"].(string); strings.HasPrefix(msg, "{") {
				t.Errorf("msg is an embedded JSON object: %q", msg)
			}
			if _, ok := line["remote_addr"]; !ok {
				t.Error("remote_addr is missing")
			}
			if _, ok := line["err"]; ok != tt.wantErr {
				t.Errorf("err present = %v, want %v", ok, tt.wantErr)
			}
		})
	}
}

func decodeLogLine(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(b), &line); err != nil {
		t.Fatalf("log output is not one JSON line: %v\n%s", err, b)
	}
	return line
}
