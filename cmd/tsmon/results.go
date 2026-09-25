package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"tailscale.com/ipn/ipnstate"
)

// Result is one finished check. Generation is the runtime release
// counter consumed by later alerting changes.
type Result struct {
	Target     *Target
	Check      string
	Kind       string
	Start      time.Time
	Generation uint64
	Latency    time.Duration
	Err        error
	Ping       *ipnstate.PingResult
}

type resultConsumer interface {
	Observe(Result)
}

type publisher struct {
	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	consumers  []resultConsumer
	generation uint64
}

func newPublisher(generation uint64) *publisher {
	if generation == 0 {
		generation = 1
	}
	return &publisher{
		locks:      map[string]*sync.Mutex{},
		generation: generation,
	}
}

func (p *publisher) add(c resultConsumer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consumers = append(p.consumers, c)
}

// publish delivers res to every consumer. A per-target mutex keeps one
// target's results in completion order.
func (p *publisher) publish(res Result) {
	key := ""
	if res.Target != nil {
		key = res.Target.Name
	}
	p.mu.Lock()
	lk := p.locks[key]
	if lk == nil {
		lk = &sync.Mutex{}
		p.locks[key] = lk
	}
	cons := append([]resultConsumer{}, p.consumers...)
	if res.Generation == 0 {
		res.Generation = p.generation
	}
	p.mu.Unlock()

	lk.Lock()
	defer lk.Unlock()
	for _, c := range cons {
		c.Observe(res)
	}
}

type debugConsumer struct {
	log *slog.Logger
}

func (d debugConsumer) Observe(res Result) {
	if d.log == nil {
		return
	}
	name := ""
	if res.Target != nil {
		name = res.Target.Name
	}
	if res.Err != nil {
		d.log.Debug("check result", "target", name, "check", res.Check, "err", res.Err.Error(), "latency", res.Latency.String())
		return
	}
	d.log.Debug("check result", "target", name, "check", res.Check, "ok", true, "latency", res.Latency.String())
}

type collectConsumer struct {
	mu  sync.Mutex
	res []Result
}

func (c *collectConsumer) Observe(res Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.res = append(c.res, res)
}

func (c *collectConsumer) snapshot() []Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Result, len(c.res))
	copy(out, c.res)
	return out
}

func (a *app) runCheck(ctx context.Context, t *Target, chk check) Result {
	start := a.clock.Now()
	res, err := chk.Run(ctx, a.env, t)
	res.Target = t
	res.Check = chk.String()
	res.Kind = chk.Kind()
	if res.Start.IsZero() {
		res.Start = start
	}
	if res.Err == nil {
		res.Err = err
	}
	if res.Err != nil && isTimeout(res.Err) && !hasTimeoutPrefix(res.Err) {
		res.Err = fmt.Errorf("timeout: %w", res.Err)
	}
	return res
}

func (a *app) execute(ctx context.Context, t *Target, chk check) Result {
	res := a.runCheck(ctx, t, chk)
	a.pub.publish(res)
	return res
}

func hasTimeoutPrefix(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "timeout:")
}
