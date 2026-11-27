//go:build unix

// Package integration holds the end-to-end conformance tests of spec 13.3
// (TCP echo) and a driver for the testdata/pass and testdata/fail corpus.
//
// Run from the module root:
//
//	go test ./testdata/integration/ -v
//
// Environment:
//
//	FINC0=/path/to/finc0   use a prebuilt finc0 instead of `go build ./bootstrap/cmd/finc0`
//	FINC_RACE=0|1          0: only static builds, 1: only --race builds, unset: both
//
// Every test skips (not fails) while the compiler is not ready: when finc0
// cannot be built, or when `finc0 build`/`finc0 check` reports "not implemented".
package integration

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var workDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "finc-integration-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp:", err)
		os.Exit(2)
	}
	workDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Locating the module root and finc0.

var (
	rootOnce sync.Once
	rootDir  string
	rootErr  error
)

func moduleRoot(t testing.TB) string {
	t.Helper()
	rootOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			rootErr = err
			return
		}
		for {
			data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
			if err == nil && strings.Contains(string(data), "module fincore") {
				rootDir = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				rootErr = errors.New("go.mod for module fincore not found above the test directory")
				return
			}
			dir = parent
		}
	})
	if rootErr != nil {
		t.Fatalf("module root: %v", rootErr)
	}
	return rootDir
}

var (
	finc0Once sync.Once
	finc0Path string
	finc0Err  error
)

// requireFinc0 returns the path of a finc0 binary, building it once from
// ./bootstrap/cmd/finc0. It skips the test if that is not possible yet.
func requireFinc0(t testing.TB) string {
	t.Helper()
	root := moduleRoot(t)
	finc0Once.Do(func() {
		if p := os.Getenv("FINC0"); p != "" {
			finc0Path = p
			return
		}
		if _, err := os.Stat(filepath.Join(root, "bootstrap", "cmd", "finc0")); err != nil {
			finc0Err = fmt.Errorf("bootstrap/cmd/finc0 does not exist: %v", err)
			return
		}
		out := filepath.Join(workDir, "finc0")
		cmd := exec.Command("go", "build", "-o", out, "./bootstrap/cmd/finc0")
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			finc0Err = fmt.Errorf("go build ./bootstrap/cmd/finc0: %v\n%s", err, b)
			return
		}
		finc0Path = out
	})
	if finc0Err != nil {
		t.Skipf("SKIP: finc0 compiler not available yet: %v", finc0Err)
	}
	return finc0Path
}

func notImplemented(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "not implemented") || strings.Contains(s, "unimplemented")
}

// runFinc0 runs finc0 with args from the module root and returns its
// combined output and exit code. It skips the test on "not implemented".
func runFinc0(t testing.TB, args ...string) ([]byte, int) {
	t.Helper()
	bin := requireFinc0(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running finc0 %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	if code != 0 && notImplemented(out) {
		t.Skipf("SKIP: finc0 %s: not implemented yet:\n%s", strings.Join(args, " "), out)
	}
	return out, code
}

// ---------------------------------------------------------------------------
// Build modes (static / --race).

type buildMode struct {
	name string
	race bool
}

func buildModes() []buildMode {
	switch strings.ToLower(os.Getenv("FINC_RACE")) {
	case "0", "false", "no", "off":
		return []buildMode{{"static", false}}
	case "1", "true", "yes", "on", "only":
		return []buildMode{{"race", true}}
	}
	return []buildMode{{"static", false}, {"race", true}}
}

// forEachMode runs fn as a subtest per build mode, sequentially (the
// servers listen on ports the kernel picks, so this is not needed for port
// isolation, but it keeps the timing tests free of CPU contention).
func forEachMode(t *testing.T, fn func(t *testing.T, m buildMode)) {
	for _, m := range buildModes() {
		t.Run(m.name, func(t *testing.T) { fn(t, m) })
	}
}

var (
	buildMu    sync.Mutex
	buildCache = map[string]string{}
)

// buildFC compiles a FinCore program (path relative to the module root)
// with `finc0 build [--race] -o <bin> <src>` and returns the binary path.
func buildFC(t testing.TB, src string, m buildMode, extraFlags ...string) string {
	t.Helper()
	key := fmt.Sprintf("%s|%v|%v", src, m.race, extraFlags)
	buildMu.Lock()
	defer buildMu.Unlock()
	if bin, ok := buildCache[key]; ok {
		return bin
	}
	name := strings.NewReplacer("/", "_", ".", "_").Replace(src)
	bin := filepath.Join(workDir, fmt.Sprintf("%s-%s-%d", name, m.name, len(buildCache)))
	args := []string{"build"}
	if m.race {
		args = append(args, "--race")
	}
	args = append(args, extraFlags...)
	args = append(args, "-o", bin, src)
	out, code := runFinc0(t, args...)
	if code != 0 {
		if m.race && (strings.Contains(string(out), "cgo") || strings.Contains(string(out), "-race is not supported")) {
			t.Skipf("SKIP: --race build unsupported on this host:\n%s", out)
		}
		t.Fatalf("finc0 %s failed (exit %d):\n%s", strings.Join(args, " "), code, out)
	}
	buildCache[key] = bin
	return bin
}

// ---------------------------------------------------------------------------
// Server process management.

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type server struct {
	t      testing.TB
	addr   string
	cmd    *exec.Cmd
	output *syncBuffer
	done   chan struct{}
	exitAt time.Time
	state  *os.ProcessState
	err    error
}

// listenPrefix starts the line in which an echo program reports the
// address it is bound to.
const listenPrefix = "listening on "

// startServer runs bin with the listen address 127.0.0.1:0, so the kernel
// picks a free port (concurrent runs of the suite never collide), and waits
// for the program to report its bound address (`listening on <addr>` on
// stdout) and to accept a connection there. The address is s.addr.
func startServer(t testing.TB, bin string) *server {
	t.Helper()
	s := &server{t: t, output: &syncBuffer{}, done: make(chan struct{})}
	s.cmd = exec.Command(bin, "127.0.0.1:0")
	s.cmd.Stdout = s.output
	s.cmd.Stderr = s.output
	if err := s.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	go func() {
		s.err = s.cmd.Wait()
		s.exitAt = time.Now()
		s.state = s.cmd.ProcessState
		close(s.done)
	}()
	t.Cleanup(func() {
		select {
		case <-s.done:
		default:
			_ = s.cmd.Process.Kill()
			<-s.done
		}
		if strings.Contains(s.output.String(), "DATA RACE") {
			t.Errorf("server reported a data race:\n%s", s.output.String())
		}
	})

	deadline := time.Now().Add(15 * time.Second)
	for {
		if s.addr == "" {
			s.addr = reportedAddr(s.output.String())
		}
		if s.addr != "" {
			c, err := net.DialTimeout("tcp", s.addr, 200*time.Millisecond)
			if err == nil {
				_ = c.Close()
				return s
			}
		}
		select {
		case <-s.done:
			t.Fatalf("server exited before listening (%v); output:\n%s", s.err, s.output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not listen within 15s (reported address %q); output:\n%s", s.addr, s.output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// reportedAddr returns the address of the first complete
// `listening on <addr>` line of out, or "".
func reportedAddr(out string) string {
	for {
		line, rest, ok := strings.Cut(out, "\n")
		if !ok {
			return ""
		}
		if a, found := strings.CutPrefix(line, listenPrefix); found {
			return strings.TrimSpace(a)
		}
		out = rest
	}
}

func (s *server) pid() int { return s.cmd.Process.Pid }

func (s *server) signal(sig syscall.Signal) time.Time {
	s.t.Helper()
	now := time.Now()
	if err := s.cmd.Process.Signal(sig); err != nil {
		s.t.Fatalf("signal %v: %v", sig, err)
	}
	return now
}

func (s *server) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// waitExit waits up to timeout for the process to exit and returns its exit
// code and exit time.
func (s *server) waitExit(timeout time.Duration) (int, time.Time) {
	s.t.Helper()
	select {
	case <-s.done:
	case <-time.After(timeout):
		s.t.Fatalf("server did not exit within %v; output:\n%s", timeout, s.output.String())
	}
	code := -1
	if s.state != nil {
		code = s.state.ExitCode()
	}
	return code, s.exitAt
}

// stopGracefully sends SIGTERM and requires exit code 0.
func (s *server) stopGracefully(timeout time.Duration) {
	s.t.Helper()
	s.signal(syscall.SIGTERM)
	code, _ := s.waitExit(timeout)
	if code != 0 {
		s.t.Fatalf("server exit code = %d, want 0; output:\n%s", code, s.output.String())
	}
}
