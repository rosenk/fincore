package types

import (
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
)

// v0.2 sum types: match statements and expressions, or-patterns,
// exhaustiveness and reachability (E2012). See docs/v0_2.md.

func (c *Checker) matchStmt(s *ast.MatchStmt) {
	x := c.exprValue(s.X)
	if !x.invalid() && IsUntyped(x.typ) {
		c.defaultType(&x, "match")
	}
	c.matchPatterns(s.Range, x, s.Arms, false)
	for _, arm := range s.Arms {
		if arm.Expr != nil {
			c.exprStmt(arm.Expr, "match arm")
		}
		if arm.Block != nil {
			c.stmtList(arm.Block.Stmts)
		}
	}
}

// matchExpr checks a match expression. The arms' types must agree;
// untyped constant arms take the type of the typed arms. If every arm is
// untyped the result is untyped and is converted by its context (see
// updateType). The result is constant if the scrutinee is constant and the
// selected arm is a constant expression.
func (c *Checker) matchExpr(e *ast.MatchExpr) operand {
	x := c.exprValue(e.X)
	if !x.invalid() && IsUntyped(x.typ) {
		c.defaultType(&x, "match")
	}
	valid := c.matchPatterns(e.Range, x, e.Arms, true)
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
// scrutinee value v (-1 if none).
func (c *Checker) selectArm(v Value, arms []*ast.MatchArm) int {
	for i, arm := range arms {
		for _, alt := range patternAlts(arm.Pattern) {
			switch p := alt.(type) {
			case *ast.WildcardPattern:
				return i
			case *ast.CtorPattern:
				if vr := c.info.VariantOf(p); vr != nil {
					if ev, ok := v.(*EnumVal); ok && ev.Variant == vr.Index {
						return i
					}
				} else if pv := c.info.ValueOf(p.Name); pv != nil {
					if eq, err := Equal(v, pv); err == nil && eq {
						return i
					}
				}
			case *ast.LiteralPattern:
				if pv := c.info.ValueOf(p.Lit); pv != nil {
					if eq, err := Equal(v, pv); err == nil && eq {
						return i
					}
				}
			}
		}
	}
	return -1
}

// patternAlts returns the alternatives of a pattern (itself unless it is
// an or-pattern).
func patternAlts(p ast.Pattern) []ast.Pattern {
	if op, ok := p.(*ast.OrPattern); ok {
		return op.Alts
	}
	return []ast.Pattern{p}
}

// matchPatterns checks the patterns of a match on x and reports E2012 for
// unreachable arms and non-exhaustive matches. It reports whether every
// pattern is valid. at is the span of the match (the `match` keyword).
func (c *Checker) matchPatterns(at diag.SourceSpan, x operand, arms []*ast.MatchArm, isExpr bool) bool {
	valid := !x.invalid()
	for _, arm := range arms {
		if x.invalid() {
			if cp, ok := arm.Pattern.(*ast.CtorPattern); ok {
				for _, b := range cp.Bindings {
					c.setSymType(c.prog.Defs[b.ID()], Typ[Invalid])
				}
			}
			continue
		}
		if !c.armPattern(arm.Pattern, x.typ) {
			valid = false
		}
	}
	if !valid {
		return false
	}
	en, isEnum := x.typ.Underlying().(*Enum)
	var covered []bool
	if isEnum {
		covered = make([]bool, len(en.Variants))
	}
	allCovered := func() bool {
		for _, cv := range covered {
			if !cv {
				return false
			}
		}
		return true
	}
	var seen []Value
	wild := false
	for _, arm := range arms {
		if wild {
			c.errorf(diag.E2012, arm.Pattern.Span(), "unreachable match arm: it follows the '_' arm")
			continue
		}
		for _, alt := range patternAlts(arm.Pattern) {
			vi := -1
			var val Value
			var lit ast.Expr
			switch p := alt.(type) {
			case *ast.WildcardPattern:
				if isEnum && len(en.Variants) > 0 && allCovered() {
					c.errorf(diag.E2012, p.Range, "unreachable '_' arm: all variants of %s are covered", x.typ)
				}
				wild = true
				continue
			case *ast.CtorPattern:
				if v := c.info.VariantOf(p); v != nil {
					vi = v.Index
				} else {
					val, lit = c.info.ValueOf(p.Name), p.Name
				}
			case *ast.LiteralPattern:
				val, lit = c.info.ValueOf(p.Lit), p.Lit
			}
			if vi >= 0 {
				if covered[vi] {
					c.errorf(diag.E2012, alt.Span(), "unreachable pattern: variant %s is already covered", en.Variants[vi].Name)
				}
				covered[vi] = true
				continue
			}
			if val == nil {
				continue
			}
			dup := false
			for _, s := range seen {
				if eq, err := Equal(s, val); err == nil && eq {
					dup = true
				}
			}
			if dup {
				c.errorf(diag.E2012, alt.Span(), "unreachable pattern: duplicate case %s", exprString(lit))
				continue
			}
			seen = append(seen, val)
		}
	}
	if wild {
		return true
	}
	if isEnum {
		var missing []string
		for i, v := range en.Variants {
			if !covered[i] {
				missing = append(missing, v.Name)
			}
		}
		if len(missing) > 0 {
			c.errorf(diag.E2012, at, "non-exhaustive match on %s: missing %s", x.typ, strings.Join(missing, ", "))
		}
	} else if isExpr {
		c.errorf(diag.E2012, at, "non-exhaustive match expression on %s: missing '_' arm", x.typ)
	}
	return true
}

// armPattern checks an arm's pattern (or each alternative of an
// or-pattern) against the scrutinee type t and reports whether it is
// valid. Or-pattern alternatives may not be '_' and may not bind names.
func (c *Checker) armPattern(p ast.Pattern, t Type) bool {
	op, ok := p.(*ast.OrPattern)
	if !ok {
		return c.pattern(p, t)
	}
	valid := true
	for _, alt := range op.Alts {
		if w, ok := alt.(*ast.WildcardPattern); ok {
			c.errorf(diag.E2002, w.Range, "'_' cannot be an alternative of an or-pattern")
			valid = false
			continue
		}
		if !c.pattern(alt, t) {
			valid = false
			continue
		}
		if cp, ok := alt.(*ast.CtorPattern); ok {
			for _, b := range cp.Bindings {
				if b.Name != "_" {
					c.errorf(diag.E2002, b.Range, "or-pattern alternatives cannot bind variables (%s)", b.Name)
					valid = false
					break
				}
			}
		}
	}
	return valid
}
