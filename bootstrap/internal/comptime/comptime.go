// Package comptime implements the Layer-1 fuel-bounded compile-time
// interpreter of finc0 (spec 5.3).
//
// The type checker (package types) folds constant expressions itself and
// delegates calls of comptime functions to an Evaluator (types.Evaluator),
// which this package implements by interpreting the type-checked function
// bodies through the typed model (types.Info). Wiring:
//
//	info, diags := types.Check(prog, types.Config{Evaluator: comptime.New(fuel)})
//
// Fuel (default DefaultFuel, CLI --comptime-fuel=N) is a fresh budget per
// evaluation root, i.e. per comptime call that the checker folds (a const
// initializer, array length, Code level, El argument, or a comptime call
// in function code). Every loop iteration, every function call and every
// index operation costs one unit; when the budget is exhausted evaluation
// stops with E4001 and a call stack, reported by the checker at the root
// call (decision 4). Recursion is additionally bounded by MaxDepth frames
// (also E4001).
//
// Other evaluation failures: index/slice out of range -> E4003; division
// by zero, reading package variables, defer/scope/function literals and
// other non-comptime constructs -> E2002; calling a driver function or
// panic -> E5001.
//
// Values are types.Value; arrays and structs are mutable inside the
// interpreter and copied (types.CopyValue) on every store, which gives Go
// value semantics. Integer arithmetic wraps to the static type of the
// expression (types.Wrap).
package comptime

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
	"fincore/runtime/fincrt"
)

// DefaultFuel is the default per-root fuel budget (spec 5.3).
const DefaultFuel = 1_000_000

// MaxDepth bounds the interpreter's call depth.
const MaxDepth = 10_000

// Evaluator implements types.Evaluator.
type Evaluator struct {
	// Fuel is the budget of each evaluation root (0 means DefaultFuel).
	Fuel uint64
	// MaxDepth bounds recursion (0 means MaxDepth).
	MaxDepth int
}

// New returns an Evaluator with the given fuel budget (0 = default).
func New(fuel uint64) *Evaluator { return &Evaluator{Fuel: fuel} }

var _ types.Evaluator = (*Evaluator)(nil)

// CallComptime implements types.Evaluator.
func (ev *Evaluator) CallComptime(h types.Host, fn *resolve.Symbol, args []types.Value) (result types.Value, err *types.EvalError) {
	fuel := ev.Fuel
	if fuel == 0 {
		fuel = DefaultFuel
	}
	depth := ev.MaxDepth
	if depth == 0 {
		depth = MaxDepth
	}
	in := &interp{h: h, info: h.Info(), fuel: fuel, budget: fuel, maxDepth: depth}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*types.EvalError)
			if !ok {
				panic(r)
			}
			result, err = nil, e
		}
	}()
	return in.call(fn, nil, args, nil), nil
}

type interp struct {
	h        types.Host
	info     *types.Info
	fuel     uint64
	budget   uint64
	maxDepth int
	stack    []*frame
}

type frame struct {
	fn   *types.FuncInfo
	site ast.Node // call expression in the caller (nil for the root)
	vars map[*resolve.Symbol]*cell
	ret  types.Value
}

type cell struct{ v types.Value }

// ptrVal is a pointer at compile time.
type ptrVal struct{ r ref }

func (p ptrVal) String() string { return "&" + p.r.get().String() }

type ref interface {
	get() types.Value
	set(types.Value)
}

type cellRef struct{ c *cell }

func (r cellRef) get() types.Value  { return r.c.v }
func (r cellRef) set(v types.Value) { r.c.v = v }

type elemRef struct {
	a *types.ArrayVal
	i int
}

func (r elemRef) get() types.Value  { return r.a.Elems[r.i] }
func (r elemRef) set(v types.Value) { r.a.Elems[r.i] = v }

type fieldRef struct {
	s *types.StructVal
	i int
}

func (r fieldRef) get() types.Value  { return r.s.Fields[r.i] }
func (r fieldRef) set(v types.Value) { r.s.Fields[r.i] = v }

func (in *interp) fail(code diag.Code, format string, args ...any) {
	panic(&types.EvalError{Code: code, Msg: "comptime evaluation failed: " + fmt.Sprintf(format, args...)})
}

func (in *interp) tick() {
	if in.fuel == 0 {
		panic(&types.EvalError{Code: diag.E4001, Msg: fmt.Sprintf("comptime evaluation ran out of fuel (budget %d steps); %s", in.budget, in.stackString())})
	}
	in.fuel--
}

// stackString renders the call stack, innermost first, collapsing runs of
// identical frames.
func (in *interp) stackString() string {
	var parts []string
	for i := len(in.stack) - 1; i >= 0; {
		f := in.stack[i]
		label := f.fn.Name()
		if f.site != nil {
			label += " called at " + f.site.Span().String()
		}
		j := i
		for j-1 >= 0 && in.stack[j-1].fn == f.fn && sameSite(in.stack[j-1].site, f.site) {
			j--
		}
		if n := i - j + 1; n > 1 {
			label += fmt.Sprintf(" (x%d)", n)
		}
		parts = append(parts, label)
		i = j - 1
	}
	return "call stack (innermost first): " + strings.Join(parts, " <- ")
}

func sameSite(a, b ast.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID() == b.ID()
}

func (in *interp) top() *frame { return in.stack[len(in.stack)-1] }

// call runs fn with the given receiver (nil for functions) and arguments.
func (in *interp) call(fn *resolve.Symbol, recv types.Value, args []types.Value, site ast.Node) types.Value {
	if !in.h.EnsureBody(fn) {
		panic(&types.EvalError{}) // already reported
	}
	fi := in.info.FuncOfSym(fn)
	if fi == nil || fi.Body == nil {
		in.fail(diag.E2002, "%s has no body", fn.Name)
	}
	if fi.Effect == types.Driver {
		in.fail(diag.E5001, "cannot call driver function %s at compile time", fi.Name())
	}
	in.tick()
	if len(in.stack) >= in.maxDepth {
		panic(&types.EvalError{Code: diag.E4001, Msg: fmt.Sprintf("comptime evaluation exceeded the maximum call depth (%d); %s", in.maxDepth, in.stackString())})
	}
	f := &frame{fn: fi, site: site, vars: map[*resolve.Symbol]*cell{}}
	if fi.RecvSym != nil {
		f.vars[fi.RecvSym] = &cell{types.CopyValue(recv)}
	}
	if fi.Sig.Variadic && len(fi.Params) > 0 {
		np := len(fi.Params)
		if len(args) >= np-1 {
			rest := &types.ArrayVal{}
			for _, a := range args[np-1:] {
				rest.Elems = append(rest.Elems, types.CopyValue(a))
			}
			args = append(append([]types.Value{}, args[:np-1]...), types.SliceVal{Arr: rest, Lo: 0, Hi: len(rest.Elems)})
		}
	}
	for i, p := range fi.Params {
		if i < len(args) {
			f.vars[p] = &cell{types.CopyValue(args[i])}
		}
	}
	in.stack = append(in.stack, f)
	in.block(fi.Body.Stmts)
	in.stack = in.stack[:len(in.stack)-1]
	if len(fi.Sig.Results) > 0 && f.ret == nil {
		in.fail(diag.E2002, "%s finished without returning a value", fi.Name())
	}
	return f.ret
}

// ---------------------------------------------------------------------------
// Statements

type ctl uint8

const (
	ctlNone ctl = iota
	ctlBreak
	ctlContinue
	ctlReturn
)

func (in *interp) block(list []ast.Stmt) ctl {
	for _, s := range list {
		if c := in.stmt(s); c != ctlNone {
			return c
		}
	}
	return ctlNone
}

func (in *interp) define(sym *resolve.Symbol, v types.Value) {
	if sym == nil || sym.Name == "_" {
		return
	}
	in.top().vars[sym] = &cell{types.CopyValue(v)}
}

func (in *interp) stmt(s ast.Stmt) ctl {
	switch s := s.(type) {
	case nil:
	case *ast.BlockStmt:
		return in.block(s.Stmts)
	case *ast.DeclStmt:
		switch d := s.Decl.(type) {
		case *ast.ConstDecl:
			for _, sp := range d.Specs {
				if sym := in.info.Prog.Defs[sp.Name.ID()]; sym != nil {
					in.define(sym, in.info.SymValue(sym))
				}
			}
		case *ast.VarDecl:
			sym := in.info.Prog.Defs[d.Name.ID()]
			var v types.Value
			if d.Value != nil {
				v = in.eval(d.Value)
			} else {
				v = in.zero(in.info.SymType(sym))
			}
			in.define(sym, v)
		}
	case *ast.ShortVarDecl:
		vals := in.evalList(s.Values, len(s.Names))
		for i, id := range s.Names {
			if id.Name == "_" {
				continue
			}
			if sym := in.info.Prog.Defs[id.ID()]; sym != nil {
				in.define(sym, vals[i])
			} else if sym := in.info.Prog.Uses[id.ID()]; sym != nil {
				in.lookupCell(sym, id).v = types.CopyValue(vals[i])
			}
		}
	case *ast.AssignStmt:
		if s.Op != ast.Assign {
			r := in.lvalue(s.LHS[0])
			y := in.eval(s.RHS[0])
			op := map[ast.AssignOp]ast.BinaryOp{ast.AddAssign: ast.OpAdd, ast.SubAssign: ast.OpSub, ast.MulAssign: ast.OpMul, ast.QuoAssign: ast.OpDiv}[s.Op]
			r.set(in.arith(op, r.get(), y, in.info.TypeOf(s.LHS[0])))
			return ctlNone
		}
		refs := make([]ref, len(s.LHS))
		for i, l := range s.LHS {
			if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
				continue
			}
			refs[i] = in.lvalue(l)
		}
		vals := in.evalList(s.RHS, len(s.LHS))
		for i, r := range refs {
			if r != nil {
				r.set(types.CopyValue(vals[i]))
			}
		}
	case *ast.IncDecStmt:
		r := in.lvalue(s.X)
		op := ast.OpAdd
		if !s.Inc {
			op = ast.OpSub
		}
		r.set(in.arith(op, r.get(), types.MakeInt(1), in.info.TypeOf(s.X)))
	case *ast.ExprStmt:
		in.eval(s.X)
	case *ast.ReturnStmt:
		f := in.top()
		switch {
		case len(s.Results) == 0:
		case len(s.Results) == 1 && len(f.fn.Sig.Results) > 1:
			f.ret = in.eval(s.Results[0])
		case len(s.Results) == 1:
			f.ret = types.CopyValue(in.eval(s.Results[0]))
		default:
			tv := types.TupleVal{}
			for _, r := range s.Results {
				tv.Elems = append(tv.Elems, types.CopyValue(in.eval(r)))
			}
			f.ret = tv
		}
		return ctlReturn
	case *ast.IfStmt:
		if c := in.stmt(s.Init); c != ctlNone {
			return c
		}
		if in.cond(s.Cond) {
			return in.block(s.Then.Stmts)
		}
		return in.stmt(s.Else)
	case *ast.ForStmt:
		if c := in.stmt(s.Init); c != ctlNone {
			return c
		}
		for {
			if s.Cond != nil && !in.cond(s.Cond) {
				return ctlNone
			}
			in.tick()
			switch in.block(s.Body.Stmts) {
			case ctlBreak:
				return ctlNone
			case ctlReturn:
				return ctlReturn
			}
			in.stmt(s.Post)
		}
	case *ast.MatchStmt:
		return in.match(s)
	case *ast.BreakStmt:
		return ctlBreak
	case *ast.ContinueStmt:
		return ctlContinue
	case *ast.DeferStmt:
		in.fail(diag.E2002, "defer is not supported at compile time")
	case *ast.ScopeStmt:
		in.fail(diag.E2002, "scope is not supported at compile time")
	}
	return ctlNone
}

func (in *interp) cond(e ast.Expr) bool {
	b, ok := in.eval(e).(types.BoolVal)
	if !ok {
		in.fail(diag.E2002, "non-boolean condition")
	}
	return bool(b)
}

func (in *interp) match(s *ast.MatchStmt) ctl {
	x := in.eval(s.X)
	for _, arm := range s.Arms {
		ok := false
		switch p := arm.Pattern.(type) {
		case *ast.WildcardPattern:
			ok = true
		case *ast.LiteralPattern:
			ok = in.equal(x, in.eval(p.Lit))
		case *ast.CtorPattern:
			if v := in.info.VariantOf(p); v != nil {
				ev, isEnum := x.(*types.EnumVal)
				if isEnum && ev.Variant == v.Index {
					ok = true
					for i, b := range p.Bindings {
						if sym := in.info.Prog.Defs[b.ID()]; sym != nil && i < len(ev.Payload) {
							in.define(sym, ev.Payload[i])
						}
					}
				}
			} else if cv := in.info.ValueOf(p.Name); cv != nil {
				ok = in.equal(x, cv)
			}
		}
		if !ok {
			continue
		}
		if arm.Expr != nil {
			in.eval(arm.Expr)
			return ctlNone
		}
		return in.block(arm.Block.Stmts)
	}
	return ctlNone
}

func (in *interp) equal(a, b types.Value) bool {
	eq, err := types.Equal(a, b)
	if err != nil {
		in.fail(diag.E2002, "%v", err)
	}
	return eq
}

// evalList evaluates n values from exprs (expanding one multi-valued call).
func (in *interp) evalList(exprs []ast.Expr, n int) []types.Value {
	if len(exprs) == 1 && n > 1 {
		tv, ok := in.eval(exprs[0]).(types.TupleVal)
		if !ok || len(tv.Elems) != n {
			in.fail(diag.E2002, "expected %d values", n)
		}
		return tv.Elems
	}
	out := make([]types.Value, len(exprs))
	for i, e := range exprs {
		out[i] = in.eval(e)
	}
	return out
}

func (in *interp) zero(t types.Type) types.Value {
	if t == nil {
		in.fail(diag.E2002, "unknown type")
	}
	if _, ok := t.Underlying().(*types.Pointer); ok {
		return ptrVal{}
	}
	z, err := types.Zero(t)
	if err != nil {
		in.fail(diag.E2002, "%v", err)
	}
	return z
}

// ---------------------------------------------------------------------------
// Expressions

func (in *interp) lookupCell(sym *resolve.Symbol, id *ast.Ident) *cell {
	for i := len(in.stack) - 1; i >= 0; i-- {
		if c := in.stack[i].vars[sym]; c != nil {
			return c
		}
		break // no closures at compile time
	}
	if sym.IsGlobal() && sym.Kind == resolve.SymVar {
		in.fail(diag.E2002, "cannot read package variable %s at compile time", sym.Name)
	}
	in.fail(diag.E2002, "%s is not available at compile time", id.Name)
	return nil
}

func (in *interp) eval(e ast.Expr) types.Value {
	if v := in.info.ValueOf(e); v != nil {
		return v // constant, or a comptime call folded by the checker
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return in.eval(e.X)
	case *ast.Ident:
		sym := in.info.SymbolOf(e)
		if sym == nil {
			in.fail(diag.E2002, "unresolved identifier %s", e.Name)
		}
		switch sym.Kind {
		case resolve.SymConst:
			if v := in.h.ConstValue(sym); v != nil {
				return v
			}
			panic(&types.EvalError{})
		case resolve.SymVar:
			return in.lookupCell(sym, e).v
		}
		in.fail(diag.E2002, "%s cannot be used as a value at compile time", e.Name)
	case *ast.BinaryExpr:
		switch e.Op {
		case ast.OpLAnd:
			if !in.cond(e.X) {
				return types.BoolVal(false)
			}
			return types.BoolVal(in.cond(e.Y))
		case ast.OpLOr:
			if in.cond(e.X) {
				return types.BoolVal(true)
			}
			return types.BoolVal(in.cond(e.Y))
		}
		x, y := in.eval(e.X), in.eval(e.Y)
		switch e.Op {
		case ast.OpEq, ast.OpNe, ast.OpLt, ast.OpLe, ast.OpGt, ast.OpGe:
			v, err := types.BinaryOp(e.Op, x, y)
			if err != nil {
				in.fail(diag.E2002, "%v", err)
			}
			return v
		}
		return in.arith(e.Op, x, y, in.info.TypeOf(e))
	case *ast.UnaryExpr:
		switch e.Op {
		case ast.OpAddr:
			return ptrVal{in.lvalue(e.X)}
		case ast.OpDeref:
			return in.deref(in.eval(e.X)).get()
		}
		v, err := types.UnaryOp(e.Op, in.eval(e.X))
		if err != nil {
			in.fail(diag.E2002, "%v", err)
		}
		return types.Wrap(v, in.info.TypeOf(e))
	case *ast.CallExpr:
		return in.callExpr(e)
	case *ast.IndexExpr:
		x := in.eval(e.X)
		i := in.intArg(e.Indices[0])
		in.tick()
		switch x := x.(type) {
		case ptrVal:
			return in.indexRef(x.r.get(), i).get()
		case types.StringVal:
			if i < 0 || i >= len(x) {
				in.fail(diag.E4003, "index %d out of range [0:%d]", i, len(x))
			}
			return types.MakeUint(uint64(x[i]))
		}
		return in.indexRef(x, i).get()
	case *ast.SliceExpr:
		return in.slice(e)
	case *ast.SelectorExpr:
		sel := in.info.SelectionOf(e)
		if sel == nil || sel.Kind != types.SelField {
			if sym := in.info.Prog.Uses[e.Sel.ID()]; sym != nil {
				return in.eval(e.Sel)
			}
			in.fail(diag.E2002, "unsupported selector %s at compile time", e.Sel.Name)
		}
		x := in.eval(e.X)
		if p, ok := x.(ptrVal); ok {
			x = in.deref(p).get()
		}
		sv, ok := x.(*types.StructVal)
		if !ok {
			in.fail(diag.E2002, "field access on non-struct value")
		}
		return sv.Fields[sel.Field.Index]
	case *ast.StructLit:
		sv, ok := in.zero(in.info.TypeOf(e)).(*types.StructVal)
		if !ok {
			in.fail(diag.E2002, "invalid struct literal")
		}
		st := in.info.TypeOf(e).Underlying().(*types.Struct)
		for _, fi := range e.Fields {
			if f := st.FieldByName(fi.Name.Name); f != nil {
				sv.Fields[f.Index] = types.CopyValue(in.eval(fi.Value))
			}
		}
		return sv
	case *ast.FuncLit:
		in.fail(diag.E2002, "function literals are not supported at compile time")
	}
	in.fail(diag.E2002, "unsupported expression at compile time")
	return nil
}

func (in *interp) deref(v types.Value) ref {
	p, ok := v.(ptrVal)
	if !ok || p.r == nil {
		in.fail(diag.E2002, "nil pointer dereference")
	}
	return p.r
}

func (in *interp) arith(op ast.BinaryOp, x, y types.Value, t types.Type) types.Value {
	v, err := types.BinaryOp(op, x, y)
	if err != nil {
		in.fail(diag.E2002, "%v", err)
	}
	return types.Wrap(v, t)
}

func (in *interp) intArg(e ast.Expr) int {
	v, ok := in.eval(e).(types.IntVal)
	if !ok || !v.V.IsInt64() {
		in.fail(diag.E4003, "invalid index")
	}
	n := v.V.Int64()
	if n < 0 || n > 1<<40 {
		in.fail(diag.E4003, "index %d out of range", n)
	}
	return int(n)
}

// indexRef returns a reference to element i of an array or slice value.
func (in *interp) indexRef(x types.Value, i int) ref {
	switch x := x.(type) {
	case *types.ArrayVal:
		if i >= len(x.Elems) {
			in.fail(diag.E4003, "index %d out of range [0:%d]", i, len(x.Elems))
		}
		return elemRef{x, i}
	case types.SliceVal:
		if i >= x.Hi-x.Lo {
			in.fail(diag.E4003, "index %d out of range [0:%d]", i, x.Hi-x.Lo)
		}
		return elemRef{x.Arr, x.Lo + i}
	}
	in.fail(diag.E2002, "cannot index value %s", x)
	return nil
}

func (in *interp) lvalue(e ast.Expr) ref {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return in.lvalue(e.X)
	case *ast.Ident:
		sym := in.info.SymbolOf(e)
		if sym == nil || sym.Kind != resolve.SymVar {
			in.fail(diag.E2002, "cannot assign to %s", e.Name)
		}
		return cellRef{in.lookupCell(sym, e)}
	case *ast.IndexExpr:
		var base types.Value
		if p, ok := in.eval(e.X).(ptrVal); ok {
			base = in.deref(p).get()
		} else if _, isSlice := in.info.TypeOf(e.X).Underlying().(*types.Slice); isSlice {
			base = in.eval(e.X)
		} else {
			base = in.lvalue(e.X).get()
		}
		i := in.intArg(e.Indices[0])
		in.tick()
		return in.indexRef(base, i)
	case *ast.SelectorExpr:
		sel := in.info.SelectionOf(e)
		if sel == nil || sel.Kind != types.SelField {
			in.fail(diag.E2002, "cannot assign to %s", e.Sel.Name)
		}
		var base types.Value
		if sel.Indirect {
			base = in.deref(in.eval(e.X)).get()
		} else {
			base = in.lvalue(e.X).get()
		}
		sv, ok := base.(*types.StructVal)
		if !ok {
			in.fail(diag.E2002, "field access on non-struct value")
		}
		return fieldRef{sv, sel.Field.Index}
	case *ast.UnaryExpr:
		if e.Op == ast.OpDeref {
			return in.deref(in.eval(e.X))
		}
	}
	in.fail(diag.E2002, "expression is not addressable at compile time")
	return nil
}

func (in *interp) slice(e *ast.SliceExpr) types.Value {
	var x types.Value
	if _, isArr := in.info.TypeOf(e.X).Underlying().(*types.Array); isArr {
		x = in.lvalue(e.X).get()
	} else {
		x = in.eval(e.X)
	}
	if p, ok := x.(ptrVal); ok {
		x = in.deref(p).get()
	}
	lo, hi := 0, -1
	if e.Lo != nil {
		lo = in.intArg(e.Lo)
	}
	if e.Hi != nil {
		hi = in.intArg(e.Hi)
	}
	switch x := x.(type) {
	case types.StringVal:
		if hi < 0 {
			hi = len(x)
		}
		if lo > hi || hi > len(x) {
			in.fail(diag.E4003, "slice bounds [%d:%d] out of range [0:%d]", lo, hi, len(x))
		}
		return x[lo:hi]
	case *types.ArrayVal:
		if hi < 0 {
			hi = len(x.Elems)
		}
		if lo > hi || hi > len(x.Elems) {
			in.fail(diag.E4003, "slice bounds [%d:%d] out of range [0:%d]", lo, hi, len(x.Elems))
		}
		return types.SliceVal{Arr: x, Lo: lo, Hi: hi}
	case types.SliceVal:
		n := x.Hi - x.Lo
		capn := 0
		if x.Arr != nil {
			capn = len(x.Arr.Elems) - x.Lo
		}
		if hi < 0 {
			hi = n
		}
		if lo > hi || hi > capn {
			in.fail(diag.E4003, "slice bounds [%d:%d] out of range [0:%d]", lo, hi, capn)
		}
		if x.Arr == nil {
			return types.SliceVal{}
		}
		return types.SliceVal{Arr: x.Arr, Lo: x.Lo + lo, Hi: x.Lo + hi}
	}
	in.fail(diag.E2002, "cannot slice value")
	return nil
}

// ---------------------------------------------------------------------------
// Calls

func (in *interp) args(e *ast.CallExpr) []types.Value {
	if len(e.Args) == 1 {
		if _, ok := in.info.TypeOf(e.Args[0]).(*types.Tuple); ok {
			tv, _ := in.eval(e.Args[0]).(types.TupleVal)
			return tv.Elems
		}
	}
	out := make([]types.Value, len(e.Args))
	for i, a := range e.Args {
		out[i] = in.eval(a)
	}
	return out
}

func (in *interp) callExpr(e *ast.CallExpr) types.Value {
	ci := in.info.CallOf(e)
	if ci == nil {
		in.fail(diag.E2002, "unchecked call")
	}
	switch ci.Kind {
	case types.CallFunc:
		return in.call(ci.Callee, nil, in.args(e), e)
	case types.CallMethod:
		var recv types.Value
		if _, ok := ci.RecvType.(*types.Pointer); ok && ci.Method.PtrRecv {
			recv = in.eval(ci.Recv)
		} else {
			recv = in.eval(ci.Recv)
			if p, ok := recv.(ptrVal); ok {
				recv = in.deref(p).get()
			}
		}
		return in.call(ci.Callee, recv, in.args(e), e)
	case types.CallConversion:
		return types.Wrap(in.eval(e.Args[0]), ci.Conv)
	case types.CallVariant:
		ev := &types.EnumVal{Variant: ci.Variant.Index}
		for _, a := range in.args(e) {
			ev.Payload = append(ev.Payload, types.CopyValue(a))
		}
		return ev
	case types.CallBuiltin:
		return in.builtin(e, ci)
	}
	in.fail(diag.E2002, "%s calls are not supported at compile time", ci.Kind)
	return nil
}

func (in *interp) builtin(e *ast.CallExpr, ci *types.Call) types.Value {
	args := in.args(e)
	bytesOf := func(v types.Value) []byte {
		s, ok := v.(types.SliceVal)
		if !ok {
			in.fail(diag.E2002, "expected a byte slice")
		}
		var out []byte
		if s.Arr != nil {
			for _, b := range s.Arr.Elems[s.Lo:s.Hi] {
				u, _ := types.Uint64(b)
				out = append(out, byte(u))
			}
		}
		return out
	}
	byteSlice := func(b []byte) types.SliceVal {
		a := &types.ArrayVal{Elems: make([]types.Value, len(b))}
		for i, c := range b {
			a.Elems[i] = types.MakeUint(uint64(c))
		}
		return types.SliceVal{Arr: a, Lo: 0, Hi: len(b)}
	}
	switch ci.Builtin {
	case resolve.FLen, resolve.FCap:
		x := args[0]
		if p, ok := x.(ptrVal); ok {
			x = in.deref(p).get()
		}
		switch x := x.(type) {
		case types.StringVal:
			return types.MakeUint(uint64(len(x)))
		case *types.ArrayVal:
			return types.MakeUint(uint64(len(x.Elems)))
		case types.SliceVal:
			if ci.Builtin == resolve.FCap && x.Arr != nil {
				return types.MakeUint(uint64(len(x.Arr.Elems) - x.Lo))
			}
			return types.MakeUint(uint64(x.Hi - x.Lo))
		}
	case resolve.FCopy:
		d, ok1 := args[0].(types.SliceVal)
		s, ok2 := args[1].(types.SliceVal)
		if !ok1 || !ok2 {
			break
		}
		n := min(d.Hi-d.Lo, s.Hi-s.Lo)
		tmp := make([]types.Value, n)
		for i := 0; i < n; i++ {
			tmp[i] = types.CopyValue(s.Arr.Elems[s.Lo+i])
		}
		for i := 0; i < n; i++ {
			d.Arr.Elems[d.Lo+i] = tmp[i]
		}
		return types.MakeUint(uint64(n))
	case resolve.FBytesEqual:
		return types.BoolVal(string(bytesOf(args[0])) == string(bytesOf(args[1])))
	case resolve.FStrBytes:
		s, _ := args[0].(types.StringVal)
		return byteSlice([]byte(s))
	case resolve.FBytesToStr:
		return types.StringVal(bytesOf(args[0]))
	case resolve.FFormatUint:
		v, _ := args[0].(types.IntVal)
		digits := strconv.FormatUint(v.V.Uint64(), 10)
		d, _ := args[1].(types.SliceVal)
		if len(digits) > d.Hi-d.Lo {
			return types.TupleVal{Elems: []types.Value{types.MakeInt(0), types.ErrorVal{Name: "ErrArenaFull"}}}
		}
		for i := 0; i < len(digits); i++ {
			d.Arr.Elems[d.Lo+i] = types.MakeUint(uint64(digits[i]))
		}
		return types.TupleVal{Elems: []types.Value{types.MakeUint(uint64(len(digits))), types.ErrorVal{}}}
	case resolve.FParseUint:
		b := bytesOf(args[0])
		v, err := strconv.ParseUint(string(b), 10, 64)
		if err != nil || len(b) == 0 || strings.ContainsAny(string(b), "+-_") {
			return types.TupleVal{Elems: []types.Value{types.MakeInt(0), types.ErrorVal{Name: "ErrParse"}}}
		}
		return types.TupleVal{Elems: []types.Value{types.IntVal{V: new(big.Int).SetUint64(v)}, types.ErrorVal{}}}
	case resolve.FFormatTo:
		return in.formatTo(e, ci, args, bytesOf, byteSlice)
	case resolve.FPanic:
		in.fail(diag.E5001, "panic at compile time: %s", args[0])
	case resolve.FCodeArray:
		n, _ := types.Uint64(args[0])
		return &types.CodeVal{Kind: types.CodeArray, Len: n, Elem: in.code(args[1])}
	case resolve.FCodeSlice:
		return &types.CodeVal{Kind: types.CodeSlice, Elem: in.code(args[0])}
	case resolve.FCodeField:
		s, _ := args[0].(types.StringVal)
		return &types.FieldCodeVal{Name: string(s), Code: in.code(args[1])}
	case resolve.FCodeStruct:
		s, _ := args[0].(types.SliceVal)
		cv := &types.CodeVal{Kind: types.CodeStruct}
		if s.Arr != nil {
			for _, f := range s.Arr.Elems[s.Lo:s.Hi] {
				fc, ok := f.(*types.FieldCodeVal)
				if !ok {
					in.fail(diag.E2002, "CodeStruct: zero FieldCode element")
				}
				cv.Fields = append(cv.Fields, fc)
			}
		}
		return cv
	case resolve.FCodeUniv:
		n, _ := types.Uint64(args[0])
		return &types.CodeVal{Kind: types.CodeUniv, Univ: int(n)}
	default:
		if ci.Builtin.IsCodeCtor() {
			return &types.CodeVal{Kind: types.CodeKind(ci.Builtin - resolve.FCodeEmpty)}
		}
	}
	in.fail(diag.E2002, "builtin %s is not available at compile time", ci.Builtin)
	return nil
}

// formatTo runs a formatTo call (v0.2) with the runtime's writer
// (fincrt.Fmt), so the semantics are identical to generated code. Each
// formatted argument costs one unit of fuel; a %v Format method is
// interpreted like any other call (with its own fuel and depth).
func (in *interp) formatTo(e *ast.CallExpr, ci *types.Call, args []types.Value, bytesOf func(types.Value) []byte, byteSlice func([]byte) types.SliceVal) types.Value {
	fi := ci.Format
	if fi == nil || len(args) != len(fi.Args)+2 {
		in.fail(diag.E2002, "formatTo without format information")
	}
	d, _ := args[0].(types.SliceVal)
	buf := bytesOf(d)
	w := fincrt.NewFmt(buf)
	k := 0
	for _, p := range fi.Pieces {
		if p.Verb == 0 {
			w.Lit(p.Lit)
			continue
		}
		in.tick()
		v, fa := args[2+k], fi.Args[k]
		k++
		width := p.FormatWidth()
		iv, ok := v.(types.IntVal)
		if !ok || iv.V == nil {
			iv = types.MakeUint(0)
		}
		switch fa.Op {
		case types.FmtInt:
			w.Int(iv.V.Int64(), width, p.Zero)
		case types.FmtHexInt:
			w.HexInt(iv.V.Int64(), width, p.Zero)
		case types.FmtUint:
			w.Uint(iv.V.Uint64(), width, p.Zero)
		case types.FmtHexUint:
			w.Hex(iv.V.Uint64(), width, p.Zero)
		case types.FmtStr:
			s, _ := v.(types.StringVal)
			w.Str(string(s), width)
		case types.FmtQuoteStr:
			s, _ := v.(types.StringVal)
			w.Quote(string(s), width)
		case types.FmtBytes:
			w.Bytes(bytesOf(v), width)
		case types.FmtQuoteBytes:
			w.QuoteBytes(bytesOf(v), width)
		case types.FmtByte:
			w.Byte(byte(iv.V.Uint64()), width)
		case types.FmtBool:
			b, _ := v.(types.BoolVal)
			w.Bool(bool(b), width)
		case types.FmtFormat:
			dst := w.Dst(width)
			tmp := byteSlice(dst)
			r := in.call(fa.Method.Sym, v, []types.Value{tmp}, e)
			for i := range dst {
				u, _ := types.Uint64(tmp.Arr.Elems[i])
				dst[i] = byte(u)
			}
			n, _ := types.Uint64(r)
			w.Advance(uint(n), width)
		default:
			in.fail(diag.E2002, "formatTo argument without a format operation")
		}
	}
	for i, b := range buf {
		d.Arr.Elems[d.Lo+i] = types.MakeUint(uint64(b))
	}
	return types.MakeUint(uint64(w.N()))
}

func (in *interp) code(v types.Value) *types.CodeVal {
	c, ok := v.(*types.CodeVal)
	if !ok {
		in.fail(diag.E2002, "expected a Code value")
	}
	return c
}
