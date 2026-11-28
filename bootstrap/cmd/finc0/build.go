package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"fincore/bootstrap/internal/check"
	"fincore/bootstrap/internal/codegen/amd64"
	"fincore/bootstrap/internal/ir"
)

// buildOptions are the flags shared by build, run and test.
type buildOptions struct {
	Out     string // -o
	EmitAsm string // --emit-asm
	Fuel    uint64
}

// compileAsm runs the front end and the Linux x86_64 code generator on src.
// On compile errors the diagnostics are printed to stderr and ok is false.
func compileAsm(env *Env, src string, fuel uint64) (asmSrc []byte, ok bool) {
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
	out, err := amd64.Generate(prog)
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return nil, false
	}
	return out, true
}

// buildBinary compiles src into the static Linux x86_64 ELF64 binary opts.Out.
// Zero libc, zero GC, zero Go runtime.
func buildBinary(env *Env, src string, opts buildOptions) bool {
	asmSrc, ok := compileAsm(env, src, opts.Fuel)
	if !ok {
		return false
	}
	if opts.EmitAsm != "" {
		if err := os.WriteFile(opts.EmitAsm, asmSrc, 0o644); err != nil {
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
	if err := nativeBuild(root, asmSrc, out); err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return false
	}
	return true
}

var runtimeAsmFiles = []string{"sys.s", "fmt.s", "net.s", "scope.s"}

// nativeBuild assembles asmSrc and runtime/linux_amd64/*.s using GNU as,
// then links them into a standalone static ELF64 executable using GNU ld.
func nativeBuild(root string, asmSrc []byte, out string) error {
	dir, err := os.MkdirTemp("", "finc0-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	progS := filepath.Join(dir, "prog.s")
	if err := os.WriteFile(progS, asmSrc, 0o644); err != nil {
		return err
	}
	progO := filepath.Join(dir, "prog.o")
	if err := runAssembler(progS, progO); err != nil {
		return fmt.Errorf("assemble program: %w", err)
	}

	rtDir := filepath.Join(root, "runtime", "linux_amd64")
	objs := make([]string, 0, len(runtimeAsmFiles)+1)
	for _, name := range runtimeAsmFiles {
		srcPath := filepath.Join(rtDir, name)
		objPath := filepath.Join(dir, "rt_"+strings.TrimSuffix(name, ".s")+".o")
		if err := runAssembler(srcPath, objPath); err != nil {
			return fmt.Errorf("assemble runtime %s: %w", name, err)
		}
		objs = append(objs, objPath)
	}
	objs = append(objs, progO)

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	ldArgs := append([]string{"-static", "-z", "noexecstack", "--build-id=none", "-o", out}, objs...)
	ldCmd := exec.Command("ld", ldArgs...)
	var stderr bytes.Buffer
	ldCmd.Stderr = &stderr
	if err := ldCmd.Run(); err != nil {
		return fmt.Errorf("ld -static failed: %v\n%s", err, stderr.String())
	}
	return nil
}

func runAssembler(srcPath, objPath string) error {
	cmd := exec.Command("as", "--64", "-o", objPath, srcPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("as failed: %v\n%s", err, stderr.String())
	}
	return nil
}

// findModuleRoot locates the fincore repository root (for runtime/linux_amd64):
// $FINCORE_ROOT, else the first directory above the working directory or above
// the finc0 executable whose go.mod declares `module fincore`.
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
	return "", errors.New("cannot find the fincore repository root (go.mod with `module fincore`); set FINCORE_ROOT")
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
		fs.StringVar(&o.EmitAsm, "emit-asm", "", "also write the generated x86_64 assembly to this file")
	}
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
	const usageLine = "build [-o out_bin] [--emit-asm=out.s] [--comptime-fuel=N] <file.fc | dir>"
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
	const usageLine = "run [--comptime-fuel=N] <file.fc | dir> [-- args...]"
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
