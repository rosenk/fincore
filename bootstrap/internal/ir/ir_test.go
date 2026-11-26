package ir

import (
	"os"
	"path/filepath"
	"testing"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/comptime"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

func TestGoIdent(t *testing.T) {
	cases := map[string]string{
		"x": "x", "Point": "Point", "_": "_",
		"go": "_fc_u_go", "len": "_fc_u_len", "uint": "_fc_u_uint", "main": "_fc_u_main",
		"fincrt": "_fc_u_fincrt", "os": "_fc_u_os", "init": "_fc_u_init",
		"_fc_t0": "_fc_u__fc_t0", "_pkg_a_b": "_fc_u__pkg_a_b", "_fc_u_go": "_fc_u__fc_u_go",
	}
	for in, want := range cases {
		if got := GoIdent(in); got != want {
			t.Errorf("GoIdent(%q) = %q, want %q", in, got, want)
		}
	}
	if FieldIdent("len") != "len" || FieldIdent("type") != "_fc_u_type" {
		t.Error("FieldIdent")
	}
	if MethodIdent("Push") != "Push" || MethodIdent("ReadByte") != "_fc_m_ReadByte" || MethodIdent("range") != "_fc_m_range" {
		t.Error("MethodIdent")
	}
}

func lower(t *testing.T, files map[string]string, root string) *Program {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prog, diags, err := resolve.Load(filepath.Join(dir, root), resolve.Options{ModuleRoot: dir})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	info, diags := types.Check(prog, types.Config{Evaluator: comptime.New(10000)})
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	p, err := Lower(prog, info)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLowerPlan(t *testing.T) {
	p := lower(t, map[string]string{
		"app/main.fc": `package main

import "lib"

type Pool = El(PoolCode(4))

comptime func PoolCode(n usize) Code 0 {
	return CodeArray(n, CodeU8())
}

type Vec[N usize, T any] struct {
	data [N]T
}

func (v *Vec[N, T]) Len() usize {
	return N
}

driver func main(io Stdio) error {
	var p Pool
	unused := p[0]
	used := lib.Twice(2)
	if used == 0 {
		return ErrIO
	}
	scope s := NewScope(1) {
		s.Wait()
	}
	return nil
}
`,
		"lib/lib.fc": `package lib

func Twice(x u64) u64 {
	return x * 2
}
`,
	}, "app")
	if len(p.Packages) != 2 || p.Packages[0].Prefix != "_pkg_lib_" || p.Packages[1].Prefix != "" {
		t.Fatalf("packages: %+v", p.Packages)
	}
	root := p.Packages[1]
	if len(root.Types) != 1 || !root.Types[0].Mono() || len(root.Types[0].Methods) != 1 {
		t.Fatalf("types (alias erased, Vec mono): %+v", root.Types)
	}
	var names []string
	for _, f := range root.Funcs {
		names = append(names, p.Name(f.Info.Sym))
	}
	if len(names) != 2 || names[0] != "Len" || names[1] != "_fc_main" {
		t.Fatalf("funcs (comptime erased): %v", names)
	}
	if lib := p.Packages[0].Funcs[0]; p.Name(lib.Info.Sym) != "_pkg_lib_Twice" {
		t.Fatal(p.Name(lib.Info.Sym))
	}
	main := root.Funcs[1]
	if len(main.Scopes) != 1 || main.ScopeIndex(main.Scopes[0]) != 0 {
		t.Fatalf("scopes: %v", main.Scopes)
	}
	if len(p.Caps) != 1 || p.Caps[0] != resolve.TStdio {
		t.Fatalf("caps: %v", p.Caps)
	}
	var unused, used *resolve.Symbol
	ast.Inspect(main.Info.Body, func(n ast.Node) bool {
		if d, ok := n.(*ast.ShortVarDecl); ok {
			s := p.Prog.Defs[d.Names[0].ID()]
			switch s.Name {
			case "unused":
				unused = s
			case "used":
				used = s
			}
		}
		return true
	})
	if p.Used(unused) || !p.Used(used) {
		t.Fatalf("usage: unused=%v used=%v", p.Used(unused), p.Used(used))
	}
}

func TestTerminatingAndMatch(t *testing.T) {
	p := lower(t, map[string]string{"m.fc": `package main

type E enum {
	A
	B(u8)
}

func f(e E, n u32) u32 {
	match n {
		1 -> {
			return 1
		}
		1 -> {
			return 2
		}
		_ -> {
			return 3
		}
		4 -> {
			return 4
		}
	}
}

func g(e E) u32 {
	match e {
		B(x) -> {
			return u32(x)
		}
		A -> {
			return 0
		}
	}
}

driver func main(io Stdio) error {
	return nil
}
`}, "m.fc")
	fns := p.Packages[0].Funcs
	m1 := fns[0].Info.Body.Stmts[0].(*ast.MatchStmt)
	arms := MatchArms(p.Info, m1)
	if len(arms.Arms) != 2 || !arms.HasDefault || arms.Enum != nil {
		t.Fatalf("int match: %d arms default=%v", len(arms.Arms), arms.HasDefault)
	}
	m2 := fns[1].Info.Body.Stmts[0].(*ast.MatchStmt)
	arms = MatchArms(p.Info, m2)
	if len(arms.Arms) != 2 || !arms.HasDefault || arms.Enum == nil {
		t.Fatalf("enum match: %d arms default=%v", len(arms.Arms), arms.HasDefault)
	}
	if !IsTerminating(p.Info, m1) || !IsTerminating(p.Info, m2) {
		t.Fatal("exhaustive matches with returning arms terminate")
	}
}
