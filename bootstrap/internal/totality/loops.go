package totality

import (
	"math/big"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

type loopCtx struct {
	c    *checker
	fi   *types.FuncInfo
	loop *ast.ForStmt
	rf   *rootFacts
}

// check returns "" if the loop is proven to terminate by T1, T2 or T3,
// otherwise the reason of the most applicable rule.
func (lc *loopCtx) check() string {
	f := lc.loop
	if f.Decreases != nil {
		return lc.t3()
	}
	if f.Cond == nil {
		return "a loop without condition needs a decreases clause"
	}
	if lc.isShrinkCond(f.Cond) != nil {
		return lc.t2()
	}
	return lc.t1()
}

// ---------------------------------------------------------------------------
// T1: counting loops

func (lc *loopCtx) t1() string {
	c, f := lc.c, lc.loop
	if f.Post == nil {
		return "not a counting loop (no post statement), slice-shrink loop, or loop with decreases clause"
	}
	// counter and step from the post statement
	var ctr *resolve.Symbol
	var step uint64
	up := false
	switch p := f.Post.(type) {
	case *ast.IncDecStmt:
		ctr, step, up = c.varOf(p.X), 1, p.Inc
	case *ast.AssignStmt:
		if len(p.LHS) != 1 || len(p.RHS) != 1 || (p.Op != ast.AddAssign && p.Op != ast.SubAssign) {
			return "post statement must be i++, i--, i += c or i -= c"
		}
		ctr, up = c.varOf(p.LHS[0]), p.Op == ast.AddAssign
		k, ok := c.constUint(p.RHS[0])
		if !ok || k == 0 {
			return "step of the counting loop must be a positive constant"
		}
		step = k
	default:
		return "post statement must be i++, i--, i += c or i -= c"
	}
	if ctr == nil {
		return "post statement must update a local counter variable"
	}
	ct := c.info.SymType(ctr)
	bits, signed, ok := types.IntInfo(ct)
	if !ok {
		return "loop counter " + ctr.Name + " is not an integer"
	}
	switch init := f.Init.(type) {
	case nil:
	case *ast.ShortVarDecl:
		if len(init.Names) != 1 || c.info.SymbolOf(init.Names[0]) != ctr {
			return "init statement must declare only the counter"
		}
	case *ast.AssignStmt:
		if len(init.LHS) != 1 || c.varOf(init.LHS[0]) != ctr || init.Op != ast.Assign {
			return "init statement must assign only the counter"
		}
	default:
		return "init statement must initialize the counter"
	}
	if lc.rf.unstable(ctr) || ctr.Func != lc.fi.Node() {
		return "loop counter " + ctr.Name + " may be modified through a pointer or closure"
	}
	// condition: i < B / B > i / i <= B (counting up), mirrored down
	cmp, ok := unparen(f.Cond).(*ast.BinaryExpr)
	if !ok || !isCompare(cmp.Op) {
		return "condition must compare the counter with a bound"
	}
	op, bnd := cmp.Op, cmp.Y
	if c.varOf(cmp.X) != ctr {
		if c.varOf(cmp.Y) != ctr {
			return "condition must compare the counter " + ctr.Name + " with a bound"
		}
		op, bnd = flip(cmp.Op), cmp.X
	}
	strictOp, nonStrictOp := ast.OpLt, ast.OpLe
	if !up {
		strictOp, nonStrictOp = ast.OpGt, ast.OpGe
	}
	if op != strictOp && op != nonStrictOp {
		if up {
			return "a counter counting up needs a condition " + ctr.Name + " < bound"
		}
		return "a counter counting down needs a condition " + ctr.Name + " > bound"
	}
	// overflow: the step must not wrap past the bound
	maxV, minV := intRange(bits, signed)
	bv := c.constBig(bnd)
	stepV := new(big.Int).SetUint64(step)
	if op == nonStrictOp || step > 1 {
		safe := false
		if bv != nil {
			// up: B + step (<=) or B + step - 1 (<) must fit; down: mirrored
			lim := new(big.Int).Set(stepV)
			if op == strictOp {
				lim.Sub(lim, big.NewInt(1))
			}
			if up {
				safe = new(big.Int).Add(bv, lim).Cmp(maxV) <= 0
			} else {
				safe = new(big.Int).Sub(bv, lim).Cmp(minV) >= 0
			}
		} else if up && op == strictOp && !signed && bits == 64 && c.lenArg(bnd) != nil && step < 1<<62 {
			// len(x) <= MaxInt64, so i + step cannot wrap a 64-bit unsigned counter
			safe = true
		}
		if !safe {
			return "the counter may wrap around: use step 1 with a strict comparison, or a constant bound"
		}
	}
	// the counter is not modified in the body
	if lc.writesRoot(ctr, f.Body) {
		return "loop counter " + ctr.Name + " is modified in the loop body"
	}
	if !c.invariant(bnd, lc.fi, lc.rf, f.Body, f.Post) {
		return "loop bound is not invariant in the loop body"
	}
	return ""
}

func intRange(bits int, signed bool) (maxV, minV *big.Int) {
	one := big.NewInt(1)
	if signed {
		maxV = new(big.Int).Sub(new(big.Int).Lsh(one, uint(bits-1)), one)
		minV = new(big.Int).Neg(new(big.Int).Lsh(one, uint(bits-1)))
		return
	}
	return new(big.Int).Sub(new(big.Int).Lsh(one, uint(bits)), one), new(big.Int)
}

func (c *checker) constBig(e ast.Expr) *big.Int {
	if v, ok := c.info.ValueOf(e).(types.IntVal); ok && v.V != nil {
		return v.V
	}
	return nil
}

// writesRoot reports whether any assignment in n (including function
// literals) writes the storage of s.
func (lc *loopCtx) writesRoot(s *resolve.Symbol, nodes ...ast.Node) bool {
	writes, _ := writesAndCalls(nodes...)
	for _, w := range writes {
		if lc.c.storageRoot(w) == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// T2: slice shrink

// isShrinkCond recognizes len(rem) > 0, len(rem) != 0, 0 < len(rem),
// len(rem) >= 1 and returns rem.
func (lc *loopCtx) isShrinkCond(e ast.Expr) *resolve.Symbol {
	c := lc.c
	b, ok := unparen(e).(*ast.BinaryExpr)
	if !ok {
		return nil
	}
	x, op, y := b.X, b.Op, b.Y
	if c.lenArg(x) == nil {
		x, op, y = y, flip(op), x
	}
	a := c.lenArg(x)
	if a == nil {
		return nil
	}
	k, ok := c.constUint(y)
	if !ok || !(op == ast.OpGt && k == 0 || op == ast.OpNe && k == 0 || op == ast.OpGe && k == 1) {
		return nil
	}
	s := c.varOf(a)
	if s == nil {
		return nil
	}
	if _, ok := under(c.info.SymType(s)).(*types.Slice); !ok {
		return nil
	}
	return s
}

// shrinkStep recognizes rem[k:], rem[k:len(rem)] and rem[:len(rem)-k]
// (also rem[0:len(rem)-k]) and returns k.
func (lc *loopCtx) shrinkStep(rem *resolve.Symbol, e ast.Expr) ast.Expr {
	c := lc.c
	se, ok := unparen(e).(*ast.SliceExpr)
	if !ok || c.varOf(se.X) != rem {
		return nil
	}
	isLenRem := func(e ast.Expr) bool {
		a := c.lenArg(e)
		return a != nil && c.varOf(a) == rem
	}
	if se.Lo != nil && (se.Hi == nil || isLenRem(se.Hi)) {
		return se.Lo
	}
	if se.Hi != nil && (se.Lo == nil || lc.c.isZero(se.Lo)) {
		if b, ok := unparen(se.Hi).(*ast.BinaryExpr); ok && b.Op == ast.OpSub && isLenRem(b.X) {
			return b.Y
		}
	}
	return nil
}

func (c *checker) isZero(e ast.Expr) bool {
	n, ok := c.constUint(e)
	return ok && n == 0
}

// positive reports whether k > 0 is proven in st.
func (lc *loopCtx) positive(k ast.Expr, st *state) bool {
	if n, ok := lc.c.constUint(k); ok {
		return n > 0
	}
	s := lc.c.varOf(lc.c.stripConv(k))
	return s != nil && !lc.rf.unstable(s) && st.lb[s] >= 1
}

func (lc *loopCtx) t2() string {
	c, f := lc.c, lc.loop
	rem := lc.isShrinkCond(f.Cond)
	if f.Post != nil {
		return "a slice-shrink loop must not have a post statement"
	}
	switch init := f.Init.(type) {
	case nil:
	case *ast.ShortVarDecl:
		if len(init.Names) != 1 || c.info.SymbolOf(init.Names[0]) != rem {
			return "init statement must declare only the slice " + rem.Name
		}
	case *ast.AssignStmt:
		if len(init.LHS) != 1 || c.varOf(init.LHS[0]) != rem || init.Op != ast.Assign {
			return "init statement must assign only the slice " + rem.Name
		}
	default:
		return "init statement must initialize the slice " + rem.Name
	}
	if lc.rf.unstable(rem) || rem.Func != lc.fi.Node() {
		return "slice " + rem.Name + " may be modified through a pointer or closure"
	}
	// every write of rem is a shrink step (never lengthened)
	bad := false
	ast.Inspect(f.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, l := range n.LHS {
				if c.storageRoot(l) != rem {
					continue
				}
				if n.Op != ast.Assign || len(n.RHS) != len(n.LHS) || lc.shrinkStep(rem, n.RHS[i]) == nil {
					bad = true
				}
			}
		case *ast.IncDecStmt:
			if c.storageRoot(n.X) == rem {
				bad = true
			}
		}
		return true
	})
	if bad {
		return "slice " + rem.Name + " is modified other than by " + rem.Name + " = " + rem.Name + "[k:]"
	}
	why := ""
	w := &walker{c: c, rf: lc.rf}
	w.onAssign = func(lhs, rhs ast.Expr, pre, post *state) {
		if rhs == nil || c.varOf(lhs) != rem {
			return
		}
		if k := lc.shrinkStep(rem, rhs); k != nil && lc.positive(k, pre) {
			post.shrunk = true
		}
	}
	w.onExit = func(n ast.Node, st *state) {
		if !st.shrunk && why == "" {
			why = "slice " + rem.Name + " is not provably shortened on every path (need " + rem.Name + " = " + rem.Name + "[k:] with k > 0: a positive constant, or after `if k == 0 { return }`)"
		}
	}
	end := w.stmt(f.Body, newState())
	if !end.dead {
		w.onExit(f.Body, end)
	}
	return why
}

// ---------------------------------------------------------------------------
// T3: loops with decreases(M)

// t3 accepts `for [init;] C [; post] decreases(M)` where M is X or X - Y
// and the leaves X, Y are stable local variables, constants or len(s) of
// an invariant slice s. At every iteration end (end of body after the post
// statement, and continue) the abstract bounds must show X' <= X and
// Y' >= Y with one of them strict, and C must imply Y <= X (for M = X: X is
// unsigned) so the measure is a natural number that strictly decreases.
func (lc *loopCtx) t3() string {
	c, f := lc.c, lc.loop
	m := c.stripConv(f.Decreases.Measure)
	var xs, ys ast.Expr
	if b, ok := m.(*ast.BinaryExpr); ok && b.Op == ast.OpSub {
		xs, ys = c.stripConv(b.X), c.stripConv(b.Y)
	} else {
		xs = m
	}
	tracked := map[*resolve.Symbol]bool{}
	lenOK := map[*resolve.Symbol]bool{}
	var leafErr string
	leaf := func(e ast.Expr) {
		if e == nil {
			return
		}
		if _, ok := c.constUint(e); ok {
			return
		}
		if a := c.lenArg(e); a != nil {
			s := c.varOf(a)
			if s == nil || !c.invariant(e, lc.fi, lc.rf, f.Body, f.Post) {
				leafErr = "len(...) in the measure must be of an unmodified local slice"
				return
			}
			lenOK[s] = true
			return
		}
		s := c.varOf(e)
		if s == nil {
			leafErr = "measure must be X or X - Y with X, Y local variables, constants or len(s)"
			return
		}
		if lc.rf.unstable(s) || s.Func != lc.fi.Node() {
			leafErr = "measure variable " + s.Name + " may be modified through a pointer or closure"
			return
		}
		tracked[s] = true
	}
	leaf(xs)
	leaf(ys)
	if leafErr != "" {
		return leafErr
	}
	if f.Cond != nil {
		ast.Inspect(f.Cond, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if s := c.varOf(id); s != nil && !lc.rf.unstable(s) && s.Func == lc.fi.Node() && types.IsInteger(c.info.SymType(s)) {
					tracked[s] = true
				}
			}
			return true
		})
	}
	w := &walker{c: c, rf: lc.rf, trackBounds: true, lenTerm: func(s *resolve.Symbol) bool { return lenOK[s] }}
	start := newState()
	for s := range tracked {
		b := exact(varTerm(s))
		if types.IsUnsigned(c.info.SymType(s)) {
			b.lo = append(b.lo, bound{constTerm(0), false})
		}
		start.bnd[s] = b
	}
	if f.Cond != nil {
		w.cond(f.Cond, true, start)
	}
	// lower bound: C implies Y <= X (M = X - Y), or X is unsigned
	if ys != nil {
		if !less(w.eb(ys, start), w.eb(xs, start), false) {
			return "the loop condition must imply that the measure is non-negative (e.g. lo < hi for hi - lo)"
		}
	} else if !c.isUnsigned(xs) {
		return "a single-variable measure must be unsigned"
	}
	proves := func(st *state) bool {
		strict := false
		ok := func(e ast.Expr, upper bool) bool {
			if e == nil {
				return true
			}
			t := lc.leafTerm(e)
			b := w.eb(e, st)
			list := b.lo
			if upper {
				list = b.hi
			}
			for _, x := range list {
				if x.term == t {
					strict = strict || x.strict
					return true
				}
			}
			return false
		}
		return ok(xs, true) && ok(ys, false) && strict
	}
	w.onAssign = func(lhs, rhs ast.Expr, pre, post *state) {
		if s := c.storageRoot(lhs); s != nil && tracked[s] {
			post.dec = proves(post)
		}
	}
	why := ""
	atEnd := func(st *state) {
		st = w.stmt(f.Post, st.clone())
		if !st.dead && !st.dec && why == "" {
			why = "measure does not provably decrease on every path through the loop body"
		}
	}
	w.onExit = func(n ast.Node, st *state) { atEnd(st) }
	end := w.stmt(f.Body, start.clone())
	if !end.dead {
		atEnd(end)
	}
	return why
}

func (lc *loopCtx) leafTerm(e ast.Expr) string {
	c := lc.c
	if n, ok := c.constUint(e); ok {
		return constTerm(n)
	}
	if a := c.lenArg(e); a != nil {
		return lenTerm(c.varOf(a))
	}
	return varTerm(c.varOf(e))
}
