package comptime_test

import (
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/comptime"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/parser"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

func check(t *testing.T, src string, ev *comptime.Evaluator) (*types.Info, diag.List) {
	t.Helper()
	ids := &ast.IDAlloc{}
	f, d := parser.ParseFile("t.fc", []byte(src), ids)
	if len(d) > 0 {
		t.Fatalf("parse: %v", d)
	}
	pkg := &resolve.Package{AST: &ast.Package{Name: f.Package.Name, Files: []*ast.File{f}}}
	prog, d := resolve.NewProgram(ids, []*resolve.Package{pkg})
	if len(d) > 0 {
		t.Fatal(d)
	}
	return types.Check(prog, types.Config{Evaluator: ev})
}

func constVal(t *testing.T, info *types.Info, name string) types.Value {
	t.Helper()
	s := info.Prog.Root.Scope.LookupLocal(name)
	if s == nil {
		t.Fatalf("no %s", name)
	}
	return info.SymValue(s)
}

func typeOf(t *testing.T, info *types.Info, name string) string {
	t.Helper()
	s := info.Prog.Root.Scope.LookupLocal(name)
	if s == nil {
		t.Fatalf("no %s", name)
	}
	return info.SymType(s).String()
}

const progSrc = `package main
const (
	MaxConns = 1024
	BufSize  = 4096
)
comptime func PoolCode(maxConns usize, bufSize usize) Code 0 {
	return CodeArray(maxConns, CodeArray(bufSize, CodeU8()))
}
comptime func Pow2(k usize) usize {
	r := usize(1)
	for i := usize(0); i < k; i++ {
		r *= 2
	}
	return r
}
comptime func Sum(n usize) u8 {
	var acc [4]u8
	s := acc[:]
	for i := usize(0); i < n; i++ {
		s[i%4] += 100
	}
	var t u8
	for i := usize(0); i < len(acc); i++ {
		t += acc[i]
	}
	return t
}
type Pair struct {
	a u32
	b u32
}
comptime func Swap(p Pair) Pair {
	q := p
	q.a, q.b = p.b, p.a
	pp := &q
	pp.a += 1
	return q
}
comptime func Fields() Code 0 {
	var fs [2]FieldCode
	fs[0] = CodeField("x", CodeU32())
	fs[1] = CodeField("y", CodeSlice(CodeBool()))
	return CodeStruct(fs[:])
}
comptime func Univ() Code 2 {
	return CodeArray(3, CodeUniv(1))
}
comptime func Div(n u32) (u32, u32) {
	return n / 7, n % 7
}
type ConnPool = El(PoolCode(MaxConns, BufSize))
type S = El(Fields())
type U = El(CodeUniv(4))
type UA = El(Univ())
const TableSize = Pow2(10)
const Wrapped = Sum(10)
const Swapped = Swap(Pair{a: 1, b: 2})
var pool ConnPool
var table [TableSize]u16
var s S
var u U
var ua UA
driver func main(io Stdio) error {
	q, r := Div(100)
	_ = q + r
	return nil
}
`

func TestEvalAndFold(t *testing.T) {
	info, d := check(t, progSrc, comptime.New(0))
	if len(d) > 0 {
		t.Fatal(d)
	}
	for name, want := range map[string]string{
		"pool":  "[1024][4096]u8",
		"table": "[1024]u16",
		"s":     "struct{x u32; y []bool}",
		"u":     "Code 4",
		"ua":    "[3]Code 1",
	} {
		if got := typeOf(t, info, name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"TableSize": "1024",
		"Wrapped":   "232", // 10*100 = 1000, wrapped per u8 cell and in the sum
		"Swapped":   "{3, 1}",
	} {
		if got := constVal(t, info, name).String(); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
	// The folded tuple call in main.
	var call *ast.CallExpr
	ast.Inspect(info.Prog.Root.AST.Files[0], func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "Div" {
				call = c
			}
		}
		return true
	})
	if v := info.ValueOf(call); v == nil || v.String() != "(14, 2)" || !info.CallOf(call).Folded {
		t.Errorf("Div(100) = %v", v)
	}
}

func TestFuel(t *testing.T) {
	src := `package main
comptime func Loop(n usize) usize {
	s := usize(0)
	for i := usize(0); i < n; i++ {
		s += i
	}
	return s
}
const A = Loop(90)
const B = Loop(200)
`
	// Each root has its own budget: 1 call + 90 iterations fits in 100.
	_, d := check(t, src, comptime.New(100))
	if len(d) != 1 || d[0].Code != diag.E4001 || d[0].Span.Start.Line != 10 {
		t.Fatalf("got %v", d)
	}
	if !strings.Contains(d[0].Message, "budget 100") || !strings.Contains(d[0].Message, "Loop") {
		t.Errorf("message: %s", d[0].Message)
	}
	if _, d := check(t, src, comptime.New(1000)); len(d) != 0 {
		t.Fatalf("with 1000 fuel: %v", d)
	}
}

func TestRecursionStackAndDepth(t *testing.T) {
	src := `package main
comptime func Forever(n usize) usize {
	return Forever(n + 1)
}
const Boom = Forever(0)
`
	_, d := check(t, src, comptime.New(1000))
	if len(d) != 1 || d[0].Code != diag.E4001 || d[0].Span.Start.Line != 5 || !strings.Contains(d[0].Message, "(x999)") {
		t.Fatalf("got %v", d)
	}
	_, d = check(t, src, &comptime.Evaluator{Fuel: 1 << 30, MaxDepth: 50})
	if len(d) != 1 || d[0].Code != diag.E4001 || !strings.Contains(d[0].Message, "call depth") {
		t.Fatalf("depth: %v", d)
	}
}

func TestEvalErrors(t *testing.T) {
	cases := map[string]struct {
		src  string
		code diag.Code
	}{
		"index oob": {`package main
comptime func F(i usize) u8 {
	var a [2]u8
	return a[i]
}
const X = F(5)
`, diag.E4003},
		"div zero": {`package main
comptime func F(i u32) u32 {
	return 10 / i
}
const X = F(0)
`, diag.E2002},
		"global var": {`package main
var g u32
comptime func F() u32 {
	return g
}
const X = F()
`, diag.E2002},
		"stratification": {`package main
comptime func Lift() Code 1 {
	return CodeUniv(1)
}
`, diag.E2010},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, d := check(t, tc.src, comptime.New(0))
			if len(d) != 1 || d[0].Code != tc.code {
				t.Fatalf("got %v, want %s", d, tc.code)
			}
		})
	}
}

// v0.2 ergonomics at compile time: auto-& calls, constant strings as
// []byte (fresh copies) and constant string concatenation.
func TestErgonomics(t *testing.T) {
	src := `package main
const P = "ab"
const Q = P + "cd"
const R string = Q + "!"
type Counter struct {
	n u32
	cs [2]u32
}
func (c *Counter) Inc() {
	c.n++
}
func (c *Counter) Bump(i usize) {
	c.cs[i] += 10
}
type Box struct {
	c Counter
}
comptime func Count(k u32) u32 {
	var c Counter
	var b Box
	var arr [3]Counter
	s := arr[:]
	for i := u32(0); i < k; i++ {
		c.Inc()
		b.c.Inc()
		s[1].Inc()
	}
	arr[2].Bump(1)
	return c.n*1000 + b.c.n*100 + arr[1].n*10 + arr[2].cs[1]
}
comptime func Fresh() u8 {
	var a []byte = "xy"
	b := pick("xy")
	a[0] = 'z'
	return a[0] + b[0]
}
func pick(b []byte) []byte {
	return b
}
comptime func Concat() usize {
	return len(Q + "ef")
}
comptime func Copy() u8 {
	var d [4]u8
	n := copy(d[:], "hey")
	return d[n-1]
}
const N = Count(3)
const F = Fresh()
const L = Concat()
const C = Copy()
`
	info, d := check(t, src, comptime.New(0))
	if len(d) > 0 {
		t.Fatal(d)
	}
	for name, want := range map[string]string{
		"Q": `"abcd"`,
		"R": `"abcd!"`,
		"N": "3340",
		"F": "242", // 'z' + 'x'
		"L": "6",
		"C": "121", // 'y'
	} {
		if got := constVal(t, info, name).String(); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
}
