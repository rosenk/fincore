package types

import (
	"fmt"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
)

func (c *Checker) call(e *ast.CallExpr) operand {
	x := c.call0(e)
	// A method selector used as callee is recorded with the instantiated
	// method signature.
	if ci := c.info.Calls[e.ID()]; ci != nil && ci.Method != nil && ci.Sig != nil {
		c.info.Types[e.Fun.ID()] = ci.Sig
	}
	return x
}

func (c *Checker) call0(e *ast.CallExpr) operand {
	fun := c.rawExpr(e.Fun)
	ci := &Call{Effect: Total}
	c.info.Calls[e.ID()] = ci
	if c.fn != nil {
		c.fn.Calls = append(c.fn.Calls, e)
	}
	switch fun.mode {
	case KindType:
		return c.conversion(e, fun.typ, ci)
	case KindBuiltin:
		return c.builtinCall(e, fun.sym, ci)
	case KindVariantCtor:
		return c.variantCall(e, fun, ci)
	case KindFunc:
		if fun.method != nil {
			return c.methodCall(e, fun, ci)
		}
		sig, _ := fun.typ.(*Signature)
		if sig == nil {
			c.useArgs(e)
			return invalidOp(e)
		}
		if sig.TypeParams != nil {
			c.errorf(diag.E2002, e.Fun.Span(), "cannot call generic function %s without instantiation (type arguments must be explicit, e.g. %s[T](...))", exprString(e.Fun), exprString(e.Fun))
			c.useArgs(e)
			return invalidOp(e)
		}
		ci.Kind, ci.Callee, ci.Sig, ci.Effect = CallFunc, fun.sym, sig, sig.Effect
		if inst := c.info.InstanceOf(unparen(e.Fun)); inst != nil {
			ci.TypeArgs = inst.TypeArgs
		}
		args, ok := c.arguments(e, sig.Params, sig.Variadic, exprString(e.Fun))
		if !ok {
			return invalidOp(e)
		}
		if sig.Effect == Comptime && c.shouldFold() {
			return c.foldComptime(e, fun.sym, sig, args, ci)
		}
		return c.callResultType(e, sig)
	case KindValue, KindVar:
		sig, ok := fun.typ.Underlying().(*Signature)
		if !ok {
			c.errorf(diag.E2002, e.Fun.Span(), "cannot call non-function %s (type %s)", exprString(e.Fun), fun.typ)
			c.useArgs(e)
			return invalidOp(e)
		}
		ci.Kind, ci.Sig, ci.Effect = CallValue, sig, sig.Effect
		if BuiltinOf(fun.typ) == resolve.TCancelFunc {
			ci.ConsumesRecv = true
		}
		c.arguments(e, sig.Params, sig.Variadic, exprString(e.Fun))
		return c.callResultType(e, sig)
	case KindPackage:
		c.errorf(diag.E2002, e.Fun.Span(), "cannot call package %s", exprString(e.Fun))
	}
	c.useArgs(e)
	return invalidOp(e)
}

// useArgs checks the arguments of an erroneous call (to record their types
// and report errors inside them).
func (c *Checker) useArgs(e *ast.CallExpr) {
	for _, a := range e.Args {
		c.rawExpr(a)
	}
}

func (c *Checker) callResultType(e *ast.CallExpr, sig *Signature) operand {
	switch r := sig.Result().(type) {
	case nil:
		return operand{mode: KindNoValue}
	default:
		return operand{mode: KindValue, typ: r}
	}
}

// shouldFold reports whether a comptime call in the current context must
// be evaluated at compile time: always, except inside the body of a
// comptime function outside of constant contexts (there the interpreter
// runs the call).
func (c *Checker) shouldFold() bool {
	return !(c.fn != nil && c.fn.Effect == Comptime && c.constCtx == 0)
}

func (c *Checker) foldComptime(e *ast.CallExpr, fn *resolve.Symbol, sig *Signature, args []operand, ci *Call) operand {
	vals := make([]Value, len(args))
	for i, a := range args {
		if a.mode != KindConst || a.val == nil {
			c.errorf(diag.E2002, a.expr.Span(), "argument %s to comptime function %s must be a compile-time constant", exprString(a.expr), fn.Name)
			return invalidOp(e)
		}
		vals[i] = a.val
	}
	if c.cfg.Evaluator == nil {
		c.errorf(diag.E4001, e.Span(), "cannot evaluate comptime call %s: no comptime evaluator configured", exprString(e))
		return invalidOp(e)
	}
	v, err := c.cfg.Evaluator.CallComptime(c, fn, vals)
	if err != nil {
		if err.Code != 0 {
			c.errorf(err.Code, e.Span(), "%s", err.Msg)
		}
		return invalidOp(e)
	}
	ci.Folded = true
	r := sig.Result()
	switch r := r.(type) {
	case nil:
		return operand{mode: KindNoValue}
	case *Tuple:
		c.info.Values[e.ID()] = v
		return operand{mode: KindValue, typ: r}
	}
	return operand{mode: KindConst, typ: r, val: v}
}

// arguments checks call arguments against parameter types. f(g()) with a
// multi-valued g is supported.
func (c *Checker) arguments(e *ast.CallExpr, params []Type, variadic bool, name string) ([]operand, bool) {
	var ops []operand
	for _, a := range e.Args {
		ops = append(ops, c.rawExpr(a))
	}
	if len(ops) == 1 && ops[0].mode == KindValue {
		if tu, ok := ops[0].typ.(*Tuple); ok {
			first := ops[0]
			ops = ops[:0]
			for _, t := range tu.Types {
				ops = append(ops, operand{mode: KindValue, typ: t, expr: first.expr})
			}
		}
	}
	ok := true
	for i := range ops {
		if ops[i].typ != nil {
			if _, isTuple := ops[i].typ.(*Tuple); isTuple && len(e.Args) == 1 {
				continue
			}
		}
		c.singleValue(&ops[i])
		if ops[i].invalid() {
			ok = false
		}
	}
	n, np := len(ops), len(params)
	if (variadic && n < np-1) || (!variadic && n != np) {
		what := "not enough"
		if n > np {
			what = "too many"
		}
		c.errorf(diag.E2003, e.Range, "%s arguments in call to %s: have %d, want %d", what, name, n, np)
		return ops, false
	}
	for i := range ops {
		if ops[i].invalid() {
			continue
		}
		pt := params[min(i, np-1)]
		if !c.assignment(&ops[i], pt, "argument to "+name) {
			ok = false
		}
	}
	return ops, ok
}

func (c *Checker) methodCall(e *ast.CallExpr, fun operand, ci *Call) operand {
	m := fun.method
	if m.PtrRecv && !fun.indirect {
		c.errorf(diag.E2002, e.Fun.Span(), "cannot call pointer method %s on %s (type %s): no automatic &, use a pointer (p := &x; p.%s(...))", m.Name, exprString(fun.recv), fun.recvType, m.Name)
		c.useArgs(e)
		return invalidOp(e)
	}
	ci.Method, ci.Recv, ci.RecvType = m, fun.recv, fun.recvType
	if m.Builtin != BMNone {
		return c.builtinMethodCall(e, m, ci)
	}
	ci.Kind, ci.Callee = CallMethod, m.Sym
	sig := m.Sig
	if sig == nil {
		c.useArgs(e)
		return invalidOp(e)
	}
	base, _ := Deref(fun.recvType)
	if n, ok := base.(*Named); ok && n.Origin != nil && len(m.RecvTypeParams) > 0 {
		sig = c.subst(sig, bindings(m.RecvTypeParams, n.TypeArgs)).(*Signature)
		ci.TypeArgs = n.TypeArgs
	}
	ci.Sig, ci.Effect = sig, sig.Effect
	c.arguments(e, sig.Params, sig.Variadic, m.Recv.Name+"."+m.Name)
	return c.callResultType(e, sig)
}

func (c *Checker) builtinMethodCall(e *ast.CallExpr, m *Method, ci *Call) operand {
	ci.Kind, ci.Effect = CallBuiltinMeth, m.Effect()
	name := m.Recv.Name + "." + m.Name
	switch m.Builtin {
	case NetListenTCP, ListenerAccept, ContextWithTimeout, SignalsNotifyContext:
		ci.ResourceCtor = true
	case ConnClose, ListenerClose:
		ci.ConsumesRecv = true
	}
	ctx := c.univ.named[resolve.TContext]
	switch m.Builtin {
	case ScopeAcquireSlot:
		ci.ResourceCtor = true
		if len(e.Args) != 2 {
			c.errorf(diag.E2003, e.Range, "wrong number of arguments in call to %s: have %d, want 2", name, len(e.Args))
			c.useArgs(e)
			return invalidOp(e)
		}
		c.exprWith(e.Args[0], ctx, "argument to "+name)
		pool := c.exprValue(e.Args[1])
		if pool.invalid() {
			return invalidOp(e)
		}
		var arr *Array
		if p, ok := pool.typ.Underlying().(*Pointer); ok {
			arr, _ = p.Elem.Underlying().(*Array)
		}
		if arr == nil || arr.LenParam != nil {
			c.errorf(diag.E2002, e.Args[1].Span(), "cannot use %s (type %s) as pool argument to %s: want *[N]T", exprString(e.Args[1]), pool.typ, name)
			return invalidOp(e)
		}
		slot := c.instantiate(c.univ.named[resolve.TSlot], []Type{arr.Elem}, nil)
		ci.TypeArgs = []Type{&ConstArg{Value: arr.Len}, arr.Elem}
		ci.Sig = &Signature{Params: []Type{ctx, pool.typ}, Results: []Type{slot, Typ[Error]}, Effect: Total}
		return operand{mode: KindValue, typ: ci.Sig.Result()}
	case ScopeReleaseSlot, ScopeGo:
		want := 1
		if m.Builtin == ScopeGo {
			want = 2
		}
		if len(e.Args) != want {
			c.errorf(diag.E2003, e.Range, "wrong number of arguments in call to %s: have %d, want %d", name, len(e.Args), want)
			c.useArgs(e)
			return invalidOp(e)
		}
		ci.ConsumesArgs = []int{0}
		s := c.exprValue(e.Args[0])
		if s.invalid() {
			c.useArgs(&ast.CallExpr{Args: e.Args[1:]})
			return invalidOp(e)
		}
		sn, ok := s.typ.(*Named)
		if !ok || sn.Origin == nil || sn.Origin.Builtin != resolve.TSlot {
			c.errorf(diag.E2002, e.Args[0].Span(), "cannot use %s (type %s) as Slot argument to %s", exprString(e.Args[0]), s.typ, name)
			c.useArgs(&ast.CallExpr{Args: e.Args[1:]})
			return invalidOp(e)
		}
		elem := sn.TypeArgs[0]
		ci.TypeArgs = []Type{elem}
		ci.Sig = &Signature{Params: []Type{sn}, Effect: Total}
		if m.Builtin == ScopeGo {
			fnT := &Signature{Params: []Type{&Pointer{Elem: elem}}, Effect: Driver}
			ci.Sig.Params = append(ci.Sig.Params, fnT)
			c.exprWith(e.Args[1], fnT, "argument to "+name)
		}
		return operand{mode: KindNoValue}
	}
	sig := m.Sig
	ci.Sig = sig
	c.arguments(e, sig.Params, sig.Variadic, name)
	return c.callResultType(e, sig)
}

func containsCall(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			found = true
		}
		return !found
	})
	return found
}

var (
	bytesT = &Slice{Elem: Typ[U8]}
)

func (c *Checker) builtinCall(e *ast.CallExpr, sym *resolve.Symbol, ci *Call) operand {
	b := sym.Builtin
	ci.Kind, ci.Builtin, ci.Callee = CallBuiltin, b, sym
	name := b.String()
	nargs := func(n int) bool {
		if len(e.Args) != n {
			what := "not enough"
			if len(e.Args) > n {
				what = "too many"
			}
			c.errorf(diag.E2003, e.Range, "%s arguments in call to %s: have %d, want %d", what, name, len(e.Args), n)
			c.useArgs(e)
			return false
		}
		return true
	}
	fixed := func(params []Type, results ...Type) operand {
		sig := &Signature{Params: params, Results: results, Effect: Total}
		ci.Sig = sig
		c.arguments(e, params, false, name)
		return c.callResultType(e, sig)
	}
	if b.IsCodeCtor() {
		return c.codeCtor(e, b, ci)
	}
	switch b {
	case resolve.FLen, resolve.FCap:
		if !nargs(1) {
			return invalidOp(e)
		}
		x := c.exprValue(e.Args[0])
		if x.invalid() {
			return invalidOp(e)
		}
		t := x.typ
		viaPtr := false
		if p, ok := t.Underlying().(*Pointer); ok {
			if _, isArr := p.Elem.Underlying().(*Array); isArr {
				t, viaPtr = p.Elem, true
			}
		}
		res := Typ[Usize]
		switch u := t.Underlying().(type) {
		case *Array:
			if u.LenParam == nil && !viaPtr && !containsCall(e.Args[0]) {
				return operand{mode: KindConst, typ: res, val: MakeUint(u.Len)}
			}
			return operand{mode: KindValue, typ: res}
		case *Slice:
			return operand{mode: KindValue, typ: res}
		case *Basic:
			if IsString(u) && b == resolve.FLen {
				if IsUntyped(x.typ) {
					c.convertUntyped(&x, Typ[String])
				}
				if x.mode == KindConst {
					return operand{mode: KindConst, typ: res, val: MakeUint(uint64(len(string(x.val.(StringVal)))))}
				}
				return operand{mode: KindValue, typ: res}
			}
		}
		c.errorf(diag.E2002, e.Args[0].Span(), "invalid argument %s (type %s) for %s", exprString(e.Args[0]), x.typ, name)
		return invalidOp(e)
	case resolve.FCopy:
		if !nargs(2) {
			return invalidOp(e)
		}
		d := c.exprValue(e.Args[0])
		s := c.exprValue(e.Args[1])
		if d.invalid() || s.invalid() {
			return invalidOp(e)
		}
		ds, ok1 := d.typ.Underlying().(*Slice)
		ss, ok2 := s.typ.Underlying().(*Slice)
		if !ok1 || !ok2 || !Identical(ds.Elem, ss.Elem) {
			c.errorf(diag.E2002, e.Range, "arguments to copy must be slices with identical element types (have %s and %s)", d.typ, s.typ)
			return invalidOp(e)
		}
		ci.TypeArgs = []Type{ds.Elem}
		ci.Sig = &Signature{Params: []Type{d.typ, s.typ}, Results: []Type{Typ[Usize]}, Effect: Total}
		return operand{mode: KindValue, typ: Typ[Usize]}
	case resolve.FPanic:
		ci.Effect = Driver
		if !nargs(1) {
			return invalidOp(e)
		}
		x := c.exprValue(e.Args[0])
		if !x.invalid() {
			c.defaultType(&x, "argument to panic")
		}
		return operand{mode: KindNoValue}
	case resolve.FBytesEqual:
		return fixed([]Type{bytesT, bytesT}, Typ[Bool])
	case resolve.FStrBytes:
		return fixed([]Type{Typ[String]}, bytesT)
	case resolve.FBytesToStr:
		return fixed([]Type{bytesT}, Typ[String])
	case resolve.FFormatUint:
		return fixed([]Type{Typ[U64], bytesT}, Typ[Usize], Typ[Error])
	case resolve.FParseUint:
		return fixed([]Type{bytesT}, Typ[U64], Typ[Error])
	case resolve.FNewContext:
		ci.ResourceCtor = true
		return fixed(nil, c.univ.named[resolve.TContext], c.univ.named[resolve.TCancelFunc])
	case resolve.FNewScope:
		ci.ResourceCtor = true
		return fixed([]Type{Typ[Usize]}, c.univ.named[resolve.TScope])
	}
	c.errorf(diag.E2002, e.Range, "unsupported builtin %s", name)
	return invalidOp(e)
}

// codeCtor types the Code constructors of spec 5.1.
func (c *Checker) codeCtor(e *ast.CallExpr, b resolve.Builtin, ci *Call) operand {
	ci.Effect = Comptime
	name := b.String()
	want := 0
	switch b {
	case resolve.FCodeArray, resolve.FCodeField:
		want = 2
	case resolve.FCodeSlice, resolve.FCodeStruct, resolve.FCodeUniv:
		want = 1
	}
	if len(e.Args) != want {
		c.errorf(diag.E2003, e.Range, "wrong number of arguments in call to %s: have %d, want %d", name, len(e.Args), want)
		c.useArgs(e)
		return invalidOp(e)
	}
	codeArg := func(a ast.Expr) (operand, *Code, bool) {
		x := c.exprValue(a)
		if x.invalid() {
			return x, nil, false
		}
		cd, ok := x.typ.(*Code)
		if !ok {
			c.errorf(diag.E2002, a.Span(), "cannot use %s (type %s) as Code argument to %s", exprString(a), x.typ, name)
			return x, nil, false
		}
		return x, cd, true
	}
	result := func(level int, val Value, args ...operand) operand {
		for _, a := range args {
			if a.mode != KindConst {
				return operand{mode: KindValue, typ: &Code{Level: level}}
			}
		}
		return operand{mode: KindConst, typ: &Code{Level: level}, val: val}
	}
	switch b {
	case resolve.FCodeArray:
		n := c.exprWith(e.Args[0], Typ[Usize], "argument to "+name)
		el, cd, ok := codeArg(e.Args[1])
		if n.invalid() || !ok {
			return invalidOp(e)
		}
		var v Value
		if n.mode == KindConst && el.mode == KindConst {
			ln, _ := Uint64(n.val)
			v = &CodeVal{Kind: CodeArray, Len: ln, Elem: el.val.(*CodeVal)}
		}
		return result(cd.Level, v, n, el)
	case resolve.FCodeSlice:
		el, cd, ok := codeArg(e.Args[0])
		if !ok {
			return invalidOp(e)
		}
		var v Value
		if el.mode == KindConst {
			v = &CodeVal{Kind: CodeSlice, Elem: el.val.(*CodeVal)}
		}
		return result(cd.Level, v, el)
	case resolve.FCodeStruct:
		fc := c.univ.named[resolve.TFieldCode]
		x := c.exprWith(e.Args[0], &Slice{Elem: fc}, "argument to "+name)
		if x.invalid() {
			return invalidOp(e)
		}
		return operand{mode: KindValue, typ: &Code{Level: -1}}
	case resolve.FCodeField:
		n := c.exprWith(e.Args[0], Typ[String], "argument to "+name)
		el, _, ok := codeArg(e.Args[1])
		if n.invalid() || !ok {
			return invalidOp(e)
		}
		fc := c.univ.named[resolve.TFieldCode]
		if n.mode == KindConst && el.mode == KindConst {
			return operand{mode: KindConst, typ: fc, val: &FieldCodeVal{Name: string(n.val.(StringVal)), Code: el.val.(*CodeVal)}}
		}
		return operand{mode: KindValue, typ: fc}
	case resolve.FCodeUniv:
		n := c.exprWith(e.Args[0], Typ[Usize], "argument to "+name)
		if n.invalid() {
			return invalidOp(e)
		}
		if n.mode != KindConst {
			c.errorf(diag.E2010, e.Args[0].Span(), "CodeUniv level %s must be a compile-time constant (its type Code (n+1) depends on it)", exprString(e.Args[0]))
			return invalidOp(e)
		}
		lv, ok := Uint64(n.val)
		if !ok || lv >= 1<<20 {
			c.errorf(diag.E2010, e.Args[0].Span(), "invalid universe level %s", n.val)
			return invalidOp(e)
		}
		return operand{mode: KindConst, typ: &Code{Level: int(lv) + 1}, val: &CodeVal{Kind: CodeUniv, Univ: int(lv)}}
	}
	kind := CodeKind(b - resolve.FCodeEmpty)
	return operand{mode: KindConst, typ: &Code{Level: -1}, val: &CodeVal{Kind: kind}}
}

func (c *Checker) conversion(e *ast.CallExpr, t Type, ci *Call) operand {
	ci.Kind, ci.Conv = CallConversion, t
	if len(e.Args) != 1 {
		c.errorf(diag.E2003, e.Range, "conversion to %s needs exactly one argument, have %d", t, len(e.Args))
		c.useArgs(e)
		return invalidOp(e)
	}
	x := c.exprValue(e.Args[0])
	if x.invalid() {
		return invalidOp(e)
	}
	if (isTypeParam(t) || isTypeParam(x.typ)) && !Identical(t, x.typ) {
		c.errorf(diag.E2011, e.Span(), "conversion %s inspects a type parameter (from %s to %s)", exprString(e), x.typ, t)
		return invalidOp(e)
	}
	if IsUntyped(x.typ) {
		if IsInteger(x.typ) && IsInteger(t) || IsBoolean(x.typ) && IsBoolean(t) || IsString(x.typ) && IsString(t) {
			if x.val != nil && !Representable(x.val, t) {
				c.errorf(diag.E2002, e.Args[0].Span(), "cannot convert %s (constant %s) to %s: overflows", exprString(e.Args[0]), x.val, t)
				return invalidOp(e)
			}
			c.updateType(e.Args[0], t)
			return operand{mode: KindConst, typ: t, val: x.val}
		}
		if !c.convertUntyped(&x, t) {
			return invalidOp(e)
		}
		return operand{mode: KindValue, typ: t}
	}
	if !c.convertible(x.typ, t) {
		c.errorf(diag.E2002, e.Span(), "cannot convert %s (type %s) to %s", exprString(e.Args[0]), x.typ, t)
		return invalidOp(e)
	}
	if x.mode == KindConst && x.val != nil {
		if IsInteger(t) {
			if !Representable(x.val, t) {
				c.errorf(diag.E2002, e.Args[0].Span(), "cannot convert constant %s to %s: overflows", x.val, t)
				return invalidOp(e)
			}
		}
		return operand{mode: KindConst, typ: t, val: x.val}
	}
	return operand{mode: KindValue, typ: t}
}

func (c *Checker) convertible(v, t Type) bool {
	if c.assignable(v, t) {
		return true
	}
	vu, tu := v.Underlying(), t.Underlying()
	if Identical(vu, tu) {
		return true
	}
	if IsInteger(vu) && IsInteger(tu) {
		return true
	}
	if vp, ok := vu.(*Pointer); ok {
		if tp, ok := tu.(*Pointer); ok {
			return Identical(vp.Elem.Underlying(), tp.Elem.Underlying())
		}
	}
	return false
}

func (c *Checker) variantCall(e *ast.CallExpr, fun operand, ci *Call) operand {
	v := fun.variant
	ci.Kind, ci.Variant, ci.VariantType = CallVariant, v, fun.typ
	args, ok := c.arguments(e, v.Payload, false, fmt.Sprintf("%s.%s", fun.typ, v.Name))
	if !ok {
		return operand{mode: KindValue, typ: fun.typ}
	}
	ev := &EnumVal{Variant: v.Index}
	for _, a := range args {
		if a.mode != KindConst {
			return operand{mode: KindValue, typ: fun.typ}
		}
		ev.Payload = append(ev.Payload, a.val)
	}
	return operand{mode: KindConst, typ: fun.typ, val: ev}
}

// funcInstance handles f[T1, ...] for a generic function f.
func (c *Checker) funcInstance(e *ast.IndexExpr, x operand) operand {
	sig, _ := x.typ.(*Signature)
	if sig == nil || sig.TypeParams == nil {
		c.errorf(diag.E2002, e.Span(), "%s is not a generic function", exprString(e.X))
		return invalidOp(e)
	}
	if len(e.Indices) != len(sig.TypeParams) {
		c.errorf(diag.E2003, e.Span(), "wrong number of type arguments for %s: have %d, want %d", exprString(e.X), len(e.Indices), len(sig.TypeParams))
		return invalidOp(e)
	}
	args := make([]Type, len(e.Indices))
	for i, a := range e.Indices {
		args[i] = c.typeArg(a, sig.TypeParams[i])
		if IsInvalid(args[i]) {
			return invalidOp(e)
		}
	}
	generic := &Signature{Params: sig.Params, Results: sig.Results, Variadic: sig.Variadic, Effect: sig.Effect}
	inst := c.subst(generic, bindings(sig.TypeParams, args)).(*Signature)
	key := typeKey(&Named{Sym: x.sym}) + "[" + typeKeys(args) + "]"
	rec := c.instByKey[key]
	if rec == nil {
		label := x.sym.Name + "[" + typeList(args) + "]"
		rec = &Instance{Sym: x.sym, TypeArgs: args, Sig: inst, Key: label, Generic: anyTypeParam(args)}
		c.instByKey[key] = rec
		c.info.InstanceList = append(c.info.InstanceList, rec)
	}
	c.info.Instances[e.ID()] = rec
	return operand{mode: KindFunc, typ: inst, sym: x.sym}
}
