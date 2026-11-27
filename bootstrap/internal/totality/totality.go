package totality

import (
	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// Check runs the effect and totality checker over a well-typed program and
// returns its diagnostics (E5001, E4002, E4004), unsorted.
func Check(prog *resolve.Program, info *types.Info) diag.List {
	c := &checker{info: info, roots: map[*types.FuncInfo]*rootFacts{}}
	for _, fi := range info.Funcs {
		c.checkEffects(fi)
		if fi.Effect != types.Comptime {
			c.checkLoops(fi)
		}
	}
	c.checkRecursion()
	return c.diags
}

type checker struct {
	info  *types.Info
	diags diag.List
	roots map[*types.FuncInfo]*rootFacts
}

func (c *checker) errorf(code diag.Code, n ast.Node, format string, args ...any) {
	c.diags.Addf(code, n.Span(), format, args...)
}

// ---------------------------------------------------------------------------
// E5001: effect containment (spec 6.1)

func (c *checker) checkEffects(fi *types.FuncInfo) {
	if fi.Effect == types.Driver || fi.Body == nil {
		return
	}
	mode := "total"
	if fi.Effect == types.Comptime {
		mode = "comptime"
	}
	for _, call := range fi.Calls {
		cl := c.info.CallOf(call)
		if cl == nil || cl.Effect != types.Driver {
			continue
		}
		c.errorf(diag.E5001, call, "%s function %s calls driver %s", mode, fi.Name(), c.calleeName(call, cl))
	}
	inspectBody(fi.Body, func(n ast.Node) bool {
		if f, ok := n.(*ast.ForStmt); ok && f.Driver {
			c.errorf(diag.E5001, f, "driver for loop in %s function %s", mode, fi.Name())
		}
		return true
	})
}

func (c *checker) calleeName(call *ast.CallExpr, cl *types.Call) string {
	switch cl.Kind {
	case types.CallFunc, types.CallMethod:
		if f := c.info.FuncOfSym(cl.Callee); f != nil {
			return "function " + f.Name()
		}
	case types.CallBuiltin:
		if cl.Builtin == resolve.FPanic {
			return "builtin panic"
		}
	case types.CallBuiltinMeth:
		if cl.Method != nil && cl.Method.Recv != nil {
			return "method " + cl.Method.Recv.Name + "." + cl.Method.Name
		}
	case types.CallValue:
		return "function value"
	}
	if s, ok := call.Fun.(*ast.Ident); ok {
		return s.Name
	}
	return "function"
}

// inspectBody visits the statements and expressions of a function body,
// without descending into nested function literals (they have their own
// FuncInfo).
func inspectBody(n ast.Node, f func(ast.Node) bool) {
	ast.Inspect(n, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		return f(n)
	})
}

// ---------------------------------------------------------------------------
// loops (T1, T2, T3 for loops, T4)

func (c *checker) checkLoops(fi *types.FuncInfo) {
	if fi.Body == nil {
		return
	}
	inspectBody(fi.Body, func(n ast.Node) bool {
		f, ok := n.(*ast.ForStmt)
		if !ok || f.Driver {
			// `driver for` is exempt (decision 8); in total functions it is
			// E5001 (checkEffects).
			return true
		}
		lc := &loopCtx{c: c, fi: fi, loop: f, rf: c.rootFactsOf(fi)}
		if why := lc.check(); why != "" {
			c.errorf(diag.E4002, f, "cannot prove termination of for loop: %s", why)
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// per-declaration facts: variables whose address is taken or that are
// written by function literals. Such variables may change behind the back
// of a syntactic analysis (through a pointer or a closure call), so the
// loop and recursion rules never trust facts about them.

type rootFacts struct {
	addr       map[*resolve.Symbol]bool // &x, &x.f, &x[i] (array), x[a:b] (array)
	litWritten map[*resolve.Symbol]bool // assigned inside a literal that captures it
}

func (c *checker) rootFactsOf(fi *types.FuncInfo) *rootFacts {
	for fi.Outer != nil {
		fi = fi.Outer
	}
	if rf := c.roots[fi]; rf != nil {
		return rf
	}
	rf := &rootFacts{addr: map[*resolve.Symbol]bool{}, litWritten: map[*resolve.Symbol]bool{}}
	c.roots[fi] = rf
	if fi.Body != nil {
		c.collectRootFacts(rf, fi.Body, nil)
	}
	return rf
}

func (c *checker) collectRootFacts(rf *rootFacts, n ast.Node, lit *ast.FuncLit) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			if n != lit {
				c.collectRootFacts(rf, n.Body, n)
				return false
			}
		case *ast.UnaryExpr:
			if n.Op == ast.OpAddr {
				if s := c.storageRoot(n.X); s != nil {
					rf.addr[s] = true
				}
			}
		case *ast.CallExpr:
			if ci := c.info.CallOf(n); ci != nil && ci.AutoAddr {
				// x.M() with a pointer receiver takes &x (v0.2)
				if s := c.storageRoot(ci.Recv); s != nil {
					rf.addr[s] = true
				}
			}
		case *ast.SliceExpr:
			if _, ok := under(c.info.TypeOf(n.X)).(*types.Array); ok {
				if s := c.storageRoot(n.X); s != nil {
					rf.addr[s] = true
				}
			}
		case *ast.AssignStmt:
			if lit != nil {
				for _, l := range n.LHS {
					c.markLitWrite(rf, l, lit)
				}
			}
		case *ast.IncDecStmt:
			if lit != nil {
				c.markLitWrite(rf, n.X, lit)
			}
		}
		return true
	})
}

func (c *checker) markLitWrite(rf *rootFacts, lhs ast.Expr, lit *ast.FuncLit) {
	if s := c.storageRoot(lhs); s != nil && s.Func != ast.Node(lit) {
		rf.litWritten[s] = true
	}
}

// unstable reports whether facts about the variable s cannot be trusted.
func (rf *rootFacts) unstable(s *resolve.Symbol) bool {
	return s == nil || rf.addr[s] || rf.litWritten[s] || s.VarKind == resolve.VarGlobal
}

// storageRoot returns the variable whose own storage e denotes (a part
// of): x, x.f (no pointer indirection), x[i] for arrays, (x). It returns nil
// for expressions denoting heap storage (through pointers or slices).
func (c *checker) storageRoot(e ast.Expr) *resolve.Symbol {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.Ident:
			s := c.info.SymbolOf(x)
			if s == nil || s.Kind != resolve.SymVar {
				return nil
			}
			return s
		case *ast.SelectorExpr:
			sel := c.info.SelectionOf(x)
			if sel == nil || sel.Kind != types.SelField || sel.Indirect {
				return nil
			}
			e = x.X
		case *ast.IndexExpr:
			if _, ok := under(c.info.TypeOf(x.X)).(*types.Array); !ok || len(x.Indices) != 1 {
				return nil
			}
			e = x.X
		default:
			return nil
		}
	}
}

// ---------------------------------------------------------------------------
// small helpers

func under(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
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

// constUint returns the value of a non-negative integer constant expression.
func (c *checker) constUint(e ast.Expr) (uint64, bool) {
	if e == nil {
		return 0, false
	}
	v := c.info.ValueOf(e)
	if v == nil {
		return 0, false
	}
	return types.Uint64(v)
}

// varOf returns the variable symbol of an identifier expression (after
// parentheses), or nil.
func (c *checker) varOf(e ast.Expr) *resolve.Symbol {
	id, ok := unparen(e).(*ast.Ident)
	if !ok {
		return nil
	}
	s := c.info.SymbolOf(id)
	if s == nil || s.Kind != resolve.SymVar {
		return nil
	}
	return s
}

// identityConv returns the operand of a conversion T(x) that does not
// change the value (x already has type T), else e itself.
func (c *checker) stripConv(e ast.Expr) ast.Expr {
	for {
		e = unparen(e)
		call, ok := e.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return e
		}
		cl := c.info.CallOf(call)
		if cl == nil || cl.Kind != types.CallConversion || !types.Identical(cl.Conv, c.info.TypeOf(call.Args[0])) {
			return e
		}
		e = call.Args[0]
	}
}

// lenArg returns x for len(x) (nil otherwise).
func (c *checker) lenArg(e ast.Expr) ast.Expr {
	call, ok := c.stripConv(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil
	}
	cl := c.info.CallOf(call)
	if cl == nil || cl.Kind != types.CallBuiltin || cl.Builtin != resolve.FLen {
		return nil
	}
	return call.Args[0]
}

func (c *checker) isUnsigned(e ast.Expr) bool { return types.IsUnsigned(c.info.TypeOf(e)) }

func symType(c *checker, s *resolve.Symbol) types.Type { return c.info.SymType(s) }
