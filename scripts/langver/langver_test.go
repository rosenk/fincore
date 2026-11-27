package langver

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		{"// SINCE: v+1.2\n", "err"},
		{"// SINCE: v12345.0\n", "err"},
		{"// SINCE: v0.3 trailing\n", "err"},
		{"// SINCE: v0.3\n// SINCE: v0.9\npackage main\n", "v0.3"},
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

func TestImports(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{"package main\n\nimport \"a\"\nimport b \"x/b\"\nimport\t\"c\" // c\n", []string{"a", "x/b", "c"}},
		{"// header\npackage main\n\nimport (\n\t\"a\"\n\t// \"commented\"\n\tn \"b/c\" // why\n\n\t_ \"d\"\n)\nimport \"e\"\n", []string{"a", "b/c", "d", "e"}},
		{"package main\nimport \"a\"\n\nfunc f() {}\n\nimport \"late\"\n", []string{"a"}},
		{"package main\nconst x = 1\nimport \"late\"\n", nil},
		{"package main\n/* import \"no\"\n import \"no2\" */ import \"yes\"\n/**/import \"y2\"\n", []string{"yes", "y2"}},
		{"package main\nimport (\"one\")\nimport two words \"bad\"\nimport \"unterminated\nimport \"ok\"\n", []string{"one", "ok"}}, // "(" counts as a name
		{"package main\nimportx \"no\"\nimport \"after\"\n", nil},
	} {
		if got := Imports([]byte(tc.src)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Imports(%q) = %q, want %q", tc.src, got, tc.want)
		}
	}
}

// TestRequiredClosure checks the import-closure rule of RequiredIn.
func TestRequiredClosure(t *testing.T) {
	root := t.TempDir()
	write := func(name, src string) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// directive in a non-first file
	write("second/a.fc", "package main\n")
	write("second/b.fc", "// SINCE: v0.5\npackage main\n")
	// directive in an imported subpackage (not in a non-imported one)
	write("sub/main.fc", "package main\n\nimport \"sub/lib\"\n")
	write("sub/lib/lib.fc", "// SINCE: v0.6\npackage lib\n")
	write("sub/other/o.fc", "// SINCE: v9.9\npackage other\n")
	// import block with names and comments, transitively
	write("block/main.fc", "package main\n\nimport (\n\t\"sub/lib\" // v0.6\n\tx \"deep/x\"\n)\n")
	write("deep/x/x.fc", "package x\n\nimport \"deep/y\"\n")
	write("deep/y/y.fc", "// SINCE: v1.2\npackage y\n")
	// std through FINCROOT (or the module root without it)
	write("usestd/main.fc", "package main\nimport \"std/foo\"\n")
	write("std/foo/foo.fc", "// SINCE: v0.4\npackage foo\n")
	write("fincroot/std/foo/foo.fc", "// SINCE: v0.8\npackage foo\n")
	// a cycle
	write("cyc/a/a.fc", "package a\nimport \"cyc/b\"\n")
	write("cyc/b/b.fc", "// SINCE: v0.7\npackage b\nimport \"cyc/a\"\n")
	// imports that are not directories, or invalid
	write("nodir/main.fc", "package main\nimport \"missing\"\nimport \"nodir/main.fc\"\nimport \"../sub/lib\"\nimport \"/abs\"\n")
	// an import after the first declaration
	write("late/main.fc", "package main\n\nfunc f() {}\n\nimport \"sub/lib\"\n")
	// a malformed directive in an imported package
	write("bad/main.fc", "package main\nimport \"bad/lib\"\n")
	badLib := write("bad/lib/lib.fc", "// SINCE: 0.3\npackage lib\n")

	for _, tc := range []struct {
		path, fincRoot, want string // want "" = none
	}{
		{"second", "", "v0.5"},
		{"second/a.fc", "", ""},
		{"second/b.fc", "", "v0.5"},
		{"sub", "", "v0.6"},
		{"sub/main.fc", "", "v0.6"},
		{"block", "", "v1.2"},
		{"usestd", "", "v0.4"},
		{"usestd", filepath.Join(root, "fincroot"), "v0.8"},
		{"usestd", "fincroot", "v0.8"}, // relative to the module root
		{"usestd", filepath.Join(root, "none"), ""},
		{"cyc/a", "", "v0.7"},
		{"cyc/b", "", "v0.7"},
		{"nodir", "", ""},
		{"late", "", ""},
	} {
		v, ok, err := RequiredIn(root, tc.fincRoot, filepath.Join(root, tc.path))
		got := ""
		if ok {
			got = v.String()
		}
		if err != nil || got != tc.want {
			t.Errorf("RequiredIn(%s, FINCROOT=%q) = %q, %v; want %q", tc.path, tc.fincRoot, got, err, tc.want)
		}
	}
	if _, _, err := RequiredIn(root, "", filepath.Join(root, "bad")); err == nil || !strings.Contains(err.Error(), badLib) {
		t.Errorf("malformed directive in an import: %v", err)
	}

	// Required resolves against the working directory and $FINCROOT.
	t.Chdir(root)
	t.Setenv("FINCROOT", "fincroot")
	if v, ok, err := Required("usestd"); err != nil || !ok || v.String() != "v0.8" {
		t.Errorf("Required(usestd) = %v %v %v", v, ok, err)
	}
	if ok, err := Finc0Can("sub/main.fc"); err != nil || ok {
		t.Errorf("Finc0Can(sub/main.fc) = %v %v", ok, err)
	}
}
