package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func startHeartbeat(t *testing.T, url string, first chan struct{}) (*heartbeat, *countingClock) {
	t.Helper()
	clk := newCountingClock()
	hb := newHeartbeat(
		HeartbeatConfig{URL: urlFile(t, url), Interval: time.Minute},
		clk, discardLog(), nil, first, prometheus.NewRegistry(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hb.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return hb, clk
}

func TestHeartbeatPingsEveryIntervalAfterFirstRound(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		hits.Add(1)
	}))
	defer srv.Close()
	first := make(chan struct{})
	_, clk := startHeartbeat(t, srv.URL+"/ping/secret", first)
	time.Sleep(30 * time.Millisecond)
	if clk.tickers.Load() != 0 {
		t.Fatal("heartbeat must wait for the first round")
	}
	close(first)
	eventually(t, func() bool { return clk.tickers.Load() == 1 })
	for i := 1; i <= 5; i++ {
		clk.Advance(time.Minute)
		want := int32(i)
		eventually(t, func() bool { return hits.Load() == want })
	}
}

func TestHeartbeatFailureCountedAndNotRetriedEarly(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	first := make(chan struct{})
	close(first)
	hb, clk := startHeartbeat(t, srv.URL, first)
	eventually(t, func() bool { return clk.tickers.Load() == 1 })
	clk.Advance(time.Minute)
	eventually(t, func() bool { return testutil.ToFloat64(hb.failures) == 1 })
	time.Sleep(30 * time.Millisecond)
	if hits.Load() != 1 {
		t.Fatalf("failed ping was retried early: %d requests", hits.Load())
	}
}

func TestHeartbeatStopsOnShutdown(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	clk := newCountingClock()
	first := make(chan struct{})
	close(first)
	hb := newHeartbeat(HeartbeatConfig{URL: urlFile(t, srv.URL), Interval: time.Minute},
		clk, discardLog(), nil, first, prometheus.NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hb.run(ctx); close(done) }()
	eventually(t, func() bool { return clk.tickers.Load() == 1 })
	cancel()
	<-done
	clk.Advance(time.Hour)
	time.Sleep(30 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatal("heartbeat pinged after shutdown")
	}
}

func TestHeartbeatSetecURL(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		hits.Add(1)
	}))
	defer srv.Close()
	hb := newHeartbeat(HeartbeatConfig{
		URL: secretRef{
			Env: "", File: "", Setec: "hb/url",
			src: mapSource{values: map[string]string{"hb/url": srv.URL + "/ping"}},
		},
		Interval: time.Minute,
	}, newCountingClock(), discardLog(), nil, nil, prometheus.NewRegistry())
	hb.ping(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("pings %d", hits.Load())
	}
}
