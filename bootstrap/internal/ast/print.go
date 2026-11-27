package ast

import (
	"strings"
)

// PrintOptions controls Format.
type PrintOptions struct {
	// FullParens wraps every binary and unary expression in parentheses,
	// making precedence explicit (useful in tests and debugging).
	FullParens bool
}

// Format renders n as canonical FinCore source. The output re-parses to an
// equivalent tree (modulo spans and IDs). Blocks are indented with tabs;
// struct and enum types are printed on one line with explicit ';'.
func Format(n Node) string { return FormatWith(n, PrintOptions{}) }

// FormatWith is Format with options.
func FormatWith(n Node, opts PrintOptions) string {
	p := &printer{opts: opts}
	p.node(n)
	return p.b.String()
}

// ExprString renders an expression with full parentheses.
func ExprString(e Expr) string { return FormatWith(e, PrintOptions{FullParens: true}) }

type printer struct {
	b      strings.Builder
	indent int
	opts   PrintOptions
}

func (p *printer) w(ss ...string) {
	for _, s := range ss {
		p.b.WriteString(s)
	}
}

func (p *printer) nl() {
	p.b.WriteByte('\n')
	for range p.indent {
		p.b.WriteByte('\t')
	}
}

func (p *printer) idents(ids []*Ident) {
	for i, id := range ids {
		if i > 0 {
			p.w(", ")
		}
		p.w(id.Name)
	}
}

func (p *printer) exprs(es []Expr) {
	for i, e := range es {
		if i > 0 {
			p.w(", ")
		}
		p.node(e)
	}
}

func (p *printer) node(n Node) {
	if isNil(n) {
		return
	}
	switch n := n.(type) {
	case *File:
		p.w("package ", n.Package.Name, "\n")
		for _, d := range n.Imports {
			p.w("\n")
			p.node(d)
		}
		for _, d := range n.Decls {
			p.w("\n")
			p.node(d)
			p.w("\n")
		}
	case *ImportDecl:
		if n.Grouped {
			p.w("import (")
			p.indent++
			for _, s := range n.Specs {
				p.nl()
				p.node(s)
			}
			p.indent--
			p.nl()
			p.w(")")
		} else {
			p.w("import ")
			p.node(n.Specs[0])
		}
	case *ImportSpec:
		if n.Name != nil {
			p.w(n.Name.Name, " ")
		}
		p.w(n.Path.Raw)
	case *ConstDecl:
		if n.Grouped {
			p.w("const (")
			p.indent++
			for _, s := range n.Specs {
				p.nl()
				p.node(s)
			}
			p.indent--
			p.nl()
			p.w(")")
		} else {
			p.w("const ")
			p.node(n.Specs[0])
		}
	case *ConstSpec:
		p.w(n.Name.Name)
		if n.Type != nil {
			p.w(" ")
			p.node(n.Type)
		}
		p.w(" = ")
		p.node(n.Value)
	case *TypeDecl:
		p.w("type ", n.Name.Name)
		p.node(n.TypeParams)
		if n.Alias {
			p.w(" =")
		}
		p.w(" ")
		p.node(n.Type)
	case *VarDecl:
		p.w("var ", n.Name.Name)
		if n.Type != nil {
			p.w(" ")
			p.node(n.Type)
		}
		if n.Value != nil {
			p.w(" = ")
			p.node(n.Value)
		}
	case *FuncDecl:
		if n.Modifier != ModNone {
			p.w(n.Modifier.String(), " ")
		}
		p.w("func ")
		if n.Recv != nil {
			p.node(n.Recv)
			p.w(" ")
		}
		p.w(n.Name.Name)
		p.node(n.TypeParams)
		p.node(n.Sig)
		if n.Decreases != nil {
			p.w(" ")
			p.node(n.Decreases)
		}
		p.w(" ")
		p.node(n.Body)
	case *Receiver:
		p.w("(", n.Name.Name, " ")
		if n.Pointer {
			p.w("*")
		}
		p.w(n.TypeName.Name)
		if len(n.TypeParams) > 0 {
			p.w("[")
			p.idents(n.TypeParams)
			p.w("]")
		}
		p.w(")")
	case *TypeParamList:
		p.w("[")
		for i, tp := range n.Params {
			if i > 0 {
				p.w(", ")
			}
			p.node(tp)
		}
		p.w("]")
	case *TypeParam:
		p.idents(n.Names)
		p.w(" ")
		p.node(n.Constraint)
	case *Signature:
		p.w("(")
		for i, prm := range n.Params {
			if i > 0 {
				p.w(", ")
			}
			p.node(prm)
		}
		p.w(")")
		if len(n.Results) > 0 {
			p.w(" ")
			if n.ResultParen || len(n.Results) > 1 {
				p.w("(")
			}
			for i, t := range n.Results {
				if i > 0 {
					p.w(", ")
				}
				p.node(t)
			}
			if n.ResultParen || len(n.Results) > 1 {
				p.w(")")
			}
		}
	case *Param:
		p.idents(n.Names)
		p.w(" ")
		if n.Variadic {
			p.w("...")
		}
		p.node(n.Type)
	case *DecreasesClause:
		p.w("decreases(")
		p.node(n.Measure)
		p.w(")")

	case *NamedType:
		if n.Pkg != nil {
			p.w(n.Pkg.Name, ".")
		}
		p.w(n.Name.Name)
		if len(n.Args) > 0 {
			p.w("[")
			p.exprs(n.Args)
			p.w("]")
		}
	case *ArrayType:
		p.w("[")
		p.node(n.Len)
		p.w("]")
		p.node(n.Elem)
	case *SliceType:
		p.w("[]")
		p.node(n.Elem)
	case *PointerType:
		p.w("*")
		p.node(n.Elem)
	case *StructType:
		p.w("struct {")
		for i, f := range n.Fields {
			if i > 0 {
				p.w(";")
			}
			p.w(" ")
			p.node(f)
		}
		if len(n.Fields) > 0 {
			p.w(" ")
		}
		p.w("}")
	case *FieldDecl:
		p.idents(n.Names)
		p.w(" ")
		p.node(n.Type)
	case *EnumType:
		p.w("enum {")
		for i, v := range n.Variants {
			if i > 0 {
				p.w(";")
			}
			p.w(" ")
			p.node(v)
		}
		if len(n.Variants) > 0 {
			p.w(" ")
		}
		p.w("}")
	case *Variant:
		p.w(n.Name.Name)
		if n.HasParens {
			p.w("(")
			for i, t := range n.Payload {
				if i > 0 {
					p.w(", ")
				}
				p.node(t)
			}
			p.w(")")
		}
	case *FuncType:
		if n.Driver {
			p.w("driver ")
		}
		p.w("func")
		p.node(n.Sig)
	case *CodeType:
		p.w("Code ")
		p.node(n.Level)
	case *ElType:
		p.w("El(")
		p.node(n.Code)
		p.w(")")
	case *AnyType:
		p.w("any")

	case *BlockStmt:
		p.w("{")
		p.indent++
		for _, s := range n.Stmts {
			p.nl()
			p.node(s)
		}
		p.indent--
		if len(n.Stmts) > 0 {
			p.nl()
		}
		p.w("}")
	case *DeclStmt:
		p.node(n.Decl)
	case *ShortVarDecl:
		p.idents(n.Names)
		p.w(" := ")
		p.exprs(n.Values)
	case *AssignStmt:
		p.exprs(n.LHS)
		p.w(" ", n.Op.String(), " ")
		p.exprs(n.RHS)
	case *IncDecStmt:
		p.node(n.X)
		if n.Inc {
			p.w("++")
		} else {
			p.w("--")
		}
	case *ExprStmt:
		p.node(n.X)
	case *IfStmt:
		p.w("if ")
		if n.Init != nil {
			p.node(n.Init)
			p.w("; ")
		}
		p.node(n.Cond)
		p.w(" ")
		p.node(n.Then)
		if n.Else != nil {
			p.w(" else ")
			p.node(n.Else)
		}
	case *MatchStmt:
		p.w("match ")
		p.node(n.X)
		p.w(" {")
		p.indent++
		for _, a := range n.Arms {
			p.nl()
			p.node(a)
		}
		p.indent--
		p.nl()
		p.w("}")
	case *MatchArm:
		p.node(n.Pattern)
		if n.Guard != nil {
			p.w(" if ")
			p.node(n.Guard)
		}
		p.w(" -> ")
		if n.Block != nil {
			p.node(n.Block)
		} else {
			p.node(n.Expr)
		}
	case *WildcardPattern:
		p.w("_")
	case *CtorPattern:
		if n.Qual != nil {
			p.w(n.Qual.Name, ".")
		}
		p.w(n.Name.Name)
		if n.HasParens {
			p.w("(")
			for i, a := range n.Args {
				if i > 0 {
					p.w(", ")
				}
				p.node(a)
			}
			p.w(")")
		}
	case *LiteralPattern:
		p.node(n.Lit)
	case *OrPattern:
		for i, a := range n.Alts {
			if i > 0 {
				p.w(" | ")
			}
			if _, ok := a.(*AtPattern); ok {
				p.w("(")
				p.node(a)
				p.w(")")
			} else {
				p.node(a)
			}
		}
	case *TuplePattern:
		p.w("(")
		for i, a := range n.Elems {
			if i > 0 {
				p.w(", ")
			}
			p.node(a)
		}
		p.w(")")
	case *RestPattern:
		p.w("..")
	case *AtPattern:
		p.w(n.Name.Name, " @ ")
		if _, ok := n.Pattern.(*OrPattern); ok {
			p.w("(")
			p.node(n.Pattern)
			p.w(")")
		} else {
			p.node(n.Pattern)
		}
	case *TupleExpr:
		p.w("(")
		for i, a := range n.Elems {
			if i > 0 {
				p.w(", ")
			}
			p.node(a)
		}
		p.w(")")
	case *IsExpr:
		if p.opts.FullParens {
			p.w("(")
		}
		p.node(n.X)
		p.w(" is ")
		p.node(n.Pattern)
		if p.opts.FullParens {
			p.w(")")
		}
	case *MatchExpr:
		// one line, so that diagnostics embedding it stay single-line
		p.w("match ")
		p.node(n.X)
		p.w(" { ")
		for i, a := range n.Arms {
			if i > 0 {
				p.w("; ")
			}
			p.node(a)
		}
		p.w(" }")
	case *ForStmt:
		if n.Driver {
			p.w("driver ")
		}
		p.w("for ")
		if n.ThreeClause {
			p.node(n.Init)
			p.w("; ")
			p.node(n.Cond)
			p.w("; ")
			p.node(n.Post)
			if n.Post != nil {
				p.w(" ")
			}
		} else if n.Cond != nil {
			p.node(n.Cond)
			p.w(" ")
		}
		if n.Decreases != nil {
			p.node(n.Decreases)
			p.w(" ")
		}
		p.node(n.Body)
	case *ScopeStmt:
		p.w("scope ", n.Name.Name, " := ")
		p.node(n.Value)
		p.w(" ")
		p.node(n.Body)
	case *DeferStmt:
		p.w("defer ")
		p.node(n.X)
	case *ReturnStmt:
		p.w("return")
		if len(n.Results) > 0 {
			p.w(" ")
			p.exprs(n.Results)
		}
	case *BreakStmt:
		p.w("break")
	case *ContinueStmt:
		p.w("continue")

	case *Ident:
		p.w(n.Name)
	case *BasicLit:
		p.w(n.Raw)
	case *BoolLit:
		if n.Value {
			p.w("true")
		} else {
			p.w("false")
		}
	case *NilLit:
		p.w("nil")
	case *BinaryExpr:
		if p.opts.FullParens {
			p.w("(")
		}
		p.node(n.X)
		p.w(" ", n.Op.String(), " ")
		p.node(n.Y)
		if p.opts.FullParens {
			p.w(")")
		}
	case *UnaryExpr:
		if p.opts.FullParens {
			p.w("(")
		}
		p.w(n.Op.String())
		p.node(n.X)
		if p.opts.FullParens {
			p.w(")")
		}
	case *ParenExpr:
		p.w("(")
		p.node(n.X)
		p.w(")")
	case *CallExpr:
		p.node(n.Fun)
		p.w("(")
		p.exprs(n.Args)
		p.w(")")
	case *IndexExpr:
		p.node(n.X)
		p.w("[")
		p.exprs(n.Indices)
		p.w("]")
	case *SliceExpr:
		p.node(n.X)
		p.w("[")
		p.node(n.Lo)
		p.w(":")
		p.node(n.Hi)
		p.w("]")
	case *SelectorExpr:
		p.node(n.X)
		p.w(".", n.Sel.Name)
	case *FuncLit:
		if n.Driver {
			p.w("driver ")
		}
		p.w("func")
		p.node(n.Sig)
		p.w(" ")
		p.node(n.Body)
	case *StructLit:
		p.node(n.Type)
		p.w("{")
		for i, f := range n.Fields {
			if i > 0 {
				p.w(", ")
			}
			p.node(f)
		}
		p.w("}")
	case *FieldInit:
		p.w(n.Name.Name, ": ")
		p.node(n.Value)
	case *TypeExpr:
		p.node(n.Type)
	}
}
