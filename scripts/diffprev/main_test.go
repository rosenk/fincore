package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fincore/scripts/langver"
)

func TestClassify(t *testing.T) {
	acc := outcome{code: 0}
	acc2 := outcome{out: "other dump", code: 0}
	rej := outcome{err: "t.fc:1:1: error[E2002]: x\n", code: 1}
	rej2 := outcome{err: "t.fc:1:1: error[E2002]: y\n", code: 1}
	for _, tc := range []struct {
		name            string
		old, cur        outcome
		newer, failTest bool
		want            class
	}{
		{"same accept", acc, acc, false, false, identical},
		{"same reject", rej, rej, false, true, identical},
		{"accepted -> rejected", acc, rej, false, false, regression},
		{"accepted -> rejected (newer test)", acc, rej, true, false, regression},
		{"rejected -> accepted", rej, acc, false, false, expected},
		{"fail test rejected -> accepted", rej, acc, false, true, regression},
		{"newer fail test rejected -> accepted", rej, acc, true, true, expected},
		{"both accept, output differs", acc, acc2, false, false, regression},
		{"both reject, diagnostics differ", rej, rej2, false, false, diagChanged},
		{"both reject, newer test", rej, rej2, true, true, expected},
		{"exit status differs", rej, outcome{err: rej.err, code: 2}, false, false, diagChanged},
	} {
		if got, why := classify(tc.old, tc.cur, tc.newer, tc.failTest); got != tc.want {
			t.Errorf("%s: %v (%s), want %v", tc.name, got, why, tc.want)
		}
	}
}

// TestSinceNewer: a `// SINCE: v9.9` case is newer than a v0.2 seed, so
// both compilers rejecting it with different diagnostics is EXPECTED.
func TestSinceNewer(t *testing.T) {
	oldVer = langver.MustParse("v0.2")
	if (tcase{}).newer() {
		t.Error("case without SINCE is newer")
	}
	for since, want := range map[string]bool{"v0.1": false, "v0.2": false, "v0.3": true, "v9.9": true} {
		v := langver.MustParse(since)
		if got := (tcase{since: &v}).newer(); got != want {
			t.Errorf("SINCE %q: newer %v, want %v", since, got, want)
		}
	}
	// The same through a corpus file's header.
	dir := t.TempDir()
	f := filepath.Join(dir, "future.fc")
	if err := os.WriteFile(f, []byte("// A dummy test of a future version.\n// SINCE: v9.9\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := (tcase{since: required(f)}); !c.newer() {
		t.Errorf("%s: not newer than %s", f, oldVer)
	}
	if v, ok := tagVersion("v0.2.0"); !ok || v != langver.MustParse("v0.2") {
		t.Errorf("v0.2.0: %v %v", v, ok)
	}
	if _, ok := tagVersion("v0.2.0.1"); ok {
		t.Error("v0.2.0.1 accepted")
	}
}

func TestReadAllow(t *testing.T) {
	a, err := readAllow(strings.NewReader("# comment\n\ntestdata/fail/x.fc # reworded E2002\nbootstrap/internal/types/check_test.go:73 [fuel 1000] # a # b\n"))
	if err != nil || len(a) != 2 || a["testdata/fail/x.fc"].reason != "reworded E2002" ||
		a["bootstrap/internal/types/check_test.go:73 [fuel 1000]"].reason != "a # b" {
		t.Fatalf("%v %v", a, err)
	}
	for _, bad := range []string{"no reason\n", "name #\n", "name #  \n"} {
		if _, err := readAllow(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOutcomeDetail(t *testing.T) {
	d := outcomeDetail(outcome{err: "a\nb\nc\n", code: 1}, outcome{err: "a\nB\nc\n", code: 1})
	if !strings.Contains(d, "from line 2") || !strings.Contains(d, "    b\n") || !strings.Contains(d, "    B\n") || strings.Contains(d, "    a\n") {
		t.Fatal(d)
	}
}
