package scope

import (
	"fmt"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// Internal depth levels (see the package documentation).
const (
	levelGlobal = 0
	levelCaller = 1
	levelFunc   = 2
)

// Check runs the scope and linearity checks (S2-S5) on a well-typed
// program and returns the diagnostics, sorted.
func Check(prog *resolve.Program, info *types.Info) diag.List {
	c := &checker{
		info:     info,
		reported: map[diagKey]bool{},
		leaked:   map[*resolve.Symbol]bool{},
		slices:   map[types.Type]bool{},
		lits:     map[*ast.FuncLit]bool{},
	}
	for _, fi := range info.Funcs {
		if fi.Decl == nil || fi.Body == nil {
			continue // literals are checked with their enclosing declaration
		}
		c.checkDecl(fi)
	}
	c.diags.Sort()
	return c.diags
}

type diagKey struct {
	code diag.Code
	file string
	off  int
}

type checker struct {
	info     *types.Info
	diags    diag.List
	reported map[diagKey]bool
	leaked   map[*resolve.Symbol]bool // E3005 is reported once per variable
	slices   map[types.Type]bool      // memo for hasSlice
	lits     map[*ast.FuncLit]bool    // literals whose linearity was analysed

	// Per declaration (including its nested literals).
	fn *fnState
}

func (c *checker) errorf(code diag.Code, sp diag.SourceSpan, format string, args ...any) {
	k := diagKey{code, sp.File, sp.Start.Offset}
	if c.reported[k] {
		return
	}
	c.reported[k] = true
	c.diags.Addf(code, sp, format, args...)
}

// origin is the depth of the storage a value may refer to; region (if
// non-nil) narrows a caller-level origin to the storage reachable from one
// parameter.
type origin struct {
	d int
	r *resolve.Symbol
}

func (o origin) String() string {
	switch {
	case o.d == levelGlobal:
		return "package-level storage"
	case o.r != nil:
		return fmt.Sprintf("the storage of parameter %s (depth %d)", o.r.Name, o.d-1)
	case o.d == levelCaller:
		return "the caller's storage"
	}
	return fmt.Sprintf("storage at depth %d", o.d-1)
}

func join(a, b origin) origin {
	switch {
	case a.d > b.d:
		return a
	case b.d > a.d:
		return b
	case a.r == b.r:
		return a
	}
	return origin{d: a.d}
}

// fits reports whether a value of origin o may be stored in storage s.
func fits(o, s origin) bool {
	return o.d < s.d || o.d == s.d && (s.r == nil || s.r == o.r)
}

// def records that the value of rhs flows into the variable sym.
type def struct {
	sym *resolve.Symbol
	rhs ast.Expr
}

// fnState holds the depth/origin tables of one declaration and its
// literals.
type fnState struct {
	depth   map[*resolve.Symbol]int
	content map[*resolve.Symbol]origin
	addr    map[*resolve.Symbol]bool
	fixed   map[*resolve.Symbol]bool // scope variables: content is fixed
	defs    []def
}

func (c *checker) checkDecl(fi *types.FuncInfo) {
	c.fn = &fnState{
		depth:   map[*resolve.Symbol]int{},
		content: map[*resolve.Symbol]origin{},
		addr:    map[*resolve.Symbol]bool{},
		fixed:   map[*resolve.Symbol]bool{},
	}
	params := fi.Params
	if fi.RecvSym != nil {
		params = append([]*resolve.Symbol{fi.RecvSym}, params...)
	}
	for _, p := range params {
		c.fn.depth[p] = levelFunc
		c.fn.content[p] = origin{d: levelCaller, r: p}
	}
	// Phase 1: depths, address-taken variables, value flows.
	w := &walker{c: c, depth: levelFunc, fnDepth: levelFunc, collect: true}
	w.block(fi.Body)
	c.fixpoint()
	// Phase 2: S2-S4 and E3004.
	w = &walker{c: c, depth: levelFunc, fnDepth: levelFunc}
	w.block(fi.Body)
	// S5 / E3003.
	c.linearity(fi)
	c.fn = nil
}

// fixpoint computes the content origin of every local variable.
func (c *checker) fixpoint() {
	for changed := true; changed; {
		changed = false
		for _, d := range c.fn.defs {
			if c.fn.fixed[d.sym] {
				continue
			}
			o := c.origin(d.rhs)
			old, ok := c.fn.content[d.sym]
			n := o
			if ok {
				n = join(old, o)
			}
			if dep, ok := c.fn.depth[d.sym]; ok && !fits(n, origin{d: dep}) {
				n = origin{d: dep} // an S2 violation, reported in phase 2
			}
			if !ok || n != old {
				c.fn.content[d.sym] = n
				changed = true
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Types

// tracked reports whether values of type t may refer to stack storage:
// Scoped types (pointers, resources, function values) and Value types that
// contain slices.
func (c *checker) tracked(t types.Type) bool {
	if t == nil {
		return false
	}
	return types.IsScoped(t) || c.hasSlice(t)
}

func (c *checker) hasSlice(t types.Type) bool {
	if v, ok := c.slices[t]; ok {
		return v
	}
	c.slices[t] = false // cycles
	r := false
	switch u := t.(type) {
	case *types.Named:
		if u.Underlying() != nil {
			r = c.hasSlice(u.Underlying())
		}
	case *types.Slice:
		r = true
	case *types.Array:
		r = c.hasSlice(u.Elem)
	case *types.Struct:
		for _, f := range u.Fields {
			r = r || c.hasSlice(f.Type)
		}
	case *types.Enum:
		for _, v := range u.Variants {
			for _, p := range v.Payload {
				r = r || c.hasSlice(p)
			}
		}
	case *types.Tuple:
		for _, e := range u.Types {
			r = r || c.hasSlice(e)
		}
	}
	c.slices[t] = r
	return r
}

func under(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
}

func isArray(t types.Type) bool {
	_, ok := under(t).(*types.Array)
	return ok
}

func isErrorType(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && b.Kind == types.Error
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// ---------------------------------------------------------------------------
// Origins

// varSym returns the variable symbol an identifier refers to (nil for
// blank, functions, constants, packages ...).
func (c *checker) varSym(id *ast.Ident) *resolve.Symbol {
	s := c.info.SymbolOf(id)
	if s == nil || s.Kind != resolve.SymVar {
		return nil
	}
	return s
}

// load is the origin of the value held by variable s.
func (c *checker) load(s *resolve.Symbol) origin {
	if s.IsGlobal() {
		return origin{d: levelGlobal}
	}
	if c.fn.addr[s] {
		return c.storageOf(s)
	}
	return c.fn.content[s]
}

// storageOf is the storage origin of variable s itself.
func (c *checker) storageOf(s *resolve.Symbol) origin {
	if s.IsGlobal() {
		return origin{d: levelGlobal}
	}
	return origin{d: c.fn.depth[s]}
}

// rootVar returns the variable whose own storage contains the addressable
// expression e (x, x.f, x[i] on arrays), or nil if e is reached through a
// pointer or slice.
func (c *checker) rootVar(e ast.Expr) *resolve.Symbol {
	switch e := unparen(e).(type) {
	case *ast.Ident:
		return c.varSym(e)
	case *ast.SelectorExpr:
		sel := c.info.SelectionOf(e)
		if sel == nil {
			if id := e.Sel; id != nil {
				return c.varSym(id) // pkg.Var
			}
			return nil
		}
		if sel.Kind != types.SelField || sel.Indirect {
			return nil
		}
		return c.rootVar(e.X)
	case *ast.IndexExpr:
		if isArray(c.info.TypeOf(e.X)) {
			return c.rootVar(e.X)
		}
	}
	return nil
}

// storage is the origin of the storage denoted by the addressable
// expression e.
func (c *checker) storage(e ast.Expr) origin {
	switch e := unparen(e).(type) {
	case *ast.Ident:
		if s := c.varSym(e); s != nil {
			return c.storageOf(s)
		}
	case *ast.SelectorExpr:
		sel := c.info.SelectionOf(e)
		if sel == nil {
			return origin{d: levelGlobal} // pkg.Var
		}
		if sel.Indirect {
			return c.origin(e.X)
		}
		return c.storage(e.X)
	case *ast.IndexExpr:
		if isArray(c.info.TypeOf(e.X)) {
			return c.storage(e.X)
		}
		return c.origin(e.X) // slice or pointer to array
	case *ast.UnaryExpr:
		if e.Op == ast.OpDeref {
			return c.origin(e.X)
		}
	}
	return c.origin(e)
}

// origin computes the origin of the value of e.
func (c *checker) origin(e ast.Expr) origin {
	switch e := unparen(e).(type) {
	case *ast.Ident:
		if s := c.varSym(e); s != nil {
			return c.load(s)
		}
	case *ast.UnaryExpr:
		switch e.Op {
		case ast.OpAddr:
			return c.storage(e.X)
		case ast.OpDeref:
			return c.origin(e.X)
		}
	case *ast.SelectorExpr:
		sel := c.info.SelectionOf(e)
		if sel == nil {
			return origin{d: levelGlobal}
		}
		if sel.Kind == types.SelField {
			return c.origin(e.X)
		}
	case *ast.IndexExpr:
		if c.info.InstanceOf(e) != nil {
			return origin{}
		}
		return c.origin(e.X)
	case *ast.SliceExpr:
		if isArray(c.info.TypeOf(e.X)) {
			return c.storage(e.X)
		}
		return c.origin(e.X)
	case *ast.CallExpr:
		return c.callOrigin(e)
	case *ast.StructLit:
		var o origin
		for _, f := range e.Fields {
			if c.tracked(c.info.TypeOf(f.Value)) {
				o = join(o, c.origin(f.Value))
			}
		}
		return o
	case *ast.FuncLit:
		var o origin
		fi := c.info.FuncOf(e)
		if fi == nil {
			return o
		}
		for _, s := range fi.Captures {
			if c.tracked(c.info.SymType(s)) {
				o = join(o, c.load(s))
			}
		}
		return o
	}
	return origin{}
}

func (c *checker) callOrigin(e *ast.CallExpr) origin {
	ci := c.info.CallOf(e)
	var o origin
	if ci == nil {
		return o
	}
	switch ci.Kind {
	case types.CallConversion:
		if len(e.Args) == 1 {
			return c.origin(e.Args[0])
		}
		return o
	case types.CallMethod, types.CallBuiltinMeth:
		if ci.Recv != nil && c.tracked(c.info.TypeOf(ci.Recv)) {
			o = c.origin(ci.Recv)
		}
	case types.CallValue:
		o = c.origin(e.Fun)
	case types.CallBuiltin:
		switch ci.Builtin {
		case resolve.FStrBytes, resolve.FBytesToStr, resolve.FNewContext, resolve.FNewScope:
			return o // views of immutable strings / fresh runtime resources
		case resolve.FFormatTo:
			return o // a byte count; dst is only borrowed (v0.2)
		}
	}
	for _, a := range e.Args {
		if c.tracked(c.info.TypeOf(a)) {
			o = join(o, c.origin(a))
		}
	}
	return o
}

// ---------------------------------------------------------------------------
// Walker: phase 1 (collect) and phase 2 (check S2-S4, E3004)

type walker struct {
	c       *checker
	depth   int // current block depth
	fnDepth int // depth of the innermost function body
	collect bool
}

func (w *walker) block(b *ast.BlockStmt) {
	if b == nil {
		return
	}
	for _, s := range b.Stmts {
		w.stmt(s)
	}
}

func (w *walker) define(id *ast.Ident) *resolve.Symbol {
	s := w.c.varSym(id)
	if s != nil && w.collect && isDef(w.c.info, id, s) {
		w.c.fn.depth[s] = w.depth
	}
	return s
}

func (w *walker) flow(s *resolve.Symbol, rhs ast.Expr) {
	if w.collect && s != nil && w.c.tracked(w.c.info.SymType(s)) {
		w.c.fn.defs = append(w.c.fn.defs, def{s, rhs})
	}
}

func (w *walker) stmt(s ast.Stmt) {
	c := w.c
	switch s := s.(type) {
	case nil:
	case *ast.BlockStmt:
		w.block(s)
	case *ast.DeclStmt:
		if d, ok := s.Decl.(*ast.VarDecl); ok {
			if d.Value != nil {
				w.expr(d.Value)
			}
			sym := w.define(d.Name)
			if d.Value != nil {
				w.flow(sym, d.Value)
				if d.Name.Name == "_" {
					w.discard(d.Value, -1, d.Range)
				}
			}
		}
	case *ast.ShortVarDecl:
		for _, v := range s.Values {
			w.expr(v)
		}
		for i, id := range s.Names {
			rhs, idx := pairRHS(s.Values, len(s.Names), i)
			if id.Name == "_" {
				if rhs != nil {
					w.discard(rhs, idx, id.Range)
				}
				continue
			}
			sym := w.define(id)
			if rhs != nil {
				w.flow(sym, rhs)
			}
		}
	case *ast.AssignStmt:
		for _, l := range s.LHS {
			if id, ok := l.(*ast.Ident); !ok || id.Name != "_" {
				w.lhsExpr(l)
			}
		}
		for _, r := range s.RHS {
			w.expr(r)
		}
		if s.Op != ast.Assign {
			return
		}
		for i, l := range s.LHS {
			rhs, idx := pairRHS(s.RHS, len(s.LHS), i)
			if rhs == nil {
				continue
			}
			if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
				w.discard(rhs, idx, s.Range)
				continue
			}
			if !c.tracked(c.info.TypeOf(l)) {
				continue
			}
			if w.collect {
				if root := c.rootVar(l); root != nil && !root.IsGlobal() {
					w.flow(root, rhs)
				}
				continue
			}
			o, st := c.origin(rhs), c.storage(l)
			if !fits(o, st) {
				c.errorf(diag.E3002, s.Range, "scope escape: a value referring to %s cannot be stored in %s, which outlives it", o, st)
			}
		}
	case *ast.IncDecStmt:
		w.expr(s.X)
	case *ast.ExprStmt:
		w.expr(s.X)
		if call, ok := unparen(s.X).(*ast.CallExpr); ok && !w.collect {
			if t := c.info.TypeOf(call); t != nil && types.IsLinear(t) {
				c.errorf(diag.E3004, s.Range, "result of type %s (Linear) is discarded", t)
			}
		}
	case *ast.IfStmt:
		w.stmt(s.Init)
		w.expr(s.Cond)
		w.block(s.Then)
		w.stmt(s.Else)
	case *ast.MatchStmt:
		w.expr(s.X)
		for _, arm := range s.Arms {
			if p, ok := arm.Pattern.(*ast.CtorPattern); ok {
				for _, b := range p.Bindings {
					if b.Name == "_" {
						continue
					}
					w.flow(w.define(b), s.X)
				}
			}
			if arm.Expr != nil {
				w.expr(arm.Expr)
				if t := c.info.TypeOf(arm.Expr); !w.collect && t != nil && types.IsLinear(t) {
					c.errorf(diag.E3004, arm.Expr.Span(), "result of type %s (Linear) is discarded", t)
				}
			}
			w.block(arm.Block)
		}
	case *ast.ForStmt:
		w.stmt(s.Init)
		if s.Cond != nil {
			w.expr(s.Cond)
		}
		w.stmt(s.Post)
		w.block(s.Body)
	case *ast.ScopeStmt:
		w.expr(s.Value)
		w.depth++
		if sym := w.c.varSym(s.Name); sym != nil && w.collect {
			c.fn.depth[sym] = w.depth
			c.fn.content[sym] = origin{d: w.depth}
			c.fn.fixed[sym] = true
		}
		w.block(s.Body)
		w.depth--
	case *ast.DeferStmt:
		w.expr(s.X)
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			w.expr(r)
		}
		if w.collect {
			return
		}
		for _, r := range s.Results {
			if !c.tracked(c.info.TypeOf(r)) {
				continue
			}
			if o := c.origin(r); o.d >= w.fnDepth {
				c.errorf(diag.E3002, r.Span(), "scope escape: the returned value refers to %s, which does not outlive the function", o)
			}
		}
	}
}

// pairRHS returns the right-hand side for target i of an n-target
// assignment, and the tuple index (-1 for a 1:1 assignment).
func pairRHS(vals []ast.Expr, n, i int) (ast.Expr, int) {
	if len(vals) == n {
		return vals[i], -1
	}
	if len(vals) == 1 {
		return vals[0], i
	}
	return nil, -1
}

// discard reports E3004 if the value (or tuple element idx) is Linear.
func (w *walker) discard(rhs ast.Expr, idx int, sp diag.SourceSpan) {
	if w.collect {
		return
	}
	t := w.c.info.TypeOf(rhs)
	if tu, ok := t.(*types.Tuple); ok && idx >= 0 && idx < len(tu.Types) {
		t = tu.Types[idx]
	}
	if t != nil && types.IsLinear(t) {
		w.c.errorf(diag.E3004, sp, "Linear value of type %s cannot be discarded with _", t)
	}
}

// lhsExpr walks the sub-expressions of an assignment target.
func (w *walker) lhsExpr(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Ident:
	default:
		w.expr(e)
	}
}

func (w *walker) expr(e ast.Expr) {
	c := w.c
	switch e := e.(type) {
	case nil:
	case *ast.ParenExpr:
		w.expr(e.X)
	case *ast.UnaryExpr:
		if e.Op == ast.OpAddr && w.collect {
			if r := c.rootVar(e.X); r != nil {
				c.fn.addr[r] = true
			}
		}
		w.expr(e.X)
	case *ast.BinaryExpr:
		w.expr(e.X)
		w.expr(e.Y)
	case *ast.SelectorExpr:
		w.expr(e.X)
	case *ast.IndexExpr:
		w.expr(e.X)
		for _, i := range e.Indices {
			w.expr(i)
		}
	case *ast.SliceExpr:
		if w.collect && isArray(c.info.TypeOf(e.X)) {
			if r := c.rootVar(e.X); r != nil {
				c.fn.addr[r] = true
			}
		}
		w.expr(e.X)
		w.expr(e.Lo)
		w.expr(e.Hi)
	case *ast.StructLit:
		for _, f := range e.Fields {
			w.expr(f.Value)
		}
	case *ast.CallExpr:
		w.expr(e.Fun)
		for _, a := range e.Args {
			w.expr(a)
		}
		if !w.collect {
			w.checkGo(e)
		}
	case *ast.FuncLit:
		w.funcLit(e)
	}
}

func (w *walker) funcLit(lit *ast.FuncLit) {
	c := w.c
	saveD, saveF := w.depth, w.fnDepth
	w.depth++
	w.fnDepth = w.depth
	if fi := c.info.FuncOf(lit); fi != nil && w.collect {
		for _, p := range fi.Params {
			c.fn.depth[p] = w.depth
			c.fn.content[p] = origin{d: w.depth - 1, r: p}
		}
	}
	w.block(lit.Body)
	w.depth, w.fnDepth = saveD, saveF
}

// checkGo applies S4 to s.Go(slot, fn).
func (w *walker) checkGo(e *ast.CallExpr) {
	c := w.c
	ci := c.info.CallOf(e)
	if ci == nil || ci.Kind != types.CallBuiltinMeth || ci.Method == nil || ci.Method.Builtin != types.ScopeGo || len(e.Args) < 2 {
		return
	}
	so := c.origin(ci.Recv)
	fn := unparen(e.Args[1])
	lit, ok := fn.(*ast.FuncLit)
	if !ok {
		if o := c.origin(fn); !fits(o, so) {
			c.errorf(diag.E3002, fn.Span(), "scope escape: task function refers to %s, which does not outlive the scope", o)
		}
		return
	}
	fi := c.info.FuncOf(lit)
	if fi == nil {
		return
	}
	for _, s := range fi.Captures {
		t := c.info.SymType(s)
		if types.IsLinear(t) || !c.tracked(t) {
			continue // Linear: moved into the task (S5); Value: copied
		}
		if o := c.load(s); !fits(o, so) {
			sp := lit.Range
			if id := firstRef(c.info, lit.Body, s); id != nil {
				sp = id.Range
			}
			c.errorf(diag.E3002, sp, "scope escape: task captures %s, which refers to %s and does not outlive the scope", s.Name, o)
		}
	}
}

// firstRef returns the first identifier in n referring to s.
func firstRef(info *types.Info, n ast.Node, s *resolve.Symbol) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(n, func(x ast.Node) bool {
		if found != nil {
			return false
		}
		if id, ok := x.(*ast.Ident); ok && info.SymbolOf(id) == s {
			found = id
			return false
		}
		return true
	})
	return found
}

// isDef reports whether id is the defining identifier of s.
func isDef(info *types.Info, id *ast.Ident, s *resolve.Symbol) bool {
	n := int(id.ID())
	return n < len(info.Prog.Defs) && info.Prog.Defs[n] == s
}
