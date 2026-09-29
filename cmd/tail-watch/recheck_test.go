package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecheckPoolBounds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var started atomic.Int32
	block := make(chan struct{})
	var releaseOnce sync.Once
	pool := newRecheckPool(ctx, recheckWorkers, func(*Target, check) {
		started.Add(1)
		<-block
	})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(block) })
		pool.shutdown()
		cancel()
	})
	began := time.Now()
	for i := range 10 {
		pool.enqueue(alertTarget("t"+string(rune('a'+i)), "icmp"))
	}
	if time.Since(began) > time.Second {
		t.Fatal("enqueue took longer than a second")
	}
	deadline := time.Now().Add(time.Second)
	for started.Load() < int32(recheckWorkers) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	pool.mu.Lock()
	queued := len(pool.queue)
	pool.mu.Unlock()
	if started.Load() != int32(recheckWorkers) || queued != 2 {
		t.Fatalf("started %d queued %d", started.Load(), queued)
	}
	releaseOnce.Do(func() { close(block) })
	deadline = time.Now().Add(time.Second)
	for started.Load() < 10 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() != 10 {
		t.Fatalf("started %d", started.Load())
	}
}

func TestInFlightTimeoutUsesStartGeneration(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	chk := gateCheck{entered: entered, release: release, err: errors.New("timeout: down")}
	child := alertTarget("nas", "tcp:9")
	child.Checks = []check{chk}
	parent := alertTarget("router", "tsping")
	al, _, tr := testAlerter(t, []*Target{child, parent}, map[string]string{"nas": "router"})
	pass(al, child)
	app := &app{
		alerts:   al,
		pub:      newPublisher(),
		clock:    al.clock,
		cfg:      nil,
		env:      nil,
		log:      nil,
		stdout:   nil,
		socket:   "",
		listen:   "",
		resolver: nil,
		presence: nil,
		class:    nil,
		learner:  nil,
		metrics:  nil,
		watch:    nil,
		probeMu:  sync.Mutex{},
		probes:   nil, notify: nil, collector: CollectorInfo{Name: "", TailscaleIP: ""},
	}
	app.pub.add(bridge{al: al})
	done := make(chan struct{})
	go func() {
		_ = app.execute(context.Background(), child, chk)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("check did not start")
	}
	failN(al, parent, "tsping", layerTunnel, 3)
	recoverTarget(al, parent)
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("check did not finish")
	}
	if gotState(al, "nas") == StateDown {
		t.Fatal("in-flight timeout counted after release")
	}
	failN(al, child, "tcp:9", layerService, 2)
	if gotState(al, "nas") == StateDown {
		t.Fatal("stale timeout advanced fresh counters")
	}
	failN(al, child, "tcp:9", layerService, 1)
	got := tr.wait(t, 3)
	if got[2].Target.Name != "nas" || got[2].From != StateSuppressed || got[2].To != StateDown {
		t.Fatalf("%+v", got[2])
	}
}

type gateCheck struct {
	entered chan struct{}
	release chan struct{}
	err     error
}

func (g gateCheck) String() string { return "tcp:9" }
func (g gateCheck) Kind() string   { return "tcp" }

func (g gateCheck) Run(ctx context.Context, _ netEnv, _ *Target) (Result, error) {
	select {
	case g.entered <- struct{}{}:
	case <-ctx.Done():
		return Result{
			Err:        ctx.Err(),
			Target:     nil,
			Check:      "",
			Kind:       "",
			Start:      time.Time{},
			Generation: 0,
			Latency:    0,
			Ping:       nil,
		}, ctx.Err()
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return Result{
			Err:        ctx.Err(),
			Target:     nil,
			Check:      "",
			Kind:       "",
			Start:      time.Time{},
			Generation: 0,
			Latency:    0,
			Ping:       nil,
		}, ctx.Err()
	}
	if g.err != nil {
		return Result{
			Err:        g.err,
			Target:     nil,
			Check:      "",
			Kind:       "",
			Start:      time.Time{},
			Generation: 0,
			Latency:    0,
			Ping:       nil,
		}, g.err
	}
	return Result{}, nil
}

type bridge struct {
	al *alerter
}

func (b bridge) Observe(res Result) {
	b.al.ObserveClassified(Classified{
		Result: res, Layer: layerService, Parent: "",
		Path: pathInfo{Kind: "", Endpoint: "", DERP: "", Latency: 0},
	})
}

func TestRecheckPoolBoundsMultipleChecks(t *testing.T) {
	t.Parallel()
	var running atomic.Int32
	var peak atomic.Int32
	started := make(chan struct{}, 100)
	release := make(chan struct{})
	var once sync.Once
	pool := newRecheckPool(t.Context(), recheckWorkers, func(*Target, check) {
		n := running.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		running.Add(-1)
	})
	t.Cleanup(func() { once.Do(func() { close(release) }); pool.shutdown() })
	began := time.Now()
	for range 12 {
		pool.enqueue(alertTarget("nas", "icmp", "tcp:9"))
	}
	if time.Since(began) > time.Second {
		t.Fatal("enqueue blocked")
	}
	for range recheckWorkers {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	pool.mu.Lock()
	queued := len(pool.queue)
	pool.mu.Unlock()
	if queued != 16 || peak.Load() != recheckWorkers {
		t.Fatalf("queued=%d peak=%d", queued, peak.Load())
	}
	once.Do(func() { close(release) })
	for range 16 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("queued check did not start")
		}
	}
	if peak.Load() > recheckWorkers {
		t.Fatalf("peak=%d", peak.Load())
	}
}

func TestRecheckPoolFIFOAndCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan string, 4)
	release := make(chan struct{}, 4)
	pool := newRecheckPool(ctx, 1, func(target *Target, chk check) {
		started <- target.Name + "/" + chk.String()
		<-release
	})
	t.Cleanup(func() { cancel(); close(release); pool.shutdown() })
	pool.enqueue(alertTarget("a", "icmp", "tcp:9"))
	pool.enqueue(alertTarget("b", "icmp"))
	for _, want := range []string{"a/icmp", "a/tcp:9"} {
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("worker stalled")
		}
		if want == "a/tcp:9" {
			cancel()
		}
		release <- struct{}{}
	}
	pool.shutdown()
	select {
	case got := <-started:
		t.Fatalf("started after cancel: %q", got)
	default:
	}
}

func TestScheduledCheckAndRecheckSerialize(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	chk := gateCheck{entered: entered, release: release, err: nil}
	target := alertTarget("nas", "tcp:9")
	target.Checks = []check{chk}
	var runtimeApp app
	runtimeApp.clock = newAlertClock()
	runtimeApp.pub = newPublisher()
	runtime := newRuntimeRegistry(t.Context(), &runtimeApp, nil)
	t.Cleanup(runtime.pool.shutdown)
	runtime.seed([]*Target{target})
	done := make(chan struct{})
	go func() { _ = runtimeApp.execute(t.Context(), target, chk); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("scheduled check did not start")
	}
	runtime.enqueueRecheck(target.Name)
	select {
	case <-entered:
		t.Fatal("recheck overlapped scheduled check")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduled check did not finish")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("recheck did not start after scheduled check")
	}
}

func TestRecheckUsesTargetTimeout(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	chk := gateCheck{entered: entered, release: make(chan struct{}), err: nil}
	target := alertTarget("nas", "tcp:9")
	target.Timeout = 20 * time.Millisecond
	var runtimeApp app
	runtimeApp.clock = newAlertClock()
	runtimeApp.pub = newPublisher()
	began := time.Now()
	result := runtimeApp.execute(t.Context(), target, chk)
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("result=%+v", result)
	}
	if time.Since(began) > time.Second {
		t.Fatal("recheck ignored target timeout")
	}
}
