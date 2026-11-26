package totality

import (
	"fmt"
	"slices"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// A structured, path-sensitive walk over a function or loop body.
//
// The abstract state holds
//   - lb: proven lower bounds (>= 1) of stable integer variables, learned
//     from branch conditions (`if n == 0 { return }` gives n >= 1 after the
//     if) and killed by assignments;
//   - shrunk: the T2 slice was strictly shortened on the current path;
//   - dec: for T3 loops, the measure is proven smaller than at the start
//     of the iteration (recomputed after every write of a measure
//     variable, so that a join of "lo increased" and "hi decreased" keeps
//     the fact "hi - lo decreased");
//   - bnd: for T3 loops, bounds of local variables relative to the values
//     of the measure variables at the start of the iteration (see eb).
//
// Joins (after if/else, match) intersect facts. A nested loop is analysed
// with the facts about variables it writes removed, and its exit state is
// its entry state (the body may run zero times). `return`, `break`,
// `continue` and calls of panic end a path; `continue` and the end of the
// analysed loop body are reported to onExit.

type bound struct {
	term   string // "v<SymID>": a variable's value at iteration start; "c<n>": constant n; "l<SymID>": len of an invariant slice
	strict bool
}

type bnds struct{ lo, hi []bound }

type state struct {
	dead   bool
	lb     map[*resolve.Symbol]uint64
	shrunk bool
	dec    bool // T3: the measure is proven smaller than at iteration start
	bnd    map[*resolve.Symbol]bnds
}

func newState() *state {
	return &state{lb: map[*resolve.Symbol]uint64{}, bnd: map[*resolve.Symbol]bnds{}}
}

func (s *state) clone() *state {
	n := &state{dead: s.dead, shrunk: s.shrunk, dec: s.dec, lb: make(map[*resolve.Symbol]uint64, len(s.lb)), bnd: make(map[*resolve.Symbol]bnds, len(s.bnd))}
	for k, v := range s.lb {
		n.lb[k] = v
	}
	for k, v := range s.bnd {
		n.bnd[k] = v
	}
	return n
}

func join(a, b *state) *state {
	if a.dead {
		return b
	}
	if b.dead {
		return a
	}
	n := newState()
	n.shrunk = a.shrunk && b.shrunk
	n.dec = a.dec && b.dec
	for k, v := range a.lb {
		if w, ok := b.lb[k]; ok {
			n.lb[k] = min(v, w)
		}
	}
	for k, v := range a.bnd {
		if w, ok := b.bnd[k]; ok {
			n.bnd[k] = bnds{lo: meetBounds(v.lo, w.lo), hi: meetBounds(v.hi, w.hi)}
		}
	}
	return n
}

func meetBounds(a, b []bound) []bound {
	var out []bound
	for _, x := range a {
		for _, y := range b {
			if x.term == y.term {
				out = append(out, bound{x.term, x.strict && y.strict})
			}
		}
	}
	return out
}

func addBound(list []bound, nb bound) []bound {
	for i, b := range list {
		if b.term == nb.term {
			if nb.strict && !b.strict {
				list = slices.Clone(list)
				list[i].strict = true
			}
			return list
		}
	}
	return append(slices.Clone(list), nb)
}

type walker struct {
	c     *checker
	rf    *rootFacts
	depth int // loop nesting relative to the analysed loop body

	trackBounds bool
	lenTerm     func(s *resolve.Symbol) bool // len(s) is invariant (T3 loops)

	onCall   func(call *ast.CallExpr, st *state)
	onAssign func(lhs, rhs ast.Expr, pre, post *state) // rhs nil if unknown
	onExit   func(n ast.Node, st *state)
}

func (w *walker) stmts(list []ast.Stmt, st *state) *state {
	for _, s := range list {
		st = w.stmt(s, st)
	}
	return st
}

func (w *walker) stmt(s ast.Stmt, st *state) *state {
	if st.dead || s == nil {
		return st
	}
	switch s := s.(type) {
	case *ast.BlockStmt:
		return w.stmts(s.Stmts, st)
	case *ast.ExprStmt:
		w.expr(s.X, st)
		if w.isPanic(s.X) {
			st.dead = true
		}
	case *ast.ShortVarDecl:
		for _, v := range s.Values {
			w.expr(v, st)
		}
		lhs := make([]ast.Expr, len(s.Names))
		for i, id := range s.Names {
			lhs[i] = id
		}
		return w.assign(lhs, s.Values, ast.Assign, st)
	case *ast.DeclStmt:
		if vd, ok := s.Decl.(*ast.VarDecl); ok {
			var vals []ast.Expr
			if vd.Value != nil {
				w.expr(vd.Value, st)
				vals = []ast.Expr{vd.Value}
			}
			return w.assign([]ast.Expr{vd.Name}, vals, ast.Assign, st)
		}
	case *ast.AssignStmt:
		for _, v := range s.RHS {
			w.expr(v, st)
		}
		for _, l := range s.LHS {
			w.lhsExpr(l, st)
		}
		return w.assign(s.LHS, s.RHS, s.Op, st)
	case *ast.IncDecStmt:
		w.lhsExpr(s.X, st)
		op := ast.AddAssign
		if !s.Inc {
			op = ast.SubAssign
		}
		return w.assign([]ast.Expr{s.X}, []ast.Expr{nil}, op, st)
	case *ast.IfStmt:
		st = w.stmt(s.Init, st)
		if st.dead {
			return st
		}
		w.expr(s.Cond, st)
		th := st.clone()
		w.cond(s.Cond, true, th)
		th = w.stmt(s.Then, th)
		el := st.clone()
		w.cond(s.Cond, false, el)
		if s.Else != nil {
			el = w.stmt(s.Else, el)
		}
		return join(th, el)
	case *ast.ForStmt:
		return w.nestedLoop(s, st)
	case *ast.MatchStmt:
		w.expr(s.X, st)
		var out *state
		for _, arm := range s.Arms {
			a := st.clone()
			w.killPattern(arm.Pattern, a)
			if arm.Expr != nil {
				w.expr(arm.Expr, a)
				if w.isPanic(arm.Expr) {
					a.dead = true
				}
			}
			if arm.Block != nil {
				a = w.stmt(arm.Block, a)
			}
			if out == nil {
				out = a
			} else {
				out = join(out, a)
			}
		}
		if out == nil || !w.c.exhaustive(s) {
			if out == nil {
				return st
			}
			out = join(out, st)
		}
		return out
	case *ast.ScopeStmt:
		w.expr(s.Value, st)
		w.kill(w.c.info.SymbolOf(s.Name), st)
		return w.stmt(s.Body, st)
	case *ast.DeferStmt:
		w.expr(s.X, st)
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			w.expr(r, st)
		}
		st.dead = true
	case *ast.BreakStmt:
		st.dead = true
	case *ast.ContinueStmt:
		if w.depth == 0 && w.onExit != nil {
			w.onExit(s, st)
		}
		st.dead = true
	}
	return st
}

func (w *walker) killPattern(p ast.Pattern, st *state) {
	if cp, ok := p.(*ast.CtorPattern); ok {
		for _, b := range cp.Bindings {
			w.kill(w.c.info.SymbolOf(b), st)
		}
	}
}

func (w *walker) nestedLoop(f *ast.ForStmt, st *state) *state {
	st = w.stmt(f.Init, st)
	if st.dead {
		return st
	}
	writes, _ := writesAndCalls(f.Body, f.Post)
	for _, l := range writes {
		w.kill(w.c.storageRoot(l), st)
	}
	if w.onAssign != nil {
		for _, l := range writes {
			w.onAssign(l, nil, st, st)
		}
	}
	if f.Cond != nil {
		w.expr(f.Cond, st)
	}
	body := st.clone()
	if f.Cond != nil {
		w.cond(f.Cond, true, body)
	}
	w.depth++
	end := w.stmt(f.Body, body)
	_ = w.stmt(f.Post, st.clone())
	w.depth--
	_ = end
	return st
}

// expr reports the calls in e (not inside function literals), refining
// the state across && and ||.
func (w *walker) expr(e ast.Expr, st *state) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ast.FuncLit:
		return
	case *ast.BinaryExpr:
		if x.Op == ast.OpLAnd || x.Op == ast.OpLOr {
			w.expr(x.X, st)
			r := st.clone()
			w.cond(x.X, x.Op == ast.OpLAnd, r)
			w.expr(x.Y, r)
			return
		}
	}
	for _, ch := range ast.Children(e) {
		if ce, ok := ch.(ast.Expr); ok {
			w.expr(ce, st)
		} else if fi, ok := ch.(*ast.FieldInit); ok {
			w.expr(fi.Value, st)
		}
	}
	if call, ok := e.(*ast.CallExpr); ok && w.onCall != nil {
		w.onCall(call, st)
	}
}

// lhsExpr reports calls inside an assignment target (index expressions).
func (w *walker) lhsExpr(e ast.Expr, st *state) {
	if _, ok := unparen(e).(*ast.Ident); ok {
		return
	}
	w.expr(e, st)
}

func (w *walker) isPanic(e ast.Expr) bool {
	call, ok := unparen(e).(*ast.CallExpr)
	if !ok {
		return false
	}
	cl := w.c.info.CallOf(call)
	return cl != nil && cl.Kind == types.CallBuiltin && cl.Builtin == resolve.FPanic
}

func (w *walker) kill(s *resolve.Symbol, st *state) {
	if s == nil {
		return
	}
	delete(st.lb, s)
	delete(st.bnd, s)
}

// assign applies `lhs op= rhs` (rhs[i] nil for ++/--, rhs shorter than lhs
// for multi-value calls).
func (w *walker) assign(lhs, rhs []ast.Expr, op ast.AssignOp, st *state) *state {
	pre := st.clone()
	newB := make([]*bnds, len(lhs))
	for i, l := range lhs {
		s := w.c.varOf(l)
		if s == nil || !w.trackBounds || w.rf.unstable(s) {
			continue
		}
		var r ast.Expr
		if len(rhs) == len(lhs) {
			r = rhs[i]
		}
		switch {
		case op == ast.Assign && r != nil:
			b := w.eb(r, pre)
			newB[i] = &b
		case op == ast.AddAssign && (r == nil || w.isConst(r, 1)):
			b := plus1(w.eb(l, pre))
			newB[i] = &b
		case op == ast.SubAssign && (r == nil || w.isConst(r, 1)):
			b := minus1(w.eb(l, pre))
			newB[i] = &b
		}
	}
	for i, l := range lhs {
		w.kill(w.c.storageRoot(l), st)
		if newB[i] != nil && (len(newB[i].lo) > 0 || len(newB[i].hi) > 0) {
			st.bnd[w.c.varOf(l)] = *newB[i]
		}
	}
	if w.onAssign != nil {
		for i, l := range lhs {
			var r ast.Expr
			if op == ast.Assign && len(rhs) == len(lhs) {
				r = rhs[i]
			}
			w.onAssign(l, r, pre, st)
		}
	}
	return st
}

func (w *walker) isConst(e ast.Expr, v uint64) bool {
	n, ok := w.c.constUint(e)
	return ok && n == v
}

// ---------------------------------------------------------------------------
// conditions

func negate(op ast.BinaryOp) ast.BinaryOp {
	switch op {
	case ast.OpLt:
		return ast.OpGe
	case ast.OpLe:
		return ast.OpGt
	case ast.OpGt:
		return ast.OpLe
	case ast.OpGe:
		return ast.OpLt
	case ast.OpEq:
		return ast.OpNe
	case ast.OpNe:
		return ast.OpEq
	}
	return op
}

func flip(op ast.BinaryOp) ast.BinaryOp {
	switch op {
	case ast.OpLt:
		return ast.OpGt
	case ast.OpLe:
		return ast.OpGe
	case ast.OpGt:
		return ast.OpLt
	case ast.OpGe:
		return ast.OpLe
	}
	return op
}

func isCompare(op ast.BinaryOp) bool { return op >= ast.OpEq && op <= ast.OpGe }

// cond adds the facts implied by e evaluating to truth.
func (w *walker) cond(e ast.Expr, truth bool, st *state) {
	switch x := unparen(e).(type) {
	case *ast.UnaryExpr:
		if x.Op == ast.OpNot {
			w.cond(x.X, !truth, st)
		}
	case *ast.BinaryExpr:
		switch {
		case x.Op == ast.OpLAnd && truth, x.Op == ast.OpLOr && !truth:
			w.cond(x.X, truth, st)
			w.cond(x.Y, truth, st)
		case isCompare(x.Op):
			op := x.Op
			if !truth {
				op = negate(op)
			}
			w.lbFact(x.X, op, x.Y, st)
			w.lbFact(x.Y, flip(op), x.X, st)
			if w.trackBounds {
				w.bndFact(x.X, op, x.Y, st)
			}
		}
	}
}

// lbFact records `v op c` for a stable integer variable v and constant c.
func (w *walker) lbFact(v ast.Expr, op ast.BinaryOp, c ast.Expr, st *state) {
	s := w.c.varOf(v)
	if s == nil || w.rf.unstable(s) || !types.IsInteger(w.c.info.SymType(s)) {
		return
	}
	k, ok := w.c.constUint(c)
	if !ok {
		return
	}
	var lb uint64
	switch op {
	case ast.OpGt:
		if k == ^uint64(0) {
			return
		}
		lb = k + 1
	case ast.OpGe, ast.OpEq:
		lb = k
	case ast.OpNe:
		if k == 0 && types.IsUnsigned(w.c.info.SymType(s)) {
			lb = 1
		}
	}
	if lb > st.lb[s] {
		st.lb[s] = lb
	}
}

func (w *walker) bndFact(x ast.Expr, op ast.BinaryOp, y ast.Expr, st *state) {
	switch op {
	case ast.OpLt:
		w.addLess(x, y, true, st)
	case ast.OpLe:
		w.addLess(x, y, false, st)
	case ast.OpGt:
		w.addLess(y, x, true, st)
	case ast.OpGe:
		w.addLess(y, x, false, st)
	case ast.OpEq:
		w.addLess(x, y, false, st)
		w.addLess(y, x, false, st)
	case ast.OpNe:
		if w.isConst(y, 0) && w.c.isUnsigned(x) {
			w.addLess(y, x, true, st)
		} else if w.isConst(x, 0) && w.c.isUnsigned(y) {
			w.addLess(x, y, true, st)
		}
	}
}

// addLess records x < y (strict) or x <= y.
func (w *walker) addLess(x, y ast.Expr, strict bool, st *state) {
	bx, by := w.eb(x, st), w.eb(y, st)
	if s := w.c.varOf(x); s != nil {
		if b, ok := st.bnd[s]; ok {
			for _, t := range by.hi {
				b.hi = addBound(b.hi, bound{t.term, strict || t.strict})
			}
			st.bnd[s] = b
		}
	}
	if s := w.c.varOf(y); s != nil {
		if b, ok := st.bnd[s]; ok {
			for _, t := range bx.lo {
				b.lo = addBound(b.lo, bound{t.term, strict || t.strict})
			}
			st.bnd[s] = b
		}
	}
}

// ---------------------------------------------------------------------------
// expression bounds (T3 loops)

func varTerm(s *resolve.Symbol) string { return fmt.Sprintf("v%d", s.ID) }
func lenTerm(s *resolve.Symbol) string { return fmt.Sprintf("l%d", s.ID) }
func constTerm(n uint64) string        { return fmt.Sprintf("c%d", n) }

func exact(t string) bnds { return bnds{lo: []bound{{t, false}}, hi: []bound{{t, false}}} }

// eb computes bounds of e relative to the iteration-start terms.
func (w *walker) eb(e ast.Expr, st *state) bnds {
	e = w.c.stripConv(e)
	if n, ok := w.c.constUint(e); ok {
		return exact(constTerm(n))
	}
	if a := w.c.lenArg(e); a != nil {
		if s := w.c.varOf(a); s != nil && w.lenTerm != nil && w.lenTerm(s) {
			return exact(lenTerm(s))
		}
		return bnds{}
	}
	switch x := e.(type) {
	case *ast.Ident:
		if s := w.c.varOf(x); s != nil {
			return st.bnd[s]
		}
	case *ast.BinaryExpr:
		switch x.Op {
		case ast.OpAdd:
			if w.isConst(x.Y, 1) {
				return plus1(w.eb(x.X, st))
			}
			if w.isConst(x.X, 1) {
				return plus1(w.eb(x.Y, st))
			}
			if b, ok := w.midpoint(x.X, x.Y, st); ok {
				return b
			}
			if b, ok := w.midpoint(x.Y, x.X, st); ok {
				return b
			}
		case ast.OpSub:
			if w.isConst(x.Y, 1) {
				return minus1(w.eb(x.X, st))
			}
		}
	}
	return bnds{}
}

// midpoint recognizes a + (b - a) / k (k >= 2) and a + ((b - a) >> k)
// (k >= 1): given a < b, the result r satisfies a <= r < b.
func (w *walker) midpoint(a, q ast.Expr, st *state) (bnds, bool) {
	qb, ok := unparen(q).(*ast.BinaryExpr)
	if !ok {
		return bnds{}, false
	}
	k, ok := w.c.constUint(qb.Y)
	if !ok || !(qb.Op == ast.OpDiv && k >= 2 || qb.Op == ast.OpShr && k >= 1) {
		return bnds{}, false
	}
	d, ok := unparen(qb.X).(*ast.BinaryExpr)
	if !ok || d.Op != ast.OpSub {
		return bnds{}, false
	}
	sa, sa2 := w.c.varOf(w.c.stripConv(a)), w.c.varOf(w.c.stripConv(d.Y))
	if sa == nil || sa != sa2 {
		return bnds{}, false
	}
	ba, bb := w.eb(a, st), w.eb(d.X, st)
	if !less(ba, bb, true) {
		return bnds{}, false
	}
	r := bnds{lo: slices.Clone(ba.lo)}
	for _, t := range bb.hi {
		r.hi = append(r.hi, bound{t.term, true})
	}
	return r, true
}

// less reports whether x < y (strict) or x <= y is implied by the bounds.
func less(x, y bnds, strict bool) bool {
	for _, a := range x.hi {
		for _, b := range y.lo {
			if a.term == b.term && (!strict || a.strict || b.strict) {
				return true
			}
		}
	}
	return false
}

// plus1: if x < t then x+1 <= t (no overflow); lower bounds become strict.
func plus1(b bnds) bnds {
	var r bnds
	for _, h := range b.hi {
		if h.strict {
			r.hi = append(r.hi, bound{h.term, false})
		}
	}
	if len(r.hi) == 0 {
		return bnds{}
	}
	for _, l := range b.lo {
		r.lo = append(r.lo, bound{l.term, true})
	}
	return r
}

// minus1: if x > t then x-1 >= t (no underflow); upper bounds become strict.
func minus1(b bnds) bnds {
	var r bnds
	for _, l := range b.lo {
		if l.strict {
			r.lo = append(r.lo, bound{l.term, false})
		}
	}
	if len(r.lo) == 0 {
		return bnds{}
	}
	for _, h := range b.hi {
		r.hi = append(r.hi, bound{h.term, true})
	}
	return r
}

// exhaustive reports whether a match statement covers all cases: it has a
// wildcard arm, or it matches an enum and names every variant.
func (c *checker) exhaustive(m *ast.MatchStmt) bool {
	seen := map[int]bool{}
	for _, arm := range m.Arms {
		switch p := arm.Pattern.(type) {
		case *ast.WildcardPattern:
			return true
		case *ast.CtorPattern:
			if v := c.info.VariantOf(p); v != nil {
				seen[v.Index] = true
			}
		}
	}
	if e, ok := under(c.info.TypeOf(m.X)).(*types.Enum); ok {
		return len(seen) == len(e.Variants)
	}
	return false
}
