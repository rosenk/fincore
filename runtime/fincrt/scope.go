package fincrt

import (
	"sync"
	"time"
)

// Scope bounds and tracks concurrent tasks (spec 8.1). Generated code uses
// *Scope: `conns := fincrt.NewScope(N)` followed by `defer conns.Wait()`.
type Scope struct {
	free   chan int // free slot indices
	wg     sync.WaitGroup
	mu     sync.Mutex
	active int
	idleCh chan struct{} // closed while active == 0
}

// Slot is a reserved index into a caller-owned pool. The zero Slot is invalid.
type Slot[T any] struct {
	idx int
	ptr *T
}

var closedCh = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// NewScope creates a scope allowing at most maxTasks concurrently held slots.
func NewScope(maxTasks uint) *Scope {
	s := &Scope{free: make(chan int, maxTasks), idleCh: closedCh}
	for i := 0; i < int(maxTasks); i++ {
		s.free <- i
	}
	return s
}

// AcquireSlot reserves a free slot, blocking until one is available or ctx is
// done. Cancellation is checked first, so a done ctx always yields an error
// even when slots are free. pool must have at least maxTasks elements;
// otherwise ErrArenaFull is returned.
func AcquireSlot[T any](s *Scope, ctx Context, pool []T) (Slot[T], error) {
	if err := ctx.Err(); err != nil {
		return Slot[T]{}, err
	}
	var idx int
	select {
	case idx = <-s.free:
	default:
		var timeout <-chan time.Time
		if !ctx.deadline.IsZero() {
			t := time.NewTimer(time.Until(ctx.deadline))
			defer t.Stop()
			timeout = t.C
		}
		select {
		case idx = <-s.free:
		case <-ctx.b().Done():
			return Slot[T]{}, ErrCancelled
		case <-timeout:
			return Slot[T]{}, ErrTimeout
		}
	}
	if idx >= len(pool) {
		s.free <- idx
		return Slot[T]{}, ErrArenaFull
	}
	return Slot[T]{idx: idx, ptr: &pool[idx]}, nil
}

// ReleaseSlot returns an unused slot (one not passed to Go). Releasing the
// zero Slot is a no-op; releasing a slot twice panics.
func ReleaseSlot[T any](s *Scope, slot Slot[T]) {
	if slot.ptr == nil {
		return
	}
	s.release(slot.idx)
}

func (s *Scope) release(idx int) {
	select {
	case s.free <- idx:
	default:
		panic("fincrt: slot released twice")
	}
}

// Go runs fn(slot buffer) in a new goroutine tracked by s. The slot is
// released automatically when fn returns.
func Go[T any](s *Scope, slot Slot[T], fn func(buf *T)) {
	if slot.ptr == nil {
		panic("fincrt: Go with invalid slot")
	}
	s.mu.Lock()
	if s.active == 0 {
		s.idleCh = make(chan struct{})
	}
	s.active++
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer func() {
			s.release(slot.idx)
			s.mu.Lock()
			s.active--
			if s.active == 0 {
				close(s.idleCh)
			}
			s.mu.Unlock()
			s.wg.Done()
		}()
		fn(slot.ptr)
	}()
}

func (s *Scope) idleChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idleCh
}

// WaitTimeout waits up to d for all tasks to finish; reports whether they did.
func (s *Scope) WaitTimeout(d Duration) bool {
	idle := s.idleChan()
	t := time.NewTimer(d.std())
	defer t.Stop()
	select {
	case <-idle:
		return true
	case <-t.C:
		select {
		case <-idle:
			return true
		default:
			return false
		}
	}
}

// Wait blocks until all tasks started with Go have finished. Idempotent.
func (s *Scope) Wait() { s.wg.Wait() }
