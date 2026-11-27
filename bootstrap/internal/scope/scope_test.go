package scope_test

import (
	"fmt"
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/comptime"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/parser"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/scope"
	"fincore/bootstrap/internal/types"
)

const mainStub = "\ndriver func main(io Stdio) error {\n\treturn nil\n}\n"

// run type-checks src (a package main without main; a stub is appended)
// and returns the scope diagnostics as "E3002@line" strings.
func run(t *testing.T, src string) []string {
	t.Helper()
	full := "package main\n" + src + mainStub
	ids := &ast.IDAlloc{}
	f, d := parser.ParseFile("t.fc", []byte(full), ids)
	if len(d) > 0 {
		t.Fatalf("parse: %v", d)
	}
	pkg := &resolve.Package{AST: &ast.Package{Name: f.Package.Name, Files: []*ast.File{f}}, Name: f.Package.Name}
	prog, d := resolve.NewProgram(ids, []*resolve.Package{pkg})
	if len(d) > 0 {
		t.Fatalf("resolve: %v", d)
	}
	info, d := types.Check(prog, types.Config{Evaluator: comptime.New(0)})
	if len(d) > 0 {
		t.Fatalf("types: %v", d)
	}
	var out []string
	for _, x := range scope.Check(prog, info) {
		out = append(out, fmt.Sprintf("%s@%d", x.Code, x.Span.Start.Line))
	}
	return out
}

// Line numbers in want refer to the snippet: line 1 is the first line
// after the package clause, i.e. want "E3002@3" is the snippet's third line.
type tcase struct {
	name string
	src  string
	want string // comma-separated "E3002@3"; "" = no diagnostics
}

func runCases(t *testing.T, cases []tcase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, tc.src)
			for i, g := range got { // shift by the package clause line
				var code diag.Code
				var line int
				fmt.Sscanf(strings.Replace(g, "@", " ", 1), "E%d %d", &code, &line)
				got[i] = fmt.Sprintf("%s@%d", code, line-1)
			}
			if s := strings.Join(got, ","); s != tc.want {
				t.Errorf("got %q, want %q\n%s", s, tc.want, tc.src)
			}
		})
	}
}

func TestEscape(t *testing.T) {
	runCases(t, []tcase{
		{"return addr of local", `
func leak() *[16]byte {
	var local [16]byte
	return &local
}`, "E3002@4"},
		{"return param pointer ok", `
type T struct { x u32 }
func id(p *T) *T {
	return p
}`, ""},
		{"return field through pointer param ok", `
type T struct { buf [8]u8 }
func view(p *T) []u8 {
	return p.buf[2:4]
}`, ""},
		{"return slice of local array", `
func f() []u8 {
	var buf [8]u8
	return buf[:]
}`, "E3002@4"},
		{"return slice of global array ok", `
var g [8]u8
func f() []u8 {
	return g[1:3]
}`, ""},
		{"return slice param ok", `
func f(b []u8) []u8 {
	return b[1:]
}`, ""},
		{"return local holding global addr ok", `
type T struct { x u32 }
var g T
func f() *T {
	p := &g
	return p
}`, ""},
		{"return local holding local addr", `
type T struct { x u32 }
func f() *T {
	var t T
	p := &t
	return p
}`, "E3002@6"},
		{"return struct with local pointer", `
type A struct { x u32 }
type P struct { a *A }
func f() P {
	var a A
	return P{a: &a}
}`, "E3002@6"},
		{"return struct built from param ok", `
type A struct { x u32 }
type P struct { a *A }
func f(a *A) P {
	return P{a: a}
}`, ""},
		{"store local addr through receiver", `
type A struct { x u32 }
type P struct { a *A }
func (p *P) set() {
	var a A
	p.a = &a
}`, "E3002@6"},
		{"store other param through receiver", `
type A struct { x u32 }
type P struct { a *A }
func (p *P) set(a *A) {
	p.a = a
}`, "E3002@5"},
		{"store own field back ok", `
type A struct { x u32 }
type P struct { a *A; b *A }
func (p *P) swap() {
	p.a = p.b
}`, ""},
		{"store local into outer local ok", `
type A struct { x u32 }
func f() u32 {
	var a A
	var b A
	q := &a
	q = &b
	return q.x
}`, ""},
		{"scope body pointer to outer var", `
type A struct { x u32 }
driver func f() {
	var a A
	q := &a
	scope s := NewScope(1) {
		var b A
		q = &b
	}
	q.x = 1
}`, "E3002@8"},
		{"scope var cannot escape", `
driver func f() {
	scope s := NewScope(1) {
		t := s
		t.Wait()
	}
}`, ""},
		{"func lit capturing local returned", `
func f() driver func() {
	var a [4]u8
	p := &a
	return driver func() {
		p[0] = 1
	}
}`, "E3002@5"},
		{"func lit capturing value copied ok", `
func f() driver func() u8 {
	var a [4]u8
	return driver func() u8 {
		return a[0]
	}
}`, ""},
		{"local scoped struct from receiver fields ok", `
type A struct { x u32 }
type P struct { a *A; n u32 }
func (p *P) g() u32 {
	q := P{a: p.a, n: 1}
	qp := &q
	return qp.a.x
}`, ""},
	})
}

const poolPrelude = `
const (
	Slots   = 4
	BufSize = 16
)
`

func TestGoClosure(t *testing.T) {
	runCases(t, []tcase{
		{"store task buf into outer var", poolPrelude + `
driver func f() error {
	ctx, cancel := NewContext()
	defer cancel()
	var pool [Slots][BufSize]byte
	var outer [BufSize]byte
	saved := &outer
	scope conns := NewScope(Slots) {
		slot, err := conns.AcquireSlot(ctx, &pool)
		if err != nil {
			return err
		}
		conns.Go(slot, driver func(buf *[BufSize]byte) {
			saved = buf
		})
	}
	saved[0] = 1
	return nil
}`, "E3002@19"},
		{"capture outer pointers ok", poolPrelude + `
driver func f() error {
	ctx, cancel := NewContext()
	defer cancel()
	var pool [Slots][BufSize]byte
	var outer [BufSize]byte
	saved := &outer
	scope conns := NewScope(Slots) {
		slot, err := conns.AcquireSlot(ctx, &pool)
		if err != nil {
			return err
		}
		conns.Go(slot, driver func(buf *[BufSize]byte) {
			buf[0] = saved[0]
			if ctx.Done() {
				buf[1] = 1
			}
		})
	}
	return nil
}`, ""},
		{"capture pointer from nested scope", poolPrelude + `
driver func f() error {
	ctx, cancel := NewContext()
	defer cancel()
	var pool [Slots][BufSize]byte
	scope conns := NewScope(Slots) {
		scope inner := NewScope(1) {
			var local [BufSize]byte
			lp := &local
			slot, err := conns.AcquireSlot(ctx, &pool)
			if err != nil {
				return err
			}
			conns.Go(slot, driver func(buf *[BufSize]byte) {
				buf[0] = lp[0]
			})
			inner.Wait()
		}
	}
	return nil
}`, "E3002@20"},
		{"move conn into task then use", poolPrelude + `
driver func f(net Net) error {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, cancel := NewContext()
	defer cancel()
	var pool [Slots][BufSize]byte
	scope conns := NewScope(Slots) {
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
			defer conn.Close()
			buf[0] = 1
		})
		_ = conn.Close()
	}
	return nil
}`, "E3003@30"},
		{"task leaks captured conn", poolPrelude + `
driver func f(net Net) error {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, cancel := NewContext()
	defer cancel()
	var pool [Slots][BufSize]byte
	scope conns := NewScope(Slots) {
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
			buf[0] = 1
			_ = conn.CloseWrite()
		})
	}
	return nil
}`, "E3005@21"},
		{"slot reused after Go", poolPrelude + `
driver func f() error {
	ctx, cancel := NewContext()
	defer cancel()
	var pool [Slots][BufSize]byte
	scope conns := NewScope(Slots) {
		slot, err := conns.AcquireSlot(ctx, &pool)
		if err != nil {
			return err
		}
		conns.Go(slot, driver func(buf *[BufSize]byte) {
			buf[0] = 1
		})
		conns.ReleaseSlot(slot)
	}
	return nil
}`, "E3003@19"},
	})
}

func TestLinear(t *testing.T) {
	runCases(t, []tcase{
		{"defer close ok", `
driver func f(net Net) error {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	defer ln.Close()
	_ = ln.Addr()
	return nil
}`, ""},
		{"close in one branch only", `
driver func f(net Net, b bool) error {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	if b {
		_ = ln.Close()
	}
	return nil
}`, "E3005@3"},
		{"close in both branches ok", `
driver func f(net Net, b bool) error {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	if b {
		_ = ln.Close()
	} else {
		_ = ln.Close()
	}
	return nil
}`, ""},
		{"no error check leaks on error path is fine, missing close not", `
driver func f(net Net) error {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	return nil
}`, "E3005@3"},
		{"err == nil refinement", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err == nil {
		_ = ln.Close()
	}
}`, ""},
		{"unchecked error is live", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	_ = err
}`, "E3005@3"},
		{"err reassigned before check", `
driver func f(net Net, ctx Context) {
	ln, err := net.ListenTCP("x")
	err = ctx.Err()
	if err != nil {
		return
	}
	_ = ln.Close()
}`, "E3005@3"},
		{"double close", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	_ = ln.Close()
	_ = ln.Close()
}`, "E3003@8"},
		{"use after close", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	_ = ln.Close()
	_ = ln.Addr()
}`, "E3003@8"},
		{"explicit close after defer ok", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	defer ln.Close()
	_ = ln.Close()
	_ = ln.Addr()
}`, ""},
		{"cancel func", `
driver func f() {
	ctx, cancel := NewContext()
	if ctx.Done() {
		cancel()
		return
	}
}`, "E3005@3"},
		{"cancel func deferred and called", `
driver func f() {
	ctx, cancel := NewContext()
	defer cancel()
	if ctx.Done() {
		cancel()
	}
}`, ""},
		{"discard with blank", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	_ = ln
}`, "E3004@7"},
		{"discard in short var decl", `
driver func f(net Net) error {
	_, err := net.ListenTCP("x")
	return err
}`, "E3004@3"},
		{"discard call result", `
driver func f(net Net) {
	net.ListenTCP("x")
}`, "E3004@3"},
		{"driver param owned leak", `
driver func f(c Conn) {
	_ = c.CloseWrite()
}`, "E3005@2"},
		{"driver param owned closed", `
driver func f(c Conn) {
	defer c.Close()
	_ = c.CloseWrite()
}`, ""},
		{"driver param moved to driver callee", `
driver func g(c Conn) {
	defer c.Close()
}
driver func f(c Conn) {
	g(c)
}`, ""},
		{"use after move to driver callee", `
driver func g(c Conn) {
	defer c.Close()
}
driver func f(c Conn) {
	g(c)
	_ = c.CloseWrite()
}`, "E3003@7"},
		{"total param borrowed", `
func w(ctx Context, c Conn) error {
	_, err := c.Write(ctx, strBytes("x"))
	return err
}
driver func f(ctx Context, c Conn) {
	defer c.Close()
	_ = w(ctx, c)
	_ = w(ctx, c)
}`, ""},
		{"total func cannot close borrowed", `
func w(c Conn) {
	_ = c.Close()
}`, "E3003@3"},
		{"move then close", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	l2 := ln
	_ = l2.Close()
	_ = ln.Close()
}`, "E3003@9"},
		{"return linear ok", `
driver func open(net Net) (Listener, error) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return ln, err
	}
	return ln, nil
}`, ""},
		{"panic ends path", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	if ln.Addr() == "" {
		panic("no")
	}
	_ = ln.Close()
}`, ""},
	})
}

func TestLoops(t *testing.T) {
	runCases(t, []tcase{
		{"move in loop body", `
driver func g(c Conn) {
	defer c.Close()
}
driver func f(c Conn) {
	for i := 0; i < 3; i++ {
		g(c)
	}
}`, "E3005@5,E3003@7"},
		{"accept loop ok", `
driver func g(c Conn) {
	defer c.Close()
}
driver func f(ctx Context, ln Listener) {
	defer ln.Close()
	driver for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			if ctx.Done() {
				break
			}
			continue
		}
		g(conn)
	}
}`, ""},
		{"leak on continue", `
driver func g(c Conn) {
	defer c.Close()
}
driver func f(ctx Context, ln Listener) {
	defer ln.Close()
	driver for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			break
		}
		if ctx.Done() {
			continue
		}
		g(conn)
	}
}`, "E3005@8"},
		{"leak on break", `
driver func f(ctx Context, ln Listener) {
	defer ln.Close()
	driver for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			break
		}
		if ctx.Done() {
			break
		}
		_ = conn.Close()
	}
}`, "E3005@5"},
		{"leak at end of iteration", `
driver func f(ctx Context, ln Listener) {
	defer ln.Close()
	driver for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			break
		}
		_ = conn.CloseWrite()
	}
}`, "E3005@5"},
		{"close outer in loop then loop again", `
driver func f(ctx Context, ln Listener) {
	for i := 0; i < 2; i++ {
		if ctx.Done() {
			_ = ln.Close()
		}
	}
	_ = ln.Close()
}`, "E3003@5,E3003@8"},
		{"close in loop then break ok", `
driver func f(ctx Context, ln Listener) {
	driver for {
		if ctx.Done() {
			_ = ln.Close()
			break
		}
	}
}`, ""},
		{"return in loop leaks", `
driver func f(ctx Context, ln Listener) {
	driver for {
		if ctx.Done() {
			return
		}
		_ = ln.Close()
		break
	}
}`, "E3005@2"},
		{"scope block exit", `
driver func f(ctx Context, ln Listener) {
	defer ln.Close()
	scope s := NewScope(1) {
		c2, cancel := NewContext()
		if c2.Done() {
			return
		}
		s.Wait()
	}
}`, "E3005@5"},
	})
}

func TestMatchExhaustive(t *testing.T) {
	runCases(t, []tcase{
		{"exhaustive enum match closes", `
type S enum {
	A
	B
}
driver func f(net Net, s S) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	match s {
		S.A -> { _ = ln.Close() }
		S.B -> { _ = ln.Close() }
	}
}`, ""},
		{"non-exhaustive int match", `
driver func f(net Net, n u32) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	match n {
		1 -> { _ = ln.Close() }
	}
}`, "E3005@3"},
	})
}

func TestInlineClosure(t *testing.T) {
	runCases(t, []tcase{
		{"deferred closure closes", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	defer driver func() {
		_ = ln.Close()
	}()
	_ = ln.Addr()
}`, ""},
		{"immediate closure closes then use", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	driver func() {
		_ = ln.Close()
	}()
	_ = ln.Addr()
}`, "E3003@10"},
		{"closure only borrows", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	driver func() {
		_ = ln.Addr()
	}()
}`, "E3005@3"},
		{"closure closes on one path", `
driver func f(net Net, b bool) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	defer driver func() {
		if b {
			_ = ln.Close()
		}
	}()
}`, "E3005@3"},
		{"stored closure borrows", `
driver func f(net Net) {
	ln, err := net.ListenTCP("x")
	if err != nil {
		return
	}
	g := driver func() {
		_ = ln.Close()
	}
	g()
	_ = ln.Close()
}`, "E3003@8"},
	})
}

func TestDiscardMatchArm(t *testing.T) {
	runCases(t, []tcase{
		{"match arm discards", `
driver func f(net Net, n u32) {
	match n {
		1 -> net.ListenTCP("x")
		_ -> net.ListenTCP("y")
	}
}`, "E3004@4,E3004@5"},
	})
}

// v0.2 sum types: match expressions are branches; payload bindings are
// borrowed (decisions 46, 49).
func TestMatchExprLinear(t *testing.T) {
	runCases(t, []tcase{
		{"match expr picks one of two listeners", `
driver func f(net Net, b bool) error {
	a, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	c, err2 := net.ListenTCP("y")
	if err2 != nil {
		_ = a.Close()
		return err2
	}
	k := match b { true -> a; _ -> c }
	return k.Close()
}`, "E3005@3,E3005@7"},
		{"match expr closes on every arm ok", `
driver func f(net Net, n u32) error {
	a, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	return match n { 0 | 1 -> a.Close(); _ -> a.Close() }
}`, ""},
		{"consume borrowed payload binding", `
type Res enum {
	Open(Listener)
	Closed
}

func g(r Res) error {
	return match r {
		Open(l) -> l.Close()
		Closed -> nil
	}
}`, "E3003@9"},
		{"use after move in arm", `
driver func f(net Net, b bool) error {
	a, err := net.ListenTCP("x")
	if err != nil {
		return err
	}
	k := match b { true -> a; _ -> a }
	_ = a.Addr()
	return k.Close()
}`, "E3003@8"},
		{"scoped value escapes through match expr", `
func f(b bool) *u32 {
	var x u32
	var y u32
	return match b { true -> &x; _ -> &y }
}`, "E3002@5"},
	})
}

// v0.2 ergonomics: x.M() with a pointer receiver means (&x).M(); the
// implicit & is checked exactly like an explicit one.
func TestAutoAddr(t *testing.T) {
	runCases(t, []tcase{
		{"call on local ok", `
type C struct { n u32 }
func (c *C) Inc() { c.n++ }
func f() u32 {
	var c C
	c.Inc()
	return c.n
}`, ""},
		{"return implicitly addressed local", `
type C struct { n u32 }
func (c *C) Self() *C { return c }
func f() *C {
	var c C
	return c.Self()
}`, "E3002@6"},
		{"return explicitly addressed local", `
type C struct { n u32 }
func (c *C) Self() *C { return c }
func f() *C {
	var c C
	return (&c).Self()
}`, "E3002@6"},
		{"store implicitly addressed local through param", `
type C struct { n u32 }
func (c *C) Self() *C { return c }
type H struct { p *C }
func f(h *H) {
	var c C
	h.p = c.Self()
}`, "E3002@7"},
		{"implicit & of a global ok", `
type C struct { n u32 }
func (c *C) Self() *C { return c }
var g C
func f() *C {
	return g.Self()
}`, ""},
		{"implicit & of a param field ok", `
type C struct { n u32 }
func (c *C) Self() *C { return c }
type H struct { c C }
func f(h *H) *C {
	return h.c.Self()
}`, ""},
		{"implicit & of a slice element ok", `
type C struct { n u32 }
func (c *C) Self() *C { return c }
func f(s []C) *C {
	return s[0].Self()
}`, ""},
		{"implicit & of a local array element", `
type C struct { n u32 }
func (c *C) Self() *C { return c }
func f() *C {
	var a [2]C
	return a[1].Self()
}`, "E3002@6"},
		{"slice returned through implicit & of local", `
type B struct { buf [8]u8 }
func (b *B) View() []u8 { return b.buf[:] }
func f() []u8 {
	var b B
	return b.View()
}`, "E3002@6"},
		{"implicit & keeps local content conservative", `
type P struct { s []u8 }
func (p *P) Touch() { }
var g [4]u8
func f() []u8 {
	var buf [4]u8
	p := P{s: g[:]}
	p.Touch()
	p.s = buf[:]
	return p.s
}`, "E3002@10"},
		{"constant string as bytes may be returned", `
func f() []byte {
	return "abc"
}
func g() []byte {
	var b []byte = "xyz"
	return b
}`, ""},
	})
}
