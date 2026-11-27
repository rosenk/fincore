package ast

// Inspect traverses the tree rooted at n in depth-first, source order. If f
// returns false for a node, its children are skipped. f is called with
// non-nil nodes only.
func Inspect(n Node, f func(Node) bool) {
	if isNil(n) || !f(n) {
		return
	}
	for _, c := range Children(n) {
		Inspect(c, f)
	}
}

func isNil(n Node) bool {
	if n == nil {
		return true
	}
	switch v := n.(type) {
	case *Ident:
		return v == nil
	case *BasicLit:
		return v == nil
	case *BlockStmt:
		return v == nil
	case *NamedType:
		return v == nil
	case *TypeParamList:
		return v == nil
	case *DecreasesClause:
		return v == nil
	case *Receiver:
		return v == nil
	case *Signature:
		return v == nil
	}
	return false
}

// Children returns the direct, non-nil children of n in source order.
func Children(n Node) []Node {
	var out []Node
	add := func(cs ...Node) {
		for _, c := range cs {
			if !isNil(c) {
				out = append(out, c)
			}
		}
	}
	// typed-nil-safe adders for optional interface fields
	addE := func(e Expr) {
		if e != nil {
			add(e)
		}
	}
	addT := func(t Type) {
		if t != nil {
			add(t)
		}
	}
	addS := func(s Stmt) {
		if s != nil {
			add(s)
		}
	}
	idents := func(ids []*Ident) {
		for _, id := range ids {
			add(id)
		}
	}
	exprs := func(es []Expr) {
		for _, e := range es {
			addE(e)
		}
	}
	switch n := n.(type) {
	case *File:
		add(n.Package)
		for _, d := range n.Imports {
			add(d)
		}
		for _, d := range n.Decls {
			add(d)
		}
	case *ImportDecl:
		for _, s := range n.Specs {
			add(s)
		}
	case *ImportSpec:
		add(n.Name, n.Path)
	case *ConstDecl:
		for _, s := range n.Specs {
			add(s)
		}
	case *ConstSpec:
		add(n.Name)
		addT(n.Type)
		addE(n.Value)
	case *TypeDecl:
		add(n.Name, n.TypeParams)
		addT(n.Type)
	case *VarDecl:
		add(n.Name)
		addT(n.Type)
		addE(n.Value)
	case *FuncDecl:
		add(n.Recv, n.Name, n.TypeParams, n.Sig, n.Decreases, n.Body)
	case *Receiver:
		add(n.Name, n.TypeName)
		idents(n.TypeParams)
	case *TypeParamList:
		for _, p := range n.Params {
			add(p)
		}
	case *TypeParam:
		idents(n.Names)
		addT(n.Constraint)
	case *Signature:
		for _, p := range n.Params {
			add(p)
		}
		for _, t := range n.Results {
			addT(t)
		}
	case *Param:
		idents(n.Names)
		addT(n.Type)
	case *DecreasesClause:
		addE(n.Measure)

	case *NamedType:
		add(n.Pkg, n.Name)
		exprs(n.Args)
	case *ArrayType:
		addE(n.Len)
		addT(n.Elem)
	case *SliceType:
		addT(n.Elem)
	case *PointerType:
		addT(n.Elem)
	case *StructType:
		for _, f := range n.Fields {
			add(f)
		}
	case *FieldDecl:
		idents(n.Names)
		addT(n.Type)
	case *EnumType:
		for _, v := range n.Variants {
			add(v)
		}
	case *Variant:
		add(n.Name)
		for _, t := range n.Payload {
			addT(t)
		}
	case *FuncType:
		add(n.Sig)
	case *CodeType:
		addE(n.Level)
	case *ElType:
		addE(n.Code)
	case *AnyType:

	case *BlockStmt:
		for _, s := range n.Stmts {
			addS(s)
		}
	case *DeclStmt:
		if n.Decl != nil {
			add(n.Decl)
		}
	case *ShortVarDecl:
		idents(n.Names)
		exprs(n.Values)
	case *AssignStmt:
		exprs(n.LHS)
		exprs(n.RHS)
	case *IncDecStmt:
		addE(n.X)
	case *ExprStmt:
		addE(n.X)
	case *IfStmt:
		addS(n.Init)
		addE(n.Cond)
		add(n.Then)
		addS(n.Else)
	case *MatchStmt:
		addE(n.X)
		for _, a := range n.Arms {
			add(a)
		}
	case *MatchArm:
		if n.Pattern != nil {
			add(n.Pattern)
		}
		addE(n.Expr)
		add(n.Block)
	case *WildcardPattern:
	case *CtorPattern:
		add(n.Qual, n.Name)
		idents(n.Bindings)
	case *LiteralPattern:
		addE(n.Lit)
	case *OrPattern:
		for _, a := range n.Alts {
			add(a)
		}
	case *ForStmt:
		addS(n.Init)
		addE(n.Cond)
		addS(n.Post)
		add(n.Decreases, n.Body)
	case *ScopeStmt:
		add(n.Name)
		addE(n.Value)
		add(n.Body)
	case *DeferStmt:
		addE(n.X)
	case *ReturnStmt:
		exprs(n.Results)
	case *BreakStmt, *ContinueStmt:

	case *Ident, *BasicLit, *BoolLit, *NilLit:
	case *BinaryExpr:
		addE(n.X)
		addE(n.Y)
	case *UnaryExpr:
		addE(n.X)
	case *ParenExpr:
		addE(n.X)
	case *CallExpr:
		addE(n.Fun)
		exprs(n.Args)
	case *IndexExpr:
		addE(n.X)
		exprs(n.Indices)
	case *SliceExpr:
		addE(n.X)
		addE(n.Lo)
		addE(n.Hi)
	case *SelectorExpr:
		addE(n.X)
		add(n.Sel)
	case *FuncLit:
		add(n.Sig, n.Body)
	case *StructLit:
		add(n.Type)
		for _, f := range n.Fields {
			add(f)
		}
	case *FieldInit:
		add(n.Name)
		addE(n.Value)
	case *TypeExpr:
		addT(n.Type)
	case *MatchExpr:
		addE(n.X)
		for _, a := range n.Arms {
			add(a)
		}
	}
	return out
}
