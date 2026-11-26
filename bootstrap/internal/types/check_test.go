package types_test

import (
	"os"
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/comptime"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/parser"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// checkSrc parses, resolves and type-checks a single-file package main.
func checkSrc(t *testing.T, src string, fuel uint64) (*types.Info, diag.List) {
	t.Helper()
	ids := &ast.IDAlloc{}
	f, d := parser.ParseFile("t.fc", []byte(src), ids)
	if len(d) > 0 {
		t.Fatalf("parse: %v", d)
	}
	pkg := &resolve.Package{AST: &ast.Package{Name: f.Package.Name, Files: []*ast.File{f}}, Name: f.Package.Name}
	prog, d := resolve.NewProgram(ids, []*resolve.Package{pkg})
	if len(d) > 0 {
		return nil, d
	}
	return types.Check(prog, types.Config{Evaluator: comptime.New(fuel)})
}

const mainStub = "\ndriver func main(io Stdio) error {\n\treturn nil\n}\n"

// codes returns "line:Exxxx" strings.
func codes(d diag.List) []string {
	var out []string
	for _, x := range d {
		out = append(out, strings.TrimPrefix(x.Code.String(), "")+"@"+itoa(x.Span.Start.Line))
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestNegative(t *testing.T) {
	cases := []struct {
		name, src, want string // want: "E2002@3"
	}{
		{"undefined", "package main\nfunc f() u32 {\n\treturn y\n}\n", "E2001@3"},
		{"unknown field", "package main\ntype P struct { x u32 }\nfunc f(p P) u32 {\n\treturn p.z\n}\n", "E2001@4"},
		{"mismatch", "package main\nfunc f() u32 {\n\tvar s string = \"a\"\n\treturn s\n}\n", "E2002@4"},
		{"mixed ints", "package main\nfunc f(a u32, b u64) u64 {\n\treturn a + b\n}\n", "E2002@3"},
		{"const overflow", "package main\nfunc f() u8 {\n\treturn 300\n}\n", "E2002@3"},
		{"string concat", "package main\nfunc f(a string) string {\n\treturn a + a\n}\n", "E2002@3"},
		{"no new vars", "package main\nfunc f() {\n\tx := 1\n\tx := 2\n\t_ = x\n}\n", "E2002@4"},
		{"ptr method on value", "package main\ntype A struct { n u32 }\nfunc (a *A) Inc() { a.n++ }\nfunc f() {\n\tvar a A\n\ta.Inc()\n}\n", "E2002@6"},
		{"generic needs args", "package main\nfunc id[T any](x T) T { return x }\nfunc f() u32 {\n\treturn id(1)\n}\n", "E2002@4"},
		{"arity call", "package main\nfunc g(a u32) u32 { return a }\nfunc f() u32 {\n\treturn g(1, 2)\n}\n", "E2003@4"},
		{"arity return", "package main\nfunc f() (u32, bool) {\n\treturn 1\n}\n", "E2003@3"},
		{"arity assign", "package main\nfunc g() (u32, bool) { return 1, true }\nfunc f() {\n\ta, b, c := g()\n\t_, _, _ = a, b, c\n}\n", "E2003@4"},
		{"missing return", "package main\nfunc f(b bool) u32 {\n\tif b {\n\t\treturn 1\n\t}\n}\n", "E2003@6"},
		{"nil pointer", "package main\nfunc f() {\n\tvar p *u32 = nil\n\t_ = p\n}\n", "E2004@3"},
		{"nil u32", "package main\nfunc f() u32 {\n\treturn nil\n}\n", "E2002@3"},
		{"nil conn return", "package main\ndriver func f() (Conn, error) {\n\treturn nil, nil\n}\n", "E2004@3"},
		{"uninit scoped", "package main\ndriver func f() {\n\tvar c Context\n\t_ = c.Done()\n}\n", "E2005@3"},
		{"uninit pointer", "package main\nfunc f() {\n\tvar p *u32\n\t_ = p\n}\n", "E2005@3"},
		{"global conn", "package main\nvar g Conn\n", "E3001@2"},
		{"global pointer", "package main\nvar arr [4]u8\nvar g *[4]u8\n", "E3001@3"},
		{"global scoped struct", "package main\ntype P struct { p *u8 }\nvar g P\n", "E3001@3"},
		{"code level", "package main\ncomptime func Bad() Code 0 {\n\treturn CodeUniv(0)\n}\n", "E2010@3"},
		{"code level arg", "package main\ncomptime func Take(c Code 1) usize { return 0 }\nconst X = Take(CodeUniv(1))\n", "E2010@3"},
		{"code no level", "package main\ntype B[T any] struct { x T }\nvar v B[Code]\n", "E2010@3"},
		{"parametric op", "package main\nfunc eq[T any](a T, b T) bool {\n\treturn a == b\n}\n", "E2011@3"},
		{"parametric conv", "package main\nfunc f[T any](a T) u32 {\n\treturn u32(a)\n}\n", "E2011@3"},
		{"array len var", "package main\nfunc f(n usize) {\n\tvar a [n]u8\n\t_ = a\n}\n", "E4003@3"},
		{"array len neg", "package main\nconst N = -1\nvar a [N]u8\n", "E4003@3"},
		{"const index oob", "package main\nfunc f() u8 {\n\tvar a [4]u8\n\treturn a[4]\n}\n", "E4003@4"},
		{"comptime fuel", "package main\ncomptime func F(n usize) usize { return F(n + 1) }\nconst X = F(0)\n", "E4001@3"},
		{"main not driver", "package main\nfunc main() error {\n\treturn nil\n}\n", "E5002@2"},
		{"main bad param", "package main\ndriver func main(n u32) error {\n\treturn nil\n}\n", "E5002@2"},
		{"main dup cap", "package main\ndriver func main(a Net, b Net) error {\n\treturn nil\n}\n", "E5002@2"},
		{"main result", "package main\ndriver func main(io Stdio) {\n}\n", "E5002@2"},
		{"expr stmt", "package main\nfunc f(a u32) {\n\ta + 1\n}\n", "E2002@3"},
		{"break outside", "package main\nfunc f() {\n\tbreak\n}\n", "E2002@3"},
		{"comptime nonconst arg", "package main\ncomptime func F(n usize) usize { return n }\ndriver func g(n usize) usize {\n\treturn F(n)\n}\n", "E2002@4"},
		{"variant arity", "package main\ntype S enum {\n\tA(u32)\n}\nfunc f() S {\n\treturn S.A(1, 2)\n}\n", "E2003@6"},
		{"pattern arity", "package main\ntype S enum {\n\tA(u32)\n}\nfunc f(s S) {\n\tmatch s {\n\t\tS.A(a, b) -> {}\n\t}\n}\n", "E2003@7"},
		{"type arg scoped", "package main\nfunc id[T any](x T) T { return x }\ndriver func f(c Context) {\n\t_ = id[Context](c)\n}\n", "E2002@4"},
		{"recursive type", "package main\ntype T struct { t T }\n", "E2002@2"},
		{"scope value", "package main\ndriver func f() {\n\tscope s := 3 {\n\t}\n}\n", "E2002@3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := checkSrc(t, tc.src, 1000)
			got := codes(d)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %v, want [%s]\n%v", got, tc.want, d)
			}
		})
	}
}

func TestPositive(t *testing.T) {
	srcs := map[string]string{
		"untyped": `package main
const K = 3
func f(a u8, b u32, c usize, d i64) u64 {
	x := a + K
	y := b << 2
	z := c * K - 1
	w := -d + K
	_ = 'a' + 1
	return u64(x) + u64(y) + u64(z) + u64(w)
}`,
		"redeclare err": `package main
func g() (u32, error) { return 1, nil }
func f() error {
	a, err := g()
	b, err := g()
	_ = a + b
	if err := h(); err != nil {
		return err
	}
	return err
}
func h() error { return ErrIO }`,
		"methods": `package main
type V struct { n u32 }
func (v V) Get() u32 { return v.n }
func (v *V) Set(n u32) { v.n = n }
func f() u32 {
	var v V
	p := &v
	p.Set(3)
	return v.Get() + p.Get()
}`,
		"code poly": `package main
comptime func C(n usize) Code 0 { return CodeArray(n, CodeU8()) }
comptime func U() Code 1 { return CodeUniv(0) }
comptime func Arr() Code 1 { return CodeArray(2, CodeUniv(0)) }
type B = El(C(8))
type L = El(CodeUniv(3))
var b B`,
		"match terminating": `package main
type S enum {
	A
	B(u32)
}
func f(s S) u32 {
	match s {
		S.A -> {
			return 0
		}
		S.B(x) -> {
			return x
		}
	}
}`,
		"for terminating": `package main
driver func f() u32 {
	driver for {
	}
}`,
		"capabilities": `package main
driver func main(fs FS, env Env, io Stdio, ex Exec, c Clock, n Net, s Signals) error {
	var names [4]string
	k, err := fs.ListSourceFiles(".", names[:])
	if err != nil || k == 0 || !fs.IsDir(".") {
		return err
	}
	if env.ArgsCount() > 1 {
		_ = io.WriteStderr(strBytes(env.Arg(1)))
	}
	ctx, cancel := NewContext()
	defer cancel()
	var buf [64]byte
	st, m, err := ex.Run(ctx, "go", names[:0], names[:0], ".", buf[:])
	_, _ = m, st.Code
	return err
}`,
	}
	for name, src := range srcs {
		t.Run(name, func(t *testing.T) {
			if _, d := checkSrc(t, src, 0); len(d) > 0 {
				t.Errorf("unexpected diagnostics:\n%v", d)
			}
		})
	}
}

// findNode returns the first node of type T satisfying pred.
func findNode[T ast.Node](info *types.Info, pred func(T) bool) T {
	var found T
	done := false
	for _, pkg := range info.Prog.Packages {
		for _, f := range pkg.AST.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				if done {
					return false
				}
				if x, ok := n.(T); ok && pred(x) {
					found, done = x, true
				}
				return !done
			})
		}
	}
	return found
}

func TestTypedModel(t *testing.T) {
	src := `package main
const (
	MaxConns = 1024
	BufSize  = 4096
)
comptime func PoolCode(maxConns usize, bufSize usize) Code 0 {
	return CodeArray(maxConns, CodeArray(bufSize, CodeU8()))
}
type ConnPool = El(PoolCode(MaxConns, BufSize))
type Vec[N usize, T any] struct {
	data [N]T
	len  usize
}
func (v *Vec[N, T]) Push(x T) error {
	if v.len >= N {
		return ErrArenaFull
	}
	v.data[v.len] = x
	v.len++
	return nil
}
func id[T any](x T) T { return x }
driver func work(conn Conn, buf *[BufSize]byte) {
	defer conn.Close()
	_ = buf
}
driver func main(net Net) error {
	var pool ConnPool
	var nums Vec[4, u32]
	np := &nums
	_ = np.Push(id[u32](7))
	ln, err := net.ListenTCP("127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, cancel := NewContext()
	defer cancel()
	scope conns := NewScope(MaxConns) {
		slot, err := conns.AcquireSlot(ctx, &pool)
		if err != nil {
			return err
		}
		conn, err := ln.Accept(ctx)
		if err != nil {
			conns.ReleaseSlot(slot)
			return err
		}
		conns.Go(slot, driver func(buf *[BufSize]byte) {
			work(conn, buf)
		})
	}
	return nil
}`
	info, d := checkSrc(t, src, 0)
	if len(d) > 0 {
		t.Fatal(d)
	}
	// El folding.
	td := findNode(info, func(n *ast.TypeDecl) bool { return n.Name.Name == "ConnPool" })
	if got := info.TypeOf(td.Type).String(); got != "[1024][4096]u8" {
		t.Errorf("ConnPool = %s", got)
	}
	pool := findNode(info, func(n *ast.VarDecl) bool { return n.Name.Name == "pool" })
	if got := info.SymType(info.SymbolOf(pool.Name)).String(); got != "[1024][4096]u8" {
		t.Errorf("pool: %s", got)
	}
	// Calls.
	call := func(name string) *types.Call {
		c := findNode(info, func(n *ast.CallExpr) bool {
			switch f := n.Fun.(type) {
			case *ast.SelectorExpr:
				return f.Sel.Name == name
			case *ast.Ident:
				return f.Name == name
			case *ast.IndexExpr:
				id, ok := f.X.(*ast.Ident)
				return ok && id.Name == name
			}
			return false
		})
		if c == nil {
			t.Fatalf("no call %s", name)
		}
		return info.CallOf(c)
	}
	if c := call("AcquireSlot"); c.Kind != types.CallBuiltinMeth || c.Method.Builtin != types.ScopeAcquireSlot ||
		!c.ResourceCtor || c.Sig.Results[0].String() != "Slot[[4096]u8]" || c.TypeArgs[0].String() != "1024" {
		t.Errorf("AcquireSlot: %+v", c)
	}
	if c := call("Go"); len(c.ConsumesArgs) != 1 || c.Sig.Params[1].String() != "driver func(*[4096]u8)" {
		t.Errorf("Go: %+v sig %v", c, c.Sig)
	}
	if c := call("Close"); !c.ConsumesRecv {
		t.Errorf("Close: %+v", c)
	}
	if c := call("cancel"); c.Kind != types.CallValue || !c.ConsumesRecv {
		t.Errorf("cancel: %+v", c)
	}
	if c := call("Push"); c.Kind != types.CallMethod || c.Method.Recv.Name != "Vec" || c.Sig.Params[0].String() != "u32" || len(c.TypeArgs) != 2 {
		t.Errorf("Push: %+v", c)
	}
	if c := call("id"); c.Kind != types.CallFunc || c.TypeArgs[0].String() != "u32" || c.Sig.Results[0].String() != "u32" {
		t.Errorf("id: %+v", c)
	}
	if c := call("PoolCode"); !c.Folded || c.Effect != types.Comptime {
		t.Errorf("PoolCode: %+v", c)
	}
	if c := call("work"); c.Effect != types.Driver || c.Callee.Name != "work" {
		t.Errorf("work: %+v", c)
	}
	// Instances.
	var keys []string
	for _, in := range info.InstanceList {
		keys = append(keys, in.Key)
	}
	if got := strings.Join(keys, " "); !strings.Contains(got, "Vec[4, u32]") || !strings.Contains(got, "id[u32]") {
		t.Errorf("instances: %s", got)
	}
	// Funcs, effects, literals, captures.
	var mainFI *types.FuncInfo
	for _, fi := range info.Funcs {
		if fi.Sym != nil && fi.Sym.Name == "main" {
			mainFI = fi
		}
	}
	if mainFI == nil || info.Main != mainFI || mainFI.Effect != types.Driver || len(mainFI.Lits) != 1 {
		t.Fatalf("main: %+v", mainFI)
	}
	lit := mainFI.Lits[0]
	if lit.Effect != types.Driver || len(lit.Captures) != 1 || lit.Captures[0].Name != "conn" || len(lit.Calls) != 1 {
		t.Errorf("lit: %+v", lit)
	}
	// Classes.
	for name, want := range map[string]types.Class{"conn": types.ClassLinear, "ctx": types.ClassScoped, "pool": types.ClassValue, "np": types.ClassScoped, "conns": types.ClassScoped, "slot": types.ClassLinear, "cancel": types.ClassLinear} {
		id := findNode(info, func(n *ast.Ident) bool {
			s := info.SymbolOf(n)
			return n.Name == name && s != nil && info.Prog.Defs[n.ID()] == s
		})
		if id == nil {
			t.Errorf("no def of %s", name)
			continue
		}
		if got := types.ClassOf(info.SymType(info.SymbolOf(id))); got != want {
			t.Errorf("class of %s = %s, want %s", name, got, want)
		}
	}
	// Constant values and untyped finalization.
	mc := findNode(info, func(n *ast.CallExpr) bool {
		id, ok := n.Fun.(*ast.Ident)
		return ok && id.Name == "NewScope"
	})
	if v := info.ValueOf(mc.Args[0]); v == nil || v.String() != "1024" || info.TypeOf(mc.Args[0]).String() != "usize" {
		t.Errorf("NewScope arg: %v %v", v, info.TypeOf(mc.Args[0]))
	}
}

// TestRobustness type-checks the parser's grammar exerciser (a
// syntactically complete but semantically invalid file) despite resolver
// errors: the checker must not panic.
func TestRobustness(t *testing.T) {
	src, err := os.ReadFile("../parser/testdata/grammar.fc")
	if err != nil {
		t.Skip(err)
	}
	ids := &ast.IDAlloc{}
	f, d := parser.ParseFile("grammar.fc", src, ids)
	if len(d) > 0 {
		t.Fatal(d)
	}
	f.Imports = nil
	pkg := &resolve.Package{AST: &ast.Package{Name: f.Package.Name, Files: []*ast.File{f}}}
	prog, _ := resolve.NewProgram(ids, []*resolve.Package{pkg})
	_, d = types.Check(prog, types.Config{Evaluator: comptime.New(1000)})
	if len(d) == 0 {
		t.Fatal("expected type errors")
	}
}
