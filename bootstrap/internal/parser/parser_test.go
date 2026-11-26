package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
)

func mustParse(t *testing.T, name, src string) (*ast.File, *ast.IDAlloc) {
	t.Helper()
	ids := &ast.IDAlloc{}
	f, diags := ParseFile(name, []byte(src), ids)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics:\n%s\nsource:\n%s", diags.Error(), src)
	}
	checkInvariants(t, f, ids)
	return f, ids
}

// checkInvariants verifies NodeIDs are unique, dense (1..Count), children
// have smaller IDs than parents, and child spans nest inside parent spans.
func checkInvariants(t *testing.T, f *ast.File, ids *ast.IDAlloc) {
	t.Helper()
	seen := make([]bool, ids.Count()+1)
	var visit func(n ast.Node)
	visit = func(n ast.Node) {
		id := n.ID()
		if id == 0 || int(id) > ids.Count() {
			t.Fatalf("%T has bad ID %d", n, id)
		}
		if seen[id] {
			t.Fatalf("%T: duplicate ID %d", n, id)
		}
		seen[id] = true
		sp := n.Span()
		if !sp.IsValid() || sp.File != f.Path || sp.End.Offset < sp.Start.Offset {
			t.Fatalf("%T #%d has bad span %+v", n, id, sp)
		}
		for _, c := range ast.Children(n) {
			if c.ID() >= id {
				t.Fatalf("child %T #%d not before parent %T #%d", c, c.ID(), n, id)
			}
			cs := c.Span()
			if cs.Start.Offset < sp.Start.Offset || cs.End.Offset > sp.End.Offset {
				t.Fatalf("child %T %v-%v outside parent %T %v-%v", c, cs.Start, cs.End, n, sp.Start, sp.End)
			}
			visit(c)
		}
	}
	visit(f)
	for i := 1; i <= ids.Count(); i++ {
		if !seen[i] {
			t.Fatalf("ID %d allocated but not reachable (IDs not dense)", i)
		}
	}
}

// roundTrip checks that Format(parse(src)) re-parses to the same Format.
func roundTrip(t *testing.T, name, src string) string {
	t.Helper()
	f, _ := mustParse(t, name, src)
	out1 := ast.Format(f)
	f2, _ := mustParse(t, name+".printed", out1)
	out2 := ast.Format(f2)
	if out1 != out2 {
		t.Fatalf("round trip mismatch:\n--- first\n%s\n--- second\n%s", out1, out2)
	}
	return out1
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTCPEchoFixture(t *testing.T) {
	src := readFixture(t, "tcp_echo.fc")
	f, _ := mustParse(t, "tcp_echo.fc", src)
	roundTrip(t, "tcp_echo.fc", src)
	if f.Package.Name != "main" {
		t.Fatal(f.Package.Name)
	}
	var kinds []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.ConstDecl:
			kinds = append(kinds, "const")
		case *ast.TypeDecl:
			kinds = append(kinds, "type "+d.Name.Name)
		case *ast.FuncDecl:
			kinds = append(kinds, d.Modifier.String()+" "+d.Name.Name)
		}
	}
	want := "const|comptime PoolCode|type ConnPool|total writeAll|driver handleConn|driver main"
	if got := strings.Join(kinds, "|"); got != want {
		t.Fatalf("decls: %s", got)
	}
	// type ConnPool = El(PoolCode(MaxConns, BufSize))
	td := f.Decls[2].(*ast.TypeDecl)
	if !td.Alias {
		t.Fatal("ConnPool should be an alias")
	}
	el := td.Type.(*ast.ElType)
	if ast.Format(el.Code) != "PoolCode(MaxConns, BufSize)" {
		t.Fatal(ast.Format(el.Code))
	}
	// comptime func PoolCode(...) Code 0
	pc := f.Decls[1].(*ast.FuncDecl)
	if ct, ok := pc.Sig.Results[0].(*ast.CodeType); !ok || ast.Format(ct.Level) != "0" {
		t.Fatalf("PoolCode result: %#v", pc.Sig.Results[0])
	}
	// writeAll: for rem := data; len(rem) > 0; { ... }
	wa := f.Decls[3].(*ast.FuncDecl)
	loop := wa.Body.Stmts[0].(*ast.ForStmt)
	if !loop.ThreeClause || loop.Init == nil || loop.Cond == nil || loop.Post != nil || loop.Driver {
		t.Fatalf("writeAll loop: %+v", loop)
	}
	// handleConn: driver for { ... }
	hc := f.Decls[4].(*ast.FuncDecl)
	if _, ok := hc.Body.Stmts[0].(*ast.DeferStmt); !ok {
		t.Fatal("expected defer")
	}
	dl := hc.Body.Stmts[1].(*ast.ForStmt)
	if !dl.Driver || dl.ThreeClause || dl.Cond != nil {
		t.Fatalf("driver for: %+v", dl)
	}
	// buf *[BufSize]byte
	bt := hc.Sig.Params[3].Type.(*ast.PointerType).Elem.(*ast.ArrayType)
	if ast.Format(bt) != "[BufSize]byte" {
		t.Fatal(ast.Format(bt))
	}
	// main: scope statement with a driver func literal argument
	mn := f.Decls[5].(*ast.FuncDecl)
	var sc *ast.ScopeStmt
	var lits []*ast.FuncLit
	ast.Inspect(mn, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ScopeStmt:
			sc = n
		case *ast.FuncLit:
			lits = append(lits, n)
		}
		return true
	})
	if sc == nil || sc.Name.Name != "conns" || ast.Format(sc.Value) != "NewScope(MaxConns)" {
		t.Fatal("scope stmt not found")
	}
	if len(lits) != 1 || !lits[0].Driver {
		t.Fatal("driver func literal not found")
	}
}

func TestGrammarFixture(t *testing.T) {
	src := readFixture(t, "grammar.fc")
	out := roundTrip(t, "grammar.fc", src)
	// Spot checks on canonical output.
	for _, want := range []string{
		"type Vec[N usize, T any] struct { data [N]T; len usize }",
		"type Map[K, V any] struct { keys []K; vals []V }",
		"type Token enum { Ident(Span); Int(u64, Span); EOF }",
		"type Handler driver func(buf *[4]byte) error",
		"type Lvl2 Code (n + 1)",
		"type SourceBuf = El(ByteBufCode(MaxSourceBytes))",
		"type Q = pkg.Type[u8, 4]",
		"type PT = Vec[4, *u8]",
		"type DT = Vec[1, driver func()]",
		"type CT = Vec[1, Code 0]",
		"func (s Scope) AcquireSlot[N usize, T any](ctx Context, pool *[N]T) (Slot[T], error) {",
		"func log(format string, args ...u8) {}",
		"func single() (u8) {",
		"comptime func ByteBufCode(cap usize) Code 0 {",
		"func (p *Parser) parseExpr(depth usize) (NodeID, error) decreases(depth) {",
		"for lo < hi decreases(hi - lo) {",
		"for i := 0; i < n; i++ decreases(n - i) {",
		"for decreases(n) {",
		"for rem := data; len(rem) > 0; {",
		"for ; ; {",
		"driver for x := 0; x < 1; x++ {",
		"\t\tToken.Ident(sp) -> handle(sp)",
		"\t\t_ -> {}",
		"if (T{a: 1}).ok {",
		"if v := f(); v > 0 {",
		"e = f[T](x) + Vec[4, u8]{len: 0} + pkg.T{a: 1} + pkg.Vec[4]{} + T{} + u32(x)",
		"import (\n\tast \"compiler/ast\"\n\t\"compiler/diag\"\n)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("canonical output lacks %q", want)
		}
	}
}

// parseExpr parses `const X = <expr>` and returns the expression.
func parseExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	f, _ := mustParse(t, "e.fc", "package p\nconst X = "+src+"\n")
	return f.Decls[0].(*ast.ConstDecl).Specs[0].Value
}

func TestPrecedence(t *testing.T) {
	cases := map[string]string{
		"a || b && c":              "(a || (b && c))",
		"a && b || c":              "((a && b) || c)",
		"a == b && c != d":         "((a == b) && (c != d))",
		"a == b < c":               "(a == (b < c))",
		"a < b == c > d":           "((a < b) == (c > d))",
		"a + b < c * d":            "((a + b) < (c * d))",
		"a + b * c":                "(a + (b * c))",
		"a - b - c":                "((a - b) - c)",
		"a / b * c % d":            "(((a / b) * c) % d)",
		"a | b & c":                "(a | (b & c))",
		"a ^ b << c":               "(a ^ (b << c))",
		"a >> b + c":               "((a >> b) + c)",
		"a & b == c":               "((a & b) == c)",
		"-a * b":                   "((-a) * b)",
		"!a && b":                  "((!a) && b)",
		"-a.b(c)[d]":               "(-a.b(c)[d])",
		"*p + 1":                   "((*p) + 1)",
		"&x.y":                     "(&x.y)",
		"**pp":                     "(*(*pp))",
		"- -x":                     "(-(-x))",
		"(a + b) * c":              "(((a + b)) * c)",
		"len(rem) > 0":             "(len(rem) > 0)",
		"30 * Second":              "(30 * Second)",
		"4 * 1024 * 1024":          "((4 * 1024) * 1024)",
		"depth - 1":                "(depth - 1)",
		"a[i:j][k]":                "a[i:j][k]",
		"f[T](x)":                  "f[T](x)",
		"f[N, T]":                  "f[N, T]",
		"a.b.c":                    "a.b.c",
		"T{a: 1 + 2}.f":            "T{a: (1 + 2)}.f",
		"u32(x) << 8":              "(u32(x) << 8)",
		"El(PoolCode(1, 2))":       "El(PoolCode(1, 2))",
		"func() u8 { return 1 }()": "func() u8 {\n\treturn 1\n}()",
	}
	for src, want := range cases {
		if got := ast.ExprString(parseExpr(t, src)); got != want {
			t.Errorf("%s:\n got %s\nwant %s", src, got, want)
		}
	}
}

func TestExprNodeKinds(t *testing.T) {
	check := func(src string, want any) {
		t.Helper()
		e := parseExpr(t, src)
		if got, w := typeName(e), typeName(want); got != w {
			t.Errorf("%s: got %s want %s", src, got, w)
		}
	}
	check("x", &ast.Ident{})
	check("1", &ast.BasicLit{})
	check(`"s"`, &ast.BasicLit{})
	check("'c'", &ast.BasicLit{})
	check("true", &ast.BoolLit{})
	check("nil", &ast.NilLit{})
	check("a + b", &ast.BinaryExpr{})
	check("-a", &ast.UnaryExpr{})
	check("(a)", &ast.ParenExpr{})
	check("f(a)", &ast.CallExpr{})
	check("u32(a)", &ast.CallExpr{})
	check("a[i]", &ast.IndexExpr{})
	check("f[A, B]", &ast.IndexExpr{})
	check("a[1:2]", &ast.SliceExpr{})
	check("a.b", &ast.SelectorExpr{})
	check("func() {}", &ast.FuncLit{})
	check("driver func() {}", &ast.FuncLit{})
	check("T{a: 1}", &ast.StructLit{})
	check("[]byte", &ast.TypeExpr{})
	check("El(x)", &ast.TypeExpr{})
	check("Code 0", &ast.TypeExpr{})
	check("func(x u8) bool", &ast.TypeExpr{})

	lit := parseExpr(t, "0x_FF").(*ast.BasicLit)
	if lit.Kind != ast.IntLit || lit.Int != 255 || lit.Raw != "0x_FF" {
		t.Errorf("int lit %+v", lit)
	}
	s := parseExpr(t, `"a\x41\n"`).(*ast.BasicLit)
	if s.Kind != ast.StringLit || s.Str != "aA\n" {
		t.Errorf("string lit %+v", s)
	}
	c := parseExpr(t, `'\t'`).(*ast.BasicLit)
	if c.Kind != ast.CharLit || c.Int != 9 {
		t.Errorf("char lit %+v", c)
	}
	sl := parseExpr(t, "pkg.Vec[4, u8]{len: 0, cap: n,}").(*ast.StructLit)
	if sl.Type.Pkg.Name != "pkg" || sl.Type.Name.Name != "Vec" || len(sl.Type.Args) != 2 || len(sl.Fields) != 2 {
		t.Errorf("struct lit %s", ast.Format(sl))
	}
	se := parseExpr(t, "a[:]").(*ast.SliceExpr)
	if se.Lo != nil || se.Hi != nil {
		t.Error("a[:] bounds")
	}
	ix := parseExpr(t, "f[[]byte, *u8, Code 1]").(*ast.IndexExpr)
	if _, ok := ix.Indices[0].(*ast.TypeExpr); !ok {
		t.Error("[]byte should be TypeExpr")
	}
	if u, ok := ix.Indices[1].(*ast.UnaryExpr); !ok || u.Op != ast.OpDeref {
		t.Error("*u8 in expression context is a deref (resolver converts)")
	}
	if pt, ok := ast.ExprAsType(ix.Indices[1]); !ok || ast.Format(pt) != "*u8" {
		t.Error("ExprAsType(*u8)")
	}
	if te, ok := ix.Indices[2].(*ast.TypeExpr); !ok || ast.Format(te.Type) != "Code 1" {
		t.Error("Code 1")
	}
}

func typeName(v any) string { return strings.TrimPrefix(fmt.Sprintf("%T", v), "*ast.") }

// parseBody parses statements inside a function body and returns them.
func parseBody(t *testing.T, body string) []ast.Stmt {
	t.Helper()
	f, _ := mustParse(t, "s.fc", "package p\nfunc f() {\n"+body+"\n}\n")
	return f.Decls[0].(*ast.FuncDecl).Body.Stmts
}

func TestStatements(t *testing.T) {
	cases := []struct {
		src  string
		kind string
		out  string
	}{
		{"x := 1", "ShortVarDecl", "x := 1"},
		{"a, b := f(), 2", "ShortVarDecl", "a, b := f(), 2"},
		{"a, b = b, a", "AssignStmt", "a, b = b, a"},
		{"x += 1", "AssignStmt", "x += 1"},
		{"x -= 1", "AssignStmt", "x -= 1"},
		{"x *= 1", "AssignStmt", "x *= 1"},
		{"x /= 1", "AssignStmt", "x /= 1"},
		{"a[i] = *p", "AssignStmt", "a[i] = *p"},
		{"_ = conn.Close()", "AssignStmt", "_ = conn.Close()"},
		{"x++", "IncDecStmt", "x++"},
		{"a.b--", "IncDecStmt", "a.b--"},
		{"f()", "ExprStmt", "f()"},
		{"const K = 1", "DeclStmt", "const K = 1"},
		{"var v u8 = 1", "DeclStmt", "var v u8 = 1"},
		{"{ f() }", "BlockStmt", "{\n\tf()\n}"},
		{"if a { b() } else { c() }", "IfStmt", "if a {\n\tb()\n} else {\n\tc()\n}"},
		{"if x := f(); x { }", "IfStmt", "if x := f(); x {}"},
		{"if a {} else if b {}", "IfStmt", "if a {} else if b {}"},
		{"match x { A -> f(); _ -> {} }", "MatchStmt", "match x {\n\tA -> f()\n\t_ -> {}\n}"},
		{"for {}", "ForStmt", "for {}"},
		{"driver for {}", "ForStmt", "driver for {}"},
		{"for x {}", "ForStmt", "for x {}"},
		{"for i := 0; i < 3; i++ {}", "ForStmt", "for i := 0; i < 3; i++ {}"},
		{"for ; x; {}", "ForStmt", "for ; x; {}"},
		{"for x decreases(n) {}", "ForStmt", "for x decreases(n) {}"},
		{"scope s := NewScope(4) { s.Wait() }", "ScopeStmt", "scope s := NewScope(4) {\n\ts.Wait()\n}"},
		{"defer f()", "DeferStmt", "defer f()"},
		{"return", "ReturnStmt", "return"},
		{"return 1, nil", "ReturnStmt", "return 1, nil"},
		{"break", "BreakStmt", "break"},
		{"continue", "ContinueStmt", "continue"},
		{"driver func() {}()", "ExprStmt", "driver func() {}()"},
	}
	for _, c := range cases {
		stmts := parseBody(t, c.src)
		if len(stmts) != 1 {
			t.Errorf("%q: %d stmts", c.src, len(stmts))
			continue
		}
		if k := typeName(stmts[0]); k != c.kind {
			t.Errorf("%q: kind %s want %s", c.src, k, c.kind)
		}
		if out := ast.Format(stmts[0]); out != c.out {
			t.Errorf("%q: format\n%s\nwant\n%s", c.src, out, c.out)
		}
	}
}

func TestMatchPatterns(t *testing.T) {
	stmts := parseBody(t, `match v {
	_ -> a()
	Red -> b()
	Color.Red -> c()
	Some(x) -> d(x)
	Token.Int(n, sp) -> { e(n, sp) }
	0 -> f()
	"s" -> g()
	'c' -> h()
	true -> i()
	nil -> j()
}`)
	m := stmts[0].(*ast.MatchStmt)
	want := []string{"WildcardPattern", "CtorPattern", "CtorPattern", "CtorPattern", "CtorPattern", "LiteralPattern", "LiteralPattern", "LiteralPattern", "LiteralPattern", "LiteralPattern"}
	if len(m.Arms) != len(want) {
		t.Fatalf("%d arms", len(m.Arms))
	}
	for i, a := range m.Arms {
		if typeName(a.Pattern) != want[i] {
			t.Errorf("arm %d: %s", i, typeName(a.Pattern))
		}
	}
	cp := m.Arms[4].Pattern.(*ast.CtorPattern)
	if cp.Qual.Name != "Token" || cp.Name.Name != "Int" || len(cp.Bindings) != 2 || !cp.HasParens {
		t.Errorf("ctor pattern %+v", cp)
	}
	if m.Arms[4].Block == nil || m.Arms[4].Expr != nil || m.Arms[0].Expr == nil {
		t.Error("arm bodies")
	}
}

func TestTypes(t *testing.T) {
	cases := map[string]string{
		"u8":                        "NamedType",
		"pkg.T":                     "NamedType",
		"Vec[4, u8]":                "NamedType",
		"[4]u8":                     "ArrayType",
		"[]u8":                      "SliceType",
		"*u8":                       "PointerType",
		"struct{ a u8 }":            "StructType",
		"enum{ A; B(u8) }":          "EnumType",
		"func(a u8) (u8, error)":    "FuncType",
		"driver func(buf *[4]byte)": "FuncType",
		"Code 0":                    "CodeType",
		"El(PoolCode(1, 2))":        "ElType",
	}
	for src, kind := range cases {
		f, _ := mustParse(t, "t.fc", "package p\ntype X "+src+"\n")
		ty := f.Decls[0].(*ast.TypeDecl).Type
		if typeName(ty) != kind {
			t.Errorf("%s: %s want %s", src, typeName(ty), kind)
		}
		if ast.Format(ty) != src && !strings.HasPrefix(src, "struct") && !strings.HasPrefix(src, "enum") {
			t.Errorf("%s: format %s", src, ast.Format(ty))
		}
	}
	f, _ := mustParse(t, "t.fc", "package p\ntype Vec[N usize, T any] struct { data [N]T; len usize }\n")
	td := f.Decls[0].(*ast.TypeDecl)
	tp := td.TypeParams.Params
	if len(tp) != 2 || tp[0].Names[0].Name != "N" || typeName(tp[0].Constraint) != "NamedType" || typeName(tp[1].Constraint) != "AnyType" {
		t.Fatalf("type params: %s", ast.Format(td))
	}
	// array vs type-parameter disambiguation
	f, _ = mustParse(t, "t.fc", "package p\ntype A [N]T\ntype B [N * 2]T\ntype C[T any] []T\ntype D[A, B any] struct{}\ntype E [a.b]u8\n")
	wantTP := []bool{false, false, true, true, false}
	for i, d := range f.Decls {
		if (d.(*ast.TypeDecl).TypeParams != nil) != wantTP[i] {
			t.Errorf("decl %d: type params = %v", i, !wantTP[i])
		}
	}
	// receivers
	f, _ = mustParse(t, "t.fc", "package p\nfunc (v *Vec[N, T]) Push(x T) {}\nfunc (s Scope) Wait() {}\n")
	r := f.Decls[0].(*ast.FuncDecl).Recv
	if !r.Pointer || r.TypeName.Name != "Vec" || len(r.TypeParams) != 2 {
		t.Errorf("recv %+v", r)
	}
	if f.Decls[1].(*ast.FuncDecl).Recv.Pointer {
		t.Error("value receiver")
	}
}

// Snippets in the style of the self-hosting compiler (spec 5.2, 6.2, 9.4).
func TestSelfHostingStyle(t *testing.T) {
	src := `package compiler

const (
	MaxSourceBytes = 4 * 1024 * 1024
	MaxOutputBytes = 8 * 1024 * 1024
	MaxNodes       = 131_072
	MaxBuckets     = 65_536
)

comptime func ByteBufCode(cap usize) Code 0 {
	return CodeArray(cap, CodeU8())
}

type SourceBuf = El(ByteBufCode(MaxSourceBytes))
type EmitBuf   = El(ByteBufCode(MaxOutputBytes))

type Vec[N usize, T any] struct { data [N]T; len usize }

type NodeID = u32

type Span struct {
	file u16
	off  u32
	len  u32
}

type AstNode enum {
	Ident(Span)
	Binary(u8, NodeID, NodeID)
	Call(NodeID, u32, u32)
	Empty
}

type Parser struct {
	arena *NodeArena
	pos   usize
}

func (v *Vec[N, T]) Push(x T) error {
	if v.len == N {
		return ErrArenaFull
	}
	v.data[v.len] = x
	v.len++
	return nil
}

func (p *Parser) parseExpr(depth usize) (NodeID, error) decreases(depth) {
	if depth == 0 {
		return 0, ErrMaxDepth
	}
	return p.parsePrimary(depth - 1)
}

func (p *Parser) parsePrimary(depth usize) (NodeID, error) decreases(depth) {
	if depth == 0 {
		return 0, ErrMaxDepth
	}
	// ... при '(' извиква p.parseExpr(depth - 1) ...
	return p.parseExpr(depth - 1)
}

func lookup(t *SymTable, h u32) (u32, bool) {
	for step := 0; step < MaxBuckets; step++ {
		idx := (h + u32(step)) % MaxBuckets
		if t.used[idx] == false {
			return 0, false
		}
		if t.hash[idx] == h {
			return idx, true
		}
	}
	return 0, false
}

func scan(src []byte) usize {
	pos := usize(0)
	for pos < len(src) decreases(len(src) - pos) {
		match src[pos] {
			' ' -> { pos++ }
			'\n' -> { pos += 1 }
			_ -> { return pos }
		}
	}
	return pos
}

func search(a []u32, x u32) bool {
	lo := usize(0)
	hi := len(a)
	for lo < hi decreases(hi - lo) {
		mid := lo + (hi-lo)/2
		if a[mid] == x {
			return true
		} else if a[mid] < x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return false
}

func newParser(arena *NodeArena) Parser {
	return Parser{arena: arena, pos: 0}
}

driver func main(fs FS, env Env, io Stdio, exec Exec) error {
	var src SourceBuf
	n, err := fs.ReadFileInto(env.Arg(1), src[:])
	if err != nil {
		_ = io.WriteStderr(strBytes("read failed\n"))
		return err
	}
	toks := Vec[MaxNodes, u32]{len: 0}
	_ = toks
	_ = n
	return nil
}
`
	f, _ := mustParse(t, "compiler.fc", src)
	roundTrip(t, "compiler.fc", src)
	var names []string
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			names = append(names, fd.Name.Name)
			if fd.Name.Name == "parseExpr" && (fd.Decreases == nil || ast.Format(fd.Decreases.Measure) != "depth") {
				t.Error("parseExpr decreases clause")
			}
		}
	}
	if strings.Join(names, ",") != "ByteBufCode,Push,parseExpr,parsePrimary,lookup,scan,search,newParser,main" {
		t.Fatal(names)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		src  string
		want string // "line:col: error[CODE]: fragment"
	}{
		{"", "1:1: error[E1001]: expected package clause, found EOF"},
		{"func f() {}", "1:1: error[E1001]: expected package clause"},
		{"package", "1:8: error[E1001]: expected identifier, found EOF"},
		{"package p\nfunc f()\n{\n}", "2:9: error[E1001]: unexpected newline; the '{' of a function body must be on the same line"},
		{"package p\nfunc f() {\n\tif x\n\t{\n\t}\n}", "3:6: error[E1001]: unexpected newline; the '{' of an if statement must be on the same line"},
		{"package p\nfunc f() u8\n{\n}", "2:12: error[E1001]: unexpected newline; the '{' of a function body must be on the same line"},
		{"package p\nfunc f() {\n\tfor i := 0; i < n; i++\n\t{\n\t}\n}", "3:24: error[E1001]: unexpected newline; the '{' of a for statement must be on the same line"},
		{"package p\nfunc f() {\n\tx :=\n}", "4:1: error[E1001]: expected expression, found '}'"},
		{"package p\nfunc f() {\n\ta.b := 1\n}", "3:2: error[E1001]: non-name a.b on left side of :="},
		{"package p\nfunc f() {\n\ta, b += 1\n}", "3:7: error[E1001]: assignment operator += requires single-valued expressions"},
		{"package p\nfunc f() {\n\ta, b\n}", "3:6: error[E1001]: expected ':=', '=' or assignment operator after expression list, found newline"},
		{"package p\nconst X", "2:8: error[E1001]: expected type, found EOF"},
		{"package p\nconst X u8", "2:11: error[E1001]: expected '=' in constant declaration"},
		{"package p\nvar x", "2:6: error[E1001]: expected type, found EOF"},
		{"package p\ntype A any", "2:8: error[E1001]: 'any' may only be used as a type parameter constraint"},
		{"package p\nfunc f(a ...u8, b u8) {}", "2:8: error[E1001]: can only use ... with final parameter"},
		{"package p\nfunc f(a, b ...u8) {}", "2:13: error[E1001]: variadic parameter must have exactly one name"},
		{"package p\nfunc f(a) {}", "2:9: error[E1001]: expected parameter type, found ')'"},
		{"package p\nfunc f() {\n\tmatch x {\n\t\t1\n\t}\n}", "4:4: error[E1001]: expected '->', found newline"},
		{"package p\nfunc f() {\n\tmatch x {\n\t\t-1 -> a()\n\t}\n}", "4:3: error[E1001]: expected pattern"},
		{"package p\nfunc f() {\n\tfor i := 0; i < n; j := 1 {\n\t}\n}", "3:21: error[E1001]: cannot declare in post statement of for loop"},
		{"package p\nfunc f() {\n\tfor i := 0; i < n {\n\t}\n}", "3:20: error[E1001]: expected ';' in for clause, found '{'"},
		{"package p\nfunc f() {\n\tif x := 1 {\n\t}\n}", "3:5: error[E1001]: cannot use x := 1 as value in if condition"},
		{"package p\nfunc f() {\n\tif x := 1; {\n\t}\n}", "3:13: error[E1001]: missing condition in if statement"},
		{"package p\nfunc f() {\n\ts := \"abc\n}", "3:7: error[E1002]: unterminated string literal"},
		{"package p\n/* open", "2:1: error[E1002]: unterminated block comment"},
		{"package p\nfunc f() {\n\tx := 1 @ 2\n}", "3:9: error[E1001]: invalid character '@'"},
		{"package p\nfunc f() {}\nimport \"x\"", "3:1: error[E1001]: imports must appear before other declarations"},
		{"package p\nfunc f() {\n\ta[]\n}", "3:4: error[E1001]: expected index expression, found ']'"},
		{"package p\nvar x = T{1}", "2:11: error[E1001]: expected field initializer 'name: value', found integer literal 1"},
		{"package p\nvar x = T{\n\ta: 1\n}", "3:6: error[E1001]: unexpected newline in struct literal"},
		{"package p\nvar x = f(\n\ta\n)", "3:3: error[E1001]: expected ',' or ')' in argument list, found newline"},
		{"package p\nfunc f() {\n\tdriver x\n}", "3:2: error[E1001]: unexpected keyword driver"},
		{"package p\nfunc f() {\n\treturn 1 2\n}", "3:11: error[E1001]: unexpected integer literal 2 after statement"},
		{"package p\nfunc f() {\n\tif x {\n\t}\n\telse {\n\t}\n}", "5:2: error[E1001]: expected expression, found keyword else"},
		{"package p\nfunc f() {\n\tif x == T{} {\n\t}\n}", "3:14: error[E1001]: unexpected '{' after statement"},
		{"package p\nfunc f() {\n\t;\n}", "3:2: error[E1001]: unexpected ';'; expected statement"},
		{"package p\nfunc f() {\n\ttype T u8\n}", "3:2: error[E1001]: unexpected keyword type in statement position"},
		{"package p\nfunc f() {", "2:11: error[E1001]: unexpected EOF; expected '}'"},
		{"package p\nfunc f() {\n\tscope s = NewScope(1) {}\n}", "3:10: error[E1001]: expected ':=', found '='"},
		{"package p\nfunc f() {\n\tif x {} else for {}\n}", "3:15: error[E1001]: expected 'if' or '{' after else, found keyword for"},
		{"package p\ntype T enum { A(); }", "2:17: error[E1001]: expected type, found ')'"},
		{"package p\nfunc f[]() {}", "2:8: error[E1001]: expected identifier, found ']'"},
		{"package p\nfunc f[T]() {}", "2:9: error[E1001]: expected type constraint (any, usize or a type), found ']'"},
		{"package p\nfunc (r *T[]) f() {}", "2:12: error[E1001]: expected identifier, found ']'"},
		{"package p\nimport x", "2:9: error[E1001]: expected import path string, found EOF"},
		{"package p\nimport \"\"", "2:8: error[E1001]: empty import path"},
		{"package p\nfunc f() (u8,) {}", "2:14: error[E1001]: expected type, found ')'"},
		{"package p\nx := 1", "2:1: error[E1001]: expected declaration (const, type, var or func), found identifier x"},
		{"package p\nconst X = 1 const Y = 2", "2:13: error[E1001]: unexpected keyword const after declaration"},
		{"package p\nfunc f() {\n\tdefer\n}", "4:1: error[E1001]: expected expression, found '}'"},
		{"package p\nvar x = 0777", "2:9: error[E1001]: invalid integer literal \"0777\": leading zero"},
	}
	for _, c := range cases {
		f, diags := ParseFile("e.fc", []byte(c.src), nil)
		if f != nil || len(diags) != 1 {
			t.Errorf("%q: expected exactly one error, got file=%v diags=%v", c.src, f != nil, diags)
			continue
		}
		got := strings.TrimPrefix(diags[0].String(), "e.fc:")
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%q:\n got %s\nwant %s", c.src, got, c.want)
		}
	}
}

func TestNestingLimit(t *testing.T) {
	deep := "package p\nconst X = " + strings.Repeat("(", MaxNesting+10) + "1" + strings.Repeat(")", MaxNesting+10) + "\n"
	_, diags := ParseFile("d.fc", []byte(deep), nil)
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "nesting too deep") {
		t.Fatalf("got %v", diags)
	}
	ok := "package p\nconst X = " + strings.Repeat("(", 200) + "1" + strings.Repeat(")", 200) + "\n"
	if _, diags := ParseFile("d.fc", []byte(ok), nil); len(diags) != 0 {
		t.Fatal(diags)
	}
}

func TestDeterministicDump(t *testing.T) {
	src := readFixture(t, "tcp_echo.fc")
	f1, _ := ParseFile("x.fc", []byte(src), nil)
	f2, _ := ParseFile("x.fc", []byte(src), nil)
	d1, d2 := ast.DumpString(f1), ast.DumpString(f2)
	if d1 != d2 || !strings.Contains(d1, "ScopeStmt") || !strings.Contains(d1, "Modifier=driver") {
		t.Fatal("dump not deterministic or incomplete")
	}
}

func writeFiles(t *testing.T, files map[string]string) string {
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
	return dir
}

func TestParseDir(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"b.fc":      "package lexer\nfunc b() {}\n",
		"a.fc":      "package lexer\nfunc a() {}\n",
		"c.fc":      "package lexer\nfunc c() {}\n",
		"notes.txt": "ignored",
		"sub/d.fc":  "package other\n",
		"z.fc.bak":  "ignored",
	})
	ids := &ast.IDAlloc{}
	pkg, diags, err := ParseDir(dir, ids)
	if err != nil || len(diags) != 0 {
		t.Fatal(err, diags)
	}
	if pkg.Name != "lexer" || len(pkg.Files) != 3 {
		t.Fatalf("pkg %s with %d files", pkg.Name, len(pkg.Files))
	}
	for i, want := range []string{"a.fc", "b.fc", "c.fc"} {
		if filepath.Base(pkg.Files[i].Path) != want {
			t.Errorf("file %d = %s", i, pkg.Files[i].Path)
		}
	}
	// IDs are unique across files of the package.
	seen := map[ast.NodeID]bool{}
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			if seen[n.ID()] {
				t.Fatalf("duplicate ID %d across files", n.ID())
			}
			seen[n.ID()] = true
			return true
		})
	}
	if len(seen) != ids.Count() {
		t.Fatalf("seen %d, allocated %d", len(seen), ids.Count())
	}

	// ParsePath on a file and on a dir.
	if pkg, _, err := ParsePath(filepath.Join(dir, "a.fc"), nil); err != nil || len(pkg.Files) != 1 {
		t.Fatal(err)
	}
	if _, _, err := ParsePath(filepath.Join(dir, "missing.fc"), nil); err == nil {
		t.Fatal("expected error")
	}
	if _, _, err := ParseDir(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "no .fc files") {
		t.Fatal(err)
	}
}

func TestParseDirErrors(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a.fc": "package one\n",
		"b.fc": "package two\n",
		"c.fc": "package one\nfunc f( {}\n",
		"d.fc": "package one\nvar s = \"x\n",
	})
	_, diags, err := ParseDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range diags {
		got = append(got, strings.TrimPrefix(d.String(), dir+string(filepath.Separator)))
	}
	want := []string{
		"b.fc:1:9: error[E1001]: package two; expected package one",
		"c.fc:2:9: error[E1001]: expected identifier, found '{'",
		"d.fc:2:9: error[E1002]: unterminated string literal",
	}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("diag %d:\n got %s\nwant %s", i, got[i], want[i])
		}
	}
	if diags[0].Code != diag.E1001 {
		t.Error("code")
	}
}

// reflectNodes collects every ast.Node reachable through struct fields by
// reflection, independently of ast.Children, to verify Children is complete.
func reflectNodes(n ast.Node, out map[ast.Node]bool) {
	var walk func(v reflect.Value)
	nodeT := reflect.TypeOf((*ast.Node)(nil)).Elem()
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Pointer:
			if v.IsNil() {
				return
			}
			if v.Type().Implements(nodeT) {
				nd := v.Interface().(ast.Node)
				if out[nd] {
					return
				}
				out[nd] = true
			}
			walk(v.Elem())
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(n))
}

func TestChildrenComplete(t *testing.T) {
	for _, name := range []string{"grammar.fc", "tcp_echo.fc"} {
		ids := &ast.IDAlloc{}
		f, diags := ParseFile(name, []byte(readFixture(t, name)), ids)
		if len(diags) > 0 {
			t.Fatal(diags)
		}
		viaReflect := map[ast.Node]bool{}
		reflectNodes(f, viaReflect)
		viaChildren := map[ast.Node]bool{}
		ast.Inspect(f, func(n ast.Node) bool { viaChildren[n] = true; return true })
		if len(viaReflect) != len(viaChildren) || len(viaChildren) != ids.Count() {
			t.Fatalf("%s: reflect %d, children %d, ids %d", name, len(viaReflect), len(viaChildren), ids.Count())
		}
		for n := range viaReflect {
			if !viaChildren[n] {
				t.Errorf("%s: %T not reachable via Children", name, n)
			}
		}
	}
}
