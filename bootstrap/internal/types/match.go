package types

import (
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
)

// Flat sum-type and scalar pattern matching: match statements and single-value
// match expressions, variant payload bindings, literals/constants, or-patterns,
// and straightforward exhaustiveness/reachability checking (E2012).

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

func (c *Checker) scrutinee(e ast.Expr) operand {
	x := c.exprValue(e)
	if !x.invalid() && IsUntyped(x.typ) {
		c.defaultType(&x, "match")
	}
	return x
}

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

func (c *Checker) selectArm(v Value, arms []*ast.MatchArm) int {
	for i, arm := range arms {
		if MatchValue(c.info, arm.Pattern, v, nil) {
			return i
		}
	}
	return -1
}

// MatchValue reports whether v matches checked pattern p, calling bind (if
// non-nil) for any payload bindings.
func MatchValue(info *Info, p ast.Pattern, v Value, bind func(sym *resolve.Symbol, v Value)) bool {
	switch p := p.(type) {
	case *ast.WildcardPattern:
		return true
	case *ast.Ident:
		if p.Name == "_" {
			return true
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
			for i, a := range p.Args {
				if !MatchValue(info, a, ev.Payload[i], bind) {
					return false
				}
			}
			return true
		}
		if cv := info.PatConst(p); cv != nil {
			return valueEq(v, cv)
		}
		return false
	case *ast.LiteralPattern:
		return valueEq(v, info.ValueOf(p.Lit))
	case *ast.OrPattern:
		for _, a := range p.Alts {
			if MatchValue(info, a, v, bind) {
				return true
			}
		}
		return false
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

func patternAlts(p ast.Pattern) []ast.Pattern {
	if op, ok := p.(*ast.OrPattern); ok {
		return op.Alts
	}
	return []ast.Pattern{p}
}

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
	}
}

func (c *Checker) invalidBindings(p ast.Pattern) {
	walkPattern(p, func(q ast.Pattern) {
		if sym, _ := c.info.PatBinding(q); sym != nil && c.info.SymType(sym) == nil {
			c.setSymType(sym, Typ[Invalid])
		}
	})
}

func (c *Checker) matchPatterns(m ast.Node, at diag.SourceSpan, x operand, arms []*ast.MatchArm, isExpr bool) bool {
	valid := !x.invalid()
	for _, arm := range arms {
		if x.invalid() {
			c.invalidBindings(arm.Pattern)
		} else if !c.topPattern(arm.Pattern, x.typ) {
			valid = false
		}
	}
	_, isEnum := x.typ.Underlying().(*Enum)
	if !valid {
		exh := isEnum
		for _, arm := range arms {
			if _, ok := arm.Pattern.(*ast.WildcardPattern); ok {
				exh = true
			}
		}
		c.info.Exhaustive[m.ID()] = exh
		return false
	}
	exhaustive := c.checkCoverage(at, x.typ, arms, isExpr)
	c.info.Exhaustive[m.ID()] = exhaustive || isEnum
	return true
}

type binding struct {
	sym *resolve.Symbol
	id  *ast.Ident
	typ Type
}

func (c *Checker) topPattern(p ast.Pattern, t Type) bool {
	var bs []binding
	ok := c.pat(p, t, &bs)
	bs = c.uniqBindings(bs)
	for _, b := range bs {
		if c.info.SymType(b.sym) == nil {
			c.setSymType(b.sym, b.typ)
		}
	}
	c.invalidBindings(p)
	return ok
}

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

func (c *Checker) pat(p ast.Pattern, t Type, out *[]binding) bool {
	switch p := p.(type) {
	case *ast.WildcardPattern:
		return true
	case *ast.LiteralPattern:
		x := c.exprValue(p.Lit)
		if x.invalid() {
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
		return c.orPat(p, t, out)
	}
	return true
}

func (c *Checker) orPat(p *ast.OrPattern, t Type, out *[]binding) bool {
	valid := true
	var first []binding
	haveFirst := false
	for i, alt := range p.Alts {
		if w, ok := alt.(*ast.WildcardPattern); ok {
			c.errorf(diag.E2002, w.Range, "'_' cannot be an alternative of an or-pattern")
			valid = false
			continue
		}
		var bs []binding
		if !c.pat(alt, t, &bs) {
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

func (c *Checker) constPat(p ast.Pattern, name *ast.Ident, sym *resolve.Symbol, t Type, parens bool) bool {
	if parens {
		c.errorf(diag.E2002, p.Span(), "constant %s cannot have bindings", sym.Name)
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

func (c *Checker) ctorPat(p *ast.CtorPattern, t Type, out *[]binding) bool {
	en, isEnum := t.Underlying().(*Enum)
	isVariant := p.Qual == nil && isEnum && en.VariantByName(p.Name.Name) != nil
	if sym := c.prog.Uses[p.Name.ID()]; sym != nil && sym.Kind == resolve.SymConst && !isVariant {
		return c.constPat(p, p.Name, sym, t, p.HasParens)
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
	if len(p.Args) != len(v.Payload) || (p.HasParens && len(v.Payload) == 0) {
		c.errorf(diag.E2003, p.Range, "variant %s has %d payload values, pattern binds %d", v.Name, len(v.Payload), len(p.Args))
		return false
	}
	for i, a := range p.Args {
		id, ok := a.(*ast.Ident)
		if !ok {
			c.errorf(diag.E2002, a.Span(), "expected identifier in variant pattern")
			return false
		}
		pt := v.Payload[i]
		c.info.Types[id.ID()] = pt
		if id.Name == "_" {
			continue
		}
		if sym := c.prog.Defs[id.ID()]; sym != nil {
			*out = append(*out, binding{sym: sym, id: id, typ: pt})
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Flat reachability and exhaustiveness

func (c *Checker) checkCoverage(at diag.SourceSpan, t Type, arms []*ast.MatchArm, isExpr bool) bool {
	en, isEnum := t.Underlying().(*Enum)
	isBool := IsBoolean(t)
	var seenVar []bool
	if isEnum {
		seenVar = make([]bool, len(en.Variants))
	}
	var seenBool [2]bool
	var seenVals []Value
	hasWild := false

	allCovered := func() bool {
		if hasWild {
			return true
		}
		if isEnum {
			for _, s := range seenVar {
				if !s {
					return false
				}
			}
			return true
		}
		if isBool {
			return seenBool[0] && seenBool[1]
		}
		return false
	}

	for _, arm := range arms {
		if hasWild {
			c.errorf(diag.E2012, arm.Pattern.Span(), "unreachable match arm: it follows the '_' arm")
			continue
		}
		for _, alt := range patternAlts(arm.Pattern) {
			switch a := alt.(type) {
			case *ast.WildcardPattern:
				if allCovered() {
					if isEnum {
						c.errorf(diag.E2012, a.Range, "unreachable '_' arm: all variants of %s are covered", t)
					} else {
						c.errorf(diag.E2012, a.Range, "unreachable '_' arm: all values of %s are covered", t)
					}
				}
				hasWild = true
			case *ast.CtorPattern:
				if v := c.info.VariantAt(a); v != nil && isEnum {
					if seenVar[v.Index] {
						c.errorf(diag.E2012, a.Range, "unreachable pattern: variant %s is already covered", v.Name)
					}
					seenVar[v.Index] = true
				} else if cv := c.info.PatConst(a); cv != nil {
					if isBool {
						idx := 0
						if b, ok := cv.(BoolVal); ok && bool(b) {
							idx = 1
						}
						if seenBool[idx] {
							c.errorf(diag.E2012, a.Range, "unreachable pattern: duplicate case %s", ast.Format(a))
						}
						seenBool[idx] = true
					} else {
						dup := false
						for _, prev := range seenVals {
							if valueEq(prev, cv) {
								dup = true
								break
							}
						}
						if dup {
							c.errorf(diag.E2012, a.Range, "unreachable pattern: duplicate case %s", ast.Format(a))
						} else {
							seenVals = append(seenVals, cv)
						}
					}
				}
			case *ast.LiteralPattern:
				lv := c.info.ValueOf(a.Lit)
				if lv == nil {
					continue
				}
				if isBool {
					idx := 0
					if b, ok := lv.(BoolVal); ok && bool(b) {
						idx = 1
					}
					if seenBool[idx] {
						c.errorf(diag.E2012, a.Range, "unreachable pattern: duplicate case %s", ast.Format(a.Lit))
					}
					seenBool[idx] = true
				} else {
					dup := false
					for _, prev := range seenVals {
						if valueEq(prev, lv) {
							dup = true
							break
						}
					}
					if dup {
						c.errorf(diag.E2012, a.Range, "unreachable pattern: duplicate case %s", ast.Format(a.Lit))
					} else {
						seenVals = append(seenVals, lv)
					}
				}
			}
		}
	}

	if allCovered() {
		return true
	}
	if isEnum {
		var missing []string
		for i, v := range en.Variants {
			if !seenVar[i] {
				missing = append(missing, v.Name)
			}
		}
		c.errorf(diag.E2012, at, "non-exhaustive match on %s: missing %s", t, strings.Join(missing, ", "))
	} else if isExpr {
		c.errorf(diag.E2012, at, "non-exhaustive match expression on %s: missing '_' arm", t)
	}
	return false
}
