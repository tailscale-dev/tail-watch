package main

import (
	"context"
	"strconv"
	"time"

	"tailscale.com/tstime"
)

const (
	maxBackoffShift = 20
	jitterFloor     = 0.5
	maxRetryDelay   = 5 * time.Minute
	lateAfter       = time.Minute
)

type resultClass int

const (
	resultOK resultClass = iota
	resultRetry
	resultPermanent
)

// deliveryResult is the classified outcome of one request.
type deliveryResult struct {
	Class      resultClass
	Status     int
	Reason     string
	RetryAfter time.Duration
	Err        error
}

func (r deliveryResult) String() string {
	switch {
	case r.Status != 0:
		return strconv.Itoa(r.Status)
	case r.Err != nil:
		return r.Err.Error()
	default:
		return r.Reason
	}
}

// sendMeta identifies one request. ID is the event id, or the batch id.
type sendMeta struct {
	ID string
	At time.Time
}

// sink renders and sends requests for one destination.
type sink interface {
	Name() string
	// Render builds the first-attempt body for evs. unitID is the event id of a
	// single event or the batch id of a group.
	Render(evs []Event, unitID string) ([]byte, error)
	// Send makes one request with body and classifies the result.
	Send(ctx context.Context, body []byte, m sendMeta) deliveryResult
	// MinInterval is the smallest spacing between requests, or zero.
	MinInterval() time.Duration
}

// mentionRenderer is implemented by a sink that prints mentions resolved when
// each event was queued. Sinks that do not implement it keep Render.
type mentionRenderer interface {
	RenderWithMentions(evs []Event, unitID string, mentions []string) ([]byte, error)
}

func renderBody(s sink, evs []Event, id string, mentions []string) ([]byte, error) {
	if mr, ok := s.(mentionRenderer); ok {
		return mr.RenderWithMentions(evs, id, mentions)
	}
	return s.Render(evs, id)
}

// retryDelay is min(2^(attempt-1) s, 5m) scaled by 0.5-1.5x jitter, raised to
// retryAfter, and capped at 5m.
func retryDelay(attempt int, jitter float64, retryAfter time.Duration) time.Duration {
	shift := min(max(attempt-1, 0), maxBackoffShift)
	base := min(time.Second<<shift, maxRetryDelay)
	d := time.Duration(float64(base) * (jitterFloor + jitter))
	return min(max(d, retryAfter), maxRetryDelay)
}

type outcomeKind int

const (
	outcomeDelivered outcomeKind = iota
	outcomePermanent
	outcomeExpired
	outcomeCancelled
)

type outcome struct {
	Kind   outcomeKind
	Result deliveryResult
}

// retryLoop is the one delivery loop shared by workers and notify-test. It
// paces requests for sinks that ask for it.
type retryLoop struct {
	clock  tstime.Clock
	jitter func() float64
	sink   sink
	last   time.Time
}

// run sends body until it is delivered, fails permanently, is expired or ctx
// ends. expired is asked before every attempt; onFail sees every failed attempt.
func (r *retryLoop) run(
	ctx context.Context,
	body []byte,
	id string,
	expired func() bool,
	onFail func(deliveryResult),
) outcome {
	var last deliveryResult
	for attempt := 1; ; attempt++ {
		if o, stop := r.precheck(ctx, expired, last); stop {
			return o
		}
		now := r.clock.Now()
		r.last = now
		last = r.sink.Send(ctx, body, sendMeta{ID: id, At: now})
		if o, stop := r.settle(ctx, last, attempt, onFail); stop {
			return o
		}
	}
}

// precheck stops the loop before an attempt when ctx ended, the event expired,
// or pacing was interrupted.
func (r *retryLoop) precheck(ctx context.Context, expired func() bool, last deliveryResult) (outcome, bool) {
	switch {
	case ctx.Err() != nil:
		return outcome{Kind: outcomeCancelled, Result: last}, true
	case expired != nil && expired():
		return outcome{Kind: outcomeExpired, Result: last}, true
	case !r.pace(ctx):
		return outcome{Kind: outcomeCancelled, Result: last}, true
	default:
		return outcome{}, false
	}
}

// pace waits out the sink's minimum spacing. It returns false if ctx ended.
func (r *retryLoop) pace(ctx context.Context) bool {
	iv := r.sink.MinInterval()
	if iv <= 0 || r.last.IsZero() {
		return true
	}
	wait := r.last.Add(iv).Sub(r.clock.Now())
	return wait <= 0 || sleepClock(ctx, r.clock, wait)
}

// settle turns one result into a final outcome, or sleeps and asks for a retry.
func (r *retryLoop) settle(
	ctx context.Context,
	res deliveryResult,
	attempt int,
	onFail func(deliveryResult),
) (outcome, bool) {
	switch res.Class {
	case resultOK:
		return outcome{Kind: outcomeDelivered, Result: res}, true
	case resultPermanent:
		if onFail != nil {
			onFail(res)
		}
		return outcome{Kind: outcomePermanent, Result: res}, true
	case resultRetry:
		if ctx.Err() != nil {
			return outcome{Kind: outcomeCancelled, Result: res}, true
		}
		if onFail != nil {
			onFail(res)
		}
		if !sleepClock(ctx, r.clock, retryDelay(attempt, r.jitter(), res.RetryAfter)) {
			return outcome{Kind: outcomeCancelled, Result: res}, true
		}
	}
	return outcome{}, false
}

// sleepClock waits d on clock, returning false if ctx ended first.
func sleepClock(ctx context.Context, clock tstime.Clock, d time.Duration) bool {
	t, ch := clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}
