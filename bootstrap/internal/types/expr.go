package types

import (
	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
)

// operand is the result of checking an expression.
type operand struct {
	mode ExprKind // KindNone = invalid
	typ  Type
	val  Value
	expr ast.Expr
	sym  *resolve.Symbol // KindFunc, KindBuiltin
	// method selections (mode KindFunc, method != nil)
	method   *Method
	recv     ast.Expr
	recvType Type
	indirect bool
	// addressable: the receiver operand is addressable (KindVar), so a
	// pointer-receiver method may be called on it (auto-&, v0.2)
	addressable bool
	// KindVariantCtor
	variant *Variant
}

func (x *operand) invalid() bool { return x.mode == KindNone }

func invalidOp(e ast.Expr) operand { return operand{mode: KindNone, typ: Typ[Invalid], expr: e} }

func (c *Checker) record(x *operand) {
	if x.expr == nil {
		return
	}
	id := x.expr.ID()
	c.info.Types[id] = x.typ
	if x.mode == KindBuiltin || x.mode == KindPackage {
		c.info.Types[id] = nil
	}
	c.info.Kinds[id] = x.mode
	if x.mode == KindConst {
		c.info.Values[id] = x.val
	}
}

// rawExpr checks e and records its type; the result may be untyped, a
// type, a builtin, a tuple, ...
func (c *Checker) rawExpr(e ast.Expr) operand {
	x := c.expr0(e)
	x.expr = e
	switch x.mode {
	case KindValue, KindVar, KindConst, KindType:
		if x.typ == nil || IsInvalid(x.typ) {
			x.mode = KindNone
		}
	}
	if x.mode == KindNone {
		x.typ = Typ[Invalid]
	}
	c.record(&x)
	return x
}

// exprValue checks that e denotes a single value.
func (c *Checker) exprValue(e ast.Expr) operand {
	x := c.rawExpr(e)
	c.singleValue(&x)
	return x
}

func (c *Checker) singleValue(x *operand) {
	switch x.mode {
	case KindNone:
		return
	case KindValue, KindVar, KindConst:
		if t, ok := x.typ.(*Tuple); ok {
			c.errorf(diag.E2003, x.expr.Span(), "multiple-value %s (%d values) in single-value context", exprString(x.expr), len(t.Types))
			x.mode = KindNone
		}
		return
	case KindNoValue:
		c.errorf(diag.E2002, x.expr.Span(), "%s (no value) used as value", exprString(x.expr))
	case KindType:
		c.errorf(diag.E2002, x.expr.Span(), "type %s is not an expression", x.typ)
	case KindBuiltin:
		c.errorf(diag.E2002, x.expr.Span(), "builtin %s must be called", exprString(x.expr))
	case KindFunc:
		if x.method != nil {
			c.errorf(diag.E2002, x.expr.Span(), "method value %s is not supported; call the method", exprString(x.expr))
		} else if sig, ok := x.typ.(*Signature); ok && sig.TypeParams != nil {
			c.errorf(diag.E2002, x.expr.Span(), "generic function %s must be instantiated with explicit type arguments", exprString(x.expr))
		} else if ok && sig.Effect == Comptime {
			c.errorf(diag.E2002, x.expr.Span(), "comptime function %s can only be called", exprString(x.expr))
		} else {
			x.mode = KindValue // function value
			return
		}
	case KindPackage:
		c.errorf(diag.E2002, x.expr.Span(), "package %s is not an expression", exprString(x.expr))
	case KindVariantCtor:
		c.errorf(diag.E2003, x.expr.Span(), "variant %s requires %d payload values", exprString(x.expr), len(x.variant.Payload))
	}
	x.mode = KindNone
	x.typ = Typ[Invalid]
}

// exprWith checks e and assigns it to type t (converting untyped values).
func (c *Checker) exprWith(e ast.Expr, t Type, context string) operand {
	x := c.exprValue(e)
	if !x.invalid() {
		c.assignment(&x, t, context)
	}
	return x
}

// ---------------------------------------------------------------------------
// Assignability

// assignment checks that x can be assigned to a variable of type t,
// converting untyped constants (and level-polymorphic codes). It reports
// E2004 for nil, E2010 for Code level mismatches and E2002 otherwise.
func (c *Checker) assignment(x *operand, t Type, context string) bool {
	if x.invalid() || IsInvalid(t) {
		return true
	}
	if IsUntyped(x.typ) {
		return c.convertUntyped(x, t)
	}
	if cv, ok := x.typ.(*Code); ok {
		if ct, ok := t.Underlying().(*Code); ok {
			if cv.Level < 0 {
				x.typ = t
				c.updateType(x.expr, t)
				return true
			}
			if ct.Level >= 0 && ct.Level != cv.Level {
				c.errorf(diag.E2010, x.expr.Span(), "universe level mismatch: cannot use %s (Code %d) as Code %d in %s", exprString(x.expr), cv.Level, ct.Level, context)
				return false
			}
		}
	}
	if c.assignable(x.typ, t) {
		return true
	}
	c.errorf(diag.E2002, x.expr.Span(), "cannot use %s (type %s) as type %s in %s", exprString(x.expr), x.typ, t, context)
	return false
}

// assignable reports whether a value of type v may be assigned to t.
func (c *Checker) assignable(v, t Type) bool {
	if Identical(v, t) {
		return true
	}
	_, vn := v.(*Named)
	_, tn := t.(*Named)
	if !vn || !tn {
		vu, tu := v.Underlying(), t.Underlying()
		if _, isTP := vu.(*TypeParam); !isTP && Identical(vu, tu) {
			if _, ok := vu.(*Opaque); !ok {
				return true
			}
		}
		// A function of a lower effect may be used where a higher one is
		// expected (total func where a driver func is expected).
		if vs, ok := vu.(*Signature); ok {
			if ts, ok := tu.(*Signature); ok && vs.Effect <= ts.Effect && vs.Variadic == ts.Variadic &&
				identicalList(vs.Params, ts.Params) && identicalList(vs.Results, ts.Results) {
				return true
			}
		}
	}
	return false
}

// convertUntyped converts an untyped operand to type t (reporting E2004 /
// E2002 on failure).
func (c *Checker) convertUntyped(x *operand, t Type) bool {
	if !IsUntyped(x.typ) || IsInvalid(t) {
		return true
	}
	k := basicKind(x.typ)
	ok := false
	switch k {
	case UntypedNil:
		u := t.Underlying()
		if _, isSlice := u.(*Slice); isSlice || basicKind(t) == Error {
			ok = true
			break
		}
		_, isPtr := u.(*Pointer)
		if isPtr || IsScoped(t) {
			c.errorf(diag.E2004, x.expr.Span(), "cannot use nil as %s value: nil is only valid for error and slices", t)
		} else {
			c.errorf(diag.E2002, x.expr.Span(), "cannot use nil as %s value", t)
		}
		return false
	case UntypedInt, UntypedChar:
		if IsInteger(t) && !IsUntyped(t) {
			if x.val != nil && !Representable(x.val, t) {
				c.errorf(diag.E2002, x.expr.Span(), "constant %s overflows %s", x.val, t)
				return false
			}
			ok = true
		} else if IsUntyped(t) && IsInteger(t) {
			ok = true
		}
	case UntypedBool:
		ok = IsBoolean(t)
	case UntypedString:
		ok = IsString(t)
		if !ok && IsByteSlice(t) && x.expr != nil {
			// v0.2 ergonomics: a constant string used as a []byte is an
			// implicit strBytes (a fresh copy). The constant itself is
			// typed string; the node is marked in Info.ImplicitBytes.
			if !c.convertUntyped(x, Typ[String]) {
				return false
			}
			c.info.ImplicitBytes[x.expr.ID()] = true
			x.mode, x.typ, x.val = KindValue, t, nil
			return true
		}
	}
	if _, isTP := t.(*TypeParam); isTP {
		ok = false
	}
	if !ok {
		c.errorf(diag.E2002, x.expr.Span(), "cannot use %s (%s constant) as type %s", exprString(x.expr), x.typ, t)
		return false
	}
	x.typ = t
	c.updateType(x.expr, t)
	return true
}

// defaultType gives an untyped operand its default type (for `:=` and
// var declarations without type) and returns it.
func (c *Checker) defaultType(x *operand, context string) Type {
	if x.invalid() {
		return Typ[Invalid]
	}
	if basicKind(x.typ) == UntypedNil {
		c.errorf(diag.E2004, x.expr.Span(), "use of untyped nil in %s", context)
		return Typ[Invalid]
	}
	if IsUntyped(x.typ) || isPolyCode(x.typ) {
		d := Default(x.typ)
		if IsUntyped(x.typ) {
			c.convertUntyped(x, d)
		} else {
			x.typ = d
			c.updateType(x.expr, d)
		}
		return d
	}
	return x.typ
}

func isPolyCode(t Type) bool {
	cd, ok := t.(*Code)
	return ok && cd.Level < 0
}

// updateType records the final type of an untyped expression tree.
func (c *Checker) updateType(e ast.Expr, t Type) {
	if e == nil {
		return
	}
	old := c.info.Types[e.ID()]
	if old != nil && !IsUntyped(old) && !isPolyCode(old) {
		return
	}
	c.info.Types[e.ID()] = t
	switch e := e.(type) {
	case *ast.ParenExpr:
		c.updateType(e.X, t)
	case *ast.MatchExpr:
		c.convertMatchArms(e, t)
	case *ast.UnaryExpr:
		if e.Op == ast.OpNeg || e.Op == ast.OpNot {
			c.updateType(e.X, t)
		}
	case *ast.BinaryExpr:
		switch e.Op {
		case ast.OpEq, ast.OpNe, ast.OpLt, ast.OpLe, ast.OpGt, ast.OpGe:
			// operands keep their own (default) types
		case ast.OpShl, ast.OpShr:
			c.updateType(e.X, t)
		default:
			c.updateType(e.X, t)
			c.updateType(e.Y, t)
		}
	}
}

// ---------------------------------------------------------------------------
// Expressions

func (c *Checker) expr0(e ast.Expr) operand {
	switch e := e.(type) {
	case *ast.Ident:
		return c.ident(e)
	case *ast.BasicLit:
		switch e.Kind {
		case ast.IntLit:
			return operand{mode: KindConst, typ: Typ[UntypedInt], val: MakeUint(e.Int)}
		case ast.CharLit:
			return operand{mode: KindConst, typ: Typ[UntypedChar], val: MakeUint(e.Int)}
		default:
			return operand{mode: KindConst, typ: Typ[UntypedString], val: StringVal(e.Str)}
		}
	case *ast.BoolLit:
		return operand{mode: KindConst, typ: Typ[UntypedBool], val: BoolVal(e.Value)}
	case *ast.NilLit:
		return operand{mode: KindConst, typ: Typ[UntypedNil], val: NilVal{}}
	case *ast.ParenExpr:
		x := c.rawExpr(e.X)
		x.expr = e
		return x
	case *ast.BinaryExpr:
		return c.binary(e)
	case *ast.UnaryExpr:
		return c.unary(e)
	case *ast.CallExpr:
		return c.call(e)
	case *ast.IndexExpr:
		return c.index(e)
	case *ast.SliceExpr:
		return c.sliceExpr(e)
	case *ast.SelectorExpr:
		return c.selector(e)
	case *ast.FuncLit:
		return c.funcLit(e)
	case *ast.StructLit:
		return c.structLit(e)
	case *ast.MatchExpr:
		return c.matchExpr(e)
	case *ast.IsExpr:
		return c.isExpr(e)
	case *ast.TupleExpr:
		c.errorf(diag.E2002, e.Range, "tuple %s can only be the scrutinee of a match", exprString(e))
		return invalidOp(e)
	case *ast.TypeExpr:
		t := c.typExpr(e.Type)
		if IsInvalid(t) {
			return invalidOp(e)
		}
		return operand{mode: KindType, typ: t}
	}
	return invalidOp(e)
}

func (c *Checker) ident(e *ast.Ident) operand {
	sym := c.prog.Uses[e.ID()]
	if sym == nil {
		return invalidOp(e) // reported by the resolver
	}
	return c.symOperand(sym, e)
}

func (c *Checker) symOperand(sym *resolve.Symbol, id *ast.Ident) operand {
	switch sym.Kind {
	case resolve.SymConst:
		t := c.symType(sym)
		v := c.info.SymValues[sym.ID]
		if IsInvalid(t) || v == nil {
			return invalidOp(id)
		}
		return operand{mode: KindConst, typ: t, val: v}
	case resolve.SymVar:
		t := c.symType(sym)
		if t == nil || IsInvalid(t) {
			return invalidOp(id)
		}
		if c.constCtx > 0 && (sym.IsGlobal() || c.fn == nil || c.fn.Effect != Comptime) {
			// reading a variable is fine; the operand is simply not constant
		}
		return operand{mode: KindVar, typ: t}
	case resolve.SymType:
		t := c.typeSym(sym, id)
		if IsInvalid(t) {
			return invalidOp(id)
		}
		return operand{mode: KindType, typ: t}
	case resolve.SymTypeParam:
		tp, _ := c.symType(sym).(*TypeParam)
		if tp == nil {
			return invalidOp(id)
		}
		if tp.IsConst() {
			return operand{mode: KindValue, typ: tp.Const}
		}
		return operand{mode: KindType, typ: tp}
	case resolve.SymFunc:
		t := c.symType(sym)
		if IsInvalid(t) || t == nil {
			return invalidOp(id)
		}
		return operand{mode: KindFunc, typ: t, sym: sym}
	case resolve.SymBuiltin:
		return operand{mode: KindBuiltin, typ: Typ[Invalid], sym: sym}
	case resolve.SymPackage:
		return operand{mode: KindPackage, typ: Typ[Invalid], sym: sym}
	}
	return invalidOp(id)
}

// isTypeParamOperand reports whether t is a (non-const) type parameter.
func isTypeParam(t Type) bool {
	_, ok := t.(*TypeParam)
	return ok
}

func (c *Checker) binary(e *ast.BinaryExpr) operand {
	x := c.exprValue(e.X)
	y := c.exprValue(e.Y)
	if x.invalid() || y.invalid() {
		return invalidOp(e)
	}
	if isTypeParam(x.typ) || isTypeParam(y.typ) {
		c.errorf(diag.E2011, e.OpPos, "operator %s inspects a value of type parameter type (%s, %s)", e.Op, x.typ, y.typ)
		return invalidOp(e)
	}
	switch e.Op {
	case ast.OpShl, ast.OpShr:
		return c.shift(e, x, y)
	case ast.OpEq, ast.OpNe, ast.OpLt, ast.OpLe, ast.OpGt, ast.OpGe:
		return c.comparison(e, x, y)
	}
	if !c.matchTypes(e, &x, &y) {
		return invalidOp(e)
	}
	t := x.typ
	if e.Op == ast.OpAdd && IsString(t) {
		return c.concat(e, x, y)
	}
	switch e.Op {
	case ast.OpLAnd, ast.OpLOr:
		if !IsBoolean(t) {
			c.errorf(diag.E2002, e.OpPos, "operator %s not defined on %s (type %s)", e.Op, exprString(e.X), t)
			return invalidOp(e)
		}
	default:
		if !IsInteger(t) {
			c.errorf(diag.E2002, e.OpPos, "operator %s not defined on %s (type %s)", e.Op, exprString(e.X), t)
			return invalidOp(e)
		}
	}
	if (e.Op == ast.OpDiv || e.Op == ast.OpRem) && y.mode == KindConst {
		if iv, ok := y.val.(IntVal); ok && iv.V.Sign() == 0 {
			c.errorf(diag.E2002, e.Y.Span(), "division by zero")
			return invalidOp(e)
		}
	}
	if x.mode == KindConst && y.mode == KindConst {
		v, err := BinaryOp(e.Op, x.val, y.val)
		if err != nil {
			c.errorf(diag.E2002, e.OpPos, "constant evaluation: %v", err)
			return invalidOp(e)
		}
		if !IsUntyped(t) && !Representable(v, t) {
			c.errorf(diag.E2002, e.Span(), "constant %s overflows %s", v, t)
			return invalidOp(e)
		}
		return operand{mode: KindConst, typ: t, val: v}
	}
	return operand{mode: KindValue, typ: t}
}

// concat checks `x + y` on strings (v0.2 ergonomics): both operands must
// be constants and the result is a constant; there is no run-time string
// building.
func (c *Checker) concat(e *ast.BinaryExpr, x, y operand) operand {
	for _, o := range []operand{x, y} {
		if o.mode != KindConst {
			c.errorf(diag.E2002, e.OpPos, "invalid operation: %s is not a constant (string concatenation is only allowed for constants)", exprString(o.expr))
			return invalidOp(e)
		}
	}
	v, err := BinaryOp(e.Op, x.val, y.val)
	if err != nil {
		c.errorf(diag.E2002, e.OpPos, "constant evaluation: %v", err)
		return invalidOp(e)
	}
	return operand{mode: KindConst, typ: x.typ, val: v}
}

// matchTypes converts untyped operands to the other operand's type and
// requires identical types.
func (c *Checker) matchTypes(e *ast.BinaryExpr, x, y *operand) bool {
	xu, yu := IsUntyped(x.typ), IsUntyped(y.typ)
	switch {
	case xu && !yu:
		if !c.convertUntyped(x, y.typ) {
			return false
		}
	case yu && !xu:
		if !c.convertUntyped(y, x.typ) {
			return false
		}
	case xu && yu:
		kx, ky := basicKind(x.typ), basicKind(y.typ)
		if kx != ky {
			if (kx == UntypedInt || kx == UntypedChar) && (ky == UntypedInt || ky == UntypedChar) {
				x.typ, y.typ = Typ[UntypedChar], Typ[UntypedChar]
				return true
			}
			if kx == UntypedNil || ky == UntypedNil {
				c.errorf(diag.E2004, e.OpPos, "invalid operation: operator %s on untyped nil", e.Op)
				return false
			}
			c.errorf(diag.E2002, e.OpPos, "mismatched types %s and %s", x.typ, y.typ)
			return false
		}
		return true
	}
	if !Identical(x.typ, y.typ) {
		c.errorf(diag.E2002, e.OpPos, "mismatched types %s and %s for operator %s", x.typ, y.typ, e.Op)
		return false
	}
	return true
}

func (c *Checker) shift(e *ast.BinaryExpr, x, y operand) operand {
	if !IsInteger(y.typ) {
		c.errorf(diag.E2002, e.Y.Span(), "shift count %s must be an integer", exprString(e.Y))
		return invalidOp(e)
	}
	if IsUntyped(y.typ) {
		if iv, ok := y.val.(IntVal); ok && iv.V.Sign() < 0 {
			c.errorf(diag.E2002, e.Y.Span(), "negative shift count %s", y.val)
			return invalidOp(e)
		}
		c.convertUntyped(&y, Typ[Usize])
	}
	if !IsInteger(x.typ) {
		c.errorf(diag.E2002, e.X.Span(), "shifted operand %s must be an integer", exprString(e.X))
		return invalidOp(e)
	}
	if x.mode == KindConst && y.mode == KindConst {
		v, err := BinaryOp(e.Op, x.val, y.val)
		if err != nil {
			c.errorf(diag.E2002, e.OpPos, "constant evaluation: %v", err)
			return invalidOp(e)
		}
		if !IsUntyped(x.typ) && !Representable(v, x.typ) {
			c.errorf(diag.E2002, e.Span(), "constant %s overflows %s", v, x.typ)
			return invalidOp(e)
		}
		return operand{mode: KindConst, typ: x.typ, val: v}
	}
	if IsUntyped(x.typ) {
		c.convertUntyped(&x, Default(x.typ))
	}
	return operand{mode: KindValue, typ: x.typ}
}

func (c *Checker) comparison(e *ast.BinaryExpr, x, y operand) operand {
	xn, yn := basicKind(x.typ) == UntypedNil, basicKind(y.typ) == UntypedNil
	switch {
	case xn && yn:
		c.errorf(diag.E2002, e.OpPos, "invalid operation: nil %s nil", e.Op)
		return invalidOp(e)
	case xn || yn:
		other, nilOp := &y, &x
		if yn {
			other, nilOp = &x, &y
		}
		if !c.convertUntyped(nilOp, other.typ) {
			return invalidOp(e)
		}
		if e.Op != ast.OpEq && e.Op != ast.OpNe {
			c.errorf(diag.E2002, e.OpPos, "operator %s not defined on nil", e.Op)
			return invalidOp(e)
		}
		return operand{mode: KindValue, typ: Typ[Bool]}
	}
	if !c.matchTypes(e, &x, &y) {
		return invalidOp(e)
	}
	t := x.typ
	switch e.Op {
	case ast.OpEq, ast.OpNe:
		if !Comparable(t) {
			c.errorf(diag.E2002, e.OpPos, "operator %s not defined on %s (type %s is not comparable)", e.Op, exprString(e.X), t)
			return invalidOp(e)
		}
	default:
		if !IsInteger(t) && !IsString(t) {
			c.errorf(diag.E2002, e.OpPos, "operator %s not defined on %s (type %s is not ordered)", e.Op, exprString(e.X), t)
			return invalidOp(e)
		}
	}
	if x.mode == KindConst && y.mode == KindConst {
		v, err := BinaryOp(e.Op, x.val, y.val)
		if err != nil {
			c.errorf(diag.E2002, e.OpPos, "constant evaluation: %v", err)
			return invalidOp(e)
		}
		return operand{mode: KindConst, typ: Typ[UntypedBool], val: v}
	}
	if IsUntyped(x.typ) {
		c.convertUntyped(&x, Default(x.typ))
		c.convertUntyped(&y, Default(y.typ))
	}
	return operand{mode: KindValue, typ: Typ[Bool]}
}

func (c *Checker) unary(e *ast.UnaryExpr) operand {
	switch e.Op {
	case ast.OpAddr:
		x := c.exprValue(e.X)
		if x.invalid() {
			return invalidOp(e)
		}
		if x.mode != KindVar {
			c.errorf(diag.E2002, e.Span(), "cannot take the address of %s", exprString(e.X))
			return invalidOp(e)
		}
		return operand{mode: KindValue, typ: &Pointer{Elem: x.typ}}
	case ast.OpDeref:
		x := c.rawExpr(e.X)
		if x.mode == KindType {
			return operand{mode: KindType, typ: &Pointer{Elem: x.typ}}
		}
		c.singleValue(&x)
		if x.invalid() {
			return invalidOp(e)
		}
		p, ok := x.typ.Underlying().(*Pointer)
		if !ok {
			c.errorf(diag.E2002, e.Span(), "cannot dereference %s (type %s is not a pointer)", exprString(e.X), x.typ)
			return invalidOp(e)
		}
		return operand{mode: KindVar, typ: p.Elem}
	}
	x := c.exprValue(e.X)
	if x.invalid() {
		return invalidOp(e)
	}
	if isTypeParam(x.typ) {
		c.errorf(diag.E2011, e.Span(), "operator %s inspects a value of type parameter type %s", e.Op, x.typ)
		return invalidOp(e)
	}
	switch e.Op {
	case ast.OpNot:
		if !IsBoolean(x.typ) {
			c.errorf(diag.E2002, e.Span(), "operator ! not defined on %s (type %s)", exprString(e.X), x.typ)
			return invalidOp(e)
		}
	case ast.OpNeg:
		if !IsInteger(x.typ) {
			c.errorf(diag.E2002, e.Span(), "operator - not defined on %s (type %s)", exprString(e.X), x.typ)
			return invalidOp(e)
		}
	}
	if x.mode == KindConst {
		v, err := UnaryOp(e.Op, x.val)
		if err != nil {
			c.errorf(diag.E2002, e.Span(), "constant evaluation: %v", err)
			return invalidOp(e)
		}
		if !IsUntyped(x.typ) && !Representable(v, x.typ) {
			c.errorf(diag.E2002, e.Span(), "constant %s overflows %s", v, x.typ)
			return invalidOp(e)
		}
		return operand{mode: KindConst, typ: x.typ, val: v}
	}
	return operand{mode: KindValue, typ: x.typ}
}

// indexArg checks an index expression (any integer type; untyped
// constants become usize) and returns its constant value if known.
func (c *Checker) indexArg(e ast.Expr) (Value, bool) {
	x := c.exprValue(e)
	if x.invalid() {
		return nil, false
	}
	if !IsInteger(x.typ) {
		c.errorf(diag.E2002, e.Span(), "index %s must be an integer (have %s)", exprString(e), x.typ)
		return nil, false
	}
	if x.mode == KindConst {
		if iv, ok := x.val.(IntVal); ok && iv.V.Sign() < 0 {
			c.errorf(diag.E4003, e.Span(), "index %s must not be negative", x.val)
			return nil, false
		}
	}
	if IsUntyped(x.typ) && !c.convertUntyped(&x, Typ[Usize]) {
		return nil, false
	}
	if x.mode == KindConst {
		return x.val, true
	}
	return nil, true
}

func (c *Checker) index(e *ast.IndexExpr) operand {
	x := c.rawExpr(e.X)
	switch x.mode {
	case KindNone:
		for _, i := range e.Indices {
			c.rawExpr(i)
		}
		return invalidOp(e)
	case KindFunc:
		if x.method == nil {
			return c.funcInstance(e, x)
		}
	case KindType:
		n, ok := x.typ.(*Named)
		if !ok || !n.IsGeneric() {
			c.errorf(diag.E2002, e.Span(), "%s is not a generic type", x.typ)
			return invalidOp(e)
		}
		t := c.instantiateArgs(n, e.Indices, e, e.Range)
		if IsInvalid(t) {
			return invalidOp(e)
		}
		return operand{mode: KindType, typ: t}
	}
	c.singleValue(&x)
	if x.invalid() {
		return invalidOp(e)
	}
	if len(e.Indices) != 1 {
		c.errorf(diag.E2003, e.Span(), "%s cannot be indexed with %d indices", exprString(e.X), len(e.Indices))
		return invalidOp(e)
	}
	idx, ok := c.indexArg(e.Indices[0])
	if !ok {
		return invalidOp(e)
	}
	t := x.typ
	mode := x.mode
	if p, ok := t.Underlying().(*Pointer); ok {
		if _, isArr := p.Elem.Underlying().(*Array); isArr {
			t, mode = p.Elem, KindVar
		}
	}
	switch u := t.Underlying().(type) {
	case *Array:
		if idx != nil && u.LenParam == nil {
			if n, _ := Uint64(idx); n >= u.Len {
				c.errorf(diag.E4003, e.Indices[0].Span(), "index %s out of bounds for array of length %d", idx, u.Len)
				return invalidOp(e)
			}
		}
		if mode != KindVar {
			mode = KindValue
		}
		return operand{mode: mode, typ: u.Elem}
	case *Slice:
		return operand{mode: KindVar, typ: u.Elem}
	case *Basic:
		if IsString(u) {
			if x.mode == KindConst && idx != nil {
				s := string(x.val.(StringVal))
				n, _ := Uint64(idx)
				if n >= uint64(len(s)) {
					c.errorf(diag.E4003, e.Indices[0].Span(), "index %s out of bounds for string of length %d", idx, len(s))
					return invalidOp(e)
				}
				return operand{mode: KindConst, typ: Typ[U8], val: MakeUint(uint64(s[n]))}
			}
			return operand{mode: KindValue, typ: Typ[U8]}
		}
	}
	c.errorf(diag.E2002, e.Span(), "cannot index %s (type %s)", exprString(e.X), x.typ)
	return invalidOp(e)
}

func (c *Checker) sliceExpr(e *ast.SliceExpr) operand {
	x := c.exprValue(e.X)
	var lo, hi Value
	okAll := !x.invalid()
	if e.Lo != nil {
		v, ok := c.indexArg(e.Lo)
		lo, okAll = v, okAll && ok
	}
	if e.Hi != nil {
		v, ok := c.indexArg(e.Hi)
		hi, okAll = v, okAll && ok
	}
	if !okAll {
		return invalidOp(e)
	}
	if lo != nil && hi != nil {
		l, _ := Uint64(lo)
		h, _ := Uint64(hi)
		if l > h {
			c.errorf(diag.E4003, e.Span(), "invalid slice indices: %d > %d", l, h)
			return invalidOp(e)
		}
	}
	t := x.typ
	addressable := x.mode == KindVar
	if p, ok := t.Underlying().(*Pointer); ok {
		if _, isArr := p.Elem.Underlying().(*Array); isArr {
			t, addressable = p.Elem, true
		}
	}
	switch u := t.Underlying().(type) {
	case *Array:
		if !addressable {
			c.errorf(diag.E2002, e.Span(), "cannot slice %s (value not addressable)", exprString(e.X))
			return invalidOp(e)
		}
		if u.LenParam == nil {
			for _, b := range []Value{lo, hi} {
				if n, ok := Uint64(b); ok && n > u.Len {
					c.errorf(diag.E4003, e.Span(), "slice bound %d out of range for array of length %d", n, u.Len)
					return invalidOp(e)
				}
			}
		}
		return operand{mode: KindValue, typ: &Slice{Elem: u.Elem}}
	case *Slice:
		return operand{mode: KindValue, typ: t}
	case *Basic:
		if IsString(u) {
			if IsUntyped(x.typ) {
				c.convertUntyped(&x, Typ[String])
				t = Typ[String]
			}
			return operand{mode: KindValue, typ: t}
		}
	}
	c.errorf(diag.E2002, e.Span(), "cannot slice %s (type %s)", exprString(e.X), x.typ)
	return invalidOp(e)
}

func (c *Checker) selector(e *ast.SelectorExpr) operand {
	// Package-qualified identifier (resolved by the resolver).
	if sym := c.prog.Uses[e.Sel.ID()]; sym != nil {
		if id, ok := e.X.(*ast.Ident); ok {
			c.info.Kinds[id.ID()] = KindPackage
		}
		x := c.symOperand(sym, e.Sel)
		x.expr = e.Sel
		c.record(&x)
		return x
	}
	x := c.rawExpr(e.X)
	switch x.mode {
	case KindNone, KindPackage:
		return invalidOp(e)
	case KindType:
		en, ok := x.typ.Underlying().(*Enum)
		if !ok {
			c.errorf(diag.E2001, e.Sel.Range, "%s.%s undefined (type %s is not an enum; method expressions are not supported)", exprString(e.X), e.Sel.Name, x.typ)
			return invalidOp(e)
		}
		v := en.VariantByName(e.Sel.Name)
		if v == nil {
			c.errorf(diag.E2001, e.Sel.Range, "%s.%s undefined (enum %s has no variant %s)", exprString(e.X), e.Sel.Name, x.typ, e.Sel.Name)
			return invalidOp(e)
		}
		c.info.Selections[e.ID()] = &Selection{Kind: SelVariant, Recv: x.typ, Variant: v}
		if len(v.Payload) == 0 {
			return operand{mode: KindConst, typ: x.typ, val: &EnumVal{Variant: v.Index}}
		}
		return operand{mode: KindVariantCtor, typ: x.typ, variant: v}
	}
	c.singleValue(&x)
	if x.invalid() {
		return invalidOp(e)
	}
	t, indirect := x.typ, false
	if p, ok := t.Underlying().(*Pointer); ok {
		t, indirect = p.Elem, true
	}
	if st, ok := t.Underlying().(*Struct); ok {
		if f := st.FieldByName(e.Sel.Name); f != nil {
			c.info.Selections[e.ID()] = &Selection{Kind: SelField, Recv: x.typ, Indirect: indirect, Field: f}
			c.info.Types[e.Sel.ID()] = f.Type
			mode := KindValue
			if x.mode == KindVar || indirect {
				mode = KindVar
			}
			return operand{mode: mode, typ: f.Type}
		}
	}
	if n, ok := t.(*Named); ok {
		if m := c.lookupMethod(n, e.Sel.Name); m != nil {
			c.info.Selections[e.ID()] = &Selection{Kind: SelMethod, Recv: x.typ, Indirect: indirect && !m.PtrRecv, Method: m}
			return operand{mode: KindFunc, typ: Typ[Invalid], method: m, recv: e.X, recvType: x.typ, indirect: indirect, addressable: x.mode == KindVar}
		}
	}
	c.errorf(diag.E2001, e.Sel.Range, "%s.%s undefined (type %s has no field or method %s)", exprString(e.X), e.Sel.Name, x.typ, e.Sel.Name)
	return invalidOp(e)
}

func (c *Checker) structLit(e *ast.StructLit) operand {
	t := c.typExpr(e.Type)
	if IsInvalid(t) {
		for _, f := range e.Fields {
			c.rawExpr(f.Value)
		}
		return invalidOp(e)
	}
	st, ok := t.Underlying().(*Struct)
	if !ok {
		c.errorf(diag.E2002, e.Type.Range, "invalid composite literal type %s (not a struct)", t)
		return invalidOp(e)
	}
	set := map[string]bool{}
	allConst := true
	for _, fi := range e.Fields {
		f := st.FieldByName(fi.Name.Name)
		if f == nil {
			c.errorf(diag.E2001, fi.Name.Range, "unknown field %s in struct literal of type %s", fi.Name.Name, t)
			c.rawExpr(fi.Value)
			allConst = false
			continue
		}
		if set[f.Name] {
			c.errorf(diag.E2002, fi.Name.Range, "duplicate field %s in struct literal", f.Name)
		}
		set[f.Name] = true
		c.info.Types[fi.Name.ID()] = f.Type
		x := c.exprWith(fi.Value, f.Type, "struct literal")
		if x.mode != KindConst {
			allConst = false
		}
	}
	for _, f := range st.Fields {
		if !set[f.Name] && IsScoped(f.Type) {
			c.errorf(diag.E2005, e.Range, "missing initializer for field %s of Scoped type %s", f.Name, f.Type)
		}
	}
	if allConst && ClassOf(t) == ClassValue {
		if v := c.structValue(e, st); v != nil {
			return operand{mode: KindConst, typ: t, val: v}
		}
	}
	return operand{mode: KindValue, typ: t}
}

// structValue builds the constant value of a struct literal whose fields
// are all constant (nil if a zero value cannot be built).
func (c *Checker) structValue(e *ast.StructLit, st *Struct) Value {
	z, err := Zero(st)
	if err != nil {
		return nil
	}
	sv := z.(*StructVal)
	for _, fi := range e.Fields {
		if f := st.FieldByName(fi.Name.Name); f != nil {
			v := c.info.Values[fi.Value.ID()]
			if v == nil {
				return nil
			}
			sv.Fields[f.Index] = v
		}
	}
	return sv
}

func (c *Checker) funcLit(e *ast.FuncLit) operand {
	eff := Total
	if e.Driver {
		eff = Driver
	}
	sig := c.signature(e.Sig, eff)
	fi := &FuncInfo{Lit: e, Outer: c.fn, Effect: eff, Sig: sig, Body: e.Body, Captures: c.prog.Captures[e.ID()]}
	c.info.funcOf[e] = fi
	fi.Params = c.paramSyms(e.Sig, sig)
	if c.fn != nil {
		c.fn.Lits = append(c.fn.Lits, fi)
	} else {
		c.errorf(diag.E2002, e.Range, "function literal outside a function")
	}
	saveFn, saveLoops, saveCC := c.fn, c.loops, c.constCtx
	c.fn, c.loops, c.constCtx = fi, 0, 0
	c.funcBody(fi)
	c.fn, c.loops, c.constCtx = saveFn, saveLoops, saveCC
	return operand{mode: KindValue, typ: sig}
}
