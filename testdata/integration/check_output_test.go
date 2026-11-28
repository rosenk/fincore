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
)

// diagLineRE matches one diagnostic line.
var diagLineRE = regexp.MustCompile(`^\S+\.fc:\d+:\d+: error\[E\d{4}\]: \S`)

// TestCheckFailureOutput verifies that a failing `finc0 check` prints only
// diagnostics on stderr, nothing on stdout, and exits with status 1.
func TestCheckFailureOutput(t *testing.T) {
	root := moduleRoot(t)
	bin := requireFinc0(t)
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
	for name := range cases {
		t.Run(name, func(t *testing.T) {
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
