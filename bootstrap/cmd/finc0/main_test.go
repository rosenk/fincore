package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = "../../internal/parser/testdata/tcp_echo.fc"

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(&Env{Stdout: &out, Stderr: &errb}, args)
	return code, out.String(), errb.String()
}

func TestParseFixture(t *testing.T) {
	code, out, errs := run("parse", fixture)
	if code != ExitOK || out != "" || errs != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	code, out1, _ := run("parse", "--dump-ast", fixture)
	_, out2, _ := run("parse", "--dump-ast", fixture)
	if code != ExitOK || out1 != out2 || !strings.HasPrefix(out1, "Package main (1 files)\nFile #") {
		t.Fatalf("dump: code=%d\n%s", code, out1[:min(len(out1), 200)])
	}
}

func TestParseErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.fc")
	if err := os.WriteFile(bad, []byte("package p\nfunc f( {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("parse", bad)
	if code != ExitError || errs != bad+":2:9: error[E1001]: expected identifier, found '{'\n" {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	if code, _, _ := run("parse", filepath.Join(dir, "missing.fc")); code != ExitError {
		t.Fatal(code)
	}
	// directory form
	if err := os.WriteFile(bad, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("parse", dir); code != ExitOK {
		t.Fatal(code, errs)
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := run(); code != ExitUsage {
		t.Fatal(code)
	}
	if code, _, _ := run("frobnicate"); code != ExitUsage {
		t.Fatal(code)
	}
	if code, _, _ := run("parse"); code != ExitUsage {
		t.Fatal(code)
	}
	if code, _, _ := run("parse", "--bogus", "x.fc"); code != ExitUsage {
		t.Fatal(code)
	}
	for _, c := range []string{"build", "run", "test"} {
		code, _, errs := run(c, "x")
		if code != ExitUsage || !strings.Contains(errs, "not implemented") {
			t.Errorf("%s: %d %q", c, code, errs)
		}
	}
	if code, out, _ := run("help"); code != ExitOK || !strings.Contains(out, "finc0 parse [--dump-ast]") {
		t.Fatal(out)
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.fc")
	bad := filepath.Join(dir, "bad.fc")
	loop := filepath.Join(dir, "loop.fc")
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(good, "package main\ndriver func main(io Stdio) error {\n\treturn nil\n}\n")
	write(bad, "package main\nvar g Conn\nvar h Listener\n")
	write(loop, "package main\ncomptime func F(n usize) usize {\n\tfor i := usize(0); i < n; i++ {\n\t}\n\treturn n\n}\nconst X = F(50)\n")
	if code, out, errs := run("check", good); code != ExitOK || out != "" || errs != "" {
		t.Fatalf("good: %d %q %q", code, out, errs)
	}
	code, _, errs := run("check", bad)
	want := bad + ":2:5: error[E3001]: package-level variable g cannot have Scoped type Conn\n" +
		bad + ":3:5: error[E3001]: package-level variable h cannot have Scoped type Listener\n"
	if code != ExitError || errs != want {
		t.Fatalf("bad: %d %q", code, errs)
	}
	if code, _, _ := run("check", "--comptime-fuel=100", loop); code != ExitOK {
		t.Fatal("fuel 100 should suffice")
	}
	if code, _, errs := run("check", "--comptime-fuel=10", loop); code != ExitError || !strings.Contains(errs, ":7:11: error[E4001]") {
		t.Fatalf("fuel 10: %d %q", code, errs)
	}
	if code, _, _ := run("check", "--comptime-fuel=x", good); code != ExitUsage {
		t.Fatal(code)
	}
}
