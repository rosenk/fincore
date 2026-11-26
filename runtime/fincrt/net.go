package fincrt

import (
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"time"
)

// Net is the networking capability.
type Net struct{}

func DefaultNet() Net { return Net{} }

type listener struct {
	raw    *net.TCPListener
	closed atomic.Bool
}

// Listener is a small copyable handle to a TCP listener.
// The zero Listener behaves as closed.
type Listener struct{ l *listener }

type conn struct {
	raw    *net.TCPConn
	closed atomic.Bool
}

// Conn is a small copyable handle to a TCP connection.
// The zero Conn behaves as closed.
type Conn struct{ c *conn }

// ListenTCP listens on a TCP address such as "127.0.0.1:0".
func (Net) ListenTCP(addr string) (Listener, error) {
	a, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return Listener{}, ErrIO
	}
	raw, err := net.ListenTCP("tcp", a)
	if err != nil {
		return Listener{}, ErrIO
	}
	return Listener{&listener{raw: raw}}, nil
}

// translate maps a Go I/O error to a FinCore sentinel (spec 8.2).
func translate(ctx Context, closed *atomic.Bool, err error) error {
	var ne net.Error
	switch {
	case err == nil:
		return nil
	case ctx.cancelled():
		return ErrCancelled
	case errors.Is(err, io.EOF):
		return ErrEOF
	case closed.Load() || errors.Is(err, net.ErrClosed):
		return ErrClosed
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return ErrTimeout
	default:
		return ErrIO
	}
}

// Accept waits for a connection; interruptible by ctx (cancel or deadline).
func (ln Listener) Accept(ctx Context) (Conn, error) {
	if ln.l == nil || ln.l.closed.Load() {
		return Conn{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Conn{}, err
	}
	l := ln.l
	if err := l.raw.SetDeadline(ctx.deadline); err != nil {
		return Conn{}, translate(ctx, &l.closed, err)
	}
	stop := ctx.whenDone(func() { _ = l.raw.SetDeadline(time.Now()) })
	raw, err := l.raw.AcceptTCP()
	stop()
	if err != nil {
		return Conn{}, translate(ctx, &l.closed, err)
	}
	return Conn{&conn{raw: raw}}, nil
}

// Addr returns the listener's local address ("" for a zero Listener).
func (ln Listener) Addr() string {
	if ln.l == nil {
		return ""
	}
	return ln.l.raw.Addr().String()
}

// Close closes the listener. Idempotent: later calls return nil.
func (ln Listener) Close() error {
	if ln.l == nil || ln.l.closed.Swap(true) {
		return nil
	}
	if err := ln.l.raw.Close(); err != nil {
		return ErrIO
	}
	return nil
}

// Read reads into buf; interruptible by ctx (cancel -> ErrCancelled,
// deadline -> ErrTimeout). Peer close -> ErrEOF.
func (c Conn) Read(ctx Context, buf []byte) (uint, error) {
	if c.c == nil || c.c.closed.Load() {
		return 0, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	cc := c.c
	if err := cc.raw.SetReadDeadline(ctx.deadline); err != nil {
		return 0, translate(ctx, &cc.closed, err)
	}
	stop := ctx.whenDone(func() { _ = cc.raw.SetReadDeadline(time.Now()) })
	n, err := cc.raw.Read(buf)
	stop()
	return uint(n), translate(ctx, &cc.closed, err)
}

// Write writes data; interruptible by ctx like Read. It may return n < len(data)
// together with an error.
func (c Conn) Write(ctx Context, data []byte) (uint, error) {
	if c.c == nil || c.c.closed.Load() {
		return 0, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	cc := c.c
	if err := cc.raw.SetWriteDeadline(ctx.deadline); err != nil {
		return 0, translate(ctx, &cc.closed, err)
	}
	stop := ctx.whenDone(func() { _ = cc.raw.SetWriteDeadline(time.Now()) })
	n, err := cc.raw.Write(data)
	stop()
	return uint(n), translate(ctx, &cc.closed, err)
}

// CloseWrite half-closes the connection (sends FIN).
func (c Conn) CloseWrite() error {
	if c.c == nil || c.c.closed.Load() {
		return ErrClosed
	}
	if err := c.c.raw.CloseWrite(); err != nil {
		return translate(Context{}, &c.c.closed, err)
	}
	return nil
}

// Close closes the connection. Idempotent: later calls return nil.
func (c Conn) Close() error {
	if c.c == nil || c.c.closed.Swap(true) {
		return nil
	}
	if err := c.c.raw.Close(); err != nil {
		return ErrIO
	}
	return nil
}
