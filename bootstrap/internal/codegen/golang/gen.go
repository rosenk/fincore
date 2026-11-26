// Package golang is the deterministic Go code generator of finc0
// (milestone M4, spec 9.5). It turns a lowered program (package ir) into a
// single gofmt-ed Go source file `package main` that imports only the
// runtime package fincore/runtime/fincrt (and os for the entry point).
//
// # Emitted code
//
//   - Types follow spec 9.5.1 (usize -> uint, u8 -> uint8, ..., El(...)
//     folded to concrete arrays; resources -> fincrt types, Scope ->
//     *fincrt.Scope). Named types are declared with their underlying type
//     literal; aliases are erased.
//   - Enums are tagged structs: `struct { _tag uint8; _<Variant>_<i> T; ... }`
//     (one field per payload element). A value carries its variant in _tag
//     and zeros in all fields of other variants, so == stays structural.
//     Variant construction is a keyed composite literal, match is a switch
//     on _tag whose last arm becomes `default` when the match is exhaustive.
//   - Generics: ordinary type parameters become Go type parameters (with
//     explicit instantiation at call sites). Types and functions with const
//     parameters ([N usize]) are monomorphized over the const arguments,
//     keeping the remaining type parameters generic; instances are named
//     `_fc_<len(name)><name>_<v1>_<v2>...` (e.g. Vec[4, u32] ->
//     `_fc_3Vec_4[uint32]`). Instances are discovered on demand while
//     emitting and emitted in discovery order.
//   - `scope s := NewScope(N) { body }` becomes a plain block (no IIFE):
//     `{ s := fincrt.NewScope(N); _fc_sK = s; body; s.Wait() }`. Explicit
//     s.Wait() calls are emitted on every exit edge: normal fall-through,
//     before break/continue that leave the block, and before return (the
//     results are first evaluated into temporaries, then all enclosing
//     scopes of the function are waited for, innermost first). As a safety
//     net for panics, each function containing scope statements declares
//     `var _fc_sK *fincrt.Scope` plus one function-level
//     `defer func() { if _fc_sK != nil { _fc_sK.Wait() } }()` per scope
//     statement (no defer accumulation in loops). Wait is idempotent.
//   - Constants are inlined as typed literals; package-level variables
//     with zero initial values are emitted as `var x T` (no huge literals).
//   - `driver`, `decreases` and comptime functions are erased.
//   - main becomes `func _fc_main(caps...) error`; Go's main injects exactly
//     the requested capabilities and prints `error: <msg>` + exit 1 on a
//     non-nil error.
//
// Determinism (spec 9.2): declarations are emitted in package/file/source
// order, instances in discovery order, temporaries are numbered _fc_t0,
// _fc_t1, ... per top-level function, labels _fc_L0, ...; no Go map is
// iterated for output and no paths or timestamps are emitted.
package golang

import (
	"bytes"
	"fmt"
	"go/format"
	"sort"
	"strconv"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/ir"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// RuntimeImport is the import path of the runtime support package.
const RuntimeImport = "fincore/runtime/fincrt"

// Error is a code generation failure (an unsupported construct).
type Error struct {
	Node ast.Node
	Msg  string
}

func (e *Error) Error() string {
	if e.Node != nil {
		return fmt.Sprintf("%s: codegen: %s", e.Node.Span(), e.Msg)
	}
	return "codegen: " + e.Msg
}

// Generate emits the Go program for p.
func Generate(p *ir.Program) (out []byte, err error) {
	g := &gen{p: p, info: p.Info, monoIdx: map[string]*mono{}}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*Error); ok {
				out, err = nil, e
				return
			}
			panic(r)
		}
	}()
	if p.Main == nil {
		return nil, &Error{Msg: "the root package has no main function"}
	}
	g.program()
	src := g.assemble()
	formatted, ferr := format.Source(src)
	if ferr != nil {
		return src, fmt.Errorf("codegen: generated Go does not parse: %v", ferr)
	}
	return formatted, nil
}

// gen holds the state of one Generate call.
type gen struct {
	p    *ir.Program
	info *types.Info

	typesBuf bytes.Buffer // constants and types
	varsBuf  bytes.Buffer
	funcsBuf bytes.Buffer

	usesRuntime bool

	// monomorphized instances (const parameters), in discovery order
	monos   []*mono
	monoIdx map[string]*mono // lookups only
	next    int              // next mono to emit
}

// mono is a monomorphized instance of a type or function with const
// parameters.
type mono struct {
	key    string
	name   string
	td     *ir.TypeDecl // type instance
	fn     *ir.Func     // function instance
	values []uint64     // const argument values, aligned with ConstParams
}

func (g *gen) fail(n ast.Node, format string, args ...any) {
	panic(&Error{Node: n, Msg: fmt.Sprintf(format, args...)})
}

func (g *gen) rt(name string) string {
	g.usesRuntime = true
	return "fincrt." + name
}

func (g *gen) program() {
	for _, pk := range g.p.Packages {
		for _, sym := range pk.Src.Decls {
			if sym.Kind == resolve.SymConst {
				if txt := g.constDecl(sym); txt != "" {
					g.typesBuf.WriteString(txt + "\n\n")
				}
			}
		}
	}
	for _, pk := range g.p.Packages {
		for _, td := range pk.Types {
			if td.Mono() {
				continue
			}
			g.typeDecl(td, nil, g.p.Name(td.Sym))
			for _, m := range td.Methods {
				g.funcDecl(m, newEnv(), "")
			}
		}
	}
	for _, pk := range g.p.Packages {
		for _, gl := range pk.Globals {
			g.global(gl)
		}
	}
	for _, pk := range g.p.Packages {
		for _, f := range pk.Funcs {
			if f.Recv != nil || f.Mono() {
				continue
			}
			g.funcDecl(f, newEnv(), "")
		}
	}
	for g.next < len(g.monos) {
		m := g.monos[g.next]
		g.next++
		if m.td != nil {
			env := newEnv()
			env.bind(m.td.ConstParams, m.values)
			g.typeDecl(m.td, env, m.name)
			for _, f := range m.td.Methods {
				menv := newEnv()
				menv.bind(f.ConstParams, m.values)
				g.funcDecl(f, menv, m.name)
			}
		} else {
			env := newEnv()
			env.bind(m.fn.ConstParams, m.values)
			g.funcDecl(m.fn, env, m.name)
		}
	}
	g.entryPoint()
}

func (g *gen) assemble() []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by finc0 from FinCore sources. DO NOT EDIT.\n\npackage main\n\n")
	imports := []string{"os"}
	if g.usesRuntime {
		imports = append(imports, RuntimeImport)
	}
	sort.Strings(imports)
	b.WriteString("import (\n")
	for _, im := range imports {
		b.WriteString(strconv.Quote(im) + "\n")
	}
	b.WriteString(")\n\n")
	b.Write(g.typesBuf.Bytes())
	b.Write(g.varsBuf.Bytes())
	b.Write(g.funcsBuf.Bytes())
	return b.Bytes()
}

// ---------------------------------------------------------------------------
// Constants

// constName returns the Go name of a constant that is declared as a Go
// constant: scalar (integer, bool, string) constants of basic or named
// basic types, and the predeclared Duration/Signal constants. It returns
// "" for other constants (structs, enums, errors, Code values), which are
// inlined as values at their uses.
func (g *gen) constName(sym *resolve.Symbol) string {
	if sym.Builtin != resolve.BNone {
		if sym.Builtin.IsConst() && !sym.Builtin.IsErrorSentinel() {
			return g.rt(sym.Builtin.String())
		}
		return ""
	}
	switch g.info.SymValue(sym).(type) {
	case types.IntVal, types.BoolVal, types.StringVal:
	default:
		return ""
	}
	t := g.info.SymType(sym)
	if t == nil {
		return ""
	}
	if n, ok := t.(*types.Named); ok && (n.Origin != nil || len(n.TypeParams) > 0) {
		return ""
	}
	if _, ok := t.Underlying().(*types.Basic); !ok {
		return ""
	}
	return g.p.Name(sym)
}

// constDecl renders `const Name [T] = value` ("" if the constant is not
// declared in Go, see constName).
func (g *gen) constDecl(sym *resolve.Symbol) string {
	if sym.Name == "_" || g.constName(sym) == "" {
		return ""
	}
	t := g.info.SymType(sym)
	var lit string
	switch v := g.info.SymValue(sym).(type) {
	case types.IntVal:
		lit = v.V.String()
	case types.BoolVal:
		lit = strconv.FormatBool(bool(v))
	case types.StringVal:
		lit = strconv.Quote(string(v))
	}
	if g.info.IsUntypedConst(sym) {
		return fmt.Sprintf("const %s = %s", g.p.Name(sym), lit)
	}
	return fmt.Sprintf("const %s %s = %s", g.p.Name(sym), g.goType(sym.Ident, t, newEnv()), lit)
}

// ---------------------------------------------------------------------------
// Environments: bindings of const parameters to values.

type env struct {
	consts map[*types.TypeParam]uint64 // lookups only
	syms   map[*resolve.Symbol]uint64  // lookups only
}

func newEnv() *env {
	return &env{consts: map[*types.TypeParam]uint64{}, syms: map[*resolve.Symbol]uint64{}}
}

func (e *env) bind(tps []*types.TypeParam, vals []uint64) {
	for i, tp := range tps {
		e.consts[tp] = vals[i]
		if tp.Sym != nil {
			e.syms[tp.Sym] = vals[i]
		}
	}
}

func (e *env) lookup(tp *types.TypeParam) (uint64, bool) {
	v, ok := e.consts[tp]
	return v, ok
}

// ---------------------------------------------------------------------------
// Monomorphization

func (g *gen) monoName(base string, vals []uint64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "_fc_%d%s", len(base), base)
	for _, v := range vals {
		fmt.Fprintf(&b, "_%d", v)
	}
	return b.String()
}

func (g *gen) monoKey(kind string, sym *resolve.Symbol, vals []uint64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d", kind, sym.ID)
	for _, v := range vals {
		fmt.Fprintf(&b, ",%d", v)
	}
	return b.String()
}

func (g *gen) typeDeclOf(n *types.Named) *ir.TypeDecl {
	for _, pk := range g.p.Packages {
		if pk.Src != n.Pkg {
			continue
		}
		for _, td := range pk.Types {
			if td.Named == n {
				return td
			}
		}
	}
	return nil
}

func (g *gen) monoType(td *ir.TypeDecl, vals []uint64) string {
	key := g.monoKey("type", td.Sym, vals)
	if m := g.monoIdx[key]; m != nil {
		return m.name
	}
	m := &mono{key: key, name: g.monoName(g.p.Name(td.Sym), vals), td: td, values: vals}
	g.monoIdx[key] = m
	g.monos = append(g.monos, m)
	return m.name
}

func (g *gen) monoFunc(f *ir.Func, vals []uint64) string {
	key := g.monoKey("func", f.Info.Sym, vals)
	if m := g.monoIdx[key]; m != nil {
		return m.name
	}
	if len(g.monos) > 100000 {
		g.fail(f.Info.Node(), "too many monomorphized instances (unbounded polymorphic recursion?)")
	}
	m := &mono{key: key, name: g.monoName(g.p.Name(f.Info.Sym), vals), fn: f, values: vals}
	g.monoIdx[key] = m
	g.monos = append(g.monos, m)
	return m.name
}

// constArg resolves a type argument for a const parameter to its value.
func (g *gen) constArg(n ast.Node, t types.Type, e *env) uint64 {
	switch t := t.(type) {
	case *types.ConstArg:
		return t.Value
	case *types.TypeParam:
		if v, ok := e.lookup(t); ok {
			return v
		}
	}
	g.fail(n, "unresolved const type argument %v", t)
	return 0
}

// ---------------------------------------------------------------------------
// Declarations

func (g *gen) typeDecl(td *ir.TypeDecl, e *env, name string) {
	if e == nil {
		e = newEnv()
	}
	w := &g.typesBuf
	under := td.Named.Underlying()
	fmt.Fprintf(w, "type %s%s %s\n\n", name, g.typeParamList(td.TypeParams), g.typeLit(td.Sym.Decl, under, e))
}

// typeParamList renders `[A any, B any]` (empty for none).
func (g *gen) typeParamList(tps []*types.TypeParam) string {
	if len(tps) == 0 {
		return ""
	}
	parts := make([]string, len(tps))
	for i, tp := range tps {
		parts[i] = g.tpName(tp) + " any"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// typeArgNames renders `[A, B]` for a receiver.
func (g *gen) typeArgNames(tps []*types.TypeParam) string {
	if len(tps) == 0 {
		return ""
	}
	parts := make([]string, len(tps))
	for i, tp := range tps {
		parts[i] = g.tpName(tp)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (g *gen) tpName(tp *types.TypeParam) string {
	if tp.Sym != nil {
		return g.p.Name(tp.Sym)
	}
	return ir.GoIdent(tp.Name)
}

func (g *gen) global(gl *ir.Global) {
	w := &g.varsBuf
	name := g.p.Name(gl.Sym)
	t := g.goType(gl.Decl, gl.Type, newEnv())
	if gl.Decl == nil || gl.Decl.Value == nil {
		fmt.Fprintf(w, "var %s %s\n\n", name, t)
		return
	}
	if v := g.info.ValueOf(gl.Decl.Value); v != nil {
		if isZeroValue(v) {
			fmt.Fprintf(w, "var %s %s\n\n", name, t)
			return
		}
		fmt.Fprintf(w, "var %s %s = %s\n\n", name, t, g.value(gl.Decl.Value, v, gl.Type, newEnv()))
		return
	}
	f := &fnCtx{g: g, env: newEnv(), tmp: new(int), label: new(int)}
	fmt.Fprintf(w, "var %s %s = %s\n\n", name, t, f.expr(gl.Decl.Value))
}

// funcDecl emits a function or method. monoName is the Go name of the
// receiver type instance (methods of monomorphized types) or of the
// function instance (monomorphized functions); "" otherwise.
func (g *gen) funcDecl(f *ir.Func, e *env, monoName string) {
	fi := f.Info
	fc := &fnCtx{g: g, fn: f, env: e, tmp: new(int), label: new(int)}
	var b strings.Builder
	b.WriteString("func ")
	name := g.p.Name(fi.Sym)
	if fi.Method != nil {
		recvName := "_"
		if fi.RecvSym != nil {
			recvName = g.p.Name(fi.RecvSym)
		}
		tname := g.p.Name(f.Recv.Sym)
		if f.Recv.Mono() {
			tname = monoName
		}
		star := ""
		if fi.Method.PtrRecv {
			star = "*"
		}
		fmt.Fprintf(&b, "(%s %s%s%s) %s", recvName, star, tname, g.typeArgNames(f.TypeParams), name)
	} else {
		if f.Mono() {
			name = monoName
		}
		b.WriteString(name + g.typeParamList(f.TypeParams))
	}
	b.WriteString(fc.signature(fi))
	b.WriteString(" ")
	g.funcsBuf.WriteString(b.String())
	g.funcsBuf.WriteString(fc.body(fi.Body))
	g.funcsBuf.WriteString("\n\n")
}

// entryPoint emits Go's main.
func (g *gen) entryPoint() {
	w := &g.funcsBuf
	var args []string
	for _, c := range g.p.Caps {
		var ctor string
		switch c {
		case resolve.TNet:
			ctor = "DefaultNet"
		case resolve.TSignals:
			ctor = "DefaultSignals"
		case resolve.TClock:
			ctor = "DefaultClock"
		case resolve.TFS:
			ctor = "DefaultFS"
		case resolve.TEnv:
			ctor = "DefaultEnv"
		case resolve.TStdio:
			ctor = "DefaultStdio"
		case resolve.TExec:
			ctor = "DefaultExec"
		default:
			g.fail(g.p.Main.Decl, "unsupported main parameter type %s", c)
		}
		args = append(args, g.rt(ctor)+"()")
	}
	fmt.Fprintf(w, "func main() {\n")
	fmt.Fprintf(w, "if err := %s(%s); err != nil {\n", g.p.Name(g.p.Main.Sym), strings.Join(args, ", "))
	fmt.Fprintf(w, "_, _ = os.Stderr.WriteString(\"error: \" + err.Error() + \"\\n\")\n")
	fmt.Fprintf(w, "os.Exit(1)\n}\n}\n")
}
