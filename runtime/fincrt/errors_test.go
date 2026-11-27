package fincrt

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"
)

// TestMainExit runs MainExit in a child process (this test binary with
// FINCRT_MAINEXIT set) and checks its exit status and stderr (decision 156).
func TestMainExit(t *testing.T) {
	if which := os.Getenv("FINCRT_MAINEXIT"); which != "" {
		err := map[string]error{"nil": nil, "reported": ErrReported, "parse": ErrParse}[which]
		MainExit(err)
		_, _ = os.Stderr.WriteString("returned\n")
		os.Exit(0)
	}
	for _, tc := range []struct {
		which, stderr string
		code          int
	}{
		{"nil", "returned\n", 0},
		{"reported", "", 1},
		{"parse", "error: fincore: invalid number\n", 1},
	} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainExit$")
		cmd.Env = append(os.Environ(), "FINCRT_MAINEXIT="+tc.which)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.code || stderr.String() != tc.stderr {
			t.Errorf("%s: exit %d, stderr %q; want %d, %q", tc.which, code, stderr.String(), tc.code, tc.stderr)
		}
	}
}
