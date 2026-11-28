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
		t.Skip("invokes as and ld")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "args.fc")
	writeFile(t, src, argsProg)
	bin := filepath.Join(dir, "args")
	emit := filepath.Join(dir, "args.s")
	if code, out, errs := run("build", "-o", bin, "--emit-asm="+emit, src); code != ExitOK {
		t.Fatalf("build: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
	asmSrc, err := os.ReadFile(emit)
	if err != nil || !strings.Contains(string(asmSrc), "_fc_entry:") {
		t.Fatalf("emitted asm: %v\n%s", err, asmSrc)
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

// TestTestSince checks that `// SINCE: vX.Y` tests newer than LangVersion
// are skipped (they would fail: no .out file, invalid code) and reported.
func TestTestSince(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes as and ld")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pass", "hello.fc"), "// SINCE: v0.1\npackage main\n\ndriver func main(io Stdio) error {\n\treturn io.WriteStdout(strBytes(\"hi\\n\"))\n}\n")
	writeFile(t, filepath.Join(dir, "pass", "hello.out"), "hi\n")
	writeFile(t, filepath.Join(dir, "pass", "future.fc"), "// A test of a future language version.\n// SINCE: v9.9\npackage main\n\nnot v0.1 syntax\n")
	writeFile(t, filepath.Join(dir, "pass", "future2", "a.fc"), "// SINCE: v1.0\npackage main\n\nnot v0.1 syntax\n")
	writeFile(t, filepath.Join(dir, "fail", "future.fc"), "// SINCE: v0.2\npackage main\n\nvar g u32 // ERROR E9999\n")
	code, out, errs := run("test", "-v", dir)
	if code != ExitOK || !strings.Contains(out, "4 tests, 1 passed, 0 failed, 3 skipped (needs v9.9)") ||
		!strings.Contains(out, "future.fc (needs v9.9)") {
		t.Fatalf("test: %d %q %q", code, out, errs)
	}
	// A malformed directive fails the test.
	writeFile(t, filepath.Join(dir, "pass", "future.fc"), "// SINCE: 9.9\npackage main\n")
	code, out, _ = run("test", dir)
	if code != ExitError || !strings.Contains(out, `bad // SINCE: "9.9"`) {
		t.Fatalf("bad since: %d %q", code, out)
	}
	if v, ok := parseLangVersion("v9.9"); !ok || !(langVersion{0, 1}).less(v) || v.String() != "v9.9" {
		t.Fatal(v, ok)
	}
	for _, bad := range []string{"", "v", "v1", "v1.", "v.1", "v1.x", "v1.2.3", "v-1.2", "v+1.2", "v12345.0", "v1.2 x"} {
		if _, ok := parseLangVersion(bad); ok {
			t.Errorf("parseLangVersion(%q) ok", bad)
		}
	}
}

// TestTestSinceClosure checks that a test requires the highest SINCE of
// its import closure: every file of its package and, transitively, the
// local and std packages it imports (testdata/README.md). The temporary
// directory is the module root (the working directory).
func TestTestSinceClosure(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("FINCROOT", "fr")
	fut := "// SINCE: v9.9\npackage lib\n\nnot v0.2 syntax\n"
	// directive in a non-first file
	writeFile(t, filepath.Join(dir, "pass", "second", "a.fc"), "package main\n\nnot v0.2 syntax\n")
	writeFile(t, filepath.Join(dir, "pass", "second", "b.fc"), "// SINCE: v9.9\npackage main\n")
	// in an imported subpackage
	writeFile(t, filepath.Join(dir, "pass", "sub", "main.fc"), "package main\n\nimport \"pass/sub/lib\"\n\nnot v0.2 syntax\n")
	writeFile(t, filepath.Join(dir, "pass", "sub", "lib", "lib.fc"), fut)
	// in a std package through FINCROOT (the module root's std/fut has none)
	writeFile(t, filepath.Join(dir, "pass", "stdimp.fc"), "package main\n\nimport \"std/fut\"\n\nnot v0.2 syntax\n")
	writeFile(t, filepath.Join(dir, "fr", "std", "fut", "f.fc"), fut)
	writeFile(t, filepath.Join(dir, "std", "fut", "f.fc"), "package fut\n")
	// a cycle, reached transitively
	writeFile(t, filepath.Join(dir, "pass", "cyc.fc"), "package main\n\nimport \"lib/a\"\n")
	writeFile(t, filepath.Join(dir, "lib", "a", "a.fc"), "package a\n\nimport \"lib/b\"\n")
	writeFile(t, filepath.Join(dir, "lib", "b", "b.fc"), "// SINCE: v9.9\npackage b\n\nimport \"lib/a\"\n")
	// an import block (names, comments)
	writeFile(t, filepath.Join(dir, "pass", "blk.fc"), "package main\n\nimport (\n\t\"lib/none\" // ignored\n\t/* \"lib/x\" */\n\tf \"lib/fut\"\n)\n\nnot v0.2 syntax\n")
	writeFile(t, filepath.Join(dir, "lib", "fut", "f.fc"), fut)
	// imports that are not directories, or after a declaration, are not
	// followed: these tests run.
	writeFile(t, filepath.Join(dir, "fail", "nodir.fc"), "package main\n\nimport \"missing\" // ERROR E2001\nimport \"fail/late.fc\" // ERROR E2001\n")
	writeFile(t, filepath.Join(dir, "fail", "late.fc"), "package main\n\nvar g u32 = 1\n\nimport \"lib/fut\" // ERROR E1001\n")
	code, out, errs := run("test", "-v", dir)
	if code != ExitOK || !strings.Contains(out, "7 tests, 2 passed, 0 failed, 5 skipped (needs v9.9)") {
		t.Fatalf("test: %d %q %q", code, out, errs)
	}
	for _, n := range []string{"second", "sub", "stdimp.fc", "cyc.fc", "blk.fc"} {
		if !strings.Contains(out, n+" (needs v9.9)") {
			t.Errorf("%s not skipped:\n%s", n, out)
		}
	}
	// Without FINCROOT, std/fut is the module root's (no directive).
	t.Setenv("FINCROOT", "")
	if code, out, _ = run("test", filepath.Join(dir, "pass")); !strings.Contains(out, "5 tests, 0 passed, 1 failed, 4 skipped") ||
		!strings.Contains(out, "stdimp.fc") {
		t.Fatalf("no FINCROOT: %d %q", code, out)
	}
	// A malformed directive in an imported package fails the test, naming
	// the file.
	writeFile(t, filepath.Join(dir, "lib", "fut", "f.fc"), "// SINCE: 9.9\npackage lib\n")
	code, out, _ = run("test", filepath.Join(dir, "pass"))
	if code != ExitError || !strings.Contains(out, filepath.Join("lib", "fut", "f.fc")+`: bad // SINCE: "9.9"`) {
		t.Fatalf("bad since in an import: %d %q", code, out)
	}
}

func TestVersion(t *testing.T) {
	for _, a := range []string{"version", "--version"} {
		if code, out, _ := run(a); code != ExitOK || out != "finc0 language v"+LangVersion+"\n" {
			t.Fatalf("%s: %d %q", a, code, out)
		}
	}
}
