package fincrt

import (
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// checkLeaks fails the test if the goroutine count does not settle back to
// its starting value.
func checkLeaks(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		var now int
		for i := 0; i < 200; i++ {
			now = runtime.NumGoroutine()
			if now <= before {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		buf := make([]byte, 1<<16)
		t.Errorf("goroutine leak: before=%d after=%d\n%s", before, now, buf[:runtime.Stack(buf, true)])
	})
}

func TestErrorsDistinct(t *testing.T) {
	all := []error{ErrEOF, ErrTimeout, ErrCancelled, ErrZeroWrite, ErrClosed, ErrIO, ErrNotFound, ErrMaxDepth, ErrArenaFull, ErrExec, ErrParse}
	for i := range all {
		for j := range all {
			if (i == j) != (all[i] == all[j]) {
				t.Fatalf("errors %d and %d not distinct", i, j)
			}
		}
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := NewContext()
	if ctx.Done() || ctx.Err() != nil {
		t.Fatal("fresh context done")
	}
	cancel()
	cancel() // idempotent
	if !ctx.Done() || ctx.Err() != ErrCancelled {
		t.Fatalf("want cancelled, got %v", ctx.Err())
	}
	// Cancellation takes priority over an expired deadline.
	if ctx.WithTimeout(0).Err() != ErrCancelled {
		t.Fatal("cancel must have priority")
	}
	var zero Context
	if zero.Done() || zero.Err() != nil {
		t.Fatal("zero context must be live")
	}
}

func TestContextTimeout(t *testing.T) {
	ctx, cancel := NewContext()
	defer cancel()
	short := ctx.WithTimeout(20 * Millisecond)
	long := short.WithTimeout(10 * Second) // min deadline keeps the short one
	if !long.deadline.Equal(short.deadline) {
		t.Fatal("WithTimeout must take the min deadline")
	}
	if short.Done() {
		t.Fatal("done too early")
	}
	time.Sleep(30 * time.Millisecond)
	if !long.Done() || long.Err() != ErrTimeout {
		t.Fatalf("want ErrTimeout, got %v", long.Err())
	}
	if ctx.Done() {
		t.Fatal("parent must be unaffected")
	}
	if a := testing.AllocsPerRun(100, func() { _ = ctx.WithTimeout(Second) }); a != 0 {
		t.Fatalf("WithTimeout allocates %v", a)
	}
}

func TestDoneChan(t *testing.T) {
	checkLeaks(t)
	ctx, cancel := NewContext()
	defer cancel()
	ch, stop := ctx.WithTimeout(10 * Millisecond).DoneChan()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("DoneChan did not fire on deadline")
	}
	stop()
	ch, stop = ctx.DoneChan()
	defer stop()
	cancel()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("DoneChan did not fire on cancel")
	}
}

func TestSignals(t *testing.T) {
	ctx, stop := DefaultSignals().NotifyContext(SIGINT, SIGTERM)
	if ctx.Done() {
		t.Fatal("done before signal")
	}
	stop()
	stop()
	if ctx.Err() != ErrCancelled {
		t.Fatal("stop must cancel the context")
	}
}

func TestBuiltins(t *testing.T) {
	if !BytesEqual([]byte("ab"), StrBytes("ab")) || BytesEqual([]byte("a"), []byte("b")) {
		t.Fatal("BytesEqual")
	}
	s := "abc"
	b := StrBytes(s)
	b[0] = 'X'
	if s != "abc" || string(b) != "Xbc" {
		t.Fatal("StrBytes must return a writable copy")
	}
	if StrBytes("") != nil || BytesToStr([]byte("xy")) != "xy" {
		t.Fatal("StrBytes/BytesToStr")
	}
	dst := make([]int, 2)
	if n := Copy(dst, []int{1, 2, 3}); n != 2 || dst[1] != 2 {
		t.Fatal("Copy")
	}
	var buf [20]byte
	n, err := FormatUint(18446744073709551615, buf[:])
	if err != nil || string(buf[:n]) != "18446744073709551615" {
		t.Fatalf("FormatUint: %q %v", buf[:n], err)
	}
	if n, err := FormatUint(0, buf[:1]); err != nil || n != 1 || buf[0] != '0' {
		t.Fatal("FormatUint 0")
	}
	if _, err := FormatUint(100, buf[:2]); err != ErrArenaFull {
		t.Fatal("FormatUint small dst")
	}
	for in, want := range map[string]uint64{"0": 0, "42": 42, "18446744073709551615": 1<<64 - 1} {
		if v, err := ParseUint([]byte(in)); err != nil || v != want {
			t.Fatalf("ParseUint(%q) = %v %v", in, v, err)
		}
	}
	for _, in := range []string{"", "-1", "1a", "18446744073709551616"} {
		if _, err := ParseUint([]byte(in)); err != ErrParse {
			t.Fatalf("ParseUint(%q) want ErrParse, got %v", in, err)
		}
	}
}

func TestFS(t *testing.T) {
	fs := DefaultFS()
	dir, err := fs.MkdirTemp("fincrt-test-")
	if err != nil || !fs.IsDir(dir) {
		t.Fatal("MkdirTemp", err)
	}
	defer fs.RemoveAll(dir)
	for _, name := range []string{"b.fc", "a.fc", "c.go", "Z.fc"} {
		if err := fs.WriteFile(filepath.Join(dir, name), []byte("hello")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.fc"), 0o755); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 8)
	n, err := fs.ListSourceFiles(dir, names)
	if err != nil || n != 3 || names[0] != "Z.fc" || names[1] != "a.fc" || names[2] != "b.fc" {
		t.Fatalf("ListSourceFiles: %v %v %v", n, names, err)
	}
	if n, err := fs.ListSourceFiles(dir, names[:2]); err != ErrArenaFull || n != 2 {
		t.Fatalf("ListSourceFiles small: %v %v", n, err)
	}
	if _, err := fs.ListSourceFiles(filepath.Join(dir, "nope"), names); err != ErrNotFound {
		t.Fatal("ListSourceFiles missing", err)
	}
	buf := make([]byte, 5)
	if n, err := fs.ReadFileInto(filepath.Join(dir, "a.fc"), buf); err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("ReadFileInto exact: %v %v", n, err)
	}
	if _, err := fs.ReadFileInto(filepath.Join(dir, "a.fc"), buf[:4]); err != ErrArenaFull {
		t.Fatal("ReadFileInto small", err)
	}
	if n, err := fs.ReadFileInto(filepath.Join(dir, "a.fc"), make([]byte, 64)); err != nil || n != 5 {
		t.Fatal("ReadFileInto big", err)
	}
	if _, err := fs.ReadFileInto(filepath.Join(dir, "missing"), buf); err != ErrNotFound {
		t.Fatal("ReadFileInto missing", err)
	}
	if fs.IsDir(filepath.Join(dir, "a.fc")) {
		t.Fatal("IsDir on file")
	}
	if err := fs.RemoveAll(dir); err != nil || fs.IsDir(dir) {
		t.Fatal("RemoveAll", err)
	}
}

func TestEnvStdio(t *testing.T) {
	e := DefaultEnv()
	if e.ArgsCount() != uint(len(os.Args)) || e.Arg(0) != os.Args[0] || e.Arg(1<<20) != "" {
		t.Fatal("Args")
	}
	t.Setenv("FINCRT_TEST_VAR", "v")
	if v, ok := e.GetEnv("FINCRT_TEST_VAR"); !ok || v != "v" {
		t.Fatal("GetEnv")
	}
	if _, ok := e.GetEnv("FINCRT_TEST_NO_SUCH_VAR"); ok {
		t.Fatal("GetEnv missing")
	}
	if DefaultStdio().WriteStdout(nil) != nil || DefaultStdio().WriteStderr(nil) != nil {
		t.Fatal("Stdio")
	}
}

func TestExec(t *testing.T) {
	ex := DefaultExec()
	ctx, cancel := NewContext()
	defer cancel()
	sh, err := execLook("sh")
	if err != nil {
		t.Skip("no sh")
	}
	buf := make([]byte, 4)
	st, n, err := ex.Run(ctx, sh, []string{"-c", "printf abcdefgh >&2; exit 3"}, []string{"X=1"}, t.TempDir(), buf)
	if err != nil || st.Code != 3 || n != 4 || string(buf) != "abcd" {
		t.Fatalf("Run: %v %v %q %v", st, n, buf[:n], err)
	}
	st, _, err = ex.Run(ctx, sh, []string{"-c", `test "$FINCRT_X" = 7`}, []string{"FINCRT_X=7"}, "", nil)
	if err != nil || st.Code != 0 {
		t.Fatalf("Run env: %v %v", st, err)
	}
	if _, _, err := ex.Run(ctx, "/nonexistent/binary", nil, nil, "", nil); err != ErrExec {
		t.Fatal("want ErrExec", err)
	}
	start := time.Now()
	if _, _, err := ex.Run(ctx.WithTimeout(100*Millisecond), sh, []string{"-c", "sleep 10"}, nil, "", nil); err != ErrTimeout {
		t.Fatal("want ErrTimeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not kill process")
	}
	if gobin, err := execLook("go"); err == nil {
		st, _, err := ex.Run(ctx, gobin, []string{"version"}, nil, "", make([]byte, 256))
		if err != nil || st.Code != 0 {
			t.Fatalf("go version: %v %v", st, err)
		}
	}
	cancel()
	if _, _, err := ex.Run(ctx, sh, []string{"-c", "true"}, nil, "", nil); err != ErrCancelled {
		t.Fatal("want ErrCancelled", err)
	}
}

func TestScopeBackpressure(t *testing.T) {
	checkLeaks(t)
	ctx, cancel := NewContext()
	defer cancel()
	s := NewScope(4)
	defer s.Wait()
	var pool [4][16]byte
	release := make(chan struct{})
	var running atomic.Int32
	for i := 0; i < 4; i++ {
		slot, err := AcquireSlot(s, ctx, pool[:])
		if err != nil {
			t.Fatal(err)
		}
		Go(s, slot, func(buf *[16]byte) {
			buf[0] = 1
			running.Add(1)
			<-release
		})
	}
	got := make(chan error, 1)
	go func() {
		slot, err := AcquireSlot(s, ctx, pool[:])
		if err == nil {
			ReleaseSlot(s, slot)
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("5th AcquireSlot did not block: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if s.WaitTimeout(10 * Millisecond) {
		t.Fatal("WaitTimeout true while busy")
	}
	close(release)
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5th AcquireSlot never unblocked")
	}
	if !s.WaitTimeout(5 * Second) {
		t.Fatal("WaitTimeout false after drain")
	}
	s.Wait()
	if running.Load() != 4 {
		t.Fatal("tasks did not run")
	}
	// All 4 slots are free again.
	for i := 0; i < 4; i++ {
		if _, err := AcquireSlot(s, ctx, pool[:]); err != nil {
			t.Fatal(err)
		}
	}
	// A blocked AcquireSlot is woken by cancellation and by deadline.
	if _, err := AcquireSlot(s, ctx.WithTimeout(20*Millisecond), pool[:]); err != ErrTimeout {
		t.Fatal("want ErrTimeout", err)
	}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := AcquireSlot(s, ctx, pool[:]); err != ErrCancelled {
		t.Fatal("want ErrCancelled", err)
	}
}

func TestAcquireCancelPriority(t *testing.T) {
	ctx, cancel := NewContext()
	s := NewScope(4)
	var pool [4]int
	cancel()
	for i := 0; i < 100; i++ {
		if _, err := AcquireSlot(s, ctx, pool[:]); err != ErrCancelled {
			t.Fatal("cancel must win even with free slots", err)
		}
	}
	if !s.WaitTimeout(0) {
		t.Fatal("empty scope is idle")
	}
	s.Wait()
	ctx2, cancel2 := NewContext()
	defer cancel2()
	if _, err := AcquireSlot(NewScope(2), ctx2, pool[:1]); err != nil {
		t.Fatal(err) // slot 0 fits
	}
}

func TestScopeIdleReopen(t *testing.T) {
	checkLeaks(t)
	ctx, cancel := NewContext()
	defer cancel()
	s := NewScope(2)
	defer s.Wait()
	var pool [2]int
	for round := 0; round < 3; round++ {
		block := make(chan struct{})
		slot, err := AcquireSlot(s, ctx, pool[:])
		if err != nil {
			t.Fatal(err)
		}
		Go(s, slot, func(p *int) { *p++; <-block })
		if s.WaitTimeout(5 * Millisecond) {
			t.Fatal("idle while task running")
		}
		close(block)
		if !s.WaitTimeout(5 * Second) {
			t.Fatal("not idle after task done")
		}
	}
	slot, _ := AcquireSlot(s, ctx, pool[:])
	ReleaseSlot(s, slot)
	ReleaseSlot(s, Slot[int]{})
	defer func() {
		if recover() == nil {
			t.Fatal("double release must panic")
		}
	}()
	ReleaseSlot(s, slot)
}
