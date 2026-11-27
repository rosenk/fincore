package types

import (
	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
)

func (c *Checker) stmtList(list []ast.Stmt) {
	for _, s := range list {
		c.stmt(s)
	}
}

func (c *Checker) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case nil:
	case *ast.BlockStmt:
		c.stmtList(s.Stmts)
	case *ast.DeclStmt:
		switch d := s.Decl.(type) {
		case *ast.ConstDecl:
			for _, sp := range d.Specs {
				if sym := c.prog.Defs[sp.Name.ID()]; sym != nil {
					c.objState[sym.ID] = 1
					c.constSpec(sym, sp)
					c.objState[sym.ID] = 2
				} else {
					c.constCtx++
					c.exprValue(sp.Value)
					c.constCtx--
				}
			}
		case *ast.VarDecl:
			sym := c.prog.Defs[d.Name.ID()]
			if sym == nil { // `var _ T = x`
				sym = &resolve.Symbol{Kind: resolve.SymVar, VarKind: resolve.VarLocal, Name: "_", Ident: d.Name, Decl: d, Func: c.fn.Node()}
			}
			if sym.ID != 0 {
				c.objState[sym.ID] = 1
			}
			c.varDecl(sym, d)
			if sym.ID != 0 {
				c.objState[sym.ID] = 2
			}
		}
	case *ast.ShortVarDecl:
		c.shortVarDecl(s)
	case *ast.AssignStmt:
		c.assignStmt(s)
	case *ast.IncDecStmt:
		x := c.exprValue(s.X)
		if x.invalid() {
			return
		}
		if x.mode != KindVar {
			c.errorf(diag.E2002, s.X.Span(), "cannot assign to %s", exprString(s.X))
		} else if !IsInteger(x.typ) {
			c.errorf(diag.E2002, s.X.Span(), "invalid operation: %s (non-integer type %s)", exprString(s.X), x.typ)
		}
	case *ast.ExprStmt:
		c.exprStmt(s.X, "statement")
	case *ast.DeferStmt:
		c.exprStmt(s.X, "defer")
	case *ast.ReturnStmt:
		c.returnStmt(s)
	case *ast.IfStmt:
		c.stmt(s.Init)
		c.condition(s.Cond, "if")
		c.stmtList(s.Then.Stmts)
		c.stmt(s.Else)
	case *ast.ForStmt:
		c.stmt(s.Init)
		if s.Cond != nil {
			c.condition(s.Cond, "for")
		}
		c.stmt(s.Post)
		if s.Decreases != nil {
			c.measure(s.Decreases.Measure)
		}
		c.loops++
		c.stmtList(s.Body.Stmts)
		c.loops--
	case *ast.MatchStmt:
		c.matchStmt(s)
	case *ast.ScopeStmt:
		x := c.exprValue(s.Value)
		st := c.univ.named[resolve.TScope]
		if !x.invalid() && !Identical(x.typ, st) {
			c.errorf(diag.E2002, s.Value.Span(), "scope value %s must have type Scope (have %s)", exprString(s.Value), x.typ)
		}
		c.setSymType(c.prog.Defs[s.Name.ID()], st)
		c.stmtList(s.Body.Stmts)
	case *ast.BreakStmt:
		if c.loops == 0 {
			c.errorf(diag.E2002, s.Range, "break is not in a loop")
		}
	case *ast.ContinueStmt:
		if c.loops == 0 {
			c.errorf(diag.E2002, s.Range, "continue is not in a loop")
		}
	}
}

func (c *Checker) condition(e ast.Expr, what string) {
	x := c.exprValue(e)
	if x.invalid() {
		return
	}
	if !IsBoolean(x.typ) {
		c.errorf(diag.E2002, e.Span(), "non-boolean condition in %s statement (type %s)", what, x.typ)
		return
	}
	if IsUntyped(x.typ) {
		c.convertUntyped(&x, Typ[Bool])
	}
}

// exprStmt checks an expression statement, defer operand or match-arm
// expression: it must be a call of a function, method, function value or
// panic/copy (not a conversion, variant or pure builtin).
func (c *Checker) exprStmt(e ast.Expr, what string) {
	call, ok := unparen(e).(*ast.CallExpr)
	if !ok {
		x := c.rawExpr(e)
		if !x.invalid() {
			c.errorf(diag.E2002, e.Span(), "%s is not used (%s requires a function call)", exprString(e), what)
		}
		return
	}
	x := c.rawExpr(e)
	if x.invalid() {
		return
	}
	ci := c.info.Calls[call.ID()]
	switch ci.Kind {
	case CallConversion, CallVariant:
		c.errorf(diag.E2002, e.Span(), "%s is not used", exprString(e))
	case CallBuiltin:
		if ci.Builtin != resolve.FPanic && ci.Builtin != resolve.FCopy {
			c.errorf(diag.E2002, e.Span(), "%s is not used", exprString(e))
		}
	}
}

// lhsValues returns the operands of an assignment's right-hand side,
// expanding a single multi-valued call.
func (c *Checker) rhsValues(rhs []ast.Expr, nl int) ([]operand, bool) {
	if len(rhs) == 1 && nl > 1 {
		x := c.rawExpr(rhs[0])
		if x.invalid() {
			return nil, false
		}
		tu, ok := x.typ.(*Tuple)
		if !ok || x.mode != KindValue {
			c.singleValue(&x)
			if x.invalid() {
				return nil, false
			}
			c.errorf(diag.E2003, rhs[0].Span(), "assignment mismatch: %d variables but %s returns 1 value", nl, exprString(rhs[0]))
			return nil, false
		}
		if len(tu.Types) != nl {
			c.errorf(diag.E2003, rhs[0].Span(), "assignment mismatch: %d variables but %s returns %d values", nl, exprString(rhs[0]), len(tu.Types))
			return nil, false
		}
		var out []operand
		var vals []Value
		if tv, ok := c.info.Values[rhs[0].ID()].(TupleVal); ok {
			vals = tv.Elems
		}
		for i, t := range tu.Types {
			op := operand{mode: KindValue, typ: t, expr: rhs[0]}
			if vals != nil {
				op.mode, op.val = KindConst, vals[i]
			}
			out = append(out, op)
		}
		return out, true
	}
	var out []operand
	ok := true
	for _, r := range rhs {
		x := c.exprValue(r)
		if x.invalid() {
			ok = false
		}
		out = append(out, x)
	}
	if len(rhs) != nl {
		c.errorf(diag.E2003, rhs[0].Span(), "assignment mismatch: %d variables but %d values", nl, len(rhs))
		return nil, false
	}
	return out, ok
}

func (c *Checker) shortVarDecl(s *ast.ShortVarDecl) {
	vals, ok := c.rhsValues(s.Values, len(s.Names))
	for i, id := range s.Names {
		var x *operand
		if ok && i < len(vals) {
			x = &vals[i]
		}
		if def := c.prog.Defs[id.ID()]; def != nil {
			t := Type(Typ[Invalid])
			if x != nil && !x.invalid() {
				t = c.defaultType(x, "assignment")
			}
			c.setSymType(def, t)
			continue
		}
		if use := c.prog.Uses[id.ID()]; use != nil {
			if use.Kind != resolve.SymVar {
				c.errorf(diag.E2002, id.Range, "cannot assign to %s (not a variable)", id.Name)
				continue
			}
			t := c.symType(use)
			c.info.Types[id.ID()] = t
			c.info.Kinds[id.ID()] = KindVar
			if x != nil && !x.invalid() {
				c.assignValue(x, t)
			}
			continue
		}
		// blank identifier
		if x != nil && !x.invalid() && IsUntyped(x.typ) {
			c.defaultType(x, "assignment")
		}
	}
}

// assignValue assigns x to a variable of type t; for expanded tuple
// operands only assignability is checked.
func (c *Checker) assignValue(x *operand, t Type) {
	if _, isCall := x.expr.(*ast.CallExpr); isCall {
		if _, isTuple := c.info.Types[x.expr.ID()].(*Tuple); isTuple {
			if !c.assignable(x.typ, t) && !IsInvalid(t) {
				c.errorf(diag.E2002, x.expr.Span(), "cannot assign value of type %s to variable of type %s", x.typ, t)
			}
			return
		}
	}
	c.assignment(x, t, "assignment")
}

func (c *Checker) assignStmt(s *ast.AssignStmt) {
	if s.Op != ast.Assign {
		if len(s.LHS) != 1 || len(s.RHS) != 1 {
			c.errorf(diag.E2003, s.Range, "compound assignment %s requires exactly one operand on each side", s.Op)
			return
		}
		l := c.exprValue(s.LHS[0])
		r := c.exprValue(s.RHS[0])
		if l.invalid() || r.invalid() {
			return
		}
		if l.mode != KindVar {
			c.errorf(diag.E2002, s.LHS[0].Span(), "cannot assign to %s", exprString(s.LHS[0]))
			return
		}
		if !IsInteger(l.typ) {
			c.errorf(diag.E2002, s.Range, "operator %s not defined on %s (type %s)", s.Op, exprString(s.LHS[0]), l.typ)
			return
		}
		c.assignment(&r, l.typ, "assignment")
		return
	}
	// LHS first (Go evaluates operands left to right).
	type lhs struct {
		blank bool
		op    operand
	}
	ls := make([]lhs, len(s.LHS))
	for i, l := range s.LHS {
		if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
			ls[i].blank = true
			continue
		}
		ls[i].op = c.exprValue(l)
		if ls[i].op.invalid() {
			continue
		}
		if ls[i].op.mode != KindVar {
			c.errorf(diag.E2002, l.Span(), "cannot assign to %s", exprString(l))
			ls[i].op.mode = KindNone
		}
	}
	vals, ok := c.rhsValues(s.RHS, len(s.LHS))
	if !ok {
		return
	}
	for i := range ls {
		x := &vals[i]
		if x.invalid() {
			continue
		}
		if ls[i].blank {
			if IsUntyped(x.typ) {
				c.defaultType(x, "assignment")
			}
			continue
		}
		if ls[i].op.invalid() {
			continue
		}
		c.assignValue(x, ls[i].op.typ)
	}
}

func (c *Checker) returnStmt(s *ast.ReturnStmt) {
	if c.fn == nil {
		return
	}
	want := c.fn.Sig.Results
	if len(s.Results) == 0 {
		if len(want) > 0 {
			c.errorf(diag.E2003, s.Range, "not enough return values: have 0, want %d", len(want))
		}
		return
	}
	if len(want) == 0 {
		for _, r := range s.Results {
			c.rawExpr(r)
		}
		c.errorf(diag.E2003, s.Results[0].Span(), "too many return values: have %d, want 0", len(s.Results))
		return
	}
	if len(s.Results) == 1 && len(want) > 1 {
		x := c.rawExpr(s.Results[0])
		if x.invalid() {
			return
		}
		tu, ok := x.typ.(*Tuple)
		if !ok {
			c.singleValue(&x)
			if !x.invalid() {
				c.errorf(diag.E2003, s.Results[0].Span(), "not enough return values: have 1, want %d", len(want))
			}
			return
		}
		if len(tu.Types) != len(want) {
			c.errorf(diag.E2003, s.Results[0].Span(), "wrong number of return values: have %d, want %d", len(tu.Types), len(want))
			return
		}
		for i, t := range tu.Types {
			if !c.assignable(t, want[i]) && !IsInvalid(want[i]) {
				c.errorf(diag.E2002, s.Results[0].Span(), "cannot use result %d of type %s as type %s in return statement", i+1, t, want[i])
			}
		}
		return
	}
	if len(s.Results) != len(want) {
		for _, r := range s.Results {
			c.rawExpr(r)
		}
		what := "not enough"
		if len(s.Results) > len(want) {
			what = "too many"
		}
		c.errorf(diag.E2003, s.Results[0].Span(), "%s return values: have %d, want %d", what, len(s.Results), len(want))
		return
	}
	for i, r := range s.Results {
		c.exprWith(r, want[i], "return statement")
	}
}

// pattern checks a single (non-or) pattern against the scrutinee type t
// and reports whether it is valid.
func (c *Checker) pattern(p ast.Pattern, t Type) bool {
	en, isEnum := t.Underlying().(*Enum)
	switch p := p.(type) {
	case *ast.WildcardPattern:
	case *ast.LiteralPattern:
		x := c.exprValue(p.Lit)
		if !x.invalid() {
			if !c.assignment(&x, t, "match pattern") {
				return false
			}
			if !Comparable(t) {
				c.errorf(diag.E2002, p.Range, "cannot match values of type %s against literals", t)
				return false
			}
			return true
		}
		return false
	case *ast.CtorPattern:
		bindInvalid := func() {
			for _, b := range p.Bindings {
				c.setSymType(c.prog.Defs[b.ID()], Typ[Invalid])
			}
		}
		// A constant pattern (pkg.Const or Const). An unqualified name that
		// is a variant of the scrutinee's enum is that variant (v0.2).
		isVariant := p.Qual == nil && isEnum && en.VariantByName(p.Name.Name) != nil
		if sym := c.prog.Uses[p.Name.ID()]; sym != nil && sym.Kind == resolve.SymConst && !isVariant {
			if p.HasParens {
				c.errorf(diag.E2002, p.Range, "constant %s cannot have bindings", sym.Name)
				bindInvalid()
				return false
			}
			if isEnum {
				c.errorf(diag.E2002, p.Range, "constant %s cannot be a pattern for enum type %s", sym.Name, t)
				return false
			}
			x := c.symOperand(sym, p.Name)
			x.expr = p.Name
			c.record(&x)
			return !x.invalid() && c.assignment(&x, t, "match pattern")
		}
		var v *Variant
		switch {
		case p.Qual != nil:
			qs := c.prog.Uses[p.Qual.ID()]
			if qs == nil {
				bindInvalid()
				return false
			}
			if qs.Kind != resolve.SymType {
				c.errorf(diag.E2002, p.Qual.Range, "%s is not an enum type", p.Qual.Name)
				bindInvalid()
				return false
			}
			qt := c.typeSym(qs, p.Qual)
			c.info.Types[p.Qual.ID()] = qt
			c.info.Kinds[p.Qual.ID()] = KindType
			if IsInvalid(qt) {
				bindInvalid()
				return false
			}
			qe, ok := qt.Underlying().(*Enum)
			if !ok {
				c.errorf(diag.E2002, p.Qual.Range, "%s is not an enum type", qt)
				bindInvalid()
				return false
			}
			// v0.2: a generic enum qualifies patterns on its instances
			// (Option.Some(x) on an Option[u32]); the variant comes from
			// the instance.
			if tn, ok := t.(*Named); ok && tn.Origin != nil && tn.Origin == qt && isEnum {
				qe = en
			} else if !Identical(qt, t) {
				c.errorf(diag.E2002, p.Range, "pattern of type %s does not match scrutinee of type %s", qt, t)
				bindInvalid()
				return false
			}
			v = qe.VariantByName(p.Name.Name)
			if v == nil {
				c.errorf(diag.E2001, p.Name.Range, "enum %s has no variant %s", qt, p.Name.Name)
				bindInvalid()
				return false
			}
		case isEnum:
			v = en.VariantByName(p.Name.Name)
			if v == nil {
				c.errorf(diag.E2001, p.Name.Range, "undefined: %s (no constant or variant of %s)", p.Name.Name, t)
				bindInvalid()
				return false
			}
		default:
			c.errorf(diag.E2001, p.Name.Range, "undefined: %s", p.Name.Name)
			bindInvalid()
			return false
		}
		c.info.Variants[p.ID()] = v
		c.info.Types[p.ID()] = t
		if len(p.Bindings) != len(v.Payload) || (p.HasParens && len(v.Payload) == 0) {
			c.errorf(diag.E2003, p.Range, "variant %s has %d payload values, pattern binds %d", v.Name, len(v.Payload), len(p.Bindings))
			bindInvalid()
			return false
		}
		for i, b := range p.Bindings {
			if sym := c.prog.Defs[b.ID()]; sym != nil {
				c.setSymType(sym, v.Payload[i])
			} else {
				c.info.Types[b.ID()] = v.Payload[i]
			}
		}
		return true
	}
	return true
}

// ---------------------------------------------------------------------------
// Terminating statements (Go spec rules, adapted)

func (c *Checker) isTerminatingList(list []ast.Stmt) bool {
	return len(list) > 0 && c.isTerminating(list[len(list)-1])
}

func (c *Checker) isTerminating(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.ExprStmt:
		return c.isPanicCall(s.X)
	case *ast.BlockStmt:
		return c.isTerminatingList(s.Stmts)
	case *ast.IfStmt:
		return s.Else != nil && c.isTerminatingList(s.Then.Stmts) && c.isTerminating(s.Else)
	case *ast.ForStmt:
		return s.Cond == nil && !hasBreak(s.Body.Stmts)
	case *ast.ScopeStmt:
		return c.isTerminatingList(s.Body.Stmts)
	case *ast.MatchStmt:
		// v0.2: a match on an enum is exhaustive (E2012 otherwise), so it
		// terminates if all arms do; other matches need a '_' arm.
		exhaustive := false
		for _, arm := range s.Arms {
			if _, ok := arm.Pattern.(*ast.WildcardPattern); ok {
				exhaustive = true
			}
			if arm.Expr != nil && !c.isPanicCall(arm.Expr) {
				return false
			}
			if arm.Block != nil && !c.isTerminatingList(arm.Block.Stmts) {
				return false
			}
		}
		if !exhaustive && len(s.Arms) > 0 {
			if t := c.info.TypeOf(s.X); t != nil {
				if _, ok := t.Underlying().(*Enum); ok {
					exhaustive = true
				}
			}
		}
		return exhaustive
	}
	return false
}

func (c *Checker) isPanicCall(e ast.Expr) bool {
	call, ok := unparen(e).(*ast.CallExpr)
	if !ok {
		return false
	}
	ci := c.info.CallOf(call)
	return ci != nil && ci.Kind == CallBuiltin && ci.Builtin == resolve.FPanic
}

// hasBreak reports whether list contains a break that targets the
// enclosing loop (not nested loops or function literals).
func hasBreak(list []ast.Stmt) bool {
	for _, s := range list {
		if stmtHasBreak(s) {
			return true
		}
	}
	return false
}

func stmtHasBreak(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.BreakStmt:
		return true
	case *ast.BlockStmt:
		return hasBreak(s.Stmts)
	case *ast.IfStmt:
		return hasBreak(s.Then.Stmts) || (s.Else != nil && stmtHasBreak(s.Else))
	case *ast.MatchStmt:
		for _, a := range s.Arms {
			if a.Block != nil && hasBreak(a.Block.Stmts) {
				return true
			}
		}
	case *ast.ScopeStmt:
		return hasBreak(s.Body.Stmts)
	}
	return false
}
