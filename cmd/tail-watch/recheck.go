package main

import (
	"context"
	"sync"
)

const recheckWorkers = 8

type recheckJob struct {
	target *Target
	check  check
}

// recheckPool runs individual checks from one FIFO with at most n workers.
type recheckPool struct {
	mu         sync.Mutex
	queue      []recheckJob
	cond       *sync.Cond
	ctx        context.Context
	run        func(*Target, check)
	done       sync.WaitGroup
	closed     bool
	stopCancel func() bool
}

func newRecheckPool(ctx context.Context, n int, run func(*Target, check)) *recheckPool {
	if ctx == nil {
		ctx = context.Background()
	}
	p := &recheckPool{mu: sync.Mutex{}, queue: nil, cond: nil, ctx: ctx,
		run: run, done: sync.WaitGroup{}, closed: false, stopCancel: nil}
	p.cond = sync.NewCond(&p.mu)
	p.stopCancel = context.AfterFunc(ctx, p.close)
	p.done.Add(max(n, 1))
	for range max(n, 1) {
		go p.loop()
	}
	return p
}

func (p *recheckPool) enqueue(t *Target) {
	if p == nil || t == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil {
		return
	}
	for _, chk := range t.Checks {
		p.queue = append(p.queue, recheckJob{target: t, check: chk})
	}
	p.cond.Broadcast()
}

func (p *recheckPool) loop() {
	defer p.done.Done()
	for {
		job, ok := p.next()
		if !ok {
			return
		}
		if p.run != nil {
			p.run(job.target, job.check)
		}
	}
}

func (p *recheckPool) next() (recheckJob, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.queue) == 0 && !p.closed && p.ctx.Err() == nil {
		p.cond.Wait()
	}
	if p.closed || p.ctx.Err() != nil {
		return recheckJob{}, false
	}
	job := p.queue[0]
	p.queue[0] = recheckJob{target: nil, check: nil}
	p.queue = p.queue[1:]
	return job, true
}

func (p *recheckPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.queue = nil
	p.cond.Broadcast()
}

func (p *recheckPool) shutdown() {
	if p == nil {
		return
	}
	p.stopCancel()
	p.close()
	p.done.Wait()
}

func newRuntimeRegistry(ctx context.Context, app *app, start checkStarter) *runtimeRegistry {
	if ctx == nil {
		ctx = context.Background()
	}
	reg := &runtimeRegistry{
		app:    app,
		alert:  nil,
		start:  start,
		mu:     sync.Mutex{},
		probes: map[string][]func() error{},
		byName: map[string]*Target{},
		known:  map[string]bool{},
		pool:   nil,
	}
	reg.pool = newRecheckPool(ctx, recheckWorkers, func(t *Target, chk check) {
		reg.runRecheck(ctx, t, chk)
	})
	return reg
}

func (r *runtimeRegistry) seed(targets []*Target) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range targets {
		if t == nil || t.Name == "" {
			continue
		}
		r.known[t.Name] = true
		r.byName[t.Name] = t
	}
}

func (r *runtimeRegistry) takeSeed(name string) bool {
	if r == nil || r.known == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.known[name] {
		return false
	}
	delete(r.known, name)
	return true
}

func (r *runtimeRegistry) enqueueRecheck(name string) {
	if r == nil || r.pool == nil {
		return
	}
	r.mu.Lock()
	t := r.byName[name]
	r.mu.Unlock()
	r.pool.enqueue(t)
}

func (r *runtimeRegistry) runRecheck(ctx context.Context, t *Target, chk check) {
	if r == nil || r.app == nil || t == nil {
		return
	}
	r.mu.Lock()
	current := r.byName[t.Name] == t
	r.mu.Unlock()
	if current {
		_ = r.app.execute(ctx, t, chk)
	}
}
