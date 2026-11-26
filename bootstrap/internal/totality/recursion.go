package totality

import (
	"slices"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// T3 for recursion: Tarjan SCCs of the call graph of total functions.
//
// Nodes are the total functions, methods and function literals. Edges:
//   - static calls (CallFunc, CallMethod; methods resolved by receiver
//     type through Call.Callee) to total functions;
//   - calls through function values (CallValue) to every total function
//     that escapes as a value: every function literal and every declared
//     function referenced other than as the callee of a call.
//
// Calls to driver functions are E5001 and calls to comptime functions can
// never lead back to total code, so neither yields edges.

type edge struct {
	to    int
	call  *ast.CallExpr
	value bool // through a function value
}

type graph struct {
	nodes []*types.FuncInfo
	index map[*types.FuncInfo]int
	edges [][]edge
}

func (c *checker) buildGraph() *graph {
	g := &graph{index: map[*types.FuncInfo]int{}}
	for _, fi := range c.info.Funcs {
		if fi.Effect == types.Total && fi.Body != nil {
			g.index[fi] = len(g.nodes)
			g.nodes = append(g.nodes, fi)
		}
	}
	g.edges = make([][]edge, len(g.nodes))
	escaped := c.escapedFuncs(g)
	for i, fi := range g.nodes {
		for _, call := range fi.Calls {
			cl := c.info.CallOf(call)
			if cl == nil {
				continue
			}
			switch cl.Kind {
			case types.CallFunc, types.CallMethod:
				if j, ok := g.index[c.info.FuncOfSym(cl.Callee)]; ok {
					g.edges[i] = append(g.edges[i], edge{to: j, call: call})
				}
			case types.CallValue:
				if cl.Effect == types.Driver {
					continue
				}
				for _, j := range escaped {
					g.edges[i] = append(g.edges[i], edge{to: j, call: call, value: true})
				}
			}
		}
	}
	return g
}

// escapedFuncs returns the graph nodes that may be called through a
// function value, in node order.
func (c *checker) escapedFuncs(g *graph) []int {
	callee := map[ast.Node]bool{}
	esc := map[int]bool{}
	for _, pkg := range c.info.Prog.Packages {
		for _, f := range pkg.AST.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					fun := unparen(n.Fun)
					if ix, ok := fun.(*ast.IndexExpr); ok {
						fun = unparen(ix.X)
					}
					switch x := fun.(type) {
					case *ast.Ident:
						callee[x] = true
					case *ast.SelectorExpr:
						callee[x.Sel] = true
					}
				case *ast.FuncDecl:
					callee[n.Name] = true
				case *ast.Ident:
					if callee[n] {
						return true
					}
					if s := c.info.SymbolOf(n); s != nil && s.Kind == resolve.SymFunc {
						if j, ok := g.index[c.info.FuncOfSym(s)]; ok {
							esc[j] = true
						}
					}
				case *ast.FuncLit:
					if j, ok := g.index[c.info.FuncOf(n)]; ok {
						esc[j] = true
					}
				}
				return true
			})
		}
	}
	var out []int
	for j := range g.nodes {
		if esc[j] {
			out = append(out, j)
		}
	}
	return out
}

// tarjan returns the strongly connected components in reverse topological
// order (deterministic: nodes and edges are visited in source order).
func (g *graph) tarjan() [][]int {
	n := len(g.nodes)
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	var sccs [][]int
	next := 0
	var strong func(v int)
	strong = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, e := range g.edges[v] {
			w := e.to
			if index[w] < 0 {
				strong(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var scc []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			slices.Sort(scc)
			sccs = append(sccs, scc)
		}
	}
	for v := 0; v < n; v++ {
		if index[v] < 0 {
			strong(v)
		}
	}
	return sccs
}

func spanLess(a, b diag.SourceSpan) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Start.Offset < b.Start.Offset
}

func (c *checker) checkRecursion() {
	g := c.buildGraph()
	for _, scc := range g.tarjan() {
		inSCC := map[int]bool{}
		for _, v := range scc {
			inSCC[v] = true
		}
		self := false
		if len(scc) == 1 {
			for _, e := range g.edges[scc[0]] {
				if e.to == scc[0] {
					self = true
				}
			}
			if !self {
				continue
			}
		}
		c.checkSCC(g, scc, inSCC)
	}
}

func (c *checker) checkSCC(g *graph, scc []int, inSCC map[int]bool) {
	code := diag.E4004
	if len(scc) == 1 {
		code = diag.E4002
	}
	members := make([]*types.FuncInfo, len(scc))
	for i, v := range scc {
		members[i] = g.nodes[v]
	}
	slices.SortStableFunc(members, func(a, b *types.FuncInfo) int {
		if spanLess(a.Node().Span(), b.Node().Span()) {
			return -1
		}
		if spanLess(b.Node().Span(), a.Node().Span()) {
			return 1
		}
		return 0
	})
	// every member needs a decreases clause
	var missing []string
	for _, fi := range members {
		if fi.Decreases == nil {
			missing = append(missing, fi.Name())
		}
	}
	if len(missing) > 0 {
		if len(scc) == 1 {
			c.errorf(code, members[0].Node(), "recursive function %s has no decreases clause", members[0].Name())
		} else {
			names := make([]string, len(members))
			for i, fi := range members {
				names[i] = fi.Name()
			}
			c.errorf(code, members[0].Node(), "mutually recursive functions {%s} need a decreases clause each (missing in %s)",
				strings.Join(names, ", "), strings.Join(missing, ", "))
		}
		return
	}
	// measures: an unsigned parameter that the function never writes
	measure := map[int]int{} // node -> parameter index
	for _, v := range scc {
		fi := g.nodes[v]
		idx, why := c.measureParam(fi)
		if why != "" {
			c.errorf(code, fi.Decreases, "decreases measure of recursive function %s: %s", fi.Name(), why)
			return
		}
		measure[v] = idx
	}
	// every call inside the SCC passes a strictly smaller measure
	for _, v := range scc {
		fi := g.nodes[v]
		d := fi.Params[measure[v]]
		rf := c.rootFactsOf(fi)
		targets := map[*ast.CallExpr][]edge{}
		for _, e := range g.edges[v] {
			if inSCC[e.to] {
				targets[e.call] = append(targets[e.call], e)
			}
		}
		w := &walker{c: c, rf: rf}
		w.onCall = func(call *ast.CallExpr, st *state) {
			es := targets[call]
			if len(es) == 0 {
				return
			}
			if es[0].value {
				c.errorf(code, call, "call through a function value inside a recursive group cannot be proven to decrease a measure")
				return
			}
			callee := g.nodes[es[0].to]
			j := measure[es[0].to]
			if j >= len(call.Args) {
				c.errorf(code, call, "cannot find the measure argument of the call to %s", callee.Name())
				return
			}
			if !c.smaller(call.Args[j], d, st) {
				c.errorf(code, call, "recursive call to %s must pass a measure strictly smaller than %s (e.g. %s - 1 after `if %s == 0 { return ... }`)",
					callee.Name(), d.Name, d.Name, d.Name)
			}
		}
		w.stmt(fi.Body, newState())
	}
}

// measureParam checks that the decreases measure of fi is one of its
// (non-receiver) unsigned parameters, never assigned in fi.
func (c *checker) measureParam(fi *types.FuncInfo) (int, string) {
	s := c.varOf(c.stripConv(fi.Decreases))
	idx := slices.Index(fi.Params, s)
	if s == nil || idx < 0 {
		return 0, "the measure must be a parameter of the function"
	}
	if !types.IsUnsigned(c.info.SymType(s)) {
		return 0, "the measure parameter must be unsigned"
	}
	if fi.Sig != nil && fi.Sig.Variadic && idx == len(fi.Params)-1 {
		return 0, "the measure parameter must not be variadic"
	}
	rf := c.rootFactsOf(fi)
	if rf.unstable(s) {
		return 0, "the measure parameter " + s.Name + " must not be addressed or written by a function literal"
	}
	writes, _ := writesAndCalls(fi.Body)
	for _, wr := range writes {
		if c.storageRoot(wr) == s {
			return 0, "the measure parameter " + s.Name + " must not be assigned"
		}
	}
	return idx, ""
}

// smaller reports whether arg < d is proven: d - c with d >= c (c >= 1),
// d / c (c >= 2) or d >> c (c >= 1) with d >= 1.
func (c *checker) smaller(arg ast.Expr, d *resolve.Symbol, st *state) bool {
	b, ok := c.stripConv(arg).(*ast.BinaryExpr)
	if !ok || c.varOf(c.stripConv(b.X)) != d {
		return false
	}
	k, ok := c.constUint(b.Y)
	if !ok {
		return false
	}
	lb := st.lb[d]
	switch b.Op {
	case ast.OpSub:
		return k >= 1 && lb >= k
	case ast.OpDiv:
		return k >= 2 && lb >= 1
	case ast.OpShr:
		return k >= 1 && lb >= 1
	}
	return false
}
