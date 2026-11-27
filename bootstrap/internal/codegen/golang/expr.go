package golang

import (
	"fmt"
	"strconv"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/ir"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// fnCtx is the emission context of one function body (or a package-level
// initializer).
type fnCtx struct {
	g   *gen
	fn  *ir.Func // nil for package-level initializers
	env *env
	tmp *int // temporaries counter, shared with nested literals
	// label counter, shared with nested literals
	label *int

	w       *strings.Builder
	frames  []*frame
	pending []string // `_ = x` statements to emit at the start of the next block
	header  int      // > 0 while emitting an if/for/switch header
	// typedLits: literals must carry their type explicitly
	typedLits bool
}

type frameKind uint8

const (
	frameLoop frameKind = iota
	frameSwitch
	frameScope
)

type frame struct {
	kind     frameKind
	label    string // loops: label (assigned when a break needs it)
	scopeVar string // scope frames: the Go variable holding *fincrt.Scope
}

func (f *fnCtx) newTemp() string {
	s := fmt.Sprintf("_fc_t%d", *f.tmp)
	*f.tmp++
	return s
}

func (f *fnCtx) typeOf(e ast.Node) types.Type { return f.g.info.TypeOf(e) }

func (f *fnCtx) goType(n ast.Node, t types.Type) string { return f.g.goType(n, t, f.env) }

// composite wraps a composite literal in parentheses inside statement
// headers (Go's `if x == T{} {` ambiguity).
func (f *fnCtx) composite(s string) string {
	if f.header > 0 && strings.HasSuffix(s, "}") {
		return "(" + s + ")"
	}
	return s
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// constValue returns the compile-time value of e if e is a constant
// expression (or a folded comptime call).
func (f *fnCtx) constValue(e ast.Expr) types.Value {
	v := f.g.info.ValueOf(e)
	if v == nil {
		return nil
	}
	if f.g.info.KindOf(e) == types.KindConst {
		return v
	}
	if c, ok := e.(*ast.CallExpr); ok {
		if ci := f.g.info.CallOf(c); ci != nil && ci.Folded {
			return v
		}
	}
	return nil
}

// expr renders an expression.
func (f *fnCtx) expr(e ast.Expr) string {
	typed := f.typedLits
	f.typedLits = false // only applies to the top-level expression
	if v := f.constValue(e); v != nil {
		if f.g.info.IsImplicitBytes(e) {
			// a constant string used as []byte (v0.2): a fresh copy
			return "[]byte(" + f.constExpr(e, v, false) + ")"
		}
		return f.constExpr(e, v, typed)
	}
	switch e := e.(type) {
	case *ast.Ident:
		return f.ident(e)
	case *ast.ParenExpr:
		return "(" + f.expr(e.X) + ")"
	case *ast.BinaryExpr:
		return f.binary(e)
	case *ast.UnaryExpr:
		return f.unary(e)
	case *ast.CallExpr:
		return f.call(e)
	case *ast.IndexExpr:
		if inst := f.g.info.InstanceOf(e); inst != nil && inst.Named == nil {
			sym := f.calleeSym(e.X)
			return f.funcRef(e, sym, inst.TypeArgs)
		}
		if len(e.Indices) != 1 {
			f.g.fail(e, "unsupported index expression")
		}
		return f.primary(e.X) + "[" + f.expr(e.Indices[0]) + "]"
	case *ast.SliceExpr:
		lo, hi := "", ""
		if e.Lo != nil {
			lo = f.expr(e.Lo)
		}
		if e.Hi != nil {
			hi = f.expr(e.Hi)
		}
		return f.primary(e.X) + "[" + lo + ":" + hi + "]"
	case *ast.SelectorExpr:
		return f.selector(e)
	case *ast.FuncLit:
		return f.funcLit(e)
	case *ast.StructLit:
		return f.structLit(e)
	case *ast.MatchExpr:
		return f.matchExpr(e)
	case *ast.IsExpr:
		return f.isExpr(e)
	case *ast.BasicLit, *ast.BoolLit, *ast.NilLit:
		f.g.fail(e, "literal without a recorded value")
	}
	f.g.fail(e, "unsupported expression %T", e)
	return ""
}

// constExpr renders the constant expression e with value v.
func (f *fnCtx) constExpr(e ast.Expr, v types.Value, typed bool) string {
	if _, ok := v.(types.TupleVal); ok {
		f.g.fail(e, "multi-value constant in single-value context")
	}
	if lit, ok := e.(*ast.BasicLit); ok {
		return f.basicLit(lit, v, typed)
	}
	if ref := f.constRef(e, typed); ref != "" {
		return ref
	}
	if c, ok := e.(*ast.CallExpr); ok && len(c.Args) == 1 {
		if ci := f.g.info.CallOf(c); ci != nil && ci.Kind == types.CallConversion {
			if ref := f.constRef(unparen(c.Args[0]), false); ref != "" {
				return f.convType(c, ci.Conv) + "(" + ref + ")"
			}
		}
	}
	if p, ok := e.(*ast.ParenExpr); ok {
		if lit, ok := unparen(p).(*ast.BasicLit); ok && !typed {
			return f.basicLit(lit, v, false)
		}
	}
	return f.composite(f.g.value(e, v, f.typeOf(e), f.env))
}

// basicLit renders a literal, keeping the source spelling where Go
// accepts it. Integer and char literals are left untyped (Go converts them
// to the operand/parameter/field type, which is the type the checker
// gave them) except where typed asks for an explicit type (top level of
// `:=` right-hand sides, where Go would infer int or rune).
func (f *fnCtx) basicLit(lit *ast.BasicLit, v types.Value, typed bool) string {
	t := f.typeOf(lit)
	switch lit.Kind {
	case ast.StringLit:
		s := strconv.Quote(lit.Str)
		if _, named := t.(*types.Named); named {
			return f.goType(lit, t) + "(" + s + ")"
		}
		return s
	case ast.CharLit:
		spell := v.String()
		if !strings.Contains(lit.Raw, `\`) && len(lit.Raw) == 3 {
			spell = lit.Raw
		}
		if t == nil || types.IsUntyped(t) {
			t = types.Typ[types.U8]
		}
		if !typed {
			return spell
		}
		return f.goType(lit, t) + "(" + spell + ")"
	}
	spell := lit.Raw
	if t == nil || isUntypedOrInt(t) || !typed {
		return spell
	}
	return f.goType(lit, t) + "(" + spell + ")"
}

// convType renders the type of a conversion T(x).
func (f *fnCtx) convType(n ast.Node, t types.Type) string {
	s := f.goType(n, t)
	if strings.HasPrefix(s, "*") || strings.HasPrefix(s, "func") {
		s = "(" + s + ")"
	}
	return s
}

// constRef renders a reference to a named constant that is declared as a
// Go constant ("" if e is not such a reference). Untyped constants get an
// explicit type where typed asks for it.
func (f *fnCtx) constRef(e ast.Expr, typed bool) string {
	var sym *resolve.Symbol
	switch x := e.(type) {
	case *ast.Ident:
		sym = f.g.p.Prog.Uses[x.ID()]
	case *ast.SelectorExpr:
		sym = f.g.p.Prog.Uses[x.Sel.ID()]
	}
	if sym == nil || sym.Kind != resolve.SymConst {
		return ""
	}
	name := f.g.constName(sym)
	if name == "" {
		return ""
	}
	t := f.typeOf(e)
	if typed && f.g.info.IsUntypedConst(sym) && t != nil && !isUntypedOrInt(t) && !types.IsUntyped(t) {
		return f.goType(e, t) + "(" + name + ")"
	}
	return name
}

// typedConst renders a constant operand with an explicit Go type (used for
// the left operand of shifts, whose type Go would otherwise infer from the
// context).
func (f *fnCtx) typedConst(e ast.Expr, v types.Value) string {
	t := f.typeOf(e)
	if t == nil || types.IsUntyped(t) {
		t = types.Default(t)
	}
	if iv, ok := v.(types.IntVal); ok {
		ref := f.constRef(unparen(e), false)
		if ref == "" {
			ref = iv.V.String()
		}
		return f.goType(e, t) + "(" + ref + ")"
	}
	return f.g.value(e, v, t, f.env)
}

// primary renders e so that it can be followed by a selector, index or
// call suffix.
func (f *fnCtx) primary(e ast.Expr) string {
	s := f.expr(e)
	switch unparen(e).(type) {
	case *ast.UnaryExpr, *ast.BinaryExpr:
		if _, isParen := e.(*ast.ParenExpr); !isParen {
			return "(" + s + ")"
		}
	}
	if f.constValue(e) != nil && strings.HasPrefix(s, "-") {
		return "(" + s + ")"
	}
	return s
}

func (f *fnCtx) ident(e *ast.Ident) string {
	sym := f.g.p.Prog.Uses[e.ID()]
	if sym == nil {
		sym = f.g.p.Prog.Defs[e.ID()]
	}
	if sym == nil {
		if e.Name == "_" {
			return "_"
		}
		f.g.fail(e, "unresolved identifier %s", e.Name)
	}
	switch sym.Kind {
	case resolve.SymVar, resolve.SymFunc:
		return f.g.p.Name(sym)
	case resolve.SymTypeParam:
		if v, ok := f.env.syms[sym]; ok {
			return f.typedConst(e, types.MakeUint(v))
		}
		f.g.fail(e, "unbound const parameter %s", e.Name)
	case resolve.SymConst:
		f.g.fail(e, "constant %s without a recorded value", e.Name)
	}
	f.g.fail(e, "identifier %s (%s) cannot be used as a value", e.Name, sym.Kind)
	return ""
}

// Go operator precedence (5 binds tightest).
func goPrec(op ast.BinaryOp) int {
	switch op {
	case ast.OpLOr:
		return 1
	case ast.OpLAnd:
		return 2
	case ast.OpEq, ast.OpNe, ast.OpLt, ast.OpLe, ast.OpGt, ast.OpGe:
		return 3
	case ast.OpAdd, ast.OpSub, ast.OpOr, ast.OpXor:
		return 4
	}
	return 5
}

func (f *fnCtx) operand(e ast.Expr, parent ast.BinaryOp, right bool) string {
	s := f.expr(e)
	if b, ok := e.(*ast.BinaryExpr); ok && f.constValue(e) == nil {
		cp, pp := goPrec(b.Op), goPrec(parent)
		if cp < pp || (right && cp == pp) {
			return "(" + s + ")"
		}
	}
	return s
}

func (f *fnCtx) binary(e *ast.BinaryExpr) string {
	var x string
	if (e.Op == ast.OpShl || e.Op == ast.OpShr) && f.constValue(e.X) != nil {
		x = f.typedConst(e.X, f.constValue(e.X))
	} else {
		x = f.operand(e.X, e.Op, false)
	}
	y := f.operand(e.Y, e.Op, true)
	return x + " " + e.Op.String() + " " + y
}

func (f *fnCtx) unary(e *ast.UnaryExpr) string {
	x := f.expr(e.X)
	switch unparen(e.X).(type) {
	case *ast.UnaryExpr, *ast.BinaryExpr:
		if _, isParen := e.X.(*ast.ParenExpr); !isParen {
			x = "(" + x + ")"
		}
	}
	if strings.HasPrefix(x, "-") || strings.HasPrefix(x, "&") || strings.HasPrefix(x, "*") {
		x = "(" + x + ")"
	}
	return e.Op.String() + x
}

func (f *fnCtx) selector(e *ast.SelectorExpr) string {
	if sym := f.g.p.Prog.Uses[e.Sel.ID()]; sym != nil {
		// package-qualified name
		if v := f.constValue(e.Sel); v != nil {
			return f.composite(f.g.value(e, v, f.typeOf(e.Sel), f.env))
		}
		return f.ident(e.Sel)
	}
	sel := f.g.info.SelectionOf(e)
	if sel == nil {
		f.g.fail(e, "unresolved selector %s", e.Sel.Name)
	}
	switch sel.Kind {
	case types.SelField:
		return f.primary(e.X) + "." + ir.FieldIdent(sel.Field.Name)
	case types.SelVariant:
		f.g.fail(e, "enum variant %s with payload must be called", e.Sel.Name)
	}
	f.g.fail(e, "method value %s is not supported", e.Sel.Name)
	return ""
}

func (f *fnCtx) structLit(e *ast.StructLit) string {
	t := f.typeOf(e)
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		f.g.fail(e, "struct literal of non-struct type")
	}
	parts := make([]string, 0, len(e.Fields))
	for _, fi := range e.Fields {
		fld := st.FieldByName(fi.Name.Name)
		if fld == nil {
			f.g.fail(fi, "unknown field %s", fi.Name.Name)
		}
		parts = append(parts, ir.FieldIdent(fld.Name)+": "+f.expr(fi.Value))
	}
	return f.composite(f.goType(e, t) + "{" + strings.Join(parts, ", ") + "}")
}

func (f *fnCtx) funcLit(e *ast.FuncLit) string {
	fi := f.g.info.FuncOf(e)
	if fi == nil {
		f.g.fail(e, "function literal without FuncInfo")
	}
	inner := &fnCtx{g: f.g, fn: f.g.p.FuncOf(fi), env: f.env, tmp: f.tmp, label: f.label}
	return "func" + inner.signature(fi) + " " + inner.body(fi.Body)
}

// signature renders `(p1 T1, p2 T2) (R1, R2)` for a function.
func (f *fnCtx) signature(fi *types.FuncInfo) string {
	var b strings.Builder
	b.WriteString("(")
	n := fi.Node()
	for i, pt := range fi.Sig.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		name := "_"
		if i < len(fi.Params) && fi.Params[i] != nil {
			name = f.g.p.Name(fi.Params[i])
		}
		b.WriteString(name + " ")
		if fi.Sig.Variadic && i == len(fi.Sig.Params)-1 {
			b.WriteString("...")
		}
		b.WriteString(f.goType(n, pt))
	}
	b.WriteString(")")
	b.WriteString(f.g.results(n, fi.Sig.Results, f.env))
	return b.String()
}

// ---------------------------------------------------------------------------
// Calls

func (f *fnCtx) calleeSym(e ast.Expr) *resolve.Symbol {
	switch e := unparen(e).(type) {
	case *ast.Ident:
		return f.g.p.Prog.Uses[e.ID()]
	case *ast.SelectorExpr:
		return f.g.p.Prog.Uses[e.Sel.ID()]
	case *ast.IndexExpr:
		return f.calleeSym(e.X)
	}
	return nil
}

// funcRef renders a reference to a (possibly generic) declared function.
func (f *fnCtx) funcRef(n ast.Node, sym *resolve.Symbol, typeArgs []types.Type) string {
	if sym == nil {
		f.g.fail(n, "unresolved function")
	}
	fi := f.g.info.FuncOfSym(sym)
	if fi == nil || len(fi.TypeParams) == 0 {
		return f.g.p.Name(sym)
	}
	if len(typeArgs) != len(fi.TypeParams) {
		f.g.fail(n, "generic function %s needs %d type arguments", sym.Name, len(fi.TypeParams))
	}
	var vals []uint64
	var targs []string
	for i, tp := range fi.TypeParams {
		if tp.IsConst() {
			vals = append(vals, f.g.constArg(n, typeArgs[i], f.env))
		} else {
			targs = append(targs, f.goType(n, typeArgs[i]))
		}
	}
	name := f.g.p.Name(sym)
	if len(vals) > 0 {
		name = f.g.monoFunc(f.g.p.FuncOf(fi), vals)
	}
	if len(targs) > 0 {
		name += "[" + strings.Join(targs, ", ") + "]"
	}
	return name
}

// args renders call arguments, expanding a folded multi-value comptime
// call f(g()).
func (f *fnCtx) args(args []ast.Expr) string {
	if len(args) == 1 {
		if tv, ok := f.constValue(args[0]).(types.TupleVal); ok {
			return strings.Join(f.tupleValues(args[0], tv), ", ")
		}
	}
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = f.expr(a)
	}
	return strings.Join(parts, ", ")
}

func (f *fnCtx) tupleValues(e ast.Expr, tv types.TupleVal) []string {
	tt, ok := f.typeOf(e).(*types.Tuple)
	if !ok || len(tt.Types) != len(tv.Elems) {
		f.g.fail(e, "malformed multi-value constant")
	}
	out := make([]string, len(tv.Elems))
	for i, v := range tv.Elems {
		out[i] = f.g.value(e, v, tt.Types[i], f.env)
	}
	return out
}

func (f *fnCtx) call(e *ast.CallExpr) string {
	ci := f.g.info.CallOf(e)
	if ci == nil {
		f.g.fail(e, "call without type information")
	}
	switch ci.Kind {
	case types.CallFunc:
		return f.funcRef(e, ci.Callee, ci.TypeArgs) + "(" + f.args(e.Args) + ")"
	case types.CallMethod:
		return f.primary(ci.Recv) + "." + f.g.p.Name(ci.Callee) + "(" + f.args(e.Args) + ")"
	case types.CallBuiltinMeth:
		return f.builtinMethod(e, ci)
	case types.CallBuiltin:
		return f.builtin(e, ci)
	case types.CallConversion:
		return f.convType(e, ci.Conv) + "(" + f.expr(e.Args[0]) + ")"
	case types.CallVariant:
		return f.composite(f.g.variantLit(e, ci.VariantType, ci.Variant, len(e.Args), func(i int) string {
			return f.expr(e.Args[i])
		}, f.env))
	case types.CallValue:
		return f.primary(e.Fun) + "(" + f.args(e.Args) + ")"
	}
	f.g.fail(e, "unsupported call (%s)", ci.Kind)
	return ""
}

func (f *fnCtx) builtinMethod(e *ast.CallExpr, ci *types.Call) string {
	recv := f.primary(ci.Recv)
	switch ci.Method.Builtin {
	case types.ScopeAcquireSlot:
		var pool string
		if u, ok := unparen(e.Args[1]).(*ast.UnaryExpr); ok && u.Op == ast.OpAddr {
			pool = f.primary(u.X) + "[:]"
		} else {
			pool = f.primary(e.Args[1]) + "[:]"
		}
		return f.g.rt("AcquireSlot") + "(" + recv + ", " + f.expr(e.Args[0]) + ", " + pool + ")"
	case types.ScopeReleaseSlot:
		return f.g.rt("ReleaseSlot") + "(" + recv + ", " + f.expr(e.Args[0]) + ")"
	case types.ScopeGo:
		return f.g.rt("Go") + "(" + recv + ", " + f.expr(e.Args[0]) + ", " + f.expr(e.Args[1]) + ")"
	}
	return recv + "." + ci.Method.Name + "(" + f.args(e.Args) + ")"
}

func (f *fnCtx) builtin(e *ast.CallExpr, ci *types.Call) string {
	switch ci.Builtin {
	case resolve.FLen:
		return "uint(len(" + f.expr(e.Args[0]) + "))"
	case resolve.FCap:
		return "uint(cap(" + f.expr(e.Args[0]) + "))"
	case resolve.FPanic:
		return "panic(" + f.args(e.Args) + ")"
	case resolve.FFormatTo:
		return f.formatTo(e, ci)
	case resolve.FCopy, resolve.FBytesEqual, resolve.FStrBytes, resolve.FBytesToStr, resolve.FFormatUint,
		resolve.FParseUint, resolve.FNewContext, resolve.FNewScope:
		return f.g.rt(ci.Builtin.GoName()) + "(" + f.args(e.Args) + ")"
	}
	f.g.fail(e, "builtin %s cannot be called at run time", ci.Builtin)
	return ""
}

// formatTo expands a formatTo call (v0.2) into an immediately called
// function literal over fincrt.Fmt. All arguments are evaluated left to
// right (as the literal's arguments) before anything is written:
//
//	func(_fc_d []byte, _fc_a0 int64, ...) uint { _fc_w := fincrt.NewFmt(_fc_d); _fc_w.Lit("x="); _fc_w.Int(_fc_a0, 0, false); return _fc_w.N() }(dst, int64(x), ...)
//
// The text is produced in three passes (parameters, body, arguments) in
// output order, so that finc1 can stream it; Go types and expressions are
// rendered in that order (it fixes the discovery order of monomorphized
// instances).
func (f *fnCtx) formatTo(e *ast.CallExpr, ci *types.Call) string {
	fi := ci.Format
	if fi == nil || len(e.Args) != len(fi.Args)+2 {
		f.g.fail(e, "formatTo without format information")
	}
	var b strings.Builder
	b.WriteString("func(_fc_d []byte")
	for k, fa := range fi.Args {
		b.WriteString(", _fc_a" + strconv.Itoa(k) + " ")
		if fa.Op == types.FmtFormat {
			a := e.Args[2+k]
			b.WriteString(f.goType(a, f.typeOf(a)))
		} else {
			b.WriteString(formatParamType(fa.Op))
		}
	}
	b.WriteString(") uint { _fc_w := " + f.g.rt("NewFmt") + "(_fc_d); ")
	k := 0
	for _, p := range fi.Pieces {
		if p.Verb == 0 {
			b.WriteString("_fc_w.Lit(" + strconv.Quote(p.Lit) + "); ")
			continue
		}
		fa := fi.Args[k]
		name := "_fc_a" + strconv.Itoa(k)
		k++
		w := strconv.Itoa(p.FormatWidth())
		zero := "false"
		if p.Zero {
			zero = "true"
		}
		switch fa.Op {
		case types.FmtInt:
			b.WriteString("_fc_w.Int(" + name + ", " + w + ", " + zero + ")")
		case types.FmtUint:
			b.WriteString("_fc_w.Uint(" + name + ", " + w + ", " + zero + ")")
		case types.FmtHexInt:
			b.WriteString("_fc_w.HexInt(" + name + ", " + w + ", " + zero + ")")
		case types.FmtHexUint:
			b.WriteString("_fc_w.Hex(" + name + ", " + w + ", " + zero + ")")
		case types.FmtStr:
			b.WriteString("_fc_w.Str(" + name + ", " + w + ")")
		case types.FmtBytes:
			b.WriteString("_fc_w.Bytes(" + name + ", " + w + ")")
		case types.FmtQuoteStr:
			b.WriteString("_fc_w.Quote(" + name + ", " + w + ")")
		case types.FmtQuoteBytes:
			b.WriteString("_fc_w.QuoteBytes(" + name + ", " + w + ")")
		case types.FmtByte:
			b.WriteString("_fc_w.Byte(" + name + ", " + w + ")")
		case types.FmtBool:
			b.WriteString("_fc_w.Bool(" + name + ", " + w + ")")
		case types.FmtFormat:
			b.WriteString("_fc_w.Advance(" + name + "." + f.g.p.Name(fa.Method.Sym) + "(_fc_w.Dst(" + w + ")), " + w + ")")
		default:
			f.g.fail(e, "formatTo argument without a format operation")
		}
		b.WriteString("; ")
	}
	b.WriteString("return _fc_w.N() }(" + f.expr(e.Args[0]))
	for k, fa := range fi.Args {
		a := e.Args[2+k]
		if fa.Op == types.FmtFormat {
			b.WriteString(", " + f.expr(a))
		} else {
			b.WriteString(", " + formatParamType(fa.Op) + "(" + f.expr(a) + ")")
		}
	}
	b.WriteString(")")
	return b.String()
}

// formatParamType is the Go parameter type of a formatTo argument (also
// used as its conversion).
func formatParamType(op types.FormatOp) string {
	switch op {
	case types.FmtInt, types.FmtHexInt:
		return "int64"
	case types.FmtUint, types.FmtHexUint:
		return "uint64"
	case types.FmtStr, types.FmtQuoteStr:
		return "string"
	case types.FmtBytes, types.FmtQuoteBytes:
		return "[]byte"
	case types.FmtByte:
		return "byte"
	case types.FmtBool:
		return "bool"
	}
	return "_"
}
