package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	vectorSecret    = "whsec_test_key"
	vectorTime      = int64(1700000000)
	vectorBody      = `{"id":"evt_X","type":"test"}`
	vectorSignature = "t=1700000000,v1=1419cf10ea0dc484e3a06e7df47e222718af9df5f1bb5180b9c3bb9cde19722f"
)

// verifySignature is the receiver side: it recomputes the HMAC independently of
// signBody and rejects timestamps outside tolerance.
func verifySignature(secret, header string, body []byte, now time.Time, tolerance time.Duration) bool {
	var ts, sig string
	for part := range strings.SplitSeq(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(secs, 0)).Abs() > tolerance {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s.%s", ts, body)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig))
}

func TestSignBodyVector(t *testing.T) {
	t.Parallel()
	if got := signBody(vectorSecret, vectorTime, []byte(vectorBody)); got != vectorSignature {
		t.Fatalf("signature %s, want independently computed %s", got, vectorSignature)
	}
	if signBody(vectorSecret, vectorTime+1, []byte(vectorBody)) == vectorSignature {
		t.Fatal("changing the timestamp must change the signature")
	}
	if signBody(vectorSecret, vectorTime, []byte(vectorBody+" ")) == vectorSignature {
		t.Fatal("changing the body must change the signature")
	}
	if signBody("other", vectorTime, []byte(vectorBody)) == vectorSignature {
		t.Fatal("changing the secret must change the signature")
	}
}

func TestVerifySignatureRules(t *testing.T) {
	t.Parallel()
	now := time.Unix(vectorTime, 0)
	if !verifySignature(vectorSecret, vectorSignature, []byte(vectorBody), now, 5*time.Minute) {
		t.Fatal("valid signature rejected")
	}
	if verifySignature("wrong", vectorSignature, []byte(vectorBody), now, 5*time.Minute) {
		t.Fatal("wrong secret accepted")
	}
	if verifySignature(vectorSecret, vectorSignature, []byte(vectorBody), now.Add(time.Hour), 5*time.Minute) {
		t.Fatal("stale timestamp accepted")
	}
	if verifySignature(vectorSecret, vectorSignature, []byte(vectorBody+"x"), now, 5*time.Minute) {
		t.Fatal("tampered body accepted")
	}
}

type received struct {
	Header http.Header
	Body   []byte
	At     time.Time
}

type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []received
	status []int
	extra  map[int]func(http.ResponseWriter)
}

func newReceiver(t *testing.T, statuses ...int) *receiver {
	t.Helper()
	r := &receiver{status: statuses, extra: map[int]func(http.ResponseWriter){}, srv: nil, mu: sync.Mutex{}, got: nil}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body := make([]byte, 0, 1024)
		buf := make([]byte, 4096)
		for {
			n, err := req.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				break
			}
		}
		r.mu.Lock()
		r.got = append(r.got, received{Header: req.Header.Clone(), Body: body, At: time.Now()})
		status := http.StatusOK
		if len(r.status) > 0 {
			status = r.status[0]
			r.status = r.status[1:]
		}
		hook := r.extra[len(r.got)]
		r.mu.Unlock()
		if hook != nil {
			hook(w)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func (r *receiver) request(i int) received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[i]
}

func webhookFor(t *testing.T, url string, mut func(*SinkConfig)) *webhookSink {
	t.Helper()
	sc := testSink("hook")
	sc.URL = urlFile(t, url)
	if mut != nil {
		mut(&sc)
	}
	return newWebhookSink(sc, newSinkClient(nil))
}

func urlFile(t *testing.T, url string) secretRef {
	t.Helper()
	path := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(path, []byte(url), 0o600); err != nil {
		t.Fatal(err)
	}
	return secretRef{Env: "", File: path}
}

func TestWebhookRequestFormatAndSignature(t *testing.T) {
	t.Setenv("TW_TEST_SECRET", vectorSecret)
	t.Setenv("TW_TEST_AUTH", "Bearer abc")
	rc := newReceiver(t)
	w := webhookFor(t, rc.srv.URL+"/hook/private", func(sc *SinkConfig) {
		sc.Secret = secretRef{Env: "TW_TEST_SECRET", File: ""}
		sc.HeadersEnv = map[string]string{"Authorization": "TW_TEST_AUTH"}
	})
	ev := newDownEvent("evt_01JAB3K9T6M4X2Q8R7N5W0YZCD", goldenCollector(), goldenTransition())
	body, err := w.Render([]Event{ev}, ev.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	res := w.Send(context.Background(), body, sendMeta{ID: ev.ID, At: now})
	if res.Class != resultOK || res.Status != 200 {
		t.Fatalf("result %+v", res)
	}
	got := rc.request(0)
	if got.Header.Get("Content-Type") != "application/json" ||
		!strings.HasPrefix(got.Header.Get("User-Agent"), "tail-watch/") ||
		got.Header.Get("Authorization") != "Bearer abc" ||
		got.Header.Get("Tail-Watch-Event-Id") != ev.ID {
		t.Fatalf("headers %v", got.Header)
	}
	var decoded Event
	if decodeErr := json.Unmarshal(got.Body, &decoded); decodeErr != nil || decoded.ID != ev.ID {
		t.Fatalf("body %s (%v)", got.Body, decodeErr)
	}
	if !verifySignature(vectorSecret, got.Header.Get(signatureHeader), got.Body, now, time.Minute) {
		t.Fatal("receiver could not verify the signature")
	}
	if verifySignature("different", got.Header.Get(signatureHeader), got.Body, now, time.Minute) {
		t.Fatal("a different secret verified")
	}
}

func TestWebhookUnsignedWithoutSecret(t *testing.T) {
	t.Parallel()
	rc := newReceiver(t)
	w := webhookFor(t, rc.srv.URL, nil)
	ev := newTestEvent("evt_A", goldenCollector(), testNow())
	body, _ := w.Render([]Event{ev}, ev.ID)
	if res := w.Send(context.Background(), body, sendMeta{ID: ev.ID, At: time.Now()}); res.Class != resultOK {
		t.Fatalf("%+v", res)
	}
	if rc.request(0).Header.Get(signatureHeader) != "" {
		t.Fatal("unsigned sink must not send a signature")
	}
}

func TestWebhookResultClassification(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := []struct {
		status int
		hdr    string
		class  resultClass
		reason string
		after  time.Duration
	}{
		{200, "", resultOK, "", 0},
		{204, "", resultOK, "", 0},
		{500, "", resultRetry, failHTTP5xx, 0},
		{503, "", resultRetry, failHTTP5xx, 0},
		{408, "", resultRetry, failTimeout, 0},
		{429, "30", resultRetry, failHTTP429, 30 * time.Second},
		{429, "", resultRetry, failHTTP429, 0},
		{400, "", resultPermanent, failHTTP4xx, 0},
		{401, "", resultPermanent, failHTTP4xx, 0},
		{302, "", resultPermanent, failHTTP4xx, 0},
	}
	for _, tt := range tests {
		h := http.Header{}
		if tt.hdr != "" {
			h.Set("Retry-After", tt.hdr)
		}
		got := classifyStatus(tt.status, h, now)
		if got.Class != tt.class || got.Reason != tt.reason || got.RetryAfter != tt.after {
			t.Errorf("status %d: %+v", tt.status, got)
		}
	}
	date := now.Add(45 * time.Second).UTC().Format(http.TimeFormat)
	h := http.Header{}
	h.Set("Retry-After", date)
	if got := classifyStatus(429, h, now); got.RetryAfter < 40*time.Second || got.RetryAfter > 46*time.Second {
		t.Fatalf("HTTP-date Retry-After = %s", got.RetryAfter)
	}
}

func TestWebhookOverHTTPMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		class  resultClass
		reason string
	}{
		{"server error", 500, resultRetry, failHTTP5xx},
		{"request timeout", 408, resultRetry, failTimeout},
		{"bad request", 400, resultPermanent, failHTTP4xx},
		{"redirect not followed", 302, resultPermanent, failHTTP4xx},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rc := newReceiver(t, tt.status, http.StatusOK)
			w := webhookFor(t, rc.srv.URL, nil)
			ev := newTestEvent("evt_A", goldenCollector(), testNow())
			body, _ := w.Render([]Event{ev}, ev.ID)
			res := w.Send(context.Background(), body, sendMeta{ID: ev.ID, At: time.Now()})
			if res.Class != tt.class || res.Reason != tt.reason {
				t.Fatalf("%+v", res)
			}
			if rc.count() != 1 {
				t.Fatalf("redirects must not be followed, saw %d requests", rc.count())
			}
		})
	}
}

func TestWebhookTimeoutAndNetworkErrors(t *testing.T) {
	t.Parallel()
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	w := webhookFor(t, slow.URL+"/secret/path", nil)
	w.client.Timeout = 50 * time.Millisecond
	ev := newTestEvent("evt_A", goldenCollector(), testNow())
	body, _ := w.Render([]Event{ev}, ev.ID)
	res := w.Send(context.Background(), body, sendMeta{ID: ev.ID, At: time.Now()})
	if res.Class != resultRetry || res.Reason != failTimeout {
		t.Fatalf("timeout classified as %+v", res)
	}
	if res.Err != nil && strings.Contains(res.Err.Error(), "/secret/path") {
		t.Fatalf("error leaks the URL path: %v", res.Err)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	w2 := webhookFor(t, url+"/secret/path", nil)
	res = w2.Send(context.Background(), body, sendMeta{ID: ev.ID, At: time.Now()})
	if res.Class != resultRetry || res.Reason != failNetwork {
		t.Fatalf("dial error classified as %+v", res)
	}
	if strings.Contains(res.Err.Error(), "/secret/path") {
		t.Fatalf("error leaks the URL path: %v", res.Err)
	}
}

func TestWebhookThroughNotifierRetriesServerError(t *testing.T) {
	rc := newReceiver(t, 500, 200)
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := testSink("hook")
	sc.URL = secretRef{Env: "TW_TEST_URL", File: ""}
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	before := h.clock.timers.Load()
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool { return rc.count() == 1 })
	h.clock.waitTimers(t, before)
	h.clock.Advance(time.Second)
	eventually(t, func() bool { return rc.count() == 2 })
	if string(rc.request(0).Body) != string(rc.request(1).Body) {
		t.Fatal("retry changed the body")
	}
	if rc.request(0).Header.Get("Tail-Watch-Event-Id") != rc.request(1).Header.Get("Tail-Watch-Event-Id") {
		t.Fatal("retry changed the event id")
	}
}

func TestWebhookBatchedBurstSplits(t *testing.T) {
	rc := newReceiver(t)
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := testSink("hook")
	sc.URL = secretRef{Env: "TW_TEST_URL", File: ""}
	sc.Batch = true
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.n.mu.Lock()
	for i := range 250 {
		id := fmt.Sprintf("evt_%03d", i)
		h.n.enqueueLocked(newFlappingEvent(id, goldenCollector(), goldenTransition()))
	}
	h.n.commitLocked()
	h.n.mu.Unlock()
	h.start(t)
	eventually(t, func() bool { return rc.count() == 2 })
	h.clock.waitTimers(t, 0)
	h.clock.Advance(sc.GroupWait)
	eventually(t, func() bool { return rc.count() == 3 })
	sizes := []int{}
	var firstID string
	next := 0
	for i := range 3 {
		req := rc.request(i)
		var b batchBody
		if err := json.Unmarshal(req.Body, &b); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(b.Events))
		if req.Header.Get("Tail-Watch-Batch-Id") != b.BatchID || req.Header.Get("Tail-Watch-Event-Id") != "" ||
			!strings.HasPrefix(b.BatchID, "bat_") || b.Version != 1 {
			t.Fatalf("batch envelope %+v headers %v", b, req.Header)
		}
		for _, e := range b.Events {
			if e.ID != fmt.Sprintf("evt_%03d", next) {
				t.Fatalf("batch order broken at %d: %s", next, e.ID)
			}
			next++
		}
		if i == 0 {
			firstID = b.BatchID
		}
	}
	if fmt.Sprint(sizes) != "[100 100 50]" {
		t.Fatalf("batch sizes %v", sizes)
	}
	if firstID == "" {
		t.Fatal("no batch id")
	}
	if n := testutil.CollectAndCount(h.n.m.batchSizeH); n != 1 {
		t.Fatalf("batch_size histogram series = %d", n)
	}
}

func TestWebhookBatchRetryKeepsBytesAndFreshSignature(t *testing.T) {
	t.Setenv("TW_TEST_SECRET", vectorSecret)
	rc := newReceiver(t, 503, 200)
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := testSink("hook")
	sc.URL = secretRef{Env: "TW_TEST_URL", File: ""}
	sc.Secret = secretRef{Env: "TW_TEST_SECRET", File: ""}
	sc.Batch = true
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	h.clock.waitTimers(t, 0)
	h.clock.Advance(sc.GroupWait)
	eventually(t, func() bool { return rc.count() == 1 })
	h.clock.waitTimers(t, 1)
	h.n.ObserveTransition(upTransition(2, h.clock.Now()))
	h.clock.Advance(time.Second)
	eventually(t, func() bool { return rc.count() >= 2 })
	a, b := rc.request(0), rc.request(1)
	if string(a.Body) != string(b.Body) || a.Header.Get("Tail-Watch-Batch-Id") != b.Header.Get("Tail-Watch-Batch-Id") {
		t.Fatal("batch retry must keep body bytes and batch id")
	}
	ta, _, _ := strings.Cut(strings.TrimPrefix(a.Header.Get(signatureHeader), "t="), ",")
	tb, _, _ := strings.Cut(strings.TrimPrefix(b.Header.Get(signatureHeader), "t="), ",")
	if ta == "" || tb == "" {
		t.Fatal("signature headers missing")
	}
	var body batchBody
	if err := json.Unmarshal(b.Body, &body); err != nil || len(body.Events) != 1 {
		t.Fatalf("arrivals during retry must not join the active batch: %v %d", err, len(body.Events))
	}
}

func TestWebhookBatchDropsWholeBatchOnPermanent(t *testing.T) {
	rc := newReceiver(t, 400)
	t.Setenv("TW_TEST_URL", rc.srv.URL)
	sc := testSink("hook")
	sc.URL = secretRef{Env: "TW_TEST_URL", File: ""}
	sc.Batch = true
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: "", tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.n.mu.Lock()
	for i := range 5 {
		h.n.enqueueLocked(newFlappingEvent(fmt.Sprintf("evt_%d", i), goldenCollector(), goldenTransition()))
	}
	h.n.mu.Unlock()
	h.start(t)
	h.clock.waitTimers(t, 0)
	h.clock.Advance(sc.GroupWait)
	eventually(t, func() bool {
		return testutil.ToFloat64(h.n.m.droppedC.WithLabelValues("hook", reasonPermanent)) == 5
	})
}

func TestNoSecretsReachDisk(t *testing.T) {
	rc := newReceiver(t, 503, 503, 503)
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("TW_TEST_URL", rc.srv.URL+"/TOPSECRETPATH")
	t.Setenv("TW_TEST_SECRET", "hmac-secret-value-123")
	t.Setenv("TW_TEST_AUTH", "Bearer header-secret-456")
	sc := testSink("hook")
	sc.Queue = queueDisk
	sc.URL = secretRef{Env: "TW_TEST_URL", File: ""}
	sc.Secret = secretRef{Env: "TW_TEST_SECRET", File: ""}
	sc.HeadersEnv = map[string]string{"Authorization": "TW_TEST_AUTH"}
	h := realSinkHarness(
		t,
		harnessOpts{sinks: []SinkConfig{sc}, stateDir: dir, tags: nil, intake: 0, scripts: nil, clock: nil},
	)
	h.start(t)
	before := h.clock.timers.Load()
	h.n.ObserveTransition(downTransition(1, h.clock.Now()))
	eventually(t, func() bool { return rc.count() >= 1 })
	h.clock.waitTimers(t, before)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("state dir empty: %v", err)
	}
	for _, e := range entries {
		raw, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, secret := range []string{"TOPSECRETPATH", "hmac-secret-value-123", "header-secret-456", rc.srv.URL} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("%s contains %q", e.Name(), secret)
			}
		}
	}
}
