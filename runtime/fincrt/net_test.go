package fincrt

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// pair returns a connected (server Conn, client net.Conn) over loopback.
func pair(t *testing.T) (Listener, Conn, net.Conn) {
	t.Helper()
	ln, err := DefaultNet().ListenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cl, err := net.Dial("tcp", ln.Addr())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := NewContext()
	defer cancel()
	c, err := ln.Accept(ctx.WithTimeout(5 * Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); cl.Close(); ln.Close() })
	return ln, c, cl
}

func TestEchoRoundTrip(t *testing.T) {
	checkLeaks(t)
	_, c, cl := pair(t)
	ctx, cancel := NewContext()
	defer cancel()
	msg := bytes.Repeat([]byte("x"), 1<<16)
	go func() { cl.Write(msg); cl.(*net.TCPConn).CloseWrite() }()
	var got []byte
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(ctx.WithTimeout(5*Second), buf)
		got = append(got, buf[:n]...)
		if err == ErrEOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got, msg) {
		t.Fatal("data mismatch")
	}
	if n, err := c.Write(ctx, []byte("bye")); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(cl)
	if err != nil || string(rest) != "bye" {
		t.Fatalf("%q %v", rest, err)
	}
}

func TestReadCancel(t *testing.T) {
	checkLeaks(t)
	_, c, _ := pair(t)
	ctx, cancel := NewContext()
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	start := time.Now()
	// A long deadline is present but cancellation must win -> ErrCancelled.
	_, err := c.Read(ctx.WithTimeout(10*Second), make([]byte, 8))
	if err != ErrCancelled {
		t.Fatal("want ErrCancelled", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancel too slow")
	}
	// Conn remains usable with a fresh context (no stale deadline).
	ctx2, cancel2 := NewContext()
	defer cancel2()
	if _, err := c.Write(ctx2, []byte("ok")); err != nil {
		t.Fatal(err)
	}
}

func TestReadDeadline(t *testing.T) {
	checkLeaks(t)
	_, c, cl := pair(t)
	ctx, cancel := NewContext()
	defer cancel()
	start := time.Now()
	_, err := c.Read(ctx.WithTimeout(50*Millisecond), make([]byte, 8))
	if err != ErrTimeout {
		t.Fatal("want ErrTimeout", err)
	}
	if el := time.Since(start); el < 40*time.Millisecond || el > 3*time.Second {
		t.Fatal("bad timeout duration", el)
	}
	// Deadline must not stick: a read without deadline still works.
	go func() { time.Sleep(80 * time.Millisecond); cl.Write([]byte("hi")) }()
	buf := make([]byte, 8)
	n, err := c.Read(ctx, buf)
	if err != nil || string(buf[:n]) != "hi" {
		t.Fatalf("%q %v", buf[:n], err)
	}
	// Already-expired context fails fast.
	exp := ctx.WithTimeout(0)
	if _, err := c.Read(exp, buf); err != ErrTimeout {
		t.Fatal(err)
	}
	if _, err := c.Write(exp, buf); err != ErrTimeout {
		t.Fatal(err)
	}
}

func TestWriteCancel(t *testing.T) {
	checkLeaks(t)
	_, c, _ := pair(t) // client never reads -> buffers fill and Write blocks
	ctx, cancel := NewContext()
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	big := make([]byte, 64<<20)
	_, err := c.Write(ctx, big)
	if err != ErrCancelled {
		t.Fatal("want ErrCancelled", err)
	}
}

func TestAcceptCancel(t *testing.T) {
	checkLeaks(t)
	ln, err := DefaultNet().ListenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := NewContext()
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if _, err := ln.Accept(ctx); err != ErrCancelled {
		t.Fatal("want ErrCancelled", err)
	}
	if _, err := ln.Accept(ctx); err != ErrCancelled {
		t.Fatal("pre-cancelled", err)
	}
	ctx2, cancel2 := NewContext()
	defer cancel2()
	if _, err := ln.Accept(ctx2.WithTimeout(30 * Millisecond)); err != ErrTimeout {
		t.Fatal("want ErrTimeout", err)
	}
	// Listener still works afterwards.
	go func() {
		if cl, err := net.Dial("tcp", ln.Addr()); err == nil {
			cl.Close()
		}
	}()
	c, err := ln.Accept(ctx2.WithTimeout(5 * Second))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestAcceptUnblockedByClose(t *testing.T) {
	checkLeaks(t)
	ln, err := DefaultNet().ListenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := NewContext()
	defer cancel()
	go func() { time.Sleep(30 * time.Millisecond); ln.Close() }()
	if _, err := ln.Accept(ctx); err != ErrClosed {
		t.Fatal("want ErrClosed", err)
	}
}

func TestIdempotentClose(t *testing.T) {
	_, c, _ := pair(t)
	ln, err := DefaultNet().ListenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln2 := ln // copies share state
	if ln.Close() != nil || ln.Close() != nil || ln2.Close() != nil {
		t.Fatal("listener Close not idempotent")
	}
	ctx, cancel := NewContext()
	defer cancel()
	if _, err := ln2.Accept(ctx); err != ErrClosed {
		t.Fatal(err)
	}
	c2 := c
	if c.Close() != nil || c.Close() != nil || c2.Close() != nil {
		t.Fatal("conn Close not idempotent")
	}
	if _, err := c2.Read(ctx, make([]byte, 1)); err != ErrClosed {
		t.Fatal(err)
	}
	if _, err := c2.Write(ctx, []byte("x")); err != ErrClosed {
		t.Fatal(err)
	}
	if c2.CloseWrite() != ErrClosed {
		t.Fatal("CloseWrite after Close")
	}
	var zc Conn
	var zl Listener
	if zc.Close() != nil || zl.Close() != nil || zl.Addr() != "" {
		t.Fatal("zero handles")
	}
	if _, err := zc.Read(ctx, nil); err != ErrClosed {
		t.Fatal(err)
	}
}

// TestEchoServerShape mirrors the tcp_echo program (spec 12) with MaxConns=4:
// parallel clients, backpressure, graceful drain and forced shutdown.
func TestEchoServerShape(t *testing.T) {
	checkLeaks(t)
	ln, err := DefaultNet().ListenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	drainCtx, stopDrain := NewContext()
	defer stopDrain()
	hardCtx, cancelHard := NewContext()
	defer cancelHard()
	var pool [4][4096]byte
	done := make(chan bool)
	go func() {
		forced := false
		conns := NewScope(4)
		defer conns.Wait()
		for {
			slot, err := AcquireSlot(conns, drainCtx, pool[:])
			if err != nil {
				break
			}
			conn, err := ln.Accept(drainCtx)
			if err != nil {
				ReleaseSlot(conns, slot)
				if drainCtx.Done() {
					break
				}
				continue
			}
			Go(conns, slot, func(buf *[4096]byte) {
				defer conn.Close()
				for {
					if drainCtx.Done() {
						_ = conn.CloseWrite()
						return
					}
					n, err := conn.Read(hardCtx.WithTimeout(30*Second), buf[:])
					if err != nil {
						return
					}
					for rem := buf[:n]; len(rem) > 0; {
						m, err := conn.Write(hardCtx, rem)
						if err != nil || m == 0 {
							return
						}
						rem = rem[m:]
					}
				}
			})
		}
		_ = ln.Close()
		if !conns.WaitTimeout(300 * Millisecond) {
			forced = true
			cancelHard()
			conns.Wait()
		}
		done <- forced
	}()

	// 20 parallel echo clients in waves (only 4 served concurrently).
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cl, err := net.Dial("tcp", ln.Addr())
			if err != nil {
				t.Error(err)
				return
			}
			defer cl.Close()
			msg := bytes.Repeat([]byte{byte(i)}, 1+i*1000)
			go cl.Write(msg)
			got := make([]byte, len(msg))
			cl.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.ReadFull(cl, got); err != nil || !bytes.Equal(got, msg) {
				t.Errorf("client %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// A silent client forces the hard shutdown path.
	silent, err := net.Dial("tcp", ln.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	stopDrain()
	select {
	case forced := <-done:
		if !forced {
			t.Fatal("expected forced shutdown")
		}
		if el := time.Since(start); el < 250*time.Millisecond || el > 3*time.Second {
			t.Fatal("bad shutdown time", el)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down")
	}
	silent.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := silent.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("silent client should see EOF", err)
	}
}
