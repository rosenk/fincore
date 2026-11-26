package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"fincore/bootstrap/internal/check"
	"fincore/bootstrap/internal/codegen/golang"
	"fincore/bootstrap/internal/ir"
)

// buildOptions are the flags shared by build, run and test.
type buildOptions struct {
	Out      string // -o
	EmitGo   string // --emit-go
	Race     bool
	Trimpath bool
	Fuel     uint64
}

// compileGo runs the front end and the Go code generator on src. On
// compile errors the diagnostics are printed to stderr and ok is false.
func compileGo(env *Env, src string, fuel uint64) (goSrc []byte, ok bool) {
	res, err := check.Run(src, check.Options{Fuel: fuel})
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return nil, false
	}
	if len(res.Diags) > 0 {
		printDiags(env.Stderr, res.Diags)
		return nil, false
	}
	prog, err := ir.Lower(res.Prog, res.Info)
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return nil, false
	}
	out, err := golang.Generate(prog)
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return nil, false
	}
	return out, true
}

// buildBinary compiles src into the binary opts.Out. It returns false
// after printing errors to env.Stderr.
func buildBinary(env *Env, src string, opts buildOptions) bool {
	goSrc, ok := compileGo(env, src, opts.Fuel)
	if !ok {
		return false
	}
	if opts.EmitGo != "" {
		if err := os.WriteFile(opts.EmitGo, goSrc, 0o644); err != nil {
			fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
			return false
		}
	}
	root, err := findModuleRoot()
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return false
	}
	out, err := filepath.Abs(opts.Out)
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return false
	}
	if err := goBuild(env, root, goSrc, out, opts); err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return false
	}
	return true
}

var goDirectiveRE = regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)

// goBuild writes goSrc into a temporary module that requires the fincore
// module (via a replace directive pointing at root) and runs `go build`.
func goBuild(env *Env, root string, goSrc []byte, out string, opts buildOptions) error {
	dir, err := os.MkdirTemp("", "finc0-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	goVersion := "1.22"
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		if m := goDirectiveRE.FindSubmatch(data); m != nil {
			goVersion = string(m[1])
		}
	}
	mod := fmt.Sprintf("module fincprog\n\ngo %s\n\nrequire fincore v0.0.0\n\nreplace fincore => %s\n", goVersion, root)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), goSrc, 0o644); err != nil {
		return err
	}
	if opts.Race {
		if err := os.WriteFile(filepath.Join(dir, "zz_race.go"), []byte(raceInitSrc), 0o644); err != nil {
			return err
		}
	}
	args := []string{"build", "-o", out}
	if opts.Race {
		args = append(args, "-race")
	}
	if opts.Trimpath {
		args = append(args, "-trimpath")
	}
	args = append(args, ".")
	cmd := exec.Command(goTool(root), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if opts.Race {
		cmd.Env = append(cmd.Env, "CGO_ENABLED=1")
	} else {
		cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
	}
	var stderr bytes.Buffer
	cmd.Stdout = env.Stderr
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.ReplaceAll(stderr.String(), dir+string(filepath.Separator), "")
		return fmt.Errorf("go build failed: %v\n%s", err, msg)
	}
	return nil
}

// raceInitSrc is added to --race builds only (it is not part of the
// generated program, so --emit-go output does not depend on --race). The
// race runtime sleeps atexit_sleep_ms (default 1s) at exit whenever other
// goroutines exist, which os/signal's permanent signal loop guarantees;
// that would break the spec 13.3 shutdown deadlines. The race runtime reads
// GORACE before any Go code runs, so the program re-executes itself once
// with atexit_sleep_ms=0 added (same PID; skipped if already set).
const raceInitSrc = `//go:build race

package main

import (
	"os"
	"strings"
	"syscall"
)

func init() {
	gorace := os.Getenv("GORACE")
	if strings.Contains(gorace, "atexit_sleep_ms") {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	val := "atexit_sleep_ms=0"
	if gorace != "" {
		val = gorace + " " + val
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GORACE=") {
			env = append(env, kv)
		}
	}
	env = append(env, "GORACE="+val)
	_ = syscall.Exec(exe, os.Args, env)
}
`

// goTool returns the go command to use. Version-manager shims (mise,
// asdf, ...) pick the toolchain from the working directory's config, but
// the build runs in a temporary directory; so the real binary is resolved
// once by asking `go env GOROOT` from the module root.
func goTool(root string) string {
	if p := os.Getenv("FINC_GO"); p != "" {
		return p
	}
	goToolOnce.Do(func() {
		goToolPath = "go"
		if p, err := exec.LookPath("go"); err == nil {
			goToolPath = p
		}
		cmd := exec.Command(goToolPath, "env", "GOROOT")
		cmd.Dir = root
		if out, err := cmd.Output(); err == nil {
			p := filepath.Join(strings.TrimSpace(string(out)), "bin", "go")
			if _, err := os.Stat(p); err == nil {
				goToolPath = p
				return
			}
		}
		if p := filepath.Join(runtime.GOROOT(), "bin", "go"); runtime.GOROOT() != "" {
			if _, err := os.Stat(p); err == nil {
				goToolPath = p
			}
		}
	})
	return goToolPath
}

var (
	goToolOnce sync.Once
	goToolPath string
)

// findModuleRoot locates the fincore module root (for the runtime
// package): $FINCORE_ROOT, else the first directory above the working
// directory or above the finc0 executable whose go.mod declares
// `module fincore`.
func findModuleRoot() (string, error) {
	if r := os.Getenv("FINCORE_ROOT"); r != "" {
		abs, err := filepath.Abs(r)
		if err != nil {
			return "", err
		}
		if !isFincoreRoot(abs) {
			return "", fmt.Errorf("FINCORE_ROOT=%s does not contain the fincore go.mod", r)
		}
		return abs, nil
	}
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		starts = append(starts, filepath.Dir(exe))
	}
	for _, dir := range starts {
		for {
			if isFincoreRoot(dir) {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", errors.New("cannot find the fincore module root (go.mod with `module fincore`); set FINCORE_ROOT")
}

var moduleFincoreRE = regexp.MustCompile(`(?m)^module\s+fincore\s*$`)

func isFincoreRoot(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	return err == nil && moduleFincoreRE.Match(data)
}

// parseBuildFlags parses the flags of build/run; it returns the remaining
// positional arguments.
func parseBuildFlags(env *Env, name, usageLine string, args []string, withOut bool) (buildOptions, []string, bool) {
	fs := newFlagSet(env, name, usageLine)
	var o buildOptions
	if withOut {
		fs.StringVar(&o.Out, "o", "", "output binary (default: base name of the source)")
		fs.StringVar(&o.EmitGo, "emit-go", "", "also write the generated Go source to this file")
		fs.BoolVar(&o.Trimpath, "trimpath", false, "pass -trimpath to go build")
	}
	fs.BoolVar(&o.Race, "race", false, "build with the race detector (needs cgo)")
	fs.Uint64Var(&o.Fuel, "comptime-fuel", 1_000_000, "comptime fuel budget per evaluation")
	if err := fs.Parse(args); err != nil {
		return o, nil, false
	}
	if o.Fuel == 0 {
		fmt.Fprintf(env.Stderr, "finc0 %s: --comptime-fuel must be positive\n", name)
		return o, nil, false
	}
	return o, fs.Args(), true
}

func runBuild(env *Env, args []string) int {
	const usageLine = "build [-o out_bin] [--emit-go=out.go] [--race] [--trimpath] [--comptime-fuel=N] <file.fc | dir>"
	o, rest, ok := parseBuildFlags(env, "build", usageLine, args, true)
	if !ok {
		return ExitUsage
	}
	if len(rest) != 1 {
		fmt.Fprintf(env.Stderr, "usage: finc0 %s\n", usageLine)
		return ExitUsage
	}
	src := rest[0]
	if o.Out == "" {
		base := filepath.Base(filepath.Clean(src))
		o.Out = strings.TrimSuffix(base, ".fc")
		if o.Out == base && !isDir(src) {
			o.Out += ".bin"
		}
	}
	if !buildBinary(env, src, o) {
		return ExitError
	}
	return ExitOK
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// runRun builds to a temporary binary and executes it with the arguments
// after `--`, forwarding stdin/stdout/stderr and the exit code.
func runRun(env *Env, args []string) int {
	const usageLine = "run [--race] [--comptime-fuel=N] <file.fc | dir> [-- args...]"
	var progArgs []string
	for i, a := range args {
		if a == "--" {
			progArgs = args[i+1:]
			args = args[:i]
			break
		}
	}
	o, rest, ok := parseBuildFlags(env, "run", usageLine, args, false)
	if !ok {
		return ExitUsage
	}
	if len(rest) != 1 {
		fmt.Fprintf(env.Stderr, "usage: finc0 %s\n", usageLine)
		return ExitUsage
	}
	dir, err := os.MkdirTemp("", "finc0-run-")
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return ExitError
	}
	defer os.RemoveAll(dir)
	o.Out = filepath.Join(dir, "prog")
	if !buildBinary(env, rest[0], o) {
		return ExitError
	}
	cmd := exec.Command(o.Out, progArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if code := ee.ExitCode(); code >= 0 {
				return code
			}
			return ExitError
		}
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return ExitError
	}
	return ExitOK
}
