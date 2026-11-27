package golang

import (
	"fmt"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/ir"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

func (f *fnCtx) line(format string, args ...any) {
	fmt.Fprintf(f.w, format, args...)
	f.w.WriteByte('\n')
}

// body renders a function body including the scope safety-net prologue.
func (f *fnCtx) body(b *ast.BlockStmt) string {
	save := f.w
	f.w = &strings.Builder{}
	f.w.WriteString("{\n")
	if f.fn != nil {
		for i := range f.fn.Scopes {
			v := fmt.Sprintf("_fc_s%d", i)
			f.line("var %s *%s", v, f.g.rt("Scope"))
			f.line("defer func() {\nif %s != nil {\n%s.Wait()\n}\n}()", v, v)
		}
	}
	f.stmts(b.Stmts)
	f.w.WriteString("}")
	out := f.w.String()
	f.w = save
	return out
}

func (f *fnCtx) flushPending() {
	for _, p := range f.pending {
		f.line("%s", p)
	}
	f.pending = nil
}

func (f *fnCtx) stmts(list []ast.Stmt) {
	f.flushPending()
	for _, s := range ir.LiveStmts(f.g.info, list) {
		f.stmt(s)
	}
}

// block renders `{ stmts }`.
func (f *fnCtx) block(list []ast.Stmt) {
	f.w.WriteString("{\n")
	f.stmts(list)
	f.w.WriteString("}\n")
}

func (f *fnCtx) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case nil:
	case *ast.BlockStmt:
		f.block(s.Stmts)
	case *ast.DeclStmt:
		switch d := s.Decl.(type) {
		case *ast.VarDecl:
			f.varDecl(d)
		case *ast.ConstDecl:
			for _, sp := range d.Specs {
				if sym := f.g.p.Prog.Defs[sp.Name.ID()]; sym != nil {
					if txt := f.g.constDecl(sym); txt != "" {
						f.line("%s", txt)
					}
				}
			}
		}
	case *ast.ShortVarDecl, *ast.AssignStmt, *ast.IncDecStmt, *ast.ExprStmt:
		if txt := f.simple(s); txt != "" {
			f.line("%s", txt)
		}
		for _, p := range f.pending {
			f.line("%s", p)
		}
		f.pending = nil
	case *ast.DeferStmt:
		if txt := f.exprStmt(s.X); txt != "" {
			f.line("defer %s", txt)
		}
	case *ast.ReturnStmt:
		f.returnStmt(s)
	case *ast.IfStmt:
		f.ifStmt(s)
		f.w.WriteByte('\n')
	case *ast.ForStmt:
		f.forStmt(s)
	case *ast.MatchStmt:
		f.matchStmt(s)
	case *ast.ScopeStmt:
		f.scopeStmt(s)
	case *ast.BreakStmt:
		f.branch(s, true)
	case *ast.ContinueStmt:
		f.branch(s, false)
	default:
		f.g.fail(s, "unsupported statement %T", s)
	}
}

func (f *fnCtx) varDecl(d *ast.VarDecl) {
	sym := f.g.p.Prog.Defs[d.Name.ID()]
	name := "_"
	var t types.Type
	if sym != nil {
		name = f.g.p.Name(sym)
		t = f.g.info.SymType(sym)
	} else {
		t = f.typeOf(d.Value)
		if d.Type != nil {
			t = f.typeOf(d.Type)
		}
	}
	gt := f.goType(d, t)
	if d.Value == nil {
		f.line("var %s %s", name, gt)
	} else {
		f.line("var %s %s = %s", name, gt, f.expr(d.Value))
	}
	if sym != nil && name != "_" && !f.g.p.Used(sym) {
		f.line("_ = %s", name)
	}
}

// exprStmt renders an expression statement ("" if nothing remains, e.g.
// a folded comptime call).
func (f *fnCtx) exprStmt(e ast.Expr) string {
	if c, ok := unparen(e).(*ast.CallExpr); ok {
		if ci := f.g.info.CallOf(c); ci != nil && ci.Folded {
			return ""
		}
	}
	return f.expr(e)
}

// simple renders a simple statement on one line. `_ = x` statements for
// variables Go would consider unused are queued in f.pending.
func (f *fnCtx) simple(s ast.Stmt) string {
	switch s := s.(type) {
	case nil:
		return ""
	case *ast.ExprStmt:
		return f.exprStmt(s.X)
	case *ast.IncDecStmt:
		op := "--"
		if s.Inc {
			op = "++"
		}
		return f.lhs(s.X) + op
	case *ast.AssignStmt:
		l := make([]string, len(s.LHS))
		for i, x := range s.LHS {
			l[i] = f.lhs(x)
		}
		return strings.Join(l, ", ") + " " + s.Op.String() + " " + f.rhs(s.RHS, len(s.LHS))
	case *ast.ShortVarDecl:
		names := make([]string, len(s.Names))
		for i, id := range s.Names {
			if def := f.g.p.Prog.Defs[id.ID()]; def != nil {
				names[i] = f.g.p.Name(def)
				if names[i] != "_" && !f.g.p.Used(def) {
					f.pending = append(f.pending, "_ = "+names[i])
				}
			} else {
				names[i] = f.lhs(id)
			}
		}
		return strings.Join(names, ", ") + " := " + f.rhsDecl(s.Values, len(s.Names))
	}
	f.g.fail(s, "unsupported simple statement %T", s)
	return ""
}

func (f *fnCtx) lhs(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		if id.Name == "_" {
			return "_"
		}
		return f.ident(id)
	}
	return f.expr(e)
}

// rhsDecl renders the right-hand side of `:=`: literals at the top level
// get explicit types (Go would infer int/rune for them).
func (f *fnCtx) rhsDecl(rhs []ast.Expr, nl int) string {
	if len(rhs) == 1 && nl > 1 {
		return f.rhs(rhs, nl)
	}
	parts := make([]string, len(rhs))
	for i, r := range rhs {
		f.typedLits = true
		parts[i] = f.expr(unparen(r))
	}
	return strings.Join(parts, ", ")
}

func (f *fnCtx) rhs(rhs []ast.Expr, nl int) string {
	if len(rhs) == 1 && nl > 1 {
		if tv, ok := f.constValue(rhs[0]).(types.TupleVal); ok {
			return strings.Join(f.tupleValues(rhs[0], tv), ", ")
		}
	}
	parts := make([]string, len(rhs))
	for i, r := range rhs {
		parts[i] = f.expr(r)
	}
	return strings.Join(parts, ", ")
}

// activeScopes returns the scope variables of the enclosing scope blocks
// up to (not including) the first frame matching stop, innermost first.
func (f *fnCtx) activeScopes(stop func(*frame) bool) []string {
	var out []string
	for i := len(f.frames) - 1; i >= 0; i-- {
		fr := f.frames[i]
		if stop != nil && stop(fr) {
			break
		}
		if fr.kind == frameScope {
			out = append(out, fr.scopeVar)
		}
	}
	return out
}

func (f *fnCtx) returnStmt(s *ast.ReturnStmt) {
	scopes := f.activeScopes(nil)
	if len(scopes) == 0 || len(s.Results) == 0 {
		for _, sv := range scopes {
			f.line("%s.Wait()", sv)
		}
		switch {
		case len(s.Results) == 0:
			f.line("return")
		case len(s.Results) == 1 && len(f.fn.Info.Sig.Results) == 1:
			f.returnValue(s.Results[0])
		default:
			f.line("return %s", f.rhs(s.Results, len(f.fn.Info.Sig.Results)))
		}
		return
	}
	// Evaluate the results inside the scope, then wait, then return.
	want := f.fn.Info.Sig.Results
	var vals []string
	if len(s.Results) == 1 && len(want) > 1 {
		if tv, ok := f.constValue(s.Results[0]).(types.TupleVal); ok {
			vals = f.tupleValues(s.Results[0], tv)
		} else {
			for range want {
				vals = append(vals, f.newTemp())
			}
			f.line("%s := %s", strings.Join(vals, ", "), f.expr(s.Results[0]))
		}
	} else {
		for i, r := range s.Results {
			if f.constValue(r) != nil {
				vals = append(vals, f.expr(r))
				continue
			}
			t := f.newTemp()
			f.line("var %s %s = %s", t, f.goType(r, want[i]), f.expr(r))
			vals = append(vals, t)
		}
	}
	for _, sv := range scopes {
		f.line("%s.Wait()", sv)
	}
	f.line("return %s", strings.Join(vals, ", "))
}

func (f *fnCtx) ifStmt(s *ast.IfStmt) {
	// v0.2: variables bound by `is` in the condition are declared in a
	// block around the if statement.
	decls := f.isDecls(s.Cond)
	if len(decls) > 0 {
		f.w.WriteString("{\n")
		for _, d := range decls {
			f.line("%s", d)
		}
	}
	f.header++
	hdr := "if "
	if s.Init != nil {
		hdr += f.simple(s.Init) + "; "
	}
	hdr += f.expr(s.Cond)
	f.header--
	f.w.WriteString(hdr + " {\n")
	f.stmts(s.Then.Stmts)
	f.w.WriteString("}")
	switch e := s.Else.(type) {
	case nil:
	case *ast.IfStmt:
		f.w.WriteString(" else ")
		f.ifStmt(e)
	case *ast.BlockStmt:
		f.w.WriteString(" else {\n")
		f.stmts(e.Stmts)
		f.w.WriteString("}")
	}
	if len(decls) > 0 {
		f.w.WriteString("\n}")
	}
}

func (f *fnCtx) forStmt(s *ast.ForStmt) {
	f.header++
	var hdr string
	switch {
	case s.ThreeClause:
		init, cond, post := f.simple(s.Init), "", f.simple(s.Post)
		if s.Cond != nil {
			cond = f.expr(s.Cond)
		}
		hdr = "for " + init + "; " + cond + "; " + post + " "
	case s.Cond != nil:
		hdr = "for " + f.expr(s.Cond) + " "
	default:
		hdr = "for "
	}
	f.header--
	fr := &frame{kind: frameLoop}
	f.frames = append(f.frames, fr)
	save := f.w
	f.w = &strings.Builder{}
	f.stmts(s.Body.Stmts)
	body := f.w.String()
	f.w = save
	f.frames = f.frames[:len(f.frames)-1]
	if fr.label != "" {
		f.line("%s:", fr.label)
	}
	f.w.WriteString(hdr + "{\n" + body + "}\n")
}

// branch emits break/continue: scope blocks left by the jump are waited
// for first; a break out of a match (Go switch) is labeled.
func (f *fnCtx) branch(s ast.Stmt, isBreak bool) {
	var loop *frame
	crossedSwitch := false
	for i := len(f.frames) - 1; i >= 0; i-- {
		fr := f.frames[i]
		if fr.kind == frameLoop {
			loop = fr
			break
		}
		if fr.kind == frameSwitch {
			crossedSwitch = true
		}
		if fr.kind == frameScope {
			f.line("%s.Wait()", fr.scopeVar)
		}
	}
	if loop == nil {
		f.g.fail(s, "break/continue outside a loop")
	}
	kw := "continue"
	if isBreak {
		kw = "break"
	}
	if isBreak && crossedSwitch {
		if loop.label == "" {
			loop.label = fmt.Sprintf("_fc_L%d", *f.label)
			*f.label++
		}
		f.line("%s %s", kw, loop.label)
		return
	}
	f.line("%s", kw)
}

func (f *fnCtx) scopeStmt(s *ast.ScopeStmt) {
	sym := f.g.p.Prog.Defs[s.Name.ID()]
	name := "_"
	if sym != nil {
		name = f.g.p.Name(sym)
	}
	if name == "_" {
		name = f.newTemp()
	}
	f.w.WriteString("{\n")
	f.line("%s := %s", name, f.expr(s.Value))
	if idx := f.fn.ScopeIndex(s); idx >= 0 {
		f.line("_fc_s%d = %s", idx, name)
	}
	f.frames = append(f.frames, &frame{kind: frameScope, scopeVar: name})
	f.stmts(s.Body.Stmts)
	f.frames = f.frames[:len(f.frames)-1]
	if !ir.IsTerminatingList(f.g.info, s.Body.Stmts) {
		f.line("%s.Wait()", name)
	}
	f.w.WriteString("}\n")
}

func isPlainVar(f *fnCtx, e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	sym := f.g.p.Prog.Uses[id.ID()]
	return sym != nil && sym.Kind == resolve.SymVar && sym.Func != nil
}
