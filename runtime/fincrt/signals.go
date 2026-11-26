package fincrt

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Signal is an OS signal number (FinCore Signal = u8).
type Signal uint8

const (
	SIGINT  Signal = 2
	SIGTERM Signal = 15
)

// Signals is the capability for observing OS signals.
type Signals struct{}

func DefaultSignals() Signals { return Signals{} }

// NotifyContext returns a Context cancelled when any of sigs arrives. The
// CancelFunc stops signal notification (idempotent).
// With no sigs, the Context is merely cancellable (it does NOT catch all
// signals, unlike os/signal).
func (Signals) NotifyContext(sigs ...Signal) (Context, CancelFunc) {
	if len(sigs) == 0 {
		return NewContext()
	}
	os_ := make([]os.Signal, len(sigs))
	for i, s := range sigs {
		os_[i] = syscall.Signal(s)
	}
	b, stop := signal.NotifyContext(context.Background(), os_...)
	return Context{base: b}, CancelFunc(stop)
}
