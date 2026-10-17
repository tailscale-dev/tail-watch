package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mapSource is a fake secretSource for tests that do not run a setec server.
type mapSource struct {
	values map[string]string
}

func (m mapSource) get(name string) (string, error) {
	v, ok := m.values[name]
	if !ok {
		return "", errSetecNotFound(name)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errSetecEmpty(name)
	}
	return v, nil
}

func errSetecNotFound(name string) error {
	return &setecTestError{msg: "setec: secret not found: " + name}
}

func errSetecEmpty(name string) error {
	return &setecTestError{msg: "setec: secret " + name + " is empty"}
}

type setecTestError struct{ msg string }

func (e *setecTestError) Error() string { return e.msg }

// setecFake speaks the setec get API: POST /api/get, JSON name and version,
// and a base64 value on 200.
type setecFake struct {
	mu     sync.Mutex
	values map[string]setecFakeValue
	status int
}

type setecFakeValue struct {
	text    string
	version uint32
}

func newSetecFake() *setecFake {
	return &setecFake{mu: sync.Mutex{}, values: map[string]setecFakeValue{}, status: 0}
}

func (s *setecFake) set(name, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.values[name]
	cur.text = text
	cur.version++
	s.values[name] = cur
}

func (s *setecFake) fail() {
	s.mu.Lock()
	s.status = http.StatusInternalServerError
	s.mu.Unlock()
}

func (s *setecFake) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/get" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Name            string `json:"Name"`
			Version         uint32 `json:"Version"`
			UpdateIfChanged bool   `json:"UpdateIfChanged"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		status := s.status
		cur, ok := s.values[req.Name]
		s.mu.Unlock()
		if status != 0 {
			http.Error(w, "unavailable", status)
			return
		}
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if req.UpdateIfChanged && req.Version == cur.version {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Value   []byte `json:"Value"`
			Version uint32 `json:"Version"`
		}{Value: []byte(cur.text), Version: cur.version})
	})
}

// chanTicker is a setec.Ticker a test fires once per poll.
// tick sends, then waits until the store's poll has finished.
type chanTicker struct {
	c    chan time.Time
	done chan struct{}
}

func newChanTicker() *chanTicker {
	return &chanTicker{c: make(chan time.Time), done: make(chan struct{})}
}

func (t *chanTicker) Chan() <-chan time.Time { return t.c }
func (t *chanTicker) Stop()                  {}
func (t *chanTicker) Done()                  { t.done <- struct{}{} }

func (t *chanTicker) tick(tb testing.TB) {
	tb.Helper()
	t.c <- time.Now()
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
		tb.Fatal("setec poll did not finish")
	}
}

func TestNewSetecStore(t *testing.T) {
	t.Parallel()
	t.Run("trimmed", func(t *testing.T) {
		t.Parallel()
		fake := newSetecFake()
		fake.set("hook/url", "  https://hooks.example/hook \n")
		var dials atomic.Int32
		env := dialingEnv(func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		})
		st := openTestStore(t, env, fake, []string{"hook/url"}, nil)
		got, err := st.get("hook/url")
		if err != nil || got != "https://hooks.example/hook" {
			t.Fatalf("got %q %v", got, err)
		}
		if dials.Load() == 0 {
			t.Fatal("store did not dial through env.Dial")
		}
	})
	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		fake := newSetecFake()
		fake.set("hook/url", " \n")
		srv := httptest.NewServer(fake.handler())
		t.Cleanup(srv.Close)
		_, err := newSetecStore(context.Background(), dialingEnv(nil), setecSettings{
			Server: srv.URL, PollInterval: time.Minute, pollTicker: nil,
		}, []string{"hook/url"}, discardLog())
		if err == nil || !strings.Contains(err.Error(), "setec: secret hook/url is empty") ||
			!strings.Contains(err.Error(), setecHost(srv.URL)) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		fake := newSetecFake()
		srv := httptest.NewServer(fake.handler())
		t.Cleanup(srv.Close)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := newSetecStore(ctx, dialingEnv(nil), setecSettings{
			Server: srv.URL, PollInterval: time.Minute, pollTicker: nil,
		}, []string{"hb-url"}, discardLog())
		if err == nil || !strings.Contains(err.Error(), "setec: secret not found: hb-url") ||
			!strings.Contains(err.Error(), setecHost(srv.URL)) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("poll", func(t *testing.T) {
		t.Parallel()
		fake := newSetecFake()
		fake.set("hook/url", "https://old.example/hook")
		tk := newChanTicker()
		st := openTestStore(t, dialingEnv(nil), fake, []string{"hook/url"}, tk)
		fake.set("hook/url", "https://new.example/hook")
		tk.tick(t)
		got, err := st.get("hook/url")
		if err != nil || got != "https://new.example/hook" {
			t.Fatalf("after poll got %q %v", got, err)
		}
	})
}

func openTestStore(t *testing.T, env netEnv, fake *setecFake, names []string, tk *chanTicker) *setecStore {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	settings := setecSettings{Server: srv.URL, PollInterval: time.Minute, pollTicker: nil}
	if tk != nil {
		settings.pollTicker = tk
	}
	st, err := newSetecStore(context.Background(), env, settings, names, discardLog())
	if err != nil {
		t.Fatalf("newSetecStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSetecRotationReachesNextDelivery(t *testing.T) {
	t.Parallel()
	first := newReceiver(t)
	second := newReceiver(t)
	fake := newSetecFake()
	fake.set("hook/url", first.srv.URL)
	tk := newChanTicker()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelWarn, AddSource: false, ReplaceAttr: nil,
	}))
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	st, err := newSetecStore(context.Background(), dialingEnv(nil), setecSettings{
		Server: srv.URL, PollInterval: time.Minute, pollTicker: tk,
	}, []string{"hook/url"}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := newWebhookSink(setecHook(st), newSinkClient(nil))
	send := func() {
		t.Helper()
		res := w.Send(context.Background(), []byte(`{}`), sendMeta{ID: "evt_setec", At: time.Now()})
		if res.Class != resultOK {
			t.Fatalf("delivery %+v", res)
		}
	}
	send()
	if first.count() != 1 || second.count() != 0 {
		t.Fatalf("first delivery first=%d second=%d", first.count(), second.count())
	}
	fake.set("hook/url", second.srv.URL)
	tk.tick(t)
	send()
	if second.count() != 1 || first.count() != 1 {
		t.Fatalf("rotated delivery first=%d second=%d", first.count(), second.count())
	}
	fake.fail()
	tk.tick(t)
	send()
	if second.count() != 2 {
		t.Fatalf("failed poll did not keep the last URL: second=%d", second.count())
	}
	logged := logs.String()
	if !strings.Contains(logged, "WARN") || !strings.Contains(logged, "continuing") {
		t.Fatalf("failed poll was not logged at warn:\n%s", logged)
	}
	for _, secret := range []string{first.srv.URL, second.srv.URL} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log contains secret URL %s\n%s", secret, logged)
		}
	}
}

func setecHook(src secretSource) SinkConfig {
	sc := testSink("hook")
	sc.URL = secretRef{Env: "", File: "", Setec: "hook/url", src: src}
	return sc
}

func TestSetecValueStaysOffDisk(t *testing.T) {
	t.Parallel()
	const token = "SET-EC-DISK-TOKEN"
	rc := newReceiver(t)
	fake := newSetecFake()
	fake.set("hook/url", rc.srv.URL+"/"+token)
	st := openTestStore(t, dialingEnv(nil), fake, []string{"hook/url"}, nil)
	dir := filepath.Join(t.TempDir(), "state")
	sc := setecHook(st)
	sc.Queue = queueDisk
	h := realSinkHarness(t, harnessOpts{
		sinks: []SinkConfig{sc}, stateDir: dir, tags: nil, intake: 0, scripts: nil, clock: nil,
	})
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool { return rc.count() >= 1 })
	raw, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), rc.srv.URL) {
		t.Fatalf("%s contains the setec value", stateFileName)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("state dir: %v", err)
	}
	for _, e := range entries {
		body, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(body), token) || strings.Contains(string(body), rc.srv.URL) {
			t.Fatalf("%s contains the setec value", e.Name())
		}
	}
}

func TestSetecFailureRedactsURL(t *testing.T) {
	t.Parallel()
	const token = "SET-EC-PATH-TOKEN"
	secretURL := "http://127.0.0.1:1/" + token
	w := newWebhookSink(setecHook(mapSource{values: map[string]string{"hook/url": secretURL}}), newSinkClient(nil))
	res := w.Send(context.Background(), []byte(`{}`), sendMeta{ID: "evt_setec", At: time.Now()})
	if res.Err == nil || strings.Contains(res.Err.Error(), token) ||
		!strings.Contains(res.Err.Error(), "http://127.0.0.1:1/") {
		t.Fatalf("error %v", res.Err)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelWarn, AddSource: false, ReplaceAttr: nil,
	}))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	hb := newHeartbeat(HeartbeatConfig{
		URL: secretRef{
			Env: "", File: "", Setec: "hb/url",
			src: mapSource{values: map[string]string{"hb/url": srv.URL + "/" + token}},
		},
		Interval: time.Minute,
	}, newCountingClock(), logger, nil, nil, nil)
	hb.ping(context.Background())
	logged := logs.String()
	redacted := "http://" + setecHost(srv.URL) + "/"
	if hits.Load() != 1 || strings.Contains(logged, token) || !strings.Contains(logged, redacted) {
		t.Fatalf("hits %d log %s", hits.Load(), logged)
	}
}

func withSetecTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := setecStartupTimeout
	setecStartupTimeout = d
	t.Cleanup(func() { setecStartupTimeout = prev })
}

func stubOpenSetec(
	t *testing.T,
	fn func(context.Context, netEnv, setecSettings, []string, *slog.Logger) (*setecStore, error),
) {
	t.Helper()
	prev := openSetecStore
	openSetecStore = fn
	t.Cleanup(func() { openSetecStore = prev })
}

func useSetecTestClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	trusted, ok := srv.Client().Transport.(*http.Transport)
	if !ok || trusted.TLSClientConfig == nil {
		t.Fatal("test server has no TLS config")
	}
	tlsCfg := trusted.TLSClientConfig.Clone()
	prev := setecHTTPClient
	setecHTTPClient = func(env netEnv) *http.Client {
		c := newSinkClient(env)
		base, isTransport := c.Transport.(*http.Transport)
		if !isTransport {
			t.Fatal("transport")
		}
		tr := base.Clone()
		tr.TLSClientConfig = tlsCfg
		c.Transport = tr
		return c
	}
	t.Cleanup(func() { setecHTTPClient = prev })
}

func startSetecTLS(t *testing.T, fake *setecFake) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(fake.handler())
	t.Cleanup(srv.Close)
	useSetecTestClient(t, srv)
	return srv
}

func TestSetecLineIsFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"WARNING: error flushing cache: boom", true},
		{"get %q: unavailable (continuing)", true},
		{`[store] error fetching "x": boom (retrying)`, true},
		{"[store] begin update poll (interval=5m0s)", false},
		{`[store] update to version 2 for secret "x"`, false},
	} {
		if got := setecLineIsFailure(tc.line); got != tc.want {
			t.Errorf("setecLineIsFailure(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestApplySetecNamesSinkForMissingSecret(t *testing.T) {
	t.Parallel()
	fake := newSetecFake()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	ops := testSink("ops")
	ops.URL = secretRef{Env: "", File: "", Setec: "slack-url", src: nil}
	other := testSink("other")
	other.URL = secretRef{Env: "X", File: "", Setec: "", src: nil}
	cfg := new(Config)
	cfg.Collector.Setec = setecSettings{Server: srv.URL, PollInterval: time.Minute, pollTicker: nil}
	cfg.Sinks = []SinkConfig{ops, other}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := applySetec(ctx, cfg, dialingEnv(nil), discardLog())
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"slack-url", setecHost(srv.URL), "sink ops"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "other") {
		t.Errorf("error %q names a sink that does not use the secret", err)
	}
}
