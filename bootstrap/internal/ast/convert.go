package ast

// ExprAsType interprets a TypeOrExpr expression as a syntactic type, as the
// resolver must do for generic arguments, NamedType.Args and the operand of
// casts. It handles:
//
//	*TypeExpr                 -> its Type
//	Ident                     -> NamedType{Name}
//	Ident.Ident               -> NamedType{Pkg, Name}
//	X[A, ...] (X as above)    -> NamedType with Args
//	*X (unary deref)          -> PointerType
//	(X)                       -> ExprAsType(X)
//
// The returned nodes for converted expressions are fresh nodes that reuse
// the original NodeID and span of the expression they were built from (so
// side tables keyed by the expression's ID remain meaningful; for a bare
// Ident the NamedType wrapper and its Name share that ID). ok is false
// if e cannot denote a type.
func ExprAsType(e Expr) (t Type, ok bool) {
	switch e := e.(type) {
	case *TypeExpr:
		return e.Type, true
	case *ParenExpr:
		return ExprAsType(e.X)
	case *UnaryExpr:
		if e.Op != OpDeref {
			return nil, false
		}
		elem, ok := ExprAsType(e.X)
		if !ok {
			return nil, false
		}
		return &PointerType{Base: e.Base, Elem: elem}, true
	}
	if nt, ok := ExprAsNamedType(e); ok {
		return nt, true
	}
	return nil, false
}

// ExprAsNamedType converts Ident, Ident.Ident and their instantiations
// X[...] to a NamedType (see ExprAsType).
func ExprAsNamedType(e Expr) (*NamedType, bool) {
	switch e := e.(type) {
	case *Ident:
		return &NamedType{Base: e.Base, Name: e}, true
	case *SelectorExpr:
		if pkg, ok := e.X.(*Ident); ok {
			return &NamedType{Base: e.Base, Pkg: pkg, Name: e.Sel}, true
		}
	case *IndexExpr:
		switch x := e.X.(type) {
		case *Ident, *SelectorExpr:
			nt, ok := ExprAsNamedType(x)
			if !ok {
				return nil, false
			}
			return &NamedType{Base: e.Base, Pkg: nt.Pkg, Name: nt.Name, Args: e.Indices}, true
		}
	}
	return nil, false
}
