package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const argsProg = `package main

driver func main(io Stdio, env Env) error {
	n := env.ArgsCount()
	for i := usize(1); i < n; i++ {
		if err := io.WriteStdout(strBytes(env.Arg(i))); err != nil {
			return err
		}
		if err := io.WriteStdout(strBytes("\n")); err != nil {
			return err
		}
	}
	if n > 3 {
		return ErrIO
	}
	return nil
}
`

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAndRun(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes go build")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "args.fc")
	writeFile(t, src, argsProg)
	bin := filepath.Join(dir, "args")
	emit := filepath.Join(dir, "args.go")
	if code, out, errs := run("build", "-o", bin, "--emit-go="+emit, src); code != ExitOK {
		t.Fatalf("build: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
	goSrc, err := os.ReadFile(emit)
	if err != nil || !strings.Contains(string(goSrc), "func _fc_main(io fincrt.Stdio, env fincrt.Env) error") {
		t.Fatalf("emitted Go: %v\n%s", err, goSrc)
	}
	code, out, errs := run("run", src, "--", "a", "b c")
	if code != ExitOK || out != "a\nb c\n" {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	code, out, errs = run("run", src, "--", "x", "y", "z")
	if code != 1 || out != "x\ny\nz\n" || !strings.Contains(errs, "error: fincore: I/O error") {
		t.Fatalf("run with error: %d %q %q", code, out, errs)
	}
	bad := filepath.Join(dir, "bad.fc")
	writeFile(t, bad, "package main\nvar g Conn\n")
	if code, _, errs := run("build", "-o", bin, bad); code != ExitError || !strings.Contains(errs, "E3001") {
		t.Fatalf("bad: %d %q", code, errs)
	}
}

func TestTestCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes go build")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pass", "hello.fc"), "package main\n\ndriver func main(io Stdio) error {\n\treturn io.WriteStdout(strBytes(\"hi\\n\"))\n}\n")
	writeFile(t, filepath.Join(dir, "pass", "hello.out"), "hi\n")
	writeFile(t, filepath.Join(dir, "pass", "multi", "a.fc"), "package main\n\ndriver func main(io Stdio) error {\n\treturn io.WriteStdout(strBytes(msg))\n}\n")
	writeFile(t, filepath.Join(dir, "pass", "multi", "b.fc"), "package main\n\nconst msg = \"multi\\n\"\n")
	writeFile(t, filepath.Join(dir, "pass", "multi.out"), "multi\n")
	writeFile(t, filepath.Join(dir, "fail", "global.fc"), "package main\n\nvar g Conn // ERROR E3001\n")
	code, out, errs := run("test", dir)
	if code != ExitOK || !strings.Contains(out, "3 tests, 3 passed, 0 failed") {
		t.Fatalf("test: %d %q %q", code, out, errs)
	}
	// A wrong expectation fails.
	writeFile(t, filepath.Join(dir, "pass", "hello.out"), "bye\n")
	writeFile(t, filepath.Join(dir, "fail", "global.fc"), "package main\n\nvar g Conn // ERROR E2005\n")
	code, out, _ = run("test", filepath.Join(dir))
	if code != ExitError || !strings.Contains(out, "3 tests, 1 passed, 2 failed") ||
		!strings.Contains(out, "stdout mismatch") || !strings.Contains(out, "missing diagnostic global.fc:3: error[E2005]") {
		t.Fatalf("failing test: %d %q", code, out)
	}
}
