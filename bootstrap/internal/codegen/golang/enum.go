package golang

import (
	"fmt"
	"strconv"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/ir"
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
	f.frames = append(f.frames, &frame{kind: frameSwitch})
	f.matchSwitch(s.X, s.Arms, func(a *ast.MatchArm) {
		if a.Expr != nil {
			if txt := f.exprStmt(a.Expr); txt != "" {
				f.line("%s", txt)
			}
		}
		if a.Block != nil {
			f.stmts(a.Block.Stmts)
		}
	})
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
	f.matchSwitch(e.X, e.Arms, func(a *ast.MatchArm) {
		f.returnValue(a.Expr)
	})
	f.w.WriteString("}()")
	out := f.w.String()
	f.w, f.header = save, saveHeader
	return out
}

// returnValue emits `return e` (single result, no enclosing scope
// blocks). A match expression is lowered to a switch whose arms return
// their values (nested match expressions recursively), without a
// function literal.
func (f *fnCtx) returnValue(e ast.Expr) {
	if me, ok := unparen(e).(*ast.MatchExpr); ok && f.constValue(me) == nil {
		f.matchSwitch(me.X, me.Arms, func(a *ast.MatchArm) {
			f.returnValue(a.Expr)
		})
		return
	}
	f.line("return %s", f.expr(e))
}

// matchSwitch emits the Go switch of a match on x; body emits the body of
// an arm after its payload bindings.
func (f *fnCtx) matchSwitch(x ast.Expr, all []*ast.MatchArm, body func(a *ast.MatchArm)) {
	arms := ir.MatchArms(f.g.info, x, all)
	var lay *enumLayout
	needBind := false
	if arms.Enum != nil {
		lay = f.g.enumLayout(x, f.typeOf(x), f.env)
		for _, a := range arms.Arms {
			if cp, ok := a.Pattern.(*ast.CtorPattern); ok {
				for _, b := range cp.Bindings {
					if sym := f.g.p.Prog.Defs[b.ID()]; sym != nil && f.g.p.Used(sym) {
						needBind = true
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
		if i == len(arms.Arms)-1 && arms.HasDefault {
			f.w.WriteString("default:\n")
		} else {
			f.w.WriteString("case " + f.caseLabels(x, a) + ":\n")
		}
		if cp, ok := a.Pattern.(*ast.CtorPattern); ok && arms.Enum != nil {
			if v := f.g.info.VariantOf(cp); v != nil {
				for j, b := range cp.Bindings {
					sym := f.g.p.Prog.Defs[b.ID()]
					if sym == nil || !f.g.p.Used(sym) {
						continue
					}
					f.line("%s := %s.%s", f.g.p.Name(sym), base, lay.slots[v.Index][j])
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
