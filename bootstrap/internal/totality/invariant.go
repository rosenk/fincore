package totality

import (
	"slices"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// Loop-invariance of expressions (bounds of T1 loops, len(s) leaves of
// T3 measures).
//
// An invariant expression is built from constants, arithmetic, identity
// conversions, len/cap and access paths x.f.g (x a variable; field
// selections possibly through pointers). Each access path is an atom.
//
// A local atom (x a stable local/param of this function: no &x, not
// written by a literal, and no pointer indirection on the path) changes
// only through a write whose own storage path is rooted at x and overlaps
// the atom's path (x = .., x.f = .., x.arr[i] = ..). Writes to slice
// elements do not change a slice header, so `s[i] = v` keeps len(s).
//
// A heap atom (global root, pointer indirection, unstable or captured
// root) may also change through aliases, so it conflicts with:
//   - any call that may write memory: calls of declared functions,
//     methods, function values and capability methods; copy/formatUint
//     into a slice whose element type contains one of the atom's struct
//     types;
//   - a write to a field with the same name as a field of the path, or to
//     a value whose type contains one of the structs on the path;
//   - a write through a pointer (*p = ..) whose pointee type contains one
//     of those structs or the type of a field on the path;
//   - an element write s[i] = .. on a slice whose element type contains
//     one of those structs.
//
// This relies on typed memory: a value of type T can only alias storage
// of type T.

type atom struct {
	root    *resolve.Symbol
	path    []string     // field names, outermost first
	heap    bool         // aliasable storage
	structs []types.Type // struct types owning the fields on the path
	leafs   []types.Type // field types on the path and the root's type
}

// atomsOf decomposes an invariant-candidate expression. ok is false if e
// has a form that is never considered invariant (calls, indexing, ...).
func (c *checker) atomsOf(e ast.Expr, fi *types.FuncInfo, rf *rootFacts) (out []atom, ok bool) {
	var walk func(e ast.Expr) bool
	walk = func(e ast.Expr) bool {
		if c.info.ValueOf(e) != nil {
			return true
		}
		switch x := e.(type) {
		case *ast.ParenExpr:
			return walk(x.X)
		case *ast.BinaryExpr:
			switch x.Op {
			case ast.OpAdd, ast.OpSub, ast.OpMul, ast.OpDiv, ast.OpRem, ast.OpAnd, ast.OpOr, ast.OpXor, ast.OpShl, ast.OpShr:
				return walk(x.X) && walk(x.Y)
			}
			return false
		case *ast.UnaryExpr:
			if x.Op == ast.OpNeg {
				return walk(x.X)
			}
			return false
		case *ast.CallExpr:
			cl := c.info.CallOf(x)
			if cl == nil || len(x.Args) != 1 {
				return false
			}
			if cl.Kind == types.CallConversion {
				return walk(x.Args[0])
			}
			if cl.Kind == types.CallBuiltin && (cl.Builtin == resolve.FLen || cl.Builtin == resolve.FCap) {
				return walk(x.Args[0])
			}
			return false
		case *ast.Ident, *ast.SelectorExpr:
			a, ok := c.pathAtom(e, fi, rf)
			if ok {
				out = append(out, a)
			}
			return ok
		}
		return false
	}
	if !walk(e) {
		return nil, false
	}
	return out, true
}

func (c *checker) pathAtom(e ast.Expr, fi *types.FuncInfo, rf *rootFacts) (atom, bool) {
	var a atom
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
			continue
		case *ast.UnaryExpr:
			if x.Op != ast.OpDeref {
				return a, false
			}
			a.heap = true
			e = x.X
			continue
		case *ast.SelectorExpr:
			if s := c.info.Prog.Uses[x.Sel.ID()]; s != nil {
				// pkg.Name: a package-level variable
				if s.Kind != resolve.SymVar {
					return a, false
				}
				a.root, a.heap = s, true
				return a, true
			}
			sel := c.info.SelectionOf(x)
			if sel == nil || sel.Kind != types.SelField {
				return a, false
			}
			if sel.Indirect {
				a.heap = true
			}
			a.path = append([]string{sel.Field.Name}, a.path...)
			a.leafs = append(a.leafs, sel.Field.Type)
			owner := c.info.TypeOf(x.X)
			if p, ok := under(owner).(*types.Pointer); ok {
				owner = p.Elem
			}
			a.structs = append(a.structs, owner)
			e = x.X
			continue
		case *ast.Ident:
			s := c.info.SymbolOf(x)
			if s == nil || s.Kind != resolve.SymVar {
				return a, false
			}
			a.root = s
			a.leafs = append(a.leafs, c.info.SymType(s))
			if rf.unstable(s) || s.Func != fi.Node() {
				a.heap = true
			}
			return a, true
		}
		return a, false
	}
}

// writesAndCalls collects the assignment targets and calls in nodes,
// including those inside nested function literals.
func writesAndCalls(nodes ...ast.Node) (writes []ast.Expr, calls []*ast.CallExpr) {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		ast.Inspect(n, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				writes = append(writes, n.LHS...)
			case *ast.IncDecStmt:
				writes = append(writes, n.X)
			case *ast.ShortVarDecl:
				for _, id := range n.Names {
					writes = append(writes, id)
				}
			case *ast.CallExpr:
				calls = append(calls, n)
			}
			return true
		})
	}
	return
}

// localPath returns the storage path of an assignment target if it is part
// of a variable's own storage (ok false for heap writes).
func (c *checker) localPath(lhs ast.Expr) (root *resolve.Symbol, path []string, ok bool) {
	e := lhs
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.Ident:
			s := c.info.SymbolOf(x)
			if s == nil || s.Kind != resolve.SymVar {
				return nil, nil, false
			}
			return s, path, true
		case *ast.SelectorExpr:
			sel := c.info.SelectionOf(x)
			if sel == nil || sel.Kind != types.SelField || sel.Indirect {
				return nil, nil, false
			}
			path = append([]string{sel.Field.Name}, path...)
			e = x.X
		case *ast.IndexExpr:
			if _, isArr := under(c.info.TypeOf(x.X)).(*types.Array); !isArr {
				return nil, nil, false
			}
			path = nil // a write to an element is a (partial) write of the array
			e = x.X
		default:
			return nil, nil, false
		}
	}
}

func prefixRelated(a, b []string) bool {
	n := min(len(a), len(b))
	return slices.Equal(a[:n], b[:n])
}

// invariant reports whether e is unchanged by executing nodes (loop body,
// post statement).
func (c *checker) invariant(e ast.Expr, fi *types.FuncInfo, rf *rootFacts, nodes ...ast.Node) bool {
	atoms, ok := c.atomsOf(e, fi, rf)
	if !ok {
		return false
	}
	if len(atoms) == 0 {
		return true
	}
	writes, calls := writesAndCalls(nodes...)
	for _, a := range atoms {
		for _, w := range writes {
			if c.writeConflicts(w, a) {
				return false
			}
		}
		if a.heap {
			for _, call := range calls {
				if c.callConflicts(call, a) {
					return false
				}
			}
		}
	}
	return true
}

func (c *checker) writeConflicts(w ast.Expr, a atom) bool {
	if root, path, ok := c.localPath(w); ok && root == a.root && prefixRelated(path, a.path) {
		return true
	}
	if !a.heap {
		return false
	}
	return c.heapWriteConflicts(unparen(w), a)
}

func (c *checker) heapWriteConflicts(w ast.Expr, a atom) bool {
	switch x := w.(type) {
	case *ast.Ident:
		s := c.info.SymbolOf(x)
		if s == nil {
			return false
		}
		return containsAny(c.info.SymType(s), a.structs)
	case *ast.SelectorExpr:
		sel := c.info.SelectionOf(x)
		if sel == nil || sel.Kind != types.SelField {
			return true
		}
		return slices.Contains(a.path, sel.Field.Name) || containsAny(sel.Field.Type, a.structs)
	case *ast.IndexExpr:
		switch t := under(c.info.TypeOf(x.X)).(type) {
		case *types.Slice:
			return containsAny(t.Elem, a.structs)
		case *types.Array:
			return containsAny(t.Elem, a.structs) || c.heapWriteConflicts(unparen(x.X), a)
		}
		return true
	case *ast.UnaryExpr:
		if x.Op == ast.OpDeref {
			if p, ok := under(c.info.TypeOf(x.X)).(*types.Pointer); ok {
				return containsAny(p.Elem, a.structs) || containsAny(p.Elem, a.leafs)
			}
		}
		return true
	}
	return true
}

func (c *checker) callConflicts(call *ast.CallExpr, a atom) bool {
	cl := c.info.CallOf(call)
	if cl == nil {
		return true
	}
	switch cl.Kind {
	case types.CallConversion, types.CallVariant:
		return false
	case types.CallBuiltin:
		switch cl.Builtin {
		case resolve.FCopy, resolve.FFormatUint:
			if len(call.Args) == 0 {
				return true
			}
			dst := c.info.TypeOf(call.Args[len(call.Args)-1])
			if cl.Builtin == resolve.FCopy {
				dst = c.info.TypeOf(call.Args[0])
			}
			if s, ok := under(dst).(*types.Slice); ok {
				return containsAny(s.Elem, a.structs)
			}
			return true
		}
		return false
	}
	return true
}

// containsAny reports whether a value of type t may contain (by value) a
// value of one of the types in set.
func containsAny(t types.Type, set []types.Type) bool {
	for _, s := range set {
		if containsType(t, s, 0) {
			return true
		}
	}
	return false
}

func containsType(t, s types.Type, depth int) bool {
	if t == nil || s == nil || depth > 64 {
		return true
	}
	if types.Identical(t, s) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for _, f := range u.Fields {
			if containsType(f.Type, s, depth+1) {
				return true
			}
		}
	case *types.Array:
		return containsType(u.Elem, s, depth+1)
	case *types.Enum:
		for _, v := range u.Variants {
			for _, p := range v.Payload {
				if containsType(p, s, depth+1) {
					return true
				}
			}
		}
	case *types.TypeParam:
		return true
	}
	return false
}
