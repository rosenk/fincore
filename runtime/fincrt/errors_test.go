package fincrt

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMainFailureReport builds Go programs with the generated main of
// every finc version (also the seed v0.2.0) and checks decision 156: a
// failing main prints `error: <err>` unless the program wrote to stderr,
// and exits 1 either way.
func TestMainFailureReport(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go programs")
	}
	_, file, _, _ := runtime.Caller(0)
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	const mainTmpl = `package main

import (
	"os"

	"fincore/runtime/fincrt"
)

func _fc_main(io fincrt.Stdio) error {
	%s
	return fincrt.ErrParse
}

func main() {
	if err := _fc_main(fincrt.DefaultStdio()); err != nil {
		_, _ = os.Stderr.WriteString("error: " + err.Error() + "\n")
		os.Exit(1)
	}
}
`
	for _, tc := range []struct {
		name, body, want string
	}{
		{"silent", `_ = io.WriteStdout([]byte("out\n"))`, "error: fincore: invalid number\n"},
		{"empty stderr write", `_ = io.WriteStderr(nil)`, "error: fincore: invalid number\n"},
		{"reported", `_ = io.WriteStderr([]byte("t.fc:1:1: error[E1001]: x\n"))`, "t.fc:1:1: error[E1001]: x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gomod := "module fincrttest\n\ngo 1.22\n\nrequire fincore v0.0.0\n\nreplace fincore => " + root + "\n"
			src := bytes.Replace([]byte(mainTmpl), []byte("%s"), []byte(tc.body), 1)
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "main.go"), src, 0o644); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "prog")
			build := exec.Command("go", "build", "-o", bin, ".")
			build.Dir = dir
			build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("go build: %v\n%s", err, out)
			}
			var stderr bytes.Buffer
			cmd := exec.Command(bin)
			cmd.Stderr = &stderr
			err := cmd.Run()
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.ExitCode() != 1 {
				t.Fatalf("want exit status 1, got %v", err)
			}
			if stderr.String() != tc.want {
				t.Fatalf("stderr %q, want %q", stderr.String(), tc.want)
			}
		})
	}
	// Outside main.main, Error is the message, also after a stderr write.
	stderrUsed.Store(true)
	defer stderrUsed.Store(false)
	if ErrParse.Error() != "fincore: invalid number" {
		t.Fatal(ErrParse.Error())
	}
}
