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

// v0.2 sum types: compact enum layout, match switches and match
// expressions. See docs/v0_2.md ("Sum types").
//
// Layout rule. An enum is a Go struct `{ _tag; slots }`. _tag is uint8
// (<= 256 variants), uint16 (<= 65536) or uint32. Payload slots are shared
// between variants by Go type: the payload Go types form groups in order
// of first appearance (variants in declaration order, payload positions
// left to right); a group has as many slots as the largest number of
// payloads of that type in any single variant, and each variant maps its
// payloads of a type to that group's slots in order. Slot j of a group is
// named `_<key>_<j>`, where key is the FinCore spelling of a basic type
// (u8, u32, usize, int, bool, string, error, ...), the Go type itself if it
// is an identifier (named types, type parameters), and otherwise the
// group's index (decimal, so it cannot clash with an identifier); a key
// already taken by an earlier group also falls back to the index. A
// constructor sets only the tag and its own slots (Go zeroes the rest), so
// struct == is variant equality.

// enumLayout is the Go representation of an enum type.
type enumLayout struct {
	tag    string      // Go type of _tag
	fields [][2]string // slot fields in order: name, Go type
	slots  [][]string  // per variant: slot field of each payload position
}

func tagType(en *types.Enum) string {
	switch {
	case len(en.Variants) <= 256:
		return "uint8"
	case len(en.Variants) <= 65536:
		return "uint16"
	}
	return "uint32"
}

// basicSlotKeys maps Go basic type names to the FinCore spelling used in
// slot names.
var basicSlotKeys = map[string]string{
	"uint8": "u8", "uint16": "u16", "uint32": "u32", "uint64": "u64",
	"int8": "i8", "int16": "i16", "int32": "i32", "int64": "i64",
	"int": "int", "uint": "usize", "bool": "bool", "string": "string", "error": "error",
}

func isGoIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// enumLayout returns the layout of enum type t (a named enum, an instance
// of a generic enum, or an enum literal). Instances use the layout of the
// generic declaration (Go type parameters; const parameters bound to the
// instance's values), because that is the struct Go sees.
func (g *gen) enumLayout(n ast.Node, t types.Type, e *env) *enumLayout {
	if nt, ok := t.(*types.Named); ok {
		if nt.Origin != nil {
			le := newEnv()
			if td := g.typeDeclOf(nt.Origin); td != nil && td.Mono() {
				var vals []uint64
				for i, tp := range nt.Origin.TypeParams {
					if tp.IsConst() {
						vals = append(vals, g.constArg(n, nt.TypeArgs[i], e))
					}
				}
				le.bind(td.ConstParams, vals)
			}
			return g.computeLayout(n, nt.Origin.Underlying().(*types.Enum), le)
		}
		return g.computeLayout(n, nt.Underlying().(*types.Enum), newEnv())
	}
	en, ok := t.Underlying().(*types.Enum)
	if !ok {
		g.fail(n, "enum layout of non-enum type %v", t)
	}
	return g.computeLayout(n, en, e)
}

func (g *gen) computeLayout(n ast.Node, en *types.Enum, e *env) *enumLayout {
	l := &enumLayout{tag: tagType(en), slots: make([][]string, len(en.Variants))}
	var gts []string // group Go types
	var counts []int
	pos := make([][][2]int, len(en.Variants)) // (group, slot) per payload
	for vi, v := range en.Variants {
		used := make([]int, len(gts)+len(v.Payload))
		for _, p := range v.Payload {
			gt := g.goType(n, p, e)
			gi := -1
			for k, s := range gts {
				if s == gt {
					gi = k
				}
			}
			if gi < 0 {
				gi = len(gts)
				gts = append(gts, gt)
				counts = append(counts, 0)
			}
			used[gi]++
			if used[gi] > counts[gi] {
				counts[gi] = used[gi]
			}
			pos[vi] = append(pos[vi], [2]int{gi, used[gi] - 1})
		}
	}
	keys := make([]string, len(gts))
	for gi, gt := range gts {
		k, ok := basicSlotKeys[gt]
		if !ok && isGoIdent(gt) {
			k = gt
		}
		for _, prev := range keys[:gi] {
			if prev == k {
				k = ""
			}
		}
		if k == "" {
			k = strconv.Itoa(gi)
		}
		keys[gi] = k
	}
	for gi, gt := range gts {
		for j := 0; j < counts[gi]; j++ {
			l.fields = append(l.fields, [2]string{fmt.Sprintf("_%s_%d", keys[gi], j), gt})
		}
	}
	for vi := range en.Variants {
		for _, gs := range pos[vi] {
			l.slots[vi] = append(l.slots[vi], fmt.Sprintf("_%s_%d", keys[gs[0]], gs[1]))
		}
	}
	return l
}

// enumStruct renders the Go struct type of an enum literal.
func (g *gen) enumStruct(n ast.Node, en *types.Enum, e *env) string {
	l := g.computeLayout(n, en, e)
	var b strings.Builder
	b.WriteString("struct {\n")
	fmt.Fprintf(&b, "_tag %s\n", l.tag)
	for _, f := range l.fields {
		fmt.Fprintf(&b, "%s %s\n", f[0], f[1])
	}
	b.WriteString("}")
	return b.String()
}

// variantLit renders an enum variant construction with np payload values
// rendered by payload(i). Order (it decides the discovery order of
// monomorphized instances): the layout, the payload values, the type.
func (g *gen) variantLit(n ast.Node, t types.Type, v *types.Variant, np int, payload func(i int) string, e *env) string {
	l := g.enumLayout(n, t, e)
	var parts []string
	if v.Index != 0 {
		parts = append(parts, fmt.Sprintf("_tag: %d", v.Index))
	}
	for i := 0; i < np; i++ {
		parts = append(parts, l.slots[v.Index][i]+": "+payload(i))
	}
	tn := g.goType(n, t, e)
	return tn + "{" + strings.Join(parts, ", ") + "}"
}

func (f *fnCtx) matchStmt(s *ast.MatchStmt) {
	body := func(a *ast.MatchArm) {
		if a.Expr != nil {
			if txt := f.exprStmt(a.Expr); txt != "" {
				f.line("%s", txt)
			}
		}
		if a.Block != nil {
			f.stmts(a.Block.Stmts)
		}
	}
	if !f.flatMatch(s.X, s.Arms) {
		f.matchChain(s, s.X, s.Arms, body)
		return
	}
	f.frames = append(f.frames, &frame{kind: frameSwitch})
	f.matchSwitch(s, s.X, s.Arms, body)
	f.frames = f.frames[:len(f.frames)-1]
}

// matchExpr renders a match expression as an immediately invoked function
// literal `func() T { switch ... { case ...: return arm } }()`; the switch
// always ends with a default case (match expressions are exhaustive).
func (f *fnCtx) matchExpr(e *ast.MatchExpr) string {
	save, saveHeader := f.w, f.header
	f.w = &strings.Builder{}
	f.header = 0
	f.w.WriteString("func() " + f.goType(e, f.typeOf(e)) + " {\n")
	f.matchArms(e, func(a *ast.MatchArm) {
		f.returnValue(a.Expr)
	})
	f.w.WriteString("}()")
	out := f.w.String()
	f.w, f.header = save, saveHeader
	return out
}

// matchArms emits a match expression's switch (or v0.2 if chain).
func (f *fnCtx) matchArms(e *ast.MatchExpr, body func(a *ast.MatchArm)) {
	if f.flatMatch(e.X, e.Arms) {
		f.matchSwitch(e, e.X, e.Arms, body)
	} else {
		f.matchChain(e, e.X, e.Arms, body)
	}
}

// returnValue emits `return e` (single result, no enclosing scope
// blocks). A match expression is lowered to a switch whose arms return
// their values (nested match expressions recursively), without a
// function literal.
func (f *fnCtx) returnValue(e ast.Expr) {
	if me, ok := unparen(e).(*ast.MatchExpr); ok && f.constValue(me) == nil {
		f.matchArms(me, func(a *ast.MatchArm) {
			f.returnValue(a.Expr)
		})
		return
	}
	f.line("return %s", f.expr(e))
}

// flatMatch reports whether a match is lowered to a Go switch: no tuple
// scrutinee, no guards, and every alternative is '_', a literal, a
// constant or a variant whose payload patterns are bindings, '_' or '..'
// (bindings only outside or-patterns). Other matches (v0.2) are lowered
// to if chains (matchChain).
func (f *fnCtx) flatMatch(x ast.Expr, arms []*ast.MatchArm) bool {
	if _, ok := x.(*ast.TupleExpr); ok {
		return false
	}
	for _, a := range arms {
		if a.Guard != nil {
			return false
		}
		op, isOr := a.Pattern.(*ast.OrPattern)
		alts := []ast.Pattern{a.Pattern}
		if isOr {
			alts = op.Alts
		}
		for _, alt := range alts {
			switch p := alt.(type) {
			case *ast.WildcardPattern, *ast.LiteralPattern:
			case *ast.CtorPattern:
				// v0.2: a top-level binding (or a bare variant whose
				// name is read as a binding) needs the if chain.
				if sym, _ := f.g.info.PatBinding(p); sym != nil && (f.g.info.VariantOf(p) == nil || f.g.p.Used(sym)) {
					return false
				}
				for _, arg := range p.Args {
					switch b := arg.(type) {
					case *ast.RestPattern:
					case *ast.Ident:
						if f.g.info.VariantAt(b) != nil || f.g.info.PatConst(b) != nil || (isOr && b.Name != "_") {
							return false
						}
					default:
						return false
					}
				}
			default:
				return false
			}
		}
	}
	return true
}

// matchSwitch emits the Go switch of a flat match m on x; body emits the
// body of an arm after its payload bindings.
func (f *fnCtx) matchSwitch(m ast.Node, x ast.Expr, all []*ast.MatchArm, body func(a *ast.MatchArm)) {
	arms := ir.MatchArms(f.g.info, x, all)
	hasDefault := arms.HasDefault || f.g.info.IsExhaustive(m)
	var lay *enumLayout
	needBind := false
	if arms.Enum != nil {
		lay = f.g.enumLayout(x, f.typeOf(x), f.env)
		for _, a := range arms.Arms {
			if cp, ok := a.Pattern.(*ast.CtorPattern); ok {
				for _, b := range cp.Args {
					if id, ok := b.(*ast.Ident); ok {
						if sym := f.g.p.Prog.Defs[id.ID()]; sym != nil && f.g.p.Used(sym) {
							needBind = true
						}
					}
				}
			}
		}
	}
	f.header++
	var base, hdr string
	switch {
	case arms.Enum == nil:
		hdr = "switch " + f.expr(x)
	case needBind && !isPlainVar(f, x):
		base = f.newTemp()
		hdr = "switch " + base + " := " + f.expr(x) + "; " + base + "._tag"
	default:
		base = f.primary(x)
		hdr = "switch " + base + "._tag"
	}
	f.header--
	f.w.WriteString(hdr + " {\n")
	for i, a := range arms.Arms {
		if i == len(arms.Arms)-1 && hasDefault {
			f.w.WriteString("default:\n")
		} else {
			f.w.WriteString("case " + f.caseLabels(x, a) + ":\n")
		}
		if cp, ok := a.Pattern.(*ast.CtorPattern); ok && arms.Enum != nil {
			if v := f.g.info.VariantOf(cp); v != nil {
				for j, b := range cp.Args {
					id, ok := b.(*ast.Ident)
					if !ok {
						continue
					}
					sym := f.g.p.Prog.Defs[id.ID()]
					if sym == nil || !f.g.p.Used(sym) {
						continue
					}
					f.line("%s := %s.%s", f.g.p.Name(sym), base, lay.slots[v.Index][types.PayloadIndex(cp, j, len(v.Payload))])
				}
			}
		}
		body(a)
	}
	f.w.WriteString("}\n")
}

// caseLabels renders the case list of an arm (one label per or-pattern
// alternative).
func (f *fnCtx) caseLabels(x ast.Expr, a *ast.MatchArm) string {
	op, ok := a.Pattern.(*ast.OrPattern)
	if !ok {
		return f.caseLabel(x, a, a.Pattern)
	}
	parts := make([]string, len(op.Alts))
	for i, alt := range op.Alts {
		parts[i] = f.caseLabel(x, a, alt)
	}
	return strings.Join(parts, ", ")
}

func (f *fnCtx) caseLabel(x ast.Expr, a *ast.MatchArm, pat ast.Pattern) string {
	switch p := pat.(type) {
	case *ast.CtorPattern:
		if v := f.g.info.VariantOf(p); v != nil {
			return fmt.Sprint(v.Index)
		}
		if v := f.g.info.ValueOf(p.Name); v != nil {
			return f.g.value(p, v, f.typeOf(x), f.env)
		}
	case *ast.LiteralPattern:
		if v := f.g.info.ValueOf(p.Lit); v != nil {
			return f.g.value(p, v, f.typeOf(x), f.env)
		}
	}
	f.g.fail(a, "unsupported match pattern")
	return ""
}

// ---------------------------------------------------------------------------
// v0.2 patterns: if chains and `is` expressions

// subject is the value a pattern is matched against: Go expressions
// (paths) and types, one per element of a tuple scrutinee.
type subject struct {
	paths []string
	types []types.Type
}

func single(path string, t types.Type) subject {
	return subject{paths: []string{path}, types: []types.Type{t}}
}

// patTest returns the Go condition under which the subject matches p ("" if
// it always does). compound is set if the condition is a conjunction of
// several tests; disjunctions are parenthesized.
func (f *fnCtx) patTest(p ast.Pattern, s subject) (cond string, compound bool) {
	switch p := p.(type) {
	case *ast.TuplePattern:
		var parts []string
		for i, el := range p.Elems {
			if c, _ := f.patTest(el, single(s.paths[i], s.types[i])); c != "" {
				parts = append(parts, c)
			}
		}
		return strings.Join(parts, " && "), len(parts) > 1
	case *ast.OrPattern:
		parts := make([]string, len(p.Alts))
		for i, alt := range p.Alts {
			c, comp := f.patTest(alt, s)
			if c == "" {
				return "", false
			}
			if comp {
				c = "(" + c + ")"
			}
			parts[i] = c
		}
		return "(" + strings.Join(parts, " || ") + ")", false
	case *ast.Ident:
		if v := f.g.info.VariantAt(p); v != nil {
			return s.paths[0] + "._tag == " + strconv.Itoa(v.Index), false
		}
		if cv := f.g.info.PatConst(p); cv != nil {
			return s.paths[0] + " == " + f.g.value(p, cv, s.types[0], f.env), false
		}
	case *ast.CtorPattern:
		t := s.types[0]
		v := f.g.info.VariantOf(p)
		if v == nil {
			if cv := f.g.info.PatConst(p); cv != nil {
				return s.paths[0] + " == " + f.g.value(p, cv, t, f.env), false
			}
			return "", false // a binding
		}
		parts := []string{s.paths[0] + "._tag == " + strconv.Itoa(v.Index)}
		lay := f.g.enumLayout(p, t, f.env)
		for i, a := range p.Args {
			if _, ok := a.(*ast.RestPattern); ok {
				continue
			}
			j := types.PayloadIndex(p, i, len(v.Payload))
			if c, _ := f.patTest(a, single(s.paths[0]+"."+lay.slots[v.Index][j], v.Payload[j])); c != "" {
				parts = append(parts, c)
			}
		}
		return strings.Join(parts, " && "), len(parts) > 1
	case *ast.LiteralPattern:
		return s.paths[0] + " == " + f.g.value(p, f.g.info.ValueOf(p.Lit), s.types[0], f.env), false
	}
	return "", false
}

// patBinding is a variable bound by a pattern: Go name and type.
type patBinding struct {
	sym  *resolve.Symbol
	name string
	typ  types.Type
}

// patBindings lists the bindings of p that are read in region, in source
// order (for or-patterns those of the first alternative).
func (f *fnCtx) patBindings(p ast.Pattern, t types.Type, region ast.Node, out *[]patBinding) {
	switch p := p.(type) {
	case *ast.TuplePattern:
		tt, _ := t.(*types.Tuple)
		for i, el := range p.Elems {
			f.patBindings(el, tt.Types[i], region, out)
		}
	case *ast.OrPattern:
		f.patBindings(p.Alts[0], t, region, out)
	case *ast.Ident:
		if sym := f.g.p.Prog.Defs[p.ID()]; sym != nil && f.bindUsed(sym, region) {
			*out = append(*out, patBinding{sym: sym, name: f.g.p.Name(sym), typ: t})
		}
	case *ast.CtorPattern:
		if sym, _ := f.g.info.PatBinding(p); sym != nil && f.bindUsed(sym, region) {
			*out = append(*out, patBinding{sym: sym, name: f.g.p.Name(sym), typ: t})
		}
		v := f.g.info.VariantOf(p)
		if v == nil {
			return
		}
		for i, a := range p.Args {
			if _, ok := a.(*ast.RestPattern); ok {
				continue
			}
			f.patBindings(a, v.Payload[types.PayloadIndex(p, i, len(v.Payload))], region, out)
		}
	}
}

// emitBinds emits the bindings of p that are read in region: `x := path`,
// or `x = path` if assign. An or-pattern with bindings declares them
// (`var x T`, unless assign) and assigns them in an if chain over its
// alternatives.
func (f *fnCtx) emitBinds(p ast.Pattern, s subject, region ast.Node, assign bool) {
	switch p := p.(type) {
	case *ast.TuplePattern:
		for i, el := range p.Elems {
			f.emitBinds(el, single(s.paths[i], s.types[i]), region, assign)
		}
	case *ast.OrPattern:
		var bs []patBinding
		f.patBindings(p, subjectType(s), region, &bs)
		if len(bs) == 0 {
			return
		}
		if !assign {
			for _, b := range bs {
				f.line("var %s %s", b.name, f.goType(p, b.typ))
			}
		}
		for i, alt := range p.Alts {
			c, _ := f.patTest(alt, s)
			last := i == len(p.Alts)-1 || c == ""
			switch {
			case i == 0 && last:
				f.w.WriteString("{\n")
			case i == 0:
				f.w.WriteString("if " + c + " {\n")
			case last:
				f.w.WriteString("} else {\n")
			default:
				f.w.WriteString("} else if " + c + " {\n")
			}
			f.emitBinds(alt, s, region, true)
			if last {
				break
			}
		}
		f.w.WriteString("}\n")
	case *ast.Ident:
		if sym := f.g.p.Prog.Defs[p.ID()]; sym != nil && f.bindUsed(sym, region) {
			op := ":="
			if assign {
				op = "="
			}
			f.line("%s %s %s", f.g.p.Name(sym), op, s.paths[0])
		}
	case *ast.CtorPattern:
		if sym, _ := f.g.info.PatBinding(p); sym != nil && f.bindUsed(sym, region) {
			op := ":="
			if assign {
				op = "="
			}
			f.line("%s %s %s", f.g.p.Name(sym), op, s.paths[0])
		}
		v := f.g.info.VariantOf(p)
		if v == nil {
			return
		}
		lay := f.g.enumLayout(p, s.types[0], f.env)
		for i, a := range p.Args {
			if _, ok := a.(*ast.RestPattern); ok {
				continue
			}
			j := types.PayloadIndex(p, i, len(v.Payload))
			f.emitBinds(a, single(s.paths[0]+"."+lay.slots[v.Index][j], v.Payload[j]), region, assign)
		}
	}
}

// subjectType is the type of a subject (a *types.Tuple for a tuple).
func subjectType(s subject) types.Type {
	if len(s.types) == 1 {
		return s.types[0]
	}
	return &types.Tuple{Types: s.types}
}

// scrutineeSubject evaluates the scrutinee of a general match (v0.2) into
// temporaries (`_fc_tN := x`, one per tuple element) unless every element
// is a local variable and no arm has a guard.
func (f *fnCtx) scrutineeSubject(x ast.Expr, arms []*ast.MatchArm) (subject, []string) {
	direct := true
	for _, a := range arms {
		if a.Guard != nil {
			direct = false
		}
	}
	elems := []ast.Expr{x}
	if te, ok := x.(*ast.TupleExpr); ok {
		elems = te.Elems
	}
	for _, el := range elems {
		if !isPlainVar(f, el) {
			direct = false
		}
	}
	var s subject
	var temps []string
	for _, el := range elems {
		s.types = append(s.types, f.typeOf(el))
		if direct {
			s.paths = append(s.paths, f.primary(el))
			continue
		}
		t := f.newTemp()
		f.line("%s := %s", t, f.expr(el))
		temps = append(temps, t)
		s.paths = append(s.paths, t)
	}
	return s, temps
}

// matchChain emits a general match m (v0.2: nested, tuple and or-patterns
// with bindings, guards) as an if chain; the last arm of an exhaustive
// match is the final else. Bindings read by a guard are bound in a
// function literal evaluated in the condition; those read by the body are
// bound at the start of the arm.
func (f *fnCtx) matchChain(m ast.Node, x ast.Expr, arms []*ast.MatchArm, body func(a *ast.MatchArm)) {
	exhaustive := f.g.info.IsExhaustive(m)
	save := f.w
	f.w = &strings.Builder{}
	s, temps := f.scrutineeSubject(x, arms)
	decl := f.w.String()
	f.w = &strings.Builder{}
	for i, a := range arms {
		cond, _ := f.patTest(a.Pattern, s)
		if a.Guard != nil {
			g := f.guardCond(a, s)
			if cond == "" {
				cond = g
			} else {
				cond += " && " + g
			}
		}
		isElse := i == len(arms)-1 && exhaustive && a.Guard == nil
		if cond == "" && !isElse {
			cond = "true"
		}
		switch {
		case i == 0 && isElse:
			f.w.WriteString("{\n")
		case i == 0:
			f.w.WriteString("if " + cond + " {\n")
		case isElse:
			f.w.WriteString("} else {\n")
		default:
			f.w.WriteString("} else if " + cond + " {\n")
		}
		var region ast.Node = a.Block
		if a.Expr != nil {
			region = a.Expr
		}
		f.emitBinds(a.Pattern, s, region, false)
		body(a)
	}
	if len(arms) > 0 {
		f.w.WriteString("}\n")
	}
	chain := f.w.String()
	f.w = save
	f.w.WriteString(decl)
	for _, t := range temps {
		if !strings.Contains(chain, t) {
			f.line("_ = %s", t)
		}
	}
	f.w.WriteString(chain)
}

// guardCond renders the guard of an arm; the bindings it reads are bound
// in a function literal: `func() bool { x := path; return guard }()`.
func (f *fnCtx) guardCond(a *ast.MatchArm, s subject) string {
	var bs []patBinding
	f.patBindings(a.Pattern, subjectType(s), a.Guard, &bs)
	if len(bs) == 0 {
		f.header++
		g := "(" + f.expr(a.Guard) + ")"
		f.header--
		return g
	}
	save, saveHeader := f.w, f.header
	f.w = &strings.Builder{}
	f.header = 0
	f.w.WriteString("func() bool {\n")
	f.emitBinds(a.Pattern, s, a.Guard, false)
	f.line("return %s", f.expr(a.Guard))
	f.w.WriteString("}()")
	out := f.w.String()
	f.w, f.header = save, saveHeader
	return out
}

// isExpr renders `X is P` (v0.2). Its bindings that are read are
// declared before the statement (hoistIs) and assigned here.
func (f *fnCtx) isExpr(e *ast.IsExpr) string {
	t := f.typeOf(e.X)
	var bs []patBinding
	f.patBindings(e.Pattern, t, nil, &bs)
	if len(bs) == 0 && (isPlainVar(f, e.X) || f.singleTest(e.Pattern)) {
		c, _ := f.patTest(e.Pattern, single(f.primary(e.X), t))
		if c == "" {
			return "true"
		}
		return "(" + c + ")"
	}
	save, saveHeader := f.w, f.header
	f.w = &strings.Builder{}
	f.header = 0
	f.w.WriteString("func() bool {\n")
	tmp := f.newTemp()
	f.line("%s := %s", tmp, f.expr(e.X))
	s := single(tmp, t)
	c, _ := f.patTest(e.Pattern, s)
	switch {
	case len(bs) == 0 && c == "":
		f.line("_ = %s", tmp)
		f.line("return true")
	case len(bs) == 0:
		f.line("return %s", c)
	case c == "":
		f.emitBinds(e.Pattern, s, nil, true)
		f.line("return true")
	default:
		f.w.WriteString("if " + c + " {\n")
		f.emitBinds(e.Pattern, s, nil, true)
		f.line("return true")
		f.w.WriteString("}\n")
		f.line("return false")
	}
	f.w.WriteString("}()")
	out := f.w.String()
	f.w, f.header = save, saveHeader
	return out
}

// singleTest reports whether the test of p reads the subject once: a
// literal, a constant or a variant whose payload patterns are bindings,
// '_' or '..'.
func (f *fnCtx) singleTest(p ast.Pattern) bool {
	switch p := p.(type) {
	case *ast.LiteralPattern:
		return true
	case *ast.CtorPattern:
		for _, a := range p.Args {
			switch b := a.(type) {
			case *ast.RestPattern:
			case *ast.Ident:
				if f.g.info.VariantAt(b) != nil || f.g.info.PatConst(b) != nil {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	return false
}

// bindUsed reports whether a binding is read in region (anywhere if
// region is nil: the bindings of an `is` expression).
func (f *fnCtx) bindUsed(sym *resolve.Symbol, region ast.Node) bool {
	if region == nil {
		return f.g.p.Used(sym)
	}
	return f.g.p.UsedIn(region, sym)
}

// hoistIs declares, before statement s, the variables bound by the `is`
// expressions of s that are read anywhere (v0.2 flow bindings): `var
// _fc_tN_x T`, in source order. Each gets a fresh temporary name, so the
// declarations can neither clash with nor shadow other variables; the
// is test assigns them (isExpr). The expressions of s are those outside
// its nested blocks and function literals, including the conditions of
// an else-if chain and a for statement's init, condition and post.
func (f *fnCtx) hoistIs(s ast.Stmt) {
	var exprs []ast.Expr
	var parts func(s ast.Stmt)
	parts = func(s ast.Stmt) {
		switch s := s.(type) {
		case *ast.DeclStmt:
			if d, ok := s.Decl.(*ast.VarDecl); ok {
				exprs = append(exprs, d.Value)
			}
		case *ast.ShortVarDecl:
			exprs = append(exprs, s.Values...)
		case *ast.AssignStmt:
			exprs = append(append(exprs, s.LHS...), s.RHS...)
		case *ast.IncDecStmt:
			exprs = append(exprs, s.X)
		case *ast.ExprStmt:
			exprs = append(exprs, s.X)
		case *ast.DeferStmt:
			exprs = append(exprs, s.X)
		case *ast.ReturnStmt:
			exprs = append(exprs, s.Results...)
		case *ast.IfStmt:
			parts(s.Init)
			exprs = append(exprs, s.Cond)
			if e, ok := s.Else.(*ast.IfStmt); ok {
				parts(e)
			}
		case *ast.ForStmt:
			parts(s.Init)
			exprs = append(exprs, s.Cond)
			parts(s.Post)
		case *ast.MatchStmt:
			exprs = append(exprs, s.X)
			for _, a := range s.Arms {
				exprs = append(exprs, a.Guard, a.Expr)
			}
		case *ast.ScopeStmt:
			exprs = append(exprs, s.Value)
		}
	}
	parts(s)
	for _, e := range exprs {
		if e == nil {
			continue
		}
		ast.Inspect(e, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.IsExpr:
				var bs []patBinding
				f.patBindings(n.Pattern, f.typeOf(n.X), nil, &bs)
				for _, b := range bs {
					name := f.newTemp() + "_" + b.sym.Name
					f.g.p.SetName(b.sym, name)
					f.line("var %s %s", name, f.goType(n, b.typ))
				}
			}
			return true
		})
	}
}
