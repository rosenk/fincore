//go:build unix

package integration

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"fincore/scripts/langver"
)

// diagLineRE is one spec 11 diagnostic line.
var diagLineRE = regexp.MustCompile(`^\S+\.fc:\d+:\d+: error\[E\d{4}\]: \S`)

// TestCheckFailureOutput: a failing `finc check` prints its diagnostics on
// stderr and no other line (compiler/ returns ErrReported, decision 156,
// so its generated main adds no `error: ...` line), nothing on
// stdout, and exits with status 1. It runs the compiler under test
// ($FINC0, else finc0) and, when it has been built (make finc1), the
// current compiler bin/finc1.
func TestCheckFailureOutput(t *testing.T) {
	root := moduleRoot(t)
	compilers := map[string]string{"under test": requireFinc0(t)}
	if p := filepath.Join(root, "bin", "finc1"); fileExists(p) {
		compilers["bin/finc1"] = p
	}
	dir := t.TempDir()
	cases := map[string]string{
		"syntax.fc":   "package main\n\nfunc f(x u32) u32 {\n\treturn x 2\n}\n",
		"types.fc":    "package main\n\nfunc f() u32 {\n\treturn \"x\"\n}\n\nfunc g() bool {\n\treturn 1\n}\n",
		"scope.fc":    "package main\n\ntype L struct {\n\tsrc []byte\n}\n\nfunc (l *L) reset(src []byte) {\n\tl.src = src\n}\n",
		"totality.fc": "package main\n\nfunc spin(n u32) u32 {\n\tx := n\n\tfor x > 0 {\n\t\tx = x + 1\n\t}\n\treturn x\n}\n",
	}
	for name, src := range cases {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for cname, bin := range compilers {
		for name := range cases {
			t.Run(cname+"/"+name, func(t *testing.T) {
				stdout, stderr, code := runIn(t, root, bin, "check", filepath.Join(dir, name))
				if code != 1 {
					t.Errorf("exit status %d, want 1", code)
				}
				if stdout != "" {
					t.Errorf("stdout: %q", stdout)
				}
				lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
				if stderr == "" || !strings.HasSuffix(stderr, "\n") {
					t.Fatalf("stderr %q: want diagnostic lines", stderr)
				}
				for _, l := range lines {
					if !diagLineRE.MatchString(l) {
						t.Errorf("stderr line %q is not a diagnostic; stderr:\n%s", l, stderr)
					}
				}
			})
		}
	}
	// The current compiler without arguments: the usage on stderr and
	// nothing after it, exit 1.
	if bin, ok := compilers["bin/finc1"]; ok {
		_, stderr, code := runIn(t, root, bin)
		if code != 1 || !strings.HasPrefix(stderr, "usage: finc <command>") || strings.Contains(stderr, "error: ") {
			t.Errorf("finc1 without arguments: exit %d, stderr:\n%s", code, stderr)
		}
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// runIn runs bin with args in dir and returns stdout, stderr and the exit
// status.
func runIn(t *testing.T, dir, bin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running %s %v: %v", bin, args, err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

// TestErrReportedExit: a driver main returning ErrReported exits with
// status 1 and prints nothing; other errors still print `error: <err>`
// (decision 156, fincrt.MainExit). It needs a v0.3 compiler: the compiler
// under test if it implements v0.3, and bin/finc1 when it has been built.
func TestErrReportedExit(t *testing.T) {
	root := moduleRoot(t)
	compilers := map[string]string{}
	if !compilerLangVersion(t).Less(langver.MustParse("v0.3")) {
		compilers["under test"] = requireFinc0(t)
	}
	if p := filepath.Join(root, "bin", "finc1"); fileExists(p) {
		compilers["bin/finc1"] = p
	}
	if len(compilers) == 0 {
		t.Skip("no v0.3 compiler (set FINC0 or run make finc1)")
	}
	const src = `package main

driver func main(env Env, io Stdio) error {
	what := env.Arg(1)
	if strEq(what, "reported") {
		if err := io.WriteStderr(strBytes("tool: reported\n")); err != nil {
			return err
		}
		return ErrReported
	}
	if strEq(what, "io") {
		return ErrIO
	}
	return io.WriteStdout(strBytes("ok\n"))
}

func strEq(a string, b string) bool {
	return bytesEqual(strBytes(a), strBytes(b))
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reported.fc"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for cname, bin := range compilers {
		exe := filepath.Join(dir, "reported-"+strings.ReplaceAll(cname, "/", "-"))
		if _, stderr, code := runIn(t, root, bin, "build", "-o", exe, filepath.Join(dir, "reported.fc")); code != 0 {
			t.Fatalf("%s build: exit %d\n%s", cname, code, stderr)
		}
		for _, tc := range []struct {
			arg, stdout, stderr string
			code                int
		}{
			{"ok", "ok\n", "", 0},
			{"reported", "", "tool: reported\n", 1},
			{"io", "", "error: fincore: I/O error\n", 1},
		} {
			stdout, stderr, code := runIn(t, dir, exe, tc.arg)
			if stdout != tc.stdout || stderr != tc.stderr || code != tc.code {
				t.Errorf("%s %s: exit %d, stdout %q, stderr %q; want %d, %q, %q", cname, tc.arg, code, stdout, stderr, tc.code, tc.stdout, tc.stderr)
			}
		}
	}
}
