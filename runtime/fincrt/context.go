package fincrt

import (
	"context"
	"math"
	"time"
)

// Duration is a span of time in nanoseconds (FinCore Duration = u64).
type Duration uint64

const (
	Nanosecond  Duration = 1
	Microsecond Duration = 1_000
	Millisecond Duration = 1_000_000
	Second      Duration = 1_000_000_000
)

func (d Duration) std() time.Duration {
	if d > math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(d)
}

// CancelFunc cancels a Context. It is idempotent and safe for concurrent use.
type CancelFunc func()

// Context is a small value type: a cancellable base plus an optional absolute
// deadline (zero = none). WithTimeout never allocates or starts timers.
// The zero Context is valid and never cancelled.
type Context struct {
	base     context.Context
	deadline time.Time
}

// NewContext returns a fresh cancellable Context with no deadline.
func NewContext() (Context, CancelFunc) {
	b, cancel := context.WithCancel(context.Background())
	return Context{base: b}, CancelFunc(cancel)
}

func (c Context) b() context.Context {
	if c.base == nil {
		return context.Background()
	}
	return c.base
}

// WithTimeout returns a copy of c whose deadline is min(c.deadline, now+d).
// Zero allocations; no timers.
func (c Context) WithTimeout(d Duration) Context {
	dl := time.Now().Add(d.std())
	if !c.deadline.IsZero() && c.deadline.Before(dl) {
		dl = c.deadline
	}
	c.deadline = dl
	return c
}

func (c Context) cancelled() bool { return c.base != nil && c.base.Err() != nil }

func (c Context) expired() bool {
	return !c.deadline.IsZero() && !time.Now().Before(c.deadline)
}

// Done reports whether c is cancelled or its deadline has passed.
func (c Context) Done() bool { return c.cancelled() || c.expired() }

// Err returns ErrCancelled if the base was cancelled (takes priority),
// ErrTimeout if the deadline has passed, nil otherwise.
func (c Context) Err() error {
	if c.cancelled() {
		return ErrCancelled
	}
	if c.expired() {
		return ErrTimeout
	}
	return nil
}

// DoneChan returns a channel closed when c is cancelled or its deadline
// passes, together with a stop function that MUST be called (typically via
// defer) to release resources. Without a deadline it is the base Done channel
// (nil for the zero Context, i.e. never fires) and stop is a no-op.
func (c Context) DoneChan() (<-chan struct{}, func()) {
	if c.deadline.IsZero() {
		return c.b().Done(), func() {}
	}
	dctx, cancel := context.WithDeadline(c.b(), c.deadline)
	return dctx.Done(), cancel
}

// whenDone arranges for f to be called when c's base is cancelled. The
// returned stop function unregisters f and, if f already started, waits for
// it to finish, so f never runs after stop returns.
func (c Context) whenDone(f func()) (stop func()) {
	if c.base == nil || c.base.Done() == nil {
		return func() {}
	}
	fired := make(chan struct{})
	s := context.AfterFunc(c.base, func() { f(); close(fired) })
	return func() {
		if !s() {
			<-fired
		}
	}
}
