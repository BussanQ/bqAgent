package agent

import (
	"context"
	"sync"
	"time"
)

// LiveSpeedInterval throttles in-flight speed updates to keep the value
// readable. Each call's first measurable sample (after minSpeedWindow of
// generation) is reported immediately.
const LiveSpeedInterval = 3 * time.Second

type liveSpeedKey struct{}
type liveSpeedSinkKey struct{}

type liveSpeedSink struct {
	mu       sync.Mutex
	sink     func(float64)
	last     float64
	reported time.Time
}

// WithLiveSpeed reports the visible-output tokens per second of the current
// streamed call while it runs (at most every LiveSpeedInterval). When a call
// ends it reports the rate the turn metrics would carry, which may be 0 to
// retract an in-flight sample that the whole call no longer supports.
func WithLiveSpeed(ctx context.Context, sink func(float64)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, liveSpeedSinkKey{}, &liveSpeedSink{sink: sink})
}

type liveSpeedCall struct {
	shared *liveSpeedSink
}

// beginLiveSpeed returns nil for nested metrics wrappers so a call is
// sampled once.
func beginLiveSpeed(ctx context.Context) *liveSpeedCall {
	if ctx.Value(liveSpeedKey{}) != nil {
		return nil
	}
	shared, _ := ctx.Value(liveSpeedSinkKey{}).(*liveSpeedSink)
	if shared == nil {
		return nil
	}
	return &liveSpeedCall{shared: shared}
}

// due reports whether a sample at would pass the throttle, so callers can
// skip computing the estimate for most deltas.
func (call *liveSpeedCall) due(at time.Time) bool {
	if call == nil {
		return false
	}
	call.shared.mu.Lock()
	defer call.shared.mu.Unlock()
	return call.shared.reported.IsZero() || at.Sub(call.shared.reported) >= LiveSpeedInterval
}

func (call *liveSpeedCall) report(speed float64, at time.Time) {
	if call == nil || speed <= 0 {
		return
	}
	shared := call.shared
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if !shared.reported.IsZero() && at.Sub(shared.reported) < LiveSpeedInterval {
		return
	}
	shared.reported = at
	shared.emit(speed)
}

// restart retracts a failed attempt's in-flight sample and lets the retried
// response report its first sample without waiting out the throttle.
func (call *liveSpeedCall) restart() {
	if call == nil {
		return
	}
	call.shared.mu.Lock()
	defer call.shared.mu.Unlock()
	call.shared.reported = time.Time{}
	call.shared.emit(0)
}

func (call *liveSpeedCall) flush(speed float64) {
	if call == nil || speed < 0 {
		return
	}
	call.shared.mu.Lock()
	defer call.shared.mu.Unlock()
	// The next call (e.g. after a tool round) reports its first sample
	// without waiting out this call's throttle.
	call.shared.reported = time.Time{}
	call.shared.emit(speed)
}

func (shared *liveSpeedSink) emit(speed float64) {
	if speed != shared.last {
		shared.last = speed
		shared.sink(speed)
	}
}
