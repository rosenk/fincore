package fincrt

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FS is the file-system capability.
type FS struct{}

// Env is the command-line/environment capability.
type Env struct{}

// Stdio is the standard output/error capability.
type Stdio struct{}

// Exec is the subprocess capability.
type Exec struct{}

// ExitStatus is the result of a subprocess (Code -1 if it did not exit normally).
type ExitStatus struct {
	Code int
}

func DefaultFS() FS       { return FS{} }
func DefaultEnv() Env     { return Env{} }
func DefaultStdio() Stdio { return Stdio{} }
func DefaultExec() Exec   { return Exec{} }

func fsErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	default:
		return ErrIO
	}
}

// ReadFileInto reads the whole file into dst and returns its length.
// ErrArenaFull if the file does not fit; ErrNotFound if it does not exist.
func (FS) ReadFileInto(path string, dst []byte) (uint, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fsErr(err)
	}
	defer f.Close()
	n, err := io.ReadFull(f, dst)
	switch {
	case err == io.EOF || err == io.ErrUnexpectedEOF:
		return uint(n), nil
	case err != nil:
		return uint(n), ErrIO
	}
	var one [1]byte
	m, err := f.Read(one[:])
	if m > 0 {
		return uint(n), ErrArenaFull
	}
	if err != nil && err != io.EOF {
		return uint(n), ErrIO
	}
	return uint(n), nil
}

// WriteFile creates or truncates path and writes data (mode 0644).
func (FS) WriteFile(path string, data []byte) error {
	return fsErr(os.WriteFile(path, data, 0o644))
}

// IsDir reports whether path exists and is a directory.
func (FS) IsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// ListSourceFiles stores the names (not paths) of the non-directory `.fc`
// entries of dir into dst, sorted lexicographically by bytes, and returns
// their count. If dst is too small it is filled and ErrArenaFull is returned.
func (FS) ListSourceFiles(dir string, dst []string) (uint, error) {
	ents, err := os.ReadDir(dir) // sorted by filename
	if err != nil {
		return 0, fsErr(err)
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".fc") {
			continue
		}
		if n == len(dst) {
			return uint(n), ErrArenaFull
		}
		dst[n] = e.Name()
		n++
	}
	return uint(n), nil
}

// MkdirTemp creates a fresh directory in the OS temp dir.
func (FS) MkdirTemp(prefix string) (string, error) {
	d, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", fsErr(err)
	}
	return d, nil
}

// RemoveAll removes path recursively; a missing path is not an error.
func (FS) RemoveAll(path string) error { return fsErr(os.RemoveAll(path)) }

// ArgsCount returns len(os.Args) (including the program name at index 0).
func (Env) ArgsCount() uint { return uint(len(os.Args)) }

// Arg returns os.Args[i], or "" if out of range.
func (Env) Arg(i uint) string {
	if i >= uint(len(os.Args)) {
		return ""
	}
	return os.Args[i]
}

// ExecutableKey is the one key that Env.GetEnv answers itself instead of
// looking it up in the process environment: the value is Executable().
// It lets FinCore programs (the self-hosted compiler locating its install
// directory) learn their own path through the existing Env capability.
const ExecutableKey = "FINCRT_EXECUTABLE"

// GetEnv looks up an environment variable. The key ExecutableKey is not
// read from the environment: it returns Executable() (false if unknown).
func (Env) GetEnv(key string) (string, bool) {
	if key == ExecutableKey {
		p := Executable()
		return p, p != ""
	}
	return os.LookupEnv(key)
}

// Executable returns the absolute path of the running executable with
// symlinks resolved (os.Executable + filepath.EvalSymlinks), or "" if the
// OS cannot tell.
func Executable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}

func (Stdio) WriteStdout(data []byte) error { return writeAllTo(os.Stdout, data) }

// WriteStderr writes data to stderr. A non-empty write means the program
// reports its own failures: a failing main then exits 1 without the
// `error: <err>` line (sentinel.Error in errors.go).
func (Stdio) WriteStderr(data []byte) error {
	if len(data) > 0 {
		stderrUsed.Store(true)
	}
	return writeAllTo(os.Stderr, data)
}

func writeAllTo(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return ErrIO
	}
	return nil
}

// boundedWriter keeps the first len(buf) bytes written and drops the rest.
type boundedWriter struct {
	buf []byte
	n   int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.n += copy(w.buf[w.n:], p)
	return len(p), nil
}

// Run executes bin with args in workdir ("" = current dir). env entries
// ("K=V") are appended to the parent environment (empty = inherit as is).
// Stdout is forwarded to os.Stdout; stderr is captured into stderrBuf
// (truncated if longer) and the captured length is returned.
// A non-zero exit is reported via ExitStatus with a nil error. Errors:
// ErrExec if the process cannot be started, ErrCancelled/ErrTimeout if ctx
// ends first (the process is killed).
func (Exec) Run(ctx Context, bin string, args []string, env []string, workdir string, stderrBuf []byte) (ExitStatus, uint, error) {
	if err := ctx.Err(); err != nil {
		return ExitStatus{Code: -1}, 0, err
	}
	gctx := ctx.b()
	if !ctx.deadline.IsZero() {
		var cancel context.CancelFunc
		gctx, cancel = context.WithDeadline(gctx, ctx.deadline)
		defer cancel()
	}
	cmd := exec.CommandContext(gctx, bin, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Dir = workdir
	cmd.Stdout = os.Stdout
	w := &boundedWriter{buf: stderrBuf}
	cmd.Stderr = w
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return ExitStatus{Code: -1}, 0, ErrExec
	}
	err := cmd.Wait()
	n := uint(w.n)
	if cerr := ctx.Err(); cerr != nil && err != nil {
		return ExitStatus{Code: -1}, n, cerr
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
		return ExitStatus{Code: 0}, n, nil
	case errors.As(err, &ee) && ee.ExitCode() >= 0:
		return ExitStatus{Code: ee.ExitCode()}, n, nil
	default:
		return ExitStatus{Code: -1}, n, ErrExec
	}
}

// Clock is the time capability (spec 4.1). It has no methods in v0.1; it
// exists so that `main(clk Clock)` can be injected by generated code.
type Clock struct{}

// DefaultClock returns the Clock capability.
func DefaultClock() Clock { return Clock{} }
