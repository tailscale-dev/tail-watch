package main

import (
	"sync"
	"testing"
	"time"
)

type orderConsumer struct {
	mu      sync.Mutex
	order   []string
	block   map[string]chan struct{}
	entered map[string]chan struct{}
}

func (o *orderConsumer) Observe(res Result) {
	if o.entered != nil {
		if ch := o.entered[res.Check]; ch != nil {
			close(ch)
		}
	}
	if o.block != nil {
		if ch := o.block[res.Check]; ch != nil {
			<-ch
		}
	}
	o.mu.Lock()
	o.order = append(o.order, res.Check)
	o.mu.Unlock()
}

func TestPublishCompletionOrder(t *testing.T) {
	t.Parallel()
	earlyEntered := make(chan struct{})
	releaseEarly := make(chan struct{})
	cons := &orderConsumer{
		block:   map[string]chan struct{}{"early": releaseEarly},
		entered: map[string]chan struct{}{"early": earlyEntered},

		mu:    sync.Mutex{},
		order: nil,
	}
	pub := newPublisher()
	pub.add(cons)
	target := &Target{Dynamic: false, Name: "t",
		Addr:             "",
		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		Interval:         0,
		Timeout:          0,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Network:          "",
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0,
	}

	go pub.publish(Result{Target: target, Check: "early",
		Kind:       "",
		Start:      time.Time{},
		Generation: 0,
		Latency:    0,
		Err:        nil,
		Ping:       nil,
	})
	select {
	case <-earlyEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("early publish did not start")
	}
	lateDone := make(chan struct{})
	go func() {
		pub.publish(Result{Target: target, Check: "late",
			Kind:       "",
			Start:      time.Time{},
			Generation: 0,
			Latency:    0,
			Err:        nil,
			Ping:       nil,
		})
		close(lateDone)
	}()
	select {
	case <-lateDone:
		t.Fatal("late publish ran before early Observe finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseEarly)
	select {
	case <-lateDone:
	case <-time.After(2 * time.Second):
		t.Fatal("late publish did not finish")
	}
	if len(cons.order) != 2 || cons.order[0] != "early" || cons.order[1] != "late" {
		t.Fatalf("order %v", cons.order)
	}
}
