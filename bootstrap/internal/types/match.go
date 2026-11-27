package types

import (
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
)

// v0.2 sum types and patterns: match statements and expressions, nested,
// or-, tuple and rest patterns, guards, `is` expressions, exhaustiveness
// and reachability (E2012, see usefulness.go). See docs/v0_2.md.

func (c *Checker) matchStmt(s *ast.MatchStmt) {
	x := c.scrutinee(s.X)
	c.matchPatterns(s, s.Range, x, s.Arms, false)
	for _, arm := range s.Arms {
		if arm.Expr != nil {
			c.exprStmt(arm.Expr, "match arm")
		}
		if arm.Block != nil {
			c.stmtList(arm.Block.Stmts)
		}
	}
}

// scrutinee checks the scrutinee of a match: an expression (untyped
// constants get their default type) or a tuple of expressions (type
// *Tuple, v0.2).
func (c *Checker) scrutinee(e ast.Expr) operand {
	te, ok := e.(*ast.TupleExpr)
	if !ok {
		x := c.exprValue(e)
		if !x.invalid() && IsUntyped(x.typ) {
			c.defaultType(&x, "match")
		}
		return x
	}
	ts := make([]Type, len(te.Elems))
	bad := false
	for i, el := range te.Elems {
		x := c.exprValue(el)
		if !x.invalid() && IsUntyped(x.typ) {
			c.defaultType(&x, "match")
		}
		if x.invalid() {
			bad = true
		}
		ts[i] = x.typ
	}
	if bad {
		return invalidOp(e)
	}
	x := operand{mode: KindValue, typ: &Tuple{Types: ts}, expr: e}
	c.info.Types[e.ID()] = x.typ
	c.info.Kinds[e.ID()] = KindValue
	return x
}

// matchExpr checks a match expression. The arms' types must agree;
// untyped constant arms take the type of the typed arms. If every arm is
// untyped the result is untyped and is converted by its context (see
// updateType). The result is constant if the scrutinee is constant and the
// selected arm is a constant expression.
func (c *Checker) matchExpr(e *ast.MatchExpr) operand {
	x := c.scrutinee(e.X)
	valid := c.matchPatterns(e, e.Range, x, e.Arms, true)
	ops := make([]operand, len(e.Arms))
	var rt Type
	bad := false
	for i, arm := range e.Arms {
		ops[i] = c.exprValue(arm.Expr)
		if ops[i].invalid() {
			bad = true
			continue
		}
		if rt == nil && !IsUntyped(ops[i].typ) {
			rt = ops[i].typ
		}
	}
	if bad {
		return invalidOp(e)
	}
	if rt != nil {
		for i := range ops {
			if IsUntyped(ops[i].typ) {
				if !c.convertUntyped(&ops[i], rt) {
					bad = true
				}
			} else if !Identical(ops[i].typ, rt) {
				c.errorf(diag.E2002, e.Arms[i].Expr.Span(), "match arm has type %s, want %s", ops[i].typ, rt)
				bad = true
			}
		}
	} else {
		rt = ops[0].typ
		for i := 1; i < len(ops); i++ {
			t := ops[i].typ
			switch {
			case Identical(t, rt):
			case isUntypedNumeric(t) && isUntypedNumeric(rt):
				rt = Typ[UntypedInt]
			default:
				c.errorf(diag.E2002, e.Arms[i].Expr.Span(), "match arm has type %s, want %s", t, rt)
				bad = true
			}
		}
	}
	if bad {
		return invalidOp(e)
	}
	r := operand{mode: KindValue, typ: rt}
	if valid && x.mode == KindConst {
		if i := c.selectArm(x.val, e.Arms); i >= 0 && ops[i].mode == KindConst {
			r.mode = KindConst
			r.val = ops[i].val
		}
	}
	return r
}

func isUntypedNumeric(t Type) bool {
	k := basicKind(t)
	return k == UntypedInt || k == UntypedChar
}

// convertMatchArms converts the untyped arms of a match expression whose
// result was given type t by its context.
func (c *Checker) convertMatchArms(e *ast.MatchExpr, t Type) {
	for _, arm := range e.Arms {
		id := arm.Expr.ID()
		old := c.info.Types[id]
		if old == nil || !IsUntyped(old) {
			continue
		}
		x := operand{mode: c.info.Kinds[id], typ: old, val: c.info.Values[id], expr: arm.Expr}
		c.convertUntyped(&x, t)
	}
}

// selectArm returns the index of the arm selected by the constant
// scrutinee value v (-1 if none, or if a guard on the way is not a
// constant).
func (c *Checker) selectArm(v Value, arms []*ast.MatchArm) int {
	for i, arm := range arms {
		if !MatchValue(c.info, arm.Pattern, v, nil) {
			continue
		}
		if arm.Guard == nil {
			return i
		}
		g, ok := c.info.ValueOf(arm.Guard).(BoolVal)
		if !ok {
			return -1
		}
		if g {
			return i
		}
	}
	return -1
}

// MatchValue reports whether the value v matches pattern p (checked by the
// type checker); bind, if not nil, is called for the bindings of the
// matching pattern (v0.2). Used for constant match expressions and by the
// comptime interpreter.
func MatchValue(info *Info, p ast.Pattern, v Value, bind func(sym *resolve.Symbol, v Value)) bool {
	switch p := p.(type) {
	case *ast.WildcardPattern, *ast.RestPattern:
		return true
	case *ast.Ident:
		if vr := info.VariantAt(p); vr != nil {
			ev, ok := v.(*EnumVal)
			return ok && ev.Variant == vr.Index
		}
		if cv := info.PatConst(p); cv != nil {
			return valueEq(v, cv)
		}
		if sym, _ := info.PatBinding(p); bind != nil && sym != nil {
			bind(sym, v)
		}
		return true
	case *ast.CtorPattern:
		if vr := info.VariantAt(p); vr != nil {
			ev, ok := v.(*EnumVal)
			if !ok || ev.Variant != vr.Index {
				return false
			}
			if sym, _ := info.PatBinding(p); bind != nil && sym != nil {
				bind(sym, v)
			}
			for i, a := range p.Args {
				if _, ok := a.(*ast.RestPattern); ok {
					continue
				}
				if !MatchValue(info, a, ev.Payload[PayloadIndex(p, i, len(ev.Payload))], bind) {
					return false
				}
			}
			return true
		}
		if cv := info.PatConst(p); cv != nil {
			return valueEq(v, cv)
		}
		if sym, _ := info.PatBinding(p); bind != nil && sym != nil {
			bind(sym, v)
		}
		return true
	case *ast.LiteralPattern:
		return valueEq(v, info.ValueOf(p.Lit))
	case *ast.OrPattern:
		for _, a := range p.Alts {
			if MatchValue(info, a, v, bind) {
				return true
			}
		}
		return false
	case *ast.TuplePattern:
		tv, ok := v.(TupleVal)
		if !ok || len(tv.Elems) != len(p.Elems) {
			return false
		}
		for i, a := range p.Elems {
			if !MatchValue(info, a, tv.Elems[i], bind) {
				return false
			}
		}
		return true
	}
	return false
}

func valueEq(v, w Value) bool {
	if v == nil || w == nil {
		return false
	}
	eq, err := Equal(v, w)
	return err == nil && eq
}

// PayloadIndex returns the payload position matched by argument i of a
// variant pattern with n payloads (v0.2: arguments after a `..` count from
// the end). Argument i must not be the RestPattern itself.
func PayloadIndex(p *ast.CtorPattern, i, n int) int {
	for r, a := range p.Args {
		if _, ok := a.(*ast.RestPattern); ok && r < i {
			return n - (len(p.Args) - i)
		}
	}
	return i
}

// patternAlts returns the alternatives of a pattern (itself unless it is
// an or-pattern).
func patternAlts(p ast.Pattern) []ast.Pattern {
	if op, ok := p.(*ast.OrPattern); ok {
		return op.Alts
	}
	return []ast.Pattern{p}
}

// isExpr checks `X is Pattern` (v0.2). Bindings are declared by the
// resolver for every `is` (flow-scoped, decisions 118-125).
func (c *Checker) isExpr(e *ast.IsExpr) operand {
	x := c.exprValue(e.X)
	if !x.invalid() && IsUntyped(x.typ) {
		c.defaultType(&x, "match")
	}
	if x.invalid() {
		c.invalidBindings(e.Pattern)
		return invalidOp(e)
	}
	if !c.topPattern(e.Pattern, x.typ) {
		return invalidOp(e)
	}
	if x.mode == KindConst && !hasBindings(c.info, e.Pattern) {
		return operand{mode: KindConst, typ: Typ[UntypedBool], val: BoolVal(MatchValue(c.info, e.Pattern, x.val, nil))}
	}
	return operand{mode: KindValue, typ: Typ[Bool]}
}

// hasBindings reports whether a checked pattern binds a variable.
func hasBindings(info *Info, p ast.Pattern) bool {
	found := false
	walkPattern(p, func(q ast.Pattern) {
		if info.IsBindingPat(q) {
			found = true
		}
	})
	return found
}

// walkPattern calls f for p and every sub-pattern, in source order.
func walkPattern(p ast.Pattern, f func(ast.Pattern)) {
	f(p)
	switch p := p.(type) {
	case *ast.CtorPattern:
		for _, a := range p.Args {
			walkPattern(a, f)
		}
	case *ast.OrPattern:
		for _, a := range p.Alts {
			walkPattern(a, f)
		}
	case *ast.TuplePattern:
		for _, a := range p.Elems {
			walkPattern(a, f)
		}
	}
}

// invalidBindings gives every binding of p type Invalid (after an error).
func (c *Checker) invalidBindings(p ast.Pattern) {
	walkPattern(p, func(q ast.Pattern) {
		if sym, _ := c.info.PatBinding(q); sym != nil && c.info.SymType(sym) == nil {
			c.setSymType(sym, Typ[Invalid])
		}
	})
}

// matchPatterns checks the patterns and guards of a match on x and reports
// E2012 for unreachable arms and non-exhaustive matches. It records
// whether the match counts as exhaustive and reports whether every pattern
// is valid. at is the span of the match (the `match` keyword).
func (c *Checker) matchPatterns(m ast.Node, at diag.SourceSpan, x operand, arms []*ast.MatchArm, isExpr bool) bool {
	valid := !x.invalid()
	for _, arm := range arms {
		if x.invalid() {
			c.invalidBindings(arm.Pattern)
		} else if !c.topPattern(arm.Pattern, x.typ) {
			valid = false
		}
		if arm.Guard != nil {
			g := c.exprValue(arm.Guard)
			if !g.invalid() {
				if !IsBoolean(g.typ) {
					c.errorf(diag.E2002, arm.Guard.Span(), "non-boolean guard %s in match arm (type %s)", exprString(arm.Guard), g.typ)
				} else if IsUntyped(g.typ) {
					c.convertUntyped(&g, Typ[Bool])
				}
			}
		}
	}
	required := false
	if !x.invalid() {
		switch x.typ.Underlying().(type) {
		case *Enum, *Tuple:
			required = true
		}
	}
	if !valid {
		exh := required
		for _, arm := range arms {
			if c.catchAll(arm.Pattern) && arm.Guard == nil {
				exh = true
			}
		}
		c.info.Exhaustive[m.ID()] = exh
		return false
	}
	exhaustive := c.checkCoverage(at, x.typ, arms, isExpr)
	c.info.Exhaustive[m.ID()] = exhaustive || required
	return true
}

// ---------------------------------------------------------------------------
// Pattern checking

// binding is a variable bound by a pattern.
type binding struct {
	sym *resolve.Symbol
	id  *ast.Ident
	typ Type
}

// topPattern checks the pattern of an arm or `is` expression against the
// scrutinee type t, then gives the bindings their types. It reports
// whether the pattern is valid.
func (c *Checker) topPattern(p ast.Pattern, t Type) bool {
	var bs []binding
	ok := c.pat(p, t, false, &bs)
	bs = c.uniqBindings(bs)
	for _, b := range bs {
		if t := c.info.SymType(b.sym); t == nil {
			c.setSymType(b.sym, b.typ)
		} else if b.id != b.sym.Ident && !IsInvalid(t) && !IsInvalid(b.typ) && !Identical(b.typ, t) {
			// v0.2: an `is` binding intersected with an earlier one of
			// the same name (one symbol, see resolve.intersect).
			c.errorf(diag.E2002, b.id.Range, "variable %s has type %s here but %s in the first is test that binds it", b.id.Name, b.typ, t)
			ok = false
		}
	}
	c.invalidBindings(p)
	return ok
}

// uniqBindings reports variables bound twice (E2002) and returns the list
// without duplicates.
func (c *Checker) uniqBindings(bs []binding) []binding {
	var out []binding
	for _, b := range bs {
		dup := false
		for _, o := range out {
			if o.sym == b.sym {
				dup = true
			}
		}
		if dup {
			c.errorf(diag.E2002, b.id.Range, "variable %s is bound more than once in a pattern", b.id.Name)
			continue
		}
		out = append(out, b)
	}
	return out
}

func findBinding(bs []binding, sym *resolve.Symbol) int {
	for i, b := range bs {
		if b.sym == sym {
			return i
		}
	}
	return -1
}

// pat checks pattern p against type t and appends its bindings to out.
// payload is set for payload positions of a variant pattern, where a bare
// identifier is a binding or a variant of t's enum.
func (c *Checker) pat(p ast.Pattern, t Type, payload bool, out *[]binding) bool {
	en, isEnum := t.Underlying().(*Enum)
	switch p := p.(type) {
	case *ast.WildcardPattern:
		return true
	case *ast.RestPattern:
		c.errorf(diag.E2002, p.Range, "'..' is only allowed in the payload list of a variant pattern")
		return false
	case *ast.Ident:
		c.info.Types[p.ID()] = t
		if p.Name == "_" {
			return true
		}
		if isEnum {
			if v := en.VariantByName(p.Name); v != nil {
				if len(v.Payload) != 0 {
					c.errorf(diag.E2003, p.Range, "variant %s has %d payload values, pattern binds 0", v.Name, len(v.Payload))
					return false
				}
				c.info.Variants[p.ID()] = v
				// A name declared by the resolver for it is bound to
				// the payload (which is that variant).
				if sym := c.prog.Defs[p.ID()]; sym != nil && c.info.SymType(sym) == nil {
					c.setSymType(sym, t)
				}
				return true
			}
		}
		if sym := c.prog.Uses[p.ID()]; sym != nil && sym.Kind == resolve.SymConst {
			return c.constPat(p, p, sym, t, false)
		}
		return c.bindPat(p, t, out)
	case *ast.LiteralPattern:
		x := c.exprValue(p.Lit)
		if x.invalid() {
			return false
		}
		if _, ok := t.(*Tuple); ok {
			c.errorf(diag.E2002, p.Range, "pattern %s does not match a tuple scrutinee of type %s", exprString(p.Lit), t)
			return false
		}
		if !c.assignment(&x, t, "match pattern") {
			return false
		}
		if !Comparable(t) {
			c.errorf(diag.E2002, p.Range, "cannot match values of type %s against literals", t)
			return false
		}
		return true
	case *ast.CtorPattern:
		return c.ctorPat(p, t, out)
	case *ast.OrPattern:
		return c.orPat(p, t, payload, out)
	case *ast.TuplePattern:
		tt, ok := t.(*Tuple)
		if !ok {
			c.errorf(diag.E2002, p.Range, "tuple pattern %s does not match type %s", ast.Format(p), t)
			return false
		}
		if len(tt.Types) != len(p.Elems) {
			c.errorf(diag.E2003, p.Range, "tuple pattern has %d elements, scrutinee has %d", len(p.Elems), len(tt.Types))
			return false
		}
		valid := true
		for i, el := range p.Elems {
			if !c.pat(el, tt.Types[i], false, out) {
				valid = false
			}
		}
		return valid
	}
	return true
}

// orPat checks an or-pattern: every alternative must bind the same
// variables with identical types (v0.2).
func (c *Checker) orPat(p *ast.OrPattern, t Type, payload bool, out *[]binding) bool {
	valid := true
	var first []binding
	haveFirst := false
	for i, alt := range p.Alts {
		if w, ok := alt.(*ast.WildcardPattern); ok {
			c.errorf(diag.E2002, w.Range, "'_' cannot be an alternative of an or-pattern")
			valid = false
			continue
		}
		if id, ok := alt.(*ast.Ident); ok && id.Name == "_" {
			c.errorf(diag.E2002, id.Range, "'_' cannot be an alternative of an or-pattern")
			valid = false
			continue
		}
		var bs []binding
		if !c.pat(alt, t, payload, &bs) {
			valid = false
			continue
		}
		bs = c.uniqBindings(bs)
		if i == 0 {
			first, haveFirst = bs, true
			continue
		}
		if !haveFirst {
			continue
		}
		for _, b := range first {
			if findBinding(bs, b.sym) < 0 {
				c.errorf(diag.E2002, alt.Span(), "or-pattern alternatives must bind the same variables (%s is not bound here)", b.id.Name)
				valid = false
			}
		}
		for _, b := range bs {
			j := findBinding(first, b.sym)
			if j < 0 {
				c.errorf(diag.E2002, b.id.Range, "or-pattern alternatives must bind the same variables (%s is not bound in the first alternative)", b.id.Name)
				valid = false
			} else if !Identical(b.typ, first[j].typ) {
				c.errorf(diag.E2002, b.id.Range, "variable %s has type %s here but %s in the first alternative", b.id.Name, b.typ, first[j].typ)
				valid = false
			}
		}
	}
	*out = append(*out, first...)
	return valid
}

// bindPat checks a binding (the bare identifier id) of type t.
func (c *Checker) bindPat(id *ast.Ident, t Type, out *[]binding) bool {
	sym := c.prog.Defs[id.ID()]
	if sym == nil {
		return false // not declared (every pattern binding is since decision 118)
	}
	if _, ok := t.(*Tuple); ok {
		c.errorf(diag.E2002, id.Range, "cannot bind %s to a tuple of type %s (use '_' or a tuple pattern)", id.Name, t)
		return false
	}
	*out = append(*out, binding{sym: sym, id: id, typ: t})
	return true
}

// constPat checks the constant pattern p (the constant sym, named by name)
// against type t: the constant must be assignable to t. A constant of an
// enum type must hold a variant without payload; p then counts as that
// variant (Info.Variants).
func (c *Checker) constPat(p ast.Pattern, name *ast.Ident, sym *resolve.Symbol, t Type, parens bool) bool {
	if parens {
		c.errorf(diag.E2002, p.Span(), "constant %s cannot have bindings", sym.Name)
		return false
	}
	if _, ok := t.(*Tuple); ok {
		c.errorf(diag.E2002, p.Span(), "pattern %s does not match a tuple scrutinee of type %s", sym.Name, t)
		return false
	}
	x := c.symOperand(sym, name)
	x.expr = name
	c.record(&x)
	if x.invalid() || !c.assignment(&x, t, "match pattern") {
		return false
	}
	en, ok := t.Underlying().(*Enum)
	if !ok {
		return true
	}
	ev, ok := x.val.(*EnumVal)
	if !ok || ev.Variant >= len(en.Variants) {
		return false
	}
	v := en.Variants[ev.Variant]
	if len(v.Payload) != 0 {
		c.errorf(diag.E2002, p.Span(), "constant %s cannot be a pattern: its variant %s has a payload (match the variant instead)", sym.Name, v.Name)
		return false
	}
	c.info.Variants[p.ID()] = v
	c.info.Types[p.ID()] = t
	return true
}

// ctorPat checks a variant, constant or binding pattern
// `[Qual.]Name[(args)]` (a bare Name is a variant of t's enum, else a
// constant, else a binding).
func (c *Checker) ctorPat(p *ast.CtorPattern, t Type, out *[]binding) bool {
	en, isEnum := t.Underlying().(*Enum)
	// A constant pattern (pkg.Const or Const). An unqualified name that
	// is a variant of the scrutinee's enum is that variant (v0.2).
	isVariant := p.Qual == nil && isEnum && en.VariantByName(p.Name.Name) != nil
	if sym := c.prog.Uses[p.Name.ID()]; sym != nil && sym.Kind == resolve.SymConst && !isVariant {
		return c.constPat(p, p.Name, sym, t, p.HasParens)
	}
	if p.Qual == nil && !p.HasParens && !isVariant {
		c.info.Types[p.ID()] = t
		return c.bindPat(p.Name, t, out)
	}
	var v *Variant
	switch {
	case p.Qual != nil:
		qs := c.prog.Uses[p.Qual.ID()]
		if qs == nil {
			return false
		}
		if qs.Kind != resolve.SymType {
			c.errorf(diag.E2002, p.Qual.Range, "%s is not an enum type", p.Qual.Name)
			return false
		}
		qt := c.typeSym(qs, p.Qual)
		c.info.Types[p.Qual.ID()] = qt
		c.info.Kinds[p.Qual.ID()] = KindType
		if IsInvalid(qt) {
			return false
		}
		qe, ok := qt.Underlying().(*Enum)
		if !ok {
			c.errorf(diag.E2002, p.Qual.Range, "%s is not an enum type", qt)
			return false
		}
		// v0.2: a generic enum qualifies patterns on its instances
		// (Option.Some(x) on an Option[u32]); the variant comes from
		// the instance.
		if tn, ok := t.(*Named); ok && tn.Origin != nil && tn.Origin == qt && isEnum {
			qe = en
		} else if !Identical(qt, t) {
			c.errorf(diag.E2002, p.Range, "pattern of type %s does not match scrutinee of type %s", qt, t)
			return false
		}
		v = qe.VariantByName(p.Name.Name)
		if v == nil {
			c.errorf(diag.E2001, p.Name.Range, "enum %s has no variant %s", qt, p.Name.Name)
			return false
		}
	case isEnum:
		v = en.VariantByName(p.Name.Name)
		if v == nil {
			c.errorf(diag.E2001, p.Name.Range, "undefined: %s (no constant or variant of %s)", p.Name.Name, t)
			return false
		}
	default:
		c.errorf(diag.E2001, p.Name.Range, "undefined: %s", p.Name.Name)
		return false
	}
	c.info.Variants[p.ID()] = v
	c.info.Types[p.ID()] = t
	if p.Qual == nil && !p.HasParens {
		// A name declared by the resolver for a bare variant is bound to
		// the value (which is that variant).
		if sym := c.prog.Defs[p.Name.ID()]; sym != nil && c.info.SymType(sym) == nil {
			c.setSymType(sym, t)
		}
	}
	n, rest := len(p.Args), false
	for _, a := range p.Args {
		if r, ok := a.(*ast.RestPattern); ok {
			if rest {
				c.errorf(diag.E2002, r.Range, "at most one '..' is allowed in a payload list")
				return false
			}
			n--
			rest = true
		}
	}
	if (!rest && n != len(v.Payload)) || (rest && n > len(v.Payload)) || (p.HasParens && len(v.Payload) == 0 && !rest) {
		c.errorf(diag.E2003, p.Range, "variant %s has %d payload values, pattern binds %d", v.Name, len(v.Payload), n)
		return false
	}
	valid := true
	for i, a := range p.Args {
		if _, ok := a.(*ast.RestPattern); ok {
			continue
		}
		if !c.pat(a, v.Payload[PayloadIndex(p, i, len(v.Payload))], true, out) {
			valid = false
		}
	}
	return valid
}

// ---------------------------------------------------------------------------
// Reachability and exhaustiveness

// checkCoverage reports unreachable arms and patterns and non-exhaustive
// matches (E2012) with the usefulness algorithm of usefulness.go. It
// reports whether the unguarded arms cover every value.
func (c *Checker) checkCoverage(at diag.SourceSpan, t Type, arms []*ast.MatchArm, isExpr bool) bool {
	u := &usefulness{info: c.info}
	cols := []Type{t}
	tt, isTuple := t.(*Tuple)
	if isTuple {
		cols = tt.Types
	}
	var rows [][]ast.Pattern
	var wild ast.Pattern
	for _, arm := range arms {
		if wild != nil {
			if _, ok := wild.(*ast.WildcardPattern); ok {
				c.errorf(diag.E2012, arm.Pattern.Span(), "unreachable match arm: it follows the '_' arm")
			} else {
				c.errorf(diag.E2012, arm.Pattern.Span(), "unreachable match arm: it follows the catch-all binding %s", BareName(wild).Name)
			}
			continue
		}
		var armRows [][]ast.Pattern
		for _, alt := range patternAlts(arm.Pattern) {
			row := u.row(alt, len(cols), isTuple)
			if !u.useful(append(rows[:len(rows):len(rows)], armRows...), row, cols, maxPatternDepth) {
				c.reportUnreachable(alt, t)
			}
			armRows = append(armRows, row)
		}
		if arm.Guard == nil {
			rows = append(rows, armRows...)
			if c.catchAll(arm.Pattern) {
				wild = arm.Pattern
			}
		}
	}
	ws := u.witnesses(rows, cols, !isTuple, maxWitnesses+1, maxPatternDepth)
	if len(ws) == 0 {
		return true
	}
	more := len(ws) > maxWitnesses
	if more {
		ws = ws[:maxWitnesses]
	}
	switch t.Underlying().(type) {
	case *Enum, *Tuple:
		parts := make([]string, len(ws))
		for i, w := range ws {
			parts[i] = u.render(w, cols, isTuple)
		}
		list := strings.Join(parts, ", ")
		if more {
			list += ", ..."
		}
		c.errorf(diag.E2012, at, "non-exhaustive match on %s: missing %s", t, list)
	default:
		if isExpr {
			c.errorf(diag.E2012, at, "non-exhaustive match expression on %s: missing '_' arm", t)
		}
	}
	return false
}

// catchAll reports whether a (checked) top-level pattern is '_' or a
// binding (v0.2), which match every value.
func (c *Checker) catchAll(p ast.Pattern) bool {
	if _, ok := p.(*ast.WildcardPattern); ok {
		return true
	}
	return c.info.IsBindingPat(p)
}

// reportUnreachable reports an alternative that matches no value left by
// the earlier arms.
func (c *Checker) reportUnreachable(alt ast.Pattern, t Type) {
	switch p := alt.(type) {
	case *ast.WildcardPattern:
		if _, ok := t.Underlying().(*Enum); ok {
			c.errorf(diag.E2012, p.Range, "unreachable '_' arm: all variants of %s are covered", t)
		} else {
			c.errorf(diag.E2012, p.Range, "unreachable '_' arm: all values of %s are covered", t)
		}
		return
	case *ast.CtorPattern:
		if v := c.info.VariantAt(p); v != nil {
			if c.flatArgs(p.Args) {
				c.errorf(diag.E2012, p.Range, "unreachable pattern: variant %s is already covered", v.Name)
				return
			}
		} else if c.info.IsBindingPat(p) {
			if _, ok := t.Underlying().(*Enum); ok {
				c.errorf(diag.E2012, p.Range, "unreachable binding %s: all variants of %s are covered", p.Name.Name, t)
			} else {
				c.errorf(diag.E2012, p.Range, "unreachable binding %s: all values of %s are covered", p.Name.Name, t)
			}
			return
		} else {
			c.errorf(diag.E2012, p.Range, "unreachable pattern: duplicate case %s", exprString(p.Name))
			return
		}
	case *ast.LiteralPattern:
		c.errorf(diag.E2012, p.Range, "unreachable pattern: duplicate case %s", exprString(p.Lit))
		return
	}
	c.errorf(diag.E2012, alt.Span(), "unreachable pattern: it is covered by earlier patterns")
}

// flatArgs reports whether payload patterns are all bindings, '_' or '..'.
func (c *Checker) flatArgs(args []ast.Pattern) bool {
	for _, a := range args {
		switch a := a.(type) {
		case *ast.Ident:
			if c.info.VariantAt(a) != nil || c.info.PatConst(a) != nil {
				return false
			}
		case *ast.RestPattern:
		default:
			return false
		}
	}
	return true
}
