package langver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSince(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string // "" = none, "err" = error
	}{
		{"package main\n", ""},
		{"// SINCE: v0.3\npackage main\n", "v0.3"},
		{"// pass_x: blah\n\n//   SINCE: v9.9 (not a directive)\n// SINCE: v1.10\npackage main\n", "v1.10"},
		{"// header\npackage main\n// SINCE: v0.3\n", ""},
		{"\n\n// SINCE:v0.4\npackage main\n", "v0.4"},
		{"// SINCE: 0.3\n", "err"},
		{"// SINCE: v0\n", "err"},
	} {
		v, ok, err := Since([]byte(tc.src))
		got := ""
		switch {
		case err != nil:
			got = "err"
		case ok:
			got = v.String()
		}
		if got != tc.want {
			t.Errorf("Since(%q) = %q, want %q", tc.src, got, tc.want)
		}
	}
}

func TestFinc0Can(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := write("a.fc", "// SINCE: v0.2\npackage main\n")
	plain := write("b.fc", "package main\n")
	newer := write("pkg/c.fc", "// SINCE: v0.3\npackage pkg\n")
	write("pkg/d.fc", "package pkg\n")
	for p, want := range map[string]bool{old: true, plain: true, newer: false, filepath.Join(dir, "pkg"): false} {
		got, err := Finc0Can(p)
		if err != nil || got != want {
			t.Errorf("Finc0Can(%s) = %v, %v; want %v", p, got, err, want)
		}
	}
}
