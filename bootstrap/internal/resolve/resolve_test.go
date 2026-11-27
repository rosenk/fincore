package resolve_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, src := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func load(t *testing.T, files map[string]string, path string) (*resolve.Program, diag.List) {
	t.Helper()
	root := t.TempDir()
	write(t, root, files)
	t.Chdir(root)
	prog, d, err := resolve.Load(path, resolve.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return prog, d
}

func codesOf(d diag.List) string {
	var parts []string
	for _, x := range d {
		parts = append(parts, x.Code.String())
	}
	return strings.Join(parts, ",")
}

func TestMultiFileAndImports(t *testing.T) {
	prog, d := load(t, map[string]string{
		"app/b.fc":       "package main\nimport m \"lib/mathx\"\nfunc helper() u64 { return m.Sq(m.Base) }\n",
		"app/a.fc":       "package main\ndriver func main(io Stdio) error {\n\t_ = helper()\n\treturn nil\n}\n",
		"lib/mathx/x.fc": "package mathx\nconst Base = 7\nfunc Sq(v u64) u64 { return v * v }\nfunc hidden() {}\n",
	}, "app")
	if len(d) > 0 {
		t.Fatal(d)
	}
	if len(prog.Packages) != 2 || prog.Packages[0].Name != "mathx" || prog.Root.Name != "main" {
		t.Fatalf("packages: %v", prog.Packages)
	}
	if n := len(prog.Root.AST.Files); n != 2 || !strings.HasSuffix(prog.Root.AST.Files[0].Path, "a.fc") {
		t.Fatalf("files not sorted: %d", n)
	}
	// helper in a.fc resolves to the declaration in b.fc.
	var use *ast.Ident
	ast.Inspect(prog.Root.AST.Files[0], func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "helper" {
			use = id
		}
		return true
	})
	if s := prog.SymbolOf(use); s == nil || s.Kind != resolve.SymFunc || !strings.HasSuffix(s.Ident.Range.File, "b.fc") {
		t.Fatalf("helper -> %v", s)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		path  string
		want  string
	}{
		{"undefined", map[string]string{"a.fc": "package main\nfunc f() u32 { return x }\n"}, "a.fc", "E2001"},
		{"unexported", map[string]string{
			"a.fc":   "package main\nimport \"p\"\nfunc f() { p.hidden() }\n",
			"p/p.fc": "package p\nfunc hidden() {}\n",
		}, "a.fc", "E2001"},
		{"missing pkg", map[string]string{"a.fc": "package main\nimport \"nope\"\n"}, "a.fc", "E2001"},
		{"cycle", map[string]string{
			"a.fc":   "package main\nimport \"p\"\n",
			"p/p.fc": "package p\nimport \"q\"\n",
			"q/q.fc": "package q\nimport \"p\"\n",
		}, "a.fc", "E2006"},
		{"self cycle", map[string]string{"p/p.fc": "package p\nimport \"p\"\n"}, "p", "E2006"},
		{"redeclared", map[string]string{"a.fc": "package main\nconst X = 1\nconst X = 2\n"}, "a.fc", "E2001"},
		{"use before decl", map[string]string{"a.fc": "package main\nfunc f() {\n\ty := x\n\tx := 1\n\t_ = y\n}\n"}, "a.fc", "E2001"},
		{"no new vars", map[string]string{"a.fc": "package main\nfunc f(a u32) {\n\ta := 2\n}\n"}, "a.fc", "E2002"},
		{"blank value", map[string]string{"a.fc": "package main\nfunc f() u32 { return _ }\n"}, "a.fc", "E2001"},
		{"bad receiver", map[string]string{"a.fc": "package main\nfunc (t *T) m() {}\n"}, "a.fc", "E2001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := load(t, tc.files, tc.path)
			if got := codesOf(d); got != tc.want {
				t.Errorf("got %q, want %q\n%v", got, tc.want, d)
			}
		})
	}
}

func TestStdDir(t *testing.T) {
	cases := []struct{ ip, root, want string }{
		{"std/vec", "", ""},
		{"std/vec", "/opt/fincore", "/opt/fincore/std/vec"},
		{"std", "/opt/fincore/", "/opt/fincore/std"},
		{"std/maps", "../fr/", "../fr/std/maps"},
		{"std/maps", ".", "std/maps"},
		{"stdx/vec", "/opt/fincore", ""},
		{"lib/std/vec", "/opt/fincore", ""},
	}
	for _, c := range cases {
		if got := resolve.StdDir(c.ip, c.root); got != filepath.FromSlash(c.want) {
			t.Errorf("StdDir(%q, %q) = %q, want %q", c.ip, c.root, got, c.want)
		}
	}
}

// TestStdImportFincRoot: std/... resolves against $FINCROOT when it is
// set, else against the module root; files of FINCROOT packages are named
// by the cleaned FINCROOT path.
func TestStdImportFincRoot(t *testing.T) {
	files := map[string]string{
		"mod/a.fc":                  "package main\nimport \"std/hello\"\nimport \"lib\"\nfunc f() u32 { return hello.Which + lib.X }\n",
		"mod/lib/lib.fc":            "package lib\nconst X = 0\n",
		"mod/std/hello/hello.fc":    "package hello\nconst Which = 1\n",
		"root/std/hello/hello.fc":   "package hello\nimport \"std/inner\"\nconst Which = inner.Two\n",
		"root/std/inner/inner.fc":   "package inner\nconst Two = 2\n",
		"broken/std/hello/hello.fc": "package hello\nconst Which = missing\n",
	}
	tmp := t.TempDir()
	write(t, tmp, files)
	t.Chdir(filepath.Join(tmp, "mod"))
	which := func(prog *resolve.Program) (string, string) {
		for _, p := range prog.Packages {
			if p.Path == "std/hello" {
				return p.AST.Files[0].Path, p.Key
			}
		}
		return "", ""
	}

	t.Setenv("FINCROOT", "")
	prog, d, err := resolve.Load("a.fc", resolve.Options{})
	if err != nil || len(d) > 0 {
		t.Fatal(err, d)
	}
	if f, _ := which(prog); f != filepath.Join("std", "hello", "hello.fc") {
		t.Errorf("empty FINCROOT: std/hello from %q", f)
	}

	for _, root := range []string{filepath.Join(tmp, "root"), "../root", "../root/"} {
		t.Setenv("FINCROOT", root)
		prog, d, err = resolve.Load("a.fc", resolve.Options{})
		if err != nil || len(d) > 0 {
			t.Fatal(root, err, d)
		}
		f, key := which(prog)
		if want := filepath.Join(filepath.Clean(root), "std", "hello", "hello.fc"); f != want {
			t.Errorf("FINCROOT=%s: std/hello file %q, want %q", root, f, want)
		}
		if key != filepath.Join(tmp, "root", "std", "hello") {
			t.Errorf("FINCROOT=%s: key %q", root, key)
		}
		if len(prog.Packages) != 4 {
			t.Errorf("FINCROOT=%s: %d packages (std/inner must load from FINCROOT too)", root, len(prog.Packages))
		}
	}

	t.Setenv("FINCROOT", "../broken")
	_, d, err = resolve.Load("a.fc", resolve.Options{})
	if err != nil || codesOf(d) != "E2001" || d[0].Span.File != filepath.Join("..", "broken", "std", "hello", "hello.fc") {
		t.Errorf("broken FINCROOT package: %v %v", err, d)
	}

	t.Setenv("FINCROOT", filepath.Join(tmp, "nowhere"))
	_, d, err = resolve.Load("a.fc", resolve.Options{})
	if err != nil || codesOf(d) != "E2001" || !strings.Contains(d[0].Message, "no directory "+filepath.Join(tmp, "nowhere", "std", "hello")) {
		t.Errorf("missing FINCROOT package: %v %v", err, d)
	}
}

func TestScopesAndCaptures(t *testing.T) {
	prog, d := load(t, map[string]string{"a.fc": `package main
const x = 1
driver func f(c Conn) {
	x := 2
	a, x := 3, 4
	if x := 5; x > 0 {
		_ = x
	}
	g := func() u32 {
		h := func() u32 { return u32(a) }
		return h()
	}
	_ = g
	defer c.Close()
}
`}, "a.fc")
	if len(d) > 0 {
		t.Fatal(d)
	}
	var defs, lits []ast.Node
	ast.Inspect(prog.Root.AST.Files[0], func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			if n.Name == "x" && prog.Defs[n.ID()] != nil {
				defs = append(defs, n)
			}
		case *ast.FuncLit:
			lits = append(lits, n)
		}
		return true
	})
	// const x, x := 2, x := 5 (the second `x` in `a, x :=` is reused).
	if len(defs) != 3 {
		t.Errorf("x defined %d times, want 3", len(defs))
	}
	for _, l := range lits {
		caps := prog.Captures[l.ID()]
		if len(caps) != 1 || caps[0].Name != "a" || caps[0].VarKind != resolve.VarLocal {
			t.Errorf("captures of literal at %s: %v", l.Span(), caps)
		}
	}
	// Universe.
	for _, name := range []string{"ErrParse", "Conn", "Slot", "CodeUniv", "NewScope", "SIGTERM", "Millisecond", "byte", "panic"} {
		if prog.Universe.LookupLocal(name) == nil {
			t.Errorf("universe lacks %s", name)
		}
	}
}
