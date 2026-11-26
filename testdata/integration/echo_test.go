//go:build unix

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Variant programs (derived from examples/tcp_echo.fc) and their fixed
// listen addresses. Keep in sync with the ListenTCP literal in each file.
const (
	srcBasic     = "testdata/integration/echo_basic.fc"
	addrBasic    = "127.0.0.1:18101"
	srcMaxConns4 = "testdata/integration/echo_maxconns4.fc"
	addrMaxConns = "127.0.0.1:18102"
	srcGraceful  = "testdata/integration/echo_graceful.fc"
	addrGraceful = "127.0.0.1:18103"
	srcDrain300  = "testdata/integration/echo_drain300ms.fc"
	addrDrain300 = "127.0.0.1:18104"
)

func dial(t testing.TB, addr string) *net.TCPConn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return c.(*net.TCPConn)
}

// roundTrip writes msg and reads back exactly len(msg) bytes.
func roundTrip(c net.Conn, msg []byte, timeout time.Duration) error {
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	defer c.SetDeadline(time.Time{})
	if _, err := c.Write(msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		return fmt.Errorf("read echo: %w", err)
	}
	if !bytes.Equal(got, msg) {
		return errors.New("echo mismatch")
	}
	return nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// TestBuildExample: examples/tcp_echo.fc (spec section 12, verbatim) must
// build, statically and with --race.
func TestBuildExample(t *testing.T) {
	forEachMode(t, func(t *testing.T, m buildMode) {
		bin := buildFC(t, "examples/tcp_echo.fc", m)
		if fi, err := os.Stat(bin); err != nil || fi.Size() == 0 {
			t.Fatalf("no binary produced at %s: %v", bin, err)
		}
	})
}

// 13.3.1 Functional echo: 50 parallel clients, streams of 1 B .. 256 KiB,
// bit-identical echo.
func TestEchoFunctional(t *testing.T) {
	forEachMode(t, func(t *testing.T, m buildMode) {
		bin := buildFC(t, srcBasic, m)
		srv := startServer(t, bin, addrBasic)

		const clients = 50
		const maxSize = 256 * 1024
		var wg sync.WaitGroup
		errs := make(chan error, clients)
		for i := 0; i < clients; i++ {
			size := int(math.Round(math.Pow(maxSize, float64(i)/float64(clients-1))))
			wg.Add(1)
			go func(i, size int) {
				defer wg.Done()
				if err := echoStream(addrBasic, i, size); err != nil {
					errs <- fmt.Errorf("client %d (%d bytes): %w", i, size, err)
				}
			}(i, size)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		srv.stopGracefully(10 * time.Second)
	})
}

func echoStream(addr string, seed, size int) error {
	data := make([]byte, size)
	rng := rand.New(rand.NewPCG(uint64(seed), 0x51ce))
	for i := range data {
		data[i] = byte(rng.Uint32())
	}
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	tc := c.(*net.TCPConn)
	_ = tc.SetDeadline(time.Now().Add(60 * time.Second))

	werr := make(chan error, 1)
	go func() {
		// Write in irregular chunks to exercise partial reads on the server.
		rem := data
		chunk := 1 + seed%7000
		for len(rem) > 0 {
			n := min(chunk, len(rem))
			if _, err := tc.Write(rem[:n]); err != nil {
				werr <- err
				return
			}
			rem = rem[n:]
			chunk = chunk*3%9973 + 1
		}
		werr <- tc.CloseWrite()
	}()
	got, rerr := io.ReadAll(tc)
	if err := <-werr; err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if rerr != nil {
		return fmt.Errorf("read: %w", rerr)
	}
	if !bytes.Equal(got, data) {
		return fmt.Errorf("echo differs: got %d bytes, want %d", len(got), len(data))
	}
	return nil
}

// 13.3.2 Backpressure with MaxConns = 4: the 5th client waits (no crash) and
// is served as soon as a slot is released.
func TestEchoBackpressure(t *testing.T) {
	forEachMode(t, func(t *testing.T, m buildMode) {
		bin := buildFC(t, srcMaxConns4, m)
		srv := startServer(t, bin, addrMaxConns)

		var conns [4]*net.TCPConn
		for i := range conns {
			conns[i] = dial(t, addrMaxConns)
			defer conns[i].Close()
			if err := roundTrip(conns[i], []byte(fmt.Sprintf("client-%d", i)), 5*time.Second); err != nil {
				t.Fatalf("client %d: %v", i, err)
			}
		}

		fifth := dial(t, addrMaxConns) // completes in the kernel backlog
		defer fifth.Close()
		msg := []byte("fifth")
		if _, err := fifth.Write(msg); err != nil {
			t.Fatalf("fifth write: %v", err)
		}
		_ = fifth.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		one := make([]byte, 1)
		if n, err := fifth.Read(one); err == nil || !isTimeout(err) {
			t.Fatalf("fifth client was served while 4 slots are busy (n=%d, err=%v)", n, err)
		}
		if srv.exited() {
			t.Fatalf("server exited under backpressure; output:\n%s", srv.output.String())
		}

		// Release one slot: the 5th client must be served promptly.
		released := time.Now()
		_ = conns[0].Close()
		_ = fifth.SetReadDeadline(time.Now().Add(3 * time.Second))
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(fifth, got); err != nil {
			t.Fatalf("fifth client not served after a slot was released: %v", err)
		}
		_ = fifth.SetReadDeadline(time.Time{})
		if !bytes.Equal(got, msg) {
			t.Fatalf("fifth echo = %q, want %q", got, msg)
		}
		t.Logf("fifth client served %v after slot release", time.Since(released))

		for i := 1; i < len(conns); i++ {
			if err := roundTrip(conns[i], []byte("still-alive"), 5*time.Second); err != nil {
				t.Fatalf("client %d after release: %v", i, err)
			}
		}
		for i := 1; i < len(conns); i++ {
			_ = conns[i].Close()
		}
		_ = fifth.Close()
		srv.stopGracefully(10 * time.Second)
	})
}

// 13.3.3 Clean graceful shutdown on SIGTERM: the in-flight frame completes,
// the client gets TCP FIN (EOF), and the process exits 0 in < 1 s.
func TestEchoGracefulShutdown(t *testing.T) {
	forEachMode(t, func(t *testing.T, m buildMode) {
		bin := buildFC(t, srcGraceful, m)
		srv := startServer(t, bin, addrGraceful)

		c := dial(t, addrGraceful)
		defer c.Close()
		if err := roundTrip(c, []byte("before-sigterm"), 5*time.Second); err != nil {
			t.Fatalf("initial round trip: %v", err)
		}

		sigAt := srv.signal(syscall.SIGTERM)
		time.Sleep(100 * time.Millisecond) // let the runtime observe the signal

		// The handler is blocked in Read on hardCtx (not drainCtx). The active
		// frame (< BufSize, so it is consumed by a single Read) must be echoed
		// completely; then the handler observes drainCtx.Done() at the top of
		// its loop, calls CloseWrite (FIN) and returns.
		frame := bytes.Repeat([]byte("0123456789abcdef"), 64) // 1 KiB
		gotEOF := false
		for attempt := 0; attempt < 5 && !gotEOF; attempt++ {
			if err := roundTrip(c, frame, 5*time.Second); err != nil {
				t.Fatalf("active frame after SIGTERM (attempt %d): %v", attempt, err)
			}
			_ = c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			var one [1]byte
			_, err := c.Read(one[:])
			switch {
			case errors.Is(err, io.EOF):
				gotEOF = true
			case isTimeout(err):
				// signal not yet observed by the handler; send another frame
			default:
				t.Fatalf("expected FIN (io.EOF) after the active frame, got %v", err)
			}
		}
		if !gotEOF {
			t.Fatal("server never half-closed the connection after SIGTERM")
		}

		code, exitAt := srv.waitExit(5 * time.Second)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; output:\n%s", code, srv.output.String())
		}
		if d := exitAt.Sub(sigAt); d >= time.Second {
			t.Fatalf("graceful shutdown took %v, want < 1s", d)
		} else {
			t.Logf("graceful shutdown in %v", d)
		}
	})
}

// 13.3.4 Forced shutdown after DrainHorizon = 300ms: a silent client is cut
// off via cancelHard() + AfterFunc after ~300 ms; exit code 0.
func TestEchoForcedShutdown(t *testing.T) {
	forEachMode(t, func(t *testing.T, m buildMode) {
		bin := buildFC(t, srcDrain300, m)
		srv := startServer(t, bin, addrDrain300)

		c := dial(t, addrDrain300)
		defer c.Close()
		// Make sure the connection is owned by a handler, then go silent.
		if err := roundTrip(c, []byte("hello"), 5*time.Second); err != nil {
			t.Fatalf("initial round trip: %v", err)
		}

		closed := make(chan time.Time, 1)
		go func() {
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			var one [1]byte
			_, err := c.Read(one[:])
			if err != nil && !isTimeout(err) {
				closed <- time.Now()
				return
			}
			closed <- time.Time{}
		}()

		sigAt := srv.signal(syscall.SIGTERM)
		code, exitAt := srv.waitExit(5 * time.Second)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; output:\n%s", code, srv.output.String())
		}
		d := exitAt.Sub(sigAt)
		lo, hi := 250*time.Millisecond, 1500*time.Millisecond
		if m.race {
			hi = 3 * time.Second
		}
		if d < lo || d > hi {
			t.Fatalf("forced shutdown took %v, want ~300ms (in [%v, %v])", d, lo, hi)
		}
		t.Logf("forced shutdown in %v", d)

		select {
		case at := <-closed:
			if at.IsZero() {
				t.Fatal("silent client was not disconnected")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("silent client was not disconnected within 5s")
		}
	})
}

// 13.3.5 No leaks: /proc/$PID/fd is stable after 200 short connections.
func TestEchoFDStability(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("SKIP: /proc/$PID/fd is Linux-only")
	}
	forEachMode(t, func(t *testing.T, m buildMode) {
		bin := buildFC(t, srcBasic, m)
		srv := startServer(t, bin, addrBasic)

		short := func(i int) {
			c := dial(t, addrBasic)
			if err := roundTrip(c, []byte(fmt.Sprintf("ping-%03d", i)), 5*time.Second); err != nil {
				t.Fatalf("connection %d: %v", i, err)
			}
			_ = c.Close()
		}
		for i := 0; i < 5; i++ { // warm up
			short(i)
		}
		baseline := waitFDs(t, srv.pid(), -1, 300*time.Millisecond)
		for i := 0; i < 200; i++ {
			short(i)
		}
		after := waitFDs(t, srv.pid(), baseline, 5*time.Second)
		if after > baseline {
			t.Fatalf("fd count grew from %d to %d after 200 connections", baseline, after)
		}
		t.Logf("fd count: baseline %d, after 200 connections %d", baseline, after)
		srv.stopGracefully(10 * time.Second)
	})
}

// waitFDs polls /proc/pid/fd until count <= target (or, for target < 0, for
// the whole duration to settle) and returns the last observed count.
func waitFDs(t testing.TB, pid, target int, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	n := -1
	for {
		entries, err := os.ReadDir(filepath.Join("/proc", fmt.Sprint(pid), "fd"))
		if err != nil {
			t.Fatalf("read /proc/%d/fd: %v", pid, err)
		}
		n = len(entries)
		if target >= 0 && n <= target {
			return n
		}
		if time.Now().After(deadline) {
			return n
		}
		time.Sleep(25 * time.Millisecond)
	}
}
