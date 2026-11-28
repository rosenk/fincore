package scope

import (
	"slices"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// Abstract states of a Linear variable (bit set: the states it may be in).
const (
	stAbsent   uint8 = 1 << iota // no resource (error path of a (Linear, error) result)
	stLive                       // owned, must be finalized
	stPending                    // owned and live iff the tied error is nil
	stMoved                      // ownership transferred
	stConsumed                   // finalized (Close, ReleaseSlot, cancel())
	stDeferred                   // finalized by a deferred call; still usable
	stBorrowed                   // borrowed from the caller / enclosing function
)

const (
	stOwned = stLive | stPending
	stGone  = stMoved | stConsumed
)

// env is the abstract state at a program point.
type env struct {
	dead bool
	st   []uint8
	tie  []*resolve.Symbol // error variable of a Pending state
}

func (e *env) clone() *env {
	return &env{dead: e.dead, st: slices.Clone(e.st), tie: slices.Clone(e.tie)}
}

func deadEnv(n int) *env {
	return &env{dead: true, st: make([]uint8, n), tie: make([]*resolve.Symbol, n)}
}

func joinEnv(a, b *env) *env {
	if a.dead {
		return b.clone()
	}
	if b.dead {
		return a.clone()
	}
	r := a.clone()
	for i := range r.st {
		sa, sb := a.st[i], b.st[i]
		r.st[i] = sa | sb
		ta, tb := a.tie[i], b.tie[i]
		switch {
		case sa&stPending == 0:
			r.tie[i] = tb
		case sb&stPending == 0:
			r.tie[i] = ta
		case ta != tb:
			r.st[i] = r.st[i]&^stPending | stLive
			r.tie[i] = nil
		}
	}
	return r
}

func (e *env) equal(o *env) bool {
	return e.dead == o.dead && slices.Equal(e.st, o.st) && slices.Equal(e.tie, o.tie)
}

type loopCtx struct {
	height int // number of frames outside the loop body
	breaks *env
	conts  *env
}

// flow is the linearity analysis of one function body.
type flow struct {
	c      *checker
	vars   []*resolve.Symbol
	idx    map[*resolve.Symbol]int
	frames [][]int
	loops  []*loopCtx
	// summarized variables (captures of an immediately called literal)
	// are not leak-checked; their exit states are joined into exits.
	summarized map[int]bool
	exits      *env
}

// consumption kinds
const (
	kConsume = iota // finalize
	kMove           // transfer ownership
	kDefer          // finalize at function exit
)

func (c *checker) linearity(fi *types.FuncInfo) {
	f := &flow{c: c, idx: map[*resolve.Symbol]int{}}
	f.collectVars(fi.Body)
	var owned, borrowed []*resolve.Symbol
	if fi.RecvSym != nil && types.IsLinear(c.info.SymType(fi.RecvSym)) {
		borrowed = append(borrowed, fi.RecvSym)
	}
	for _, p := range fi.Params {
		if !types.IsLinear(c.info.SymType(p)) {
			continue
		}
		if fi.Effect == types.Driver {
			owned = append(owned, p) // decision 1
		} else {
			borrowed = append(borrowed, p)
		}
	}
	f.run(fi.Body, owned, borrowed)
}

// litLinearity analyses a function literal; goTask: it is the task of a
// Scope.Go call and owns its captured Linear variables.
func (f *flow) litLinearity(lit *ast.FuncLit, goTask bool) {
	c := f.c
	if c.lits[lit] {
		return
	}
	c.lits[lit] = true
	fi := c.info.FuncOf(lit)
	if fi == nil {
		return
	}
	g := &flow{c: c, idx: map[*resolve.Symbol]int{}}
	g.collectVars(lit.Body)
	var owned, borrowed []*resolve.Symbol
	for _, p := range fi.Params {
		if !types.IsLinear(c.info.SymType(p)) {
			continue
		}
		if fi.Effect == types.Driver {
			owned = append(owned, p)
		} else {
			borrowed = append(borrowed, p)
		}
	}
	for _, s := range fi.Captures {
		if !types.IsLinear(c.info.SymType(s)) {
			continue
		}
		if goTask {
			owned = append(owned, s)
		} else {
			borrowed = append(borrowed, s)
		}
	}
	g.run(lit.Body, owned, borrowed)
}

func (f *flow) addVar(s *resolve.Symbol) int {
	if i, ok := f.idx[s]; ok {
		return i
	}
	f.idx[s] = len(f.vars)
	f.vars = append(f.vars, s)
	return len(f.vars) - 1
}

// collectVars allocates the Linear variables declared in body (not in
// nested literals).
func (f *flow) collectVars(body *ast.BlockStmt) {
	c := f.c
	ast.Inspect(body, func(n ast.Node) bool {
		var ids []*ast.Ident
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ShortVarDecl:
			ids = n.Names
		case *ast.VarDecl:
			ids = []*ast.Ident{n.Name}
		case *ast.CtorPattern:
			for _, a := range n.Args {
				if id, ok := a.(*ast.Ident); ok {
					ids = append(ids, id)
				}
			}
		}
		for _, id := range ids {
			if s := c.varSym(id); s != nil && isDef(c.info, id, s) && types.IsLinear(c.info.SymType(s)) {
				f.addVar(s)
			}
		}
		return true
	})
}

func (f *flow) run(body *ast.BlockStmt, owned, borrowed []*resolve.Symbol) {
	var top []int
	for _, s := range owned {
		top = append(top, f.addVar(s))
	}
	for _, s := range borrowed {
		top = append(top, f.addVar(s))
	}
	n := len(f.vars)
	e := &env{st: make([]uint8, n), tie: make([]*resolve.Symbol, n)}
	for _, s := range owned {
		e.st[f.idx[s]] = stLive
	}
	for _, s := range borrowed {
		e.st[f.idx[s]] = stBorrowed
	}
	f.frames = [][]int{top}
	f.exits = deadEnv(n)
	e = f.block(body, e)
	if !e.dead {
		f.leaks(e, 0)
		f.exits = joinEnv(f.exits, e)
	}
}

// ---------------------------------------------------------------------------
// Reporting

func (f *flow) leaks(e *env, height int) {
	for _, fr := range f.frames[height:] {
		for _, i := range fr {
			if e.st[i]&stOwned != 0 && !f.summarized[i] {
				f.leak(i)
			}
		}
	}
}

func (f *flow) leak(i int) {
	s := f.vars[i]
	if f.c.leaked[s] {
		return
	}
	f.c.leaked[s] = true
	sp := diag.SourceSpan{}
	if s.Ident != nil {
		sp = s.Ident.Range
	}
	f.c.errorf(diag.E3005, sp, "Linear resource %s (%s) is not closed, released or moved on every path", s.Name, f.c.info.SymType(s))
}

// linVar returns the tracked Linear variable at the root of e (x, x.f,
// x[i]), or -1.
func (f *flow) linVar(e ast.Expr) int {
	switch e := unparen(e).(type) {
	case *ast.Ident:
		if s := f.c.varSym(e); s != nil {
			if i, ok := f.idx[s]; ok {
				return i
			}
		}
	case *ast.SelectorExpr:
		if sel := f.c.info.SelectionOf(e); sel != nil && sel.Kind == types.SelField && !sel.Indirect {
			return f.linVar(e.X)
		}
	case *ast.IndexExpr:
		if isArray(f.c.info.TypeOf(e.X)) {
			return f.linVar(e.X)
		}
	}
	return -1
}

// rootIdent returns the identifier at the root of a variable path.
func rootIdent(e ast.Expr) ast.Node {
	switch e := unparen(e).(type) {
	case *ast.SelectorExpr:
		return rootIdent(e.X)
	case *ast.IndexExpr:
		return rootIdent(e.X)
	}
	return e
}

// use is a borrow of variable i at node at.
func (f *flow) use(e *env, i int, at ast.Node) {
	if e.dead {
		return
	}
	if e.st[i]&stGone != 0 {
		f.c.errorf(diag.E3003, at.Span(), "use of Linear resource %s after it was %s", f.vars[i].Name, goneWord(e.st[i]))
	}
}

func goneWord(st uint8) string {
	if st&stMoved != 0 {
		return "moved"
	}
	return "closed/released"
}

// consume finalizes, moves or defers the finalization of variable i.
func (f *flow) consume(e *env, i int, at ast.Node, kind int) {
	if e.dead {
		return
	}
	st := e.st[i]
	name := f.vars[i].Name
	switch {
	case st&stGone != 0 && st&stDeferred == 0:
		f.c.errorf(diag.E3003, at.Span(), "use of Linear resource %s after it was %s", name, goneWord(st))
	case st&stBorrowed != 0:
		f.c.errorf(diag.E3003, at.Span(), "borrowed Linear resource %s cannot be closed or moved here (it is owned by the caller)", name)
	}
	var n uint8
	switch {
	case kind == kDefer:
		n = stDeferred
	case st&stDeferred != 0:
		// Explicit finalization of a resource whose close is deferred:
		// it stays usable until the deferred call runs.
		n = stDeferred
		if st&^stDeferred&(stOwned|stAbsent) != 0 {
			n |= stConsumed
		}
	case kind == kMove:
		n = stMoved
	default:
		n = stConsumed
	}
	e.st[i] = n | st&stBorrowed
	e.tie[i] = nil
}

// bind gives variable i a fresh owned value (tied to errSym if non-nil).
func (f *flow) bind(e *env, i int, errSym *resolve.Symbol) {
	if errSym != nil {
		e.st[i], e.tie[i] = stPending, errSym
	} else {
		e.st[i], e.tie[i] = stLive, nil
	}
	fr := f.frames[len(f.frames)-1]
	if !slices.Contains(fr, i) {
		f.frames[len(f.frames)-1] = append(fr, i)
	}
}

// untie resolves the Pending states tied to s (s is being reassigned).
func (e *env) untie(s *resolve.Symbol) {
	for i, t := range e.tie {
		if t == s && s != nil {
			e.st[i] = e.st[i]&^stPending | stLive
			e.tie[i] = nil
		}
	}
}

// refine applies the knowledge errSym != nil (nonNil) or == nil.
func (e *env) refine(errSym *resolve.Symbol, nonNil bool) {
	for i, t := range e.tie {
		if t != errSym || e.st[i]&stPending == 0 {
			continue
		}
		if nonNil {
			e.st[i] = e.st[i]&^stPending | stAbsent
		} else {
			e.st[i] = e.st[i]&^stPending | stLive
		}
		e.tie[i] = nil
	}
}

// ---------------------------------------------------------------------------
// Statements

func (f *flow) block(b *ast.BlockStmt, e *env) *env {
	if b == nil {
		return e
	}
	f.frames = append(f.frames, nil)
	for _, s := range b.Stmts {
		if e.dead {
			break
		}
		e = f.stmt(s, e)
	}
	if !e.dead {
		f.leaks(e, len(f.frames)-1)
	}
	f.frames = f.frames[:len(f.frames)-1]
	return e
}

func (f *flow) stmt(s ast.Stmt, e *env) *env {
	c := f.c
	if e.dead {
		return e
	}
	switch s := s.(type) {
	case nil:
	case *ast.BlockStmt:
		return f.block(s, e)
	case *ast.DeclStmt:
		if d, ok := s.Decl.(*ast.VarDecl); ok && d.Value != nil {
			f.assign(e, []ast.Expr{d.Name}, []ast.Expr{d.Value})
		}
	case *ast.ShortVarDecl:
		lhs := make([]ast.Expr, len(s.Names))
		for i, id := range s.Names {
			lhs[i] = id
		}
		f.assign(e, lhs, s.Values)
	case *ast.AssignStmt:
		if s.Op != ast.Assign {
			for _, l := range s.LHS {
				f.expr(l, e)
			}
			for _, r := range s.RHS {
				f.expr(r, e)
			}
			return e
		}
		f.assign(e, s.LHS, s.RHS)
	case *ast.IncDecStmt:
		f.expr(s.X, e)
	case *ast.ExprStmt:
		f.expr(s.X, e)
		if call, ok := unparen(s.X).(*ast.CallExpr); ok {
			if ci := c.info.CallOf(call); ci != nil && ci.Kind == types.CallBuiltin && ci.Builtin == resolve.FPanic {
				return deadEnv(len(e.st))
			}
		}
	case *ast.IfStmt:
		f.frames = append(f.frames, nil)
		e = f.stmt(s.Init, e)
		if !e.dead {
			f.expr(s.Cond, e)
			then, els := e.clone(), e.clone()
			if sym, nonNil, ok := f.nilTest(s.Cond); ok {
				then.refine(sym, nonNil)
				els.refine(sym, !nonNil)
			}
			then = f.block(s.Then, then)
			if s.Else != nil {
				els = f.stmt(s.Else, els)
			}
			e = joinEnv(then, els)
			if !e.dead {
				f.leaks(e, len(f.frames)-1)
			}
		}
		f.frames = f.frames[:len(f.frames)-1]
	case *ast.MatchStmt:
		f.expr(s.X, e)
		out := deadEnv(len(e.st))
		for _, arm := range s.Arms {
			a := e.clone()
			f.frames = append(f.frames, nil)
			f.borrowBindings(arm.Pattern, a)
			if arm.Expr != nil {
				f.expr(arm.Expr, a)
				if call, ok := unparen(arm.Expr).(*ast.CallExpr); ok {
					if ci := c.info.CallOf(call); ci != nil && ci.Kind == types.CallBuiltin && ci.Builtin == resolve.FPanic {
						a = deadEnv(len(e.st))
					}
				}
			} else {
				a = f.block(arm.Block, a)
			}
			f.frames = f.frames[:len(f.frames)-1]
			out = joinEnv(out, a)
		}
		if !f.exhaustive(s) {
			out = joinEnv(out, e)
		}
		return out
	case *ast.ForStmt:
		return f.loop(s, e)
	case *ast.ScopeStmt:
		f.expr(s.Value, e)
		return f.block(s.Body, e)
	case *ast.DeferStmt:
		if call, ok := unparen(s.X).(*ast.CallExpr); ok {
			f.call(call, e, true)
		} else {
			f.expr(s.X, e)
		}
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			if i := f.linVar(r); i >= 0 && types.IsLinear(c.info.TypeOf(r)) {
				f.consume(e, i, rootIdent(r), kMove)
			} else {
				f.expr(r, e)
			}
		}
		f.leaks(e, 0)
		f.exits = joinEnv(f.exits, e)
		return deadEnv(len(e.st))
	case *ast.BreakStmt, *ast.ContinueStmt:
		if len(f.loops) == 0 {
			return deadEnv(len(e.st))
		}
		l := f.loops[len(f.loops)-1]
		f.leaks(e, l.height)
		if _, ok := s.(*ast.BreakStmt); ok {
			l.breaks = joinEnv(l.breaks, e)
		} else {
			l.conts = joinEnv(l.conts, e)
		}
		return deadEnv(len(e.st))
	}
	return e
}

func (f *flow) loop(s *ast.ForStmt, e *env) *env {
	f.frames = append(f.frames, nil) // init variables
	e = f.stmt(s.Init, e)
	if e.dead {
		f.frames = f.frames[:len(f.frames)-1]
		return e
	}
	head := e
	var l *loopCtx
	for iter := 0; ; iter++ {
		l = &loopCtx{height: len(f.frames), breaks: deadEnv(len(e.st)), conts: deadEnv(len(e.st))}
		f.loops = append(f.loops, l)
		cur := head.clone()
		if s.Cond != nil {
			f.expr(s.Cond, cur)
		}
		end := f.block(s.Body, cur.clone())
		f.loops = f.loops[:len(f.loops)-1]
		next := joinEnv(end, l.conts)
		if s.Post != nil && !next.dead {
			next = f.stmt(s.Post, next)
		}
		nh := joinEnv(head, next)
		if nh.equal(head) || iter > 64 {
			break
		}
		head = nh
	}
	out := l.breaks
	if s.Cond != nil {
		out = joinEnv(out, head)
	}
	if !out.dead {
		f.leaks(out, len(f.frames)-1)
	}
	f.frames = f.frames[:len(f.frames)-1]
	return out
}

// nilTest recognizes `x != nil` (nonNil) and `x == nil`.
func (f *flow) nilTest(cond ast.Expr) (*resolve.Symbol, bool, bool) {
	b, ok := unparen(cond).(*ast.BinaryExpr)
	if !ok || b.Op != ast.OpNe && b.Op != ast.OpEq {
		return nil, false, false
	}
	x, y := unparen(b.X), unparen(b.Y)
	if _, ok := x.(*ast.NilLit); ok {
		x, y = y, x
	}
	id, ok := x.(*ast.Ident)
	if _, isNil := y.(*ast.NilLit); !ok || !isNil {
		return nil, false, false
	}
	s := f.c.varSym(id)
	if s == nil {
		return nil, false, false
	}
	return s, b.Op == ast.OpNe, true
}

// exhaustive reports whether a match statement covers all cases (v0.2:
// recorded by the type checker, see types.Info.Exhaustive).
func (f *flow) exhaustive(s *ast.MatchStmt) bool {
	return f.c.info.IsExhaustive(s)
}

// borrowBindings marks the Linear payload bindings of a pattern (match arm
// or `is` expression) as Borrowed (decision 46).
func (f *flow) borrowBindings(p ast.Pattern, a *env) {
	switch p := p.(type) {
	case *ast.Ident:
		if sym := f.c.varSym(p); sym != nil {
			if i, ok := f.idx[sym]; ok {
				a.st[i], a.tie[i] = stBorrowed, nil
			}
		}
	case *ast.CtorPattern:
		for _, x := range p.Args {
			f.borrowBindings(x, a)
		}
	case *ast.OrPattern:
		for _, x := range p.Alts {
			f.borrowBindings(x, a)
		}
	}
}

func (f *flow) matchExpr(x *ast.MatchExpr, e *env) {
	c := f.c
	f.expr(x.X, e)
	out := deadEnv(len(e.st))
	for _, arm := range x.Arms {
		a := e.clone()
		f.borrowBindings(arm.Pattern, a)
		if i := f.linVar(arm.Expr); i >= 0 && types.IsLinear(c.info.TypeOf(arm.Expr)) {
			f.consume(a, i, rootIdent(arm.Expr), kMove)
		} else {
			f.expr(arm.Expr, a)
		}
		out = joinEnv(out, a)
	}
	*e = *out
}

// assign handles `lhs = rhs`, `lhs := rhs` and `var x = v`.
func (f *flow) assign(e *env, lhs, rhs []ast.Expr) {
	c := f.c
	// Right-hand sides: a Linear variable is moved.
	for _, r := range rhs {
		if i := f.linVar(r); i >= 0 && types.IsLinear(c.info.TypeOf(r)) {
			f.consume(e, i, rootIdent(r), kMove)
		} else {
			f.expr(r, e)
		}
	}
	// Error variable of a (Linear..., error) call result.
	var errSym *resolve.Symbol
	if len(rhs) == 1 && len(lhs) > 1 {
		if tu, ok := c.info.TypeOf(rhs[0]).(*types.Tuple); ok && len(tu.Types) == len(lhs) && isErrorType(tu.Types[len(lhs)-1]) {
			if id, ok := lhs[len(lhs)-1].(*ast.Ident); ok {
				errSym = c.varSym(id)
			}
		}
	}
	for _, l := range lhs {
		if id, ok := l.(*ast.Ident); ok {
			if s := c.varSym(id); s != nil {
				e.untie(s)
			}
		}
	}
	for k, l := range lhs {
		id, ok := l.(*ast.Ident)
		if !ok {
			f.expr(l, e) // x.f = ..., a[i] = ...: borrows the root
			continue
		}
		s := c.varSym(id)
		if s == nil {
			continue // `_` (E3004 reported by the walker)
		}
		i, ok := f.idx[s]
		if !ok {
			continue
		}
		if !isDef(c.info, id, s) && e.st[i]&stOwned != 0 {
			f.leak(i) // overwriting a live resource
		}
		tie := errSym
		if k == len(lhs)-1 {
			tie = nil
		}
		f.bind(e, i, tie)
	}
}

// ---------------------------------------------------------------------------
// Expressions

func (f *flow) expr(x ast.Expr, e *env) {
	if e.dead {
		return
	}
	switch x := x.(type) {
	case nil:
	case *ast.Ident:
		if i := f.linVar(x); i >= 0 {
			f.use(e, i, x)
		}
	case *ast.ParenExpr:
		f.expr(x.X, e)
	case *ast.UnaryExpr:
		f.expr(x.X, e)
	case *ast.BinaryExpr:
		f.expr(x.X, e)
		f.expr(x.Y, e)
	case *ast.SelectorExpr:
		if f.c.info.SelectionOf(x) != nil {
			f.expr(x.X, e)
		}
	case *ast.IndexExpr:
		f.expr(x.X, e)
		for _, i := range x.Indices {
			f.expr(i, e)
		}
	case *ast.SliceExpr:
		f.expr(x.X, e)
		f.expr(x.Lo, e)
		f.expr(x.Hi, e)
	case *ast.StructLit:
		for _, fi := range x.Fields {
			if i := f.linVar(fi.Value); i >= 0 && types.IsLinear(f.c.info.TypeOf(fi.Value)) {
				f.consume(e, i, rootIdent(fi.Value), kMove)
			} else {
				f.expr(fi.Value, e)
			}
		}
	case *ast.ArrayLit:
		for _, el := range x.Elems {
			if i := f.linVar(el); i >= 0 && types.IsLinear(f.c.info.TypeOf(el)) {
				f.consume(e, i, rootIdent(el), kMove)
			} else {
				f.expr(el, e)
			}
		}
	case *ast.CallExpr:
		f.call(x, e, false)
	case *ast.FuncLit:
		f.lit(x, e, false)
	case *ast.MatchExpr:
		f.matchExpr(x, e)
	}
}

// lit handles a function literal value: captured Linear variables are
// moved into a Scope.Go task (goTask) or borrowed.
func (f *flow) lit(lit *ast.FuncLit, e *env, goTask bool) {
	c := f.c
	if fi := c.info.FuncOf(lit); fi != nil {
		for _, s := range fi.Captures {
			i, ok := f.idx[s]
			if !ok {
				continue
			}
			var at ast.Node = lit
			if id := firstRef(c.info, lit.Body, s); id != nil {
				at = id
			}
			if goTask {
				f.consume(e, i, at, kMove)
			} else {
				f.use(e, i, at)
			}
		}
	}
	f.litLinearity(lit, goTask)
}

func (f *flow) call(x *ast.CallExpr, e *env, deferred bool) {
	c := f.c
	ci := c.info.CallOf(x)
	if ci == nil {
		f.expr(x.Fun, e)
		for _, a := range x.Args {
			f.expr(a, e)
		}
		return
	}
	finalize := kConsume
	move := kMove
	if deferred {
		finalize, move = kDefer, kDefer
	}
	switch ci.Kind {
	case types.CallMethod, types.CallBuiltinMeth:
		if i := f.linVar(ci.Recv); ci.ConsumesRecv && i >= 0 {
			f.consume(e, i, rootIdent(ci.Recv), finalize)
		} else {
			f.expr(ci.Recv, e)
		}
	case types.CallValue:
		if lit, ok := unparen(x.Fun).(*ast.FuncLit); ok {
			f.inlineLit(lit, e, deferred)
		} else if i := f.linVar(x.Fun); ci.ConsumesRecv && i >= 0 {
			f.consume(e, i, rootIdent(x.Fun), finalize)
		} else {
			f.expr(x.Fun, e)
		}
	default:
		if lit, ok := unparen(x.Fun).(*ast.FuncLit); ok {
			f.inlineLit(lit, e, deferred)
		}
	}
	owned := ci.Effect == types.Driver &&
		(ci.Kind == types.CallFunc || ci.Kind == types.CallMethod || ci.Kind == types.CallValue)
	isGo := ci.Kind == types.CallBuiltinMeth && ci.Method != nil && ci.Method.Builtin == types.ScopeGo
	for k, a := range x.Args {
		if isGo && k == 1 {
			if lit, ok := unparen(a).(*ast.FuncLit); ok {
				f.lit(lit, e, true)
				continue
			}
		}
		i := f.linVar(a)
		if i >= 0 && types.IsLinear(c.info.TypeOf(a)) {
			switch {
			case slices.Contains(ci.ConsumesArgs, k):
				f.consume(e, i, rootIdent(a), finalize)
				continue
			case owned:
				f.consume(e, i, rootIdent(a), move)
				continue
			}
		}
		f.expr(a, e)
	}
}

// inlineLit handles an immediately called literal (`func() {...}()`, also
// under defer). Its captured Linear variables are analysed as owned by the
// literal: if every exit of the literal has finalized or moved one, the
// call consumes it (deferred: DeferredClose); if no exit has, the call
// only borrows it; otherwise the literal leaks it on some path (E3005).
func (f *flow) inlineLit(lit *ast.FuncLit, e *env, deferred bool) {
	c := f.c
	fi := c.info.FuncOf(lit)
	if fi == nil {
		return
	}
	g := &flow{c: c, idx: map[*resolve.Symbol]int{}, summarized: map[int]bool{}}
	g.collectVars(lit.Body)
	var owned, borrowed, caps []*resolve.Symbol
	for _, p := range fi.Params {
		if types.IsLinear(c.info.SymType(p)) {
			if fi.Effect == types.Driver {
				owned = append(owned, p)
			} else {
				borrowed = append(borrowed, p)
			}
		}
	}
	for _, s := range fi.Captures {
		if _, ok := f.idx[s]; ok {
			caps = append(caps, s)
			g.summarized[g.addVar(s)] = true
		}
	}
	g.run(lit.Body, append(owned, caps...), borrowed)
	kind := kConsume
	if deferred {
		kind = kDefer
	}
	for _, s := range caps {
		i, gi := f.idx[s], g.idx[s]
		var at ast.Node = lit
		if id := firstRef(c.info, lit.Body, s); id != nil {
			at = id
		}
		st := g.exits.st[gi]
		switch {
		case g.exits.dead || st&^stOwned == 0:
			f.use(e, i, at) // borrowed (or the literal never returns)
		case st&stOwned == 0:
			f.consume(e, i, at, kind)
		default:
			g.leak(gi)
			f.consume(e, i, at, kind)
		}
	}
}
