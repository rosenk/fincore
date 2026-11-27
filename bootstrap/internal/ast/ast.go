package ast

import "fincore/bootstrap/internal/diag"

// NodeID identifies a node; see the package documentation.
type NodeID uint32

// IDAlloc hands out NodeIDs. The zero value is ready to use.
type IDAlloc struct{ last NodeID }

// Next returns a fresh ID (the first is 1).
func (a *IDAlloc) Next() NodeID { a.last++; return a.last }

// Count returns the number of IDs allocated so far (= the largest ID).
func (a *IDAlloc) Count() int { return int(a.last) }

// Base is embedded in every node.
type Base struct {
	NodeID NodeID
	Range  diag.SourceSpan
}

// ID returns the node's identity.
func (b *Base) ID() NodeID { return b.NodeID }

// Span returns the node's source range.
func (b *Base) Span() diag.SourceSpan { return b.Range }

func (b *Base) base() *Base { return b }

// Node is implemented by all AST nodes.
type Node interface {
	ID() NodeID
	Span() diag.SourceSpan
	base() *Base
}

// Init sets the ID and span of a node (used by the parser).
func Init(n Node, id NodeID, sp diag.SourceSpan) {
	b := n.base()
	b.NodeID = id
	b.Range = sp
}

// SetSpan updates a node's span.
func SetSpan(n Node, sp diag.SourceSpan) { n.base().Range = sp }

// Expr is an expression node.
type Expr interface {
	Node
	exprNode()
}

// Type is a syntactic type node.
type Type interface {
	Node
	typeNode()
}

// Stmt is a statement node.
type Stmt interface {
	Node
	stmtNode()
}

// Decl is a top-level declaration (ConstDecl, TypeDecl, VarDecl, FuncDecl).
type Decl interface {
	Node
	declNode()
}

// Pattern is a match-arm pattern.
type Pattern interface {
	Node
	patternNode()
}

// ---------------------------------------------------------------------------
// Files and packages

// Package is the set of files of one directory sharing a package clause,
// sorted lexicographically by path.
type Package struct {
	Name  string
	Dir   string // directory, or the file path for single-file packages
	Files []*File
}

// File is a parsed source file.
type File struct {
	Base
	Path    string
	Package *Ident // package clause name
	Imports []*ImportDecl
	Decls   []Decl
}

// ImportDecl is `import spec` or `import ( spec; ... )`.
type ImportDecl struct {
	Base
	Grouped bool
	Specs   []*ImportSpec
}

// ImportSpec is `[name] "path"`.
type ImportSpec struct {
	Base
	Name *Ident    // optional local name; nil if absent
	Path *BasicLit // STRING literal; Path.Value is the decoded path
}

// ---------------------------------------------------------------------------
// Declarations

// ConstDecl is `const spec` or `const ( spec; ... )`. Also used as a statement via DeclStmt.
type ConstDecl struct {
	Base
	Grouped bool
	Specs   []*ConstSpec
}

// ConstSpec is `Name [Type] = Value`.
type ConstSpec struct {
	Base
	Name  *Ident
	Type  Type // nil if absent
	Value Expr
}

// TypeDecl is `type Name [TypeParams] [=] Type`.
type TypeDecl struct {
	Base
	Name       *Ident
	TypeParams *TypeParamList // nil if absent
	Alias      bool           // `type A = T`
	Type       Type
}

// VarDecl is `var Name Type [= Value]` or `var Name = Value`.
type VarDecl struct {
	Base
	Name  *Ident
	Type  Type // nil if absent
	Value Expr // nil if absent
}

// FuncModifier is the optional effect modifier of a function.
type FuncModifier uint8

const (
	ModNone     FuncModifier = iota // total `func`
	ModDriver                       // `driver func`
	ModComptime                     // `comptime func`
)

func (m FuncModifier) String() string {
	switch m {
	case ModDriver:
		return "driver"
	case ModComptime:
		return "comptime"
	}
	return "total"
}

// FuncDecl is a function or method declaration.
type FuncDecl struct {
	Base
	Modifier   FuncModifier
	Recv       *Receiver // nil for plain functions
	Name       *Ident
	TypeParams *TypeParamList // nil if absent
	Sig        *Signature
	Decreases  *DecreasesClause // nil if absent
	Body       *BlockStmt
}

// Receiver is `(name [*]TypeName[ [P, Q] ])`.
type Receiver struct {
	Base
	Name       *Ident
	Pointer    bool
	TypeName   *Ident
	TypeParams []*Ident // receiver type parameter names, e.g. (v *Vec[N, T])
}

// TypeParamList is `[N usize, T any]`.
type TypeParamList struct {
	Base
	Params []*TypeParam
}

// TypeParam is `A, B constraint`.
type TypeParam struct {
	Base
	Names      []*Ident
	Constraint Type // *AnyType for `any`; otherwise a Type (e.g. NamedType usize)
}

// Signature is `(params) [results]`.
type Signature struct {
	Base
	Params      []*Param
	Results     []Type
	ResultParen bool // results written as `( T, ... )`
}

// Param is `a, b T` or `a ...T` (variadic, last parameter only).
type Param struct {
	Base
	Names    []*Ident
	Variadic bool
	Type     Type
}

// DecreasesClause is `decreases(Measure)`.
type DecreasesClause struct {
	Base
	Measure Expr
}

// ---------------------------------------------------------------------------
// Types

// NamedType is `[Pkg.]Name[ [Args] ]`.
type NamedType struct {
	Base
	Pkg  *Ident // nil if unqualified
	Name *Ident
	Args []Expr // TypeOrExpr arguments; see package doc
}

// ArrayType is `[Len]Elem`.
type ArrayType struct {
	Base
	Len  Expr
	Elem Type
}

// SliceType is `[]Elem`.
type SliceType struct {
	Base
	Elem Type
}

// PointerType is `*Elem`.
type PointerType struct {
	Base
	Elem Type
}

// StructType is `struct { fields }`.
type StructType struct {
	Base
	Fields []*FieldDecl
}

// FieldDecl is `a, b T`.
type FieldDecl struct {
	Base
	Names []*Ident
	Type  Type
}

// EnumType is `enum { variants }`.
type EnumType struct {
	Base
	Variants []*Variant
}

// Variant is `Name` or `Name(T1, T2)`.
type Variant struct {
	Base
	Name      *Ident
	HasParens bool
	Payload   []Type
}

// FuncType is `[driver] func Signature`.
type FuncType struct {
	Base
	Driver bool
	Sig    *Signature
}

// CodeType is `Code Level`.
type CodeType struct {
	Base
	Level Expr
}

// ElType is `El(Code)`.
type ElType struct {
	Base
	Code Expr
}

// AnyType is the `any` type-parameter constraint.
type AnyType struct {
	Base
}

// ---------------------------------------------------------------------------
// Statements

// BlockStmt is `{ stmts }`.
type BlockStmt struct {
	Base
	Stmts []Stmt
}

// DeclStmt is a ConstDecl or VarDecl in statement position.
type DeclStmt struct {
	Base
	Decl Decl
}

// ShortVarDecl is `a, b := x, y`.
type ShortVarDecl struct {
	Base
	Names  []*Ident
	Values []Expr
}

// AssignOp is an assignment operator.
type AssignOp uint8

const (
	Assign    AssignOp = iota // =
	AddAssign                 // +=
	SubAssign                 // -=
	MulAssign                 // *=
	QuoAssign                 // /=
)

func (o AssignOp) String() string {
	return [...]string{"=", "+=", "-=", "*=", "/="}[o]
}

// AssignStmt is `lhs op rhs`.
type AssignStmt struct {
	Base
	LHS []Expr
	Op  AssignOp
	RHS []Expr
}

// IncDecStmt is `x++` or `x--`.
type IncDecStmt struct {
	Base
	X   Expr
	Inc bool
}

// ExprStmt is an expression used as a statement.
type ExprStmt struct {
	Base
	X Expr
}

// IfStmt is `if [Init;] Cond Then [else Else]`.
type IfStmt struct {
	Base
	Init Stmt // nil if absent (SimpleStmt)
	Cond Expr
	Then *BlockStmt
	Else Stmt // nil, *IfStmt or *BlockStmt
}

// MatchStmt is `match X { arms }`.
type MatchStmt struct {
	Base
	X    Expr
	Arms []*MatchArm
}

// MatchArm is `Pattern -> Expr` or `Pattern -> Block`. Exactly one of Expr,
// Block is non-nil.
type MatchArm struct {
	Base
	Pattern Pattern
	Expr    Expr
	Block   *BlockStmt
}

// WildcardPattern is `_`.
type WildcardPattern struct {
	Base
}

// CtorPattern is `[Qual.]Name[(a, b)]`.
type CtorPattern struct {
	Base
	Qual      *Ident // nil if unqualified
	Name      *Ident
	HasParens bool
	Bindings  []*Ident
}

// LiteralPattern is a literal pattern (INT, STRING, CHAR, true, false, nil).
type LiteralPattern struct {
	Base
	Lit Expr // *BasicLit, *BoolLit or *NilLit
}

// OrPattern is `P1 | P2 | ...` (v0.2): two or more alternatives, none of
// which is itself an OrPattern. A single pattern is never wrapped.
type OrPattern struct {
	Base
	Alts []Pattern
}

// ForStmt is `[driver] for [header] [decreases(M)] Body`.
//
// Forms: `for {}` (all nil), `for Cond {}` (Cond only), and the three-clause
// form `for [Init]; [Cond]; [Post] {}` (ThreeClause = true).
type ForStmt struct {
	Base
	Driver      bool
	ThreeClause bool
	Init        Stmt // SimpleStmt or nil
	Cond        Expr // nil if absent
	Post        Stmt // SimpleStmt or nil
	Decreases   *DecreasesClause
	Body        *BlockStmt
}

// ScopeStmt is `scope Name := Value Body`.
type ScopeStmt struct {
	Base
	Name  *Ident
	Value Expr
	Body  *BlockStmt
}

// DeferStmt is `defer X`.
type DeferStmt struct {
	Base
	X Expr
}

// ReturnStmt is `return [results]`.
type ReturnStmt struct {
	Base
	Results []Expr
}

// BreakStmt is `break`.
type BreakStmt struct {
	Base
}

// ContinueStmt is `continue`.
type ContinueStmt struct {
	Base
}

// ---------------------------------------------------------------------------
// Expressions

// Ident is an identifier (including `_` and predeclared names).
type Ident struct {
	Base
	Name string
}

// LitKind classifies BasicLit.
type LitKind uint8

const (
	IntLit LitKind = iota
	StringLit
	CharLit
)

func (k LitKind) String() string { return [...]string{"int", "string", "char"}[k] }

// BasicLit is an INT, STRING or CHAR literal.
type BasicLit struct {
	Base
	Kind LitKind
	Raw  string // source spelling
	// Int is the value for IntLit and CharLit (the byte value).
	Int uint64
	// Str is the decoded value for StringLit (escapes resolved).
	Str string
}

// BoolLit is `true` or `false`.
type BoolLit struct {
	Base
	Value bool
}

// NilLit is `nil`.
type NilLit struct {
	Base
}

// BinaryOp is a binary operator.
type BinaryOp uint8

const (
	OpLOr  BinaryOp = iota // ||
	OpLAnd                 // &&
	OpEq                   // ==
	OpNe                   // !=
	OpLt                   // <
	OpLe                   // <=
	OpGt                   // >
	OpGe                   // >=
	OpAdd                  // +
	OpSub                  // -
	OpOr                   // |
	OpXor                  // ^
	OpMul                  // *
	OpDiv                  // /
	OpRem                  // %
	OpAnd                  // &
	OpShl                  // <<
	OpShr                  // >>
)

var binOpStrings = [...]string{"||", "&&", "==", "!=", "<", "<=", ">", ">=", "+", "-", "|", "^", "*", "/", "%", "&", "<<", ">>"}

func (o BinaryOp) String() string { return binOpStrings[o] }

// Precedence returns the binding strength (1 = loosest) per spec section 3:
// || < && < ==,!= < relational < additive < multiplicative.
func (o BinaryOp) Precedence() int {
	switch o {
	case OpLOr:
		return 1
	case OpLAnd:
		return 2
	case OpEq, OpNe:
		return 3
	case OpLt, OpLe, OpGt, OpGe:
		return 4
	case OpAdd, OpSub, OpOr, OpXor:
		return 5
	}
	return 6
}

// BinaryExpr is `X Op Y`.
type BinaryExpr struct {
	Base
	Op    BinaryOp
	OpPos diag.SourceSpan
	X, Y  Expr
}

// UnaryOp is a prefix operator.
type UnaryOp uint8

const (
	OpNot   UnaryOp = iota // !
	OpNeg                  // -
	OpAddr                 // &
	OpDeref                // *
)

func (o UnaryOp) String() string { return [...]string{"!", "-", "&", "*"}[o] }

// UnaryExpr is `Op X`.
type UnaryExpr struct {
	Base
	Op UnaryOp
	X  Expr
}

// ParenExpr is `(X)`.
type ParenExpr struct {
	Base
	X Expr
}

// CallExpr is `Fun(Args)`; also used for casts like u32(x).
type CallExpr struct {
	Base
	Fun  Expr
	Args []Expr
}

// IndexExpr is `X[i]` (indexing) or `X[A, B]` (generic instantiation).
// See the package doc.
type IndexExpr struct {
	Base
	X       Expr
	Indices []Expr
}

// SliceExpr is `X[Lo:Hi]` (either bound may be nil).
type SliceExpr struct {
	Base
	X      Expr
	Lo, Hi Expr
}

// SelectorExpr is `X.Sel` (field access, method value or qualified name).
type SelectorExpr struct {
	Base
	X   Expr
	Sel *Ident
}

// FuncLit is `[driver] func Signature Body`.
type FuncLit struct {
	Base
	Driver bool
	Sig    *Signature
	Body   *BlockStmt
}

// StructLit is `T{a: x, b: y}`.
type StructLit struct {
	Base
	Type   *NamedType
	Fields []*FieldInit
}

// FieldInit is `Name: Value`.
type FieldInit struct {
	Base
	Name  *Ident
	Value Expr
}

// MatchExpr is `match X { arms }` in expression position (v0.2). Every arm
// has an Expr and no Block.
type MatchExpr struct {
	Base
	X    Expr
	Arms []*MatchArm
}

// TypeExpr is a syntactic type appearing in expression position (generic
// arguments, Code/El in TypeOrExpr contexts).
type TypeExpr struct {
	Base
	Type Type
}

// ---------------------------------------------------------------------------
// marker methods

func (*Ident) exprNode()        {}
func (*BasicLit) exprNode()     {}
func (*BoolLit) exprNode()      {}
func (*NilLit) exprNode()       {}
func (*BinaryExpr) exprNode()   {}
func (*UnaryExpr) exprNode()    {}
func (*ParenExpr) exprNode()    {}
func (*CallExpr) exprNode()     {}
func (*IndexExpr) exprNode()    {}
func (*SliceExpr) exprNode()    {}
func (*SelectorExpr) exprNode() {}
func (*FuncLit) exprNode()      {}
func (*StructLit) exprNode()    {}
func (*TypeExpr) exprNode()     {}
func (*MatchExpr) exprNode()    {}

func (*NamedType) typeNode()   {}
func (*ArrayType) typeNode()   {}
func (*SliceType) typeNode()   {}
func (*PointerType) typeNode() {}
func (*StructType) typeNode()  {}
func (*EnumType) typeNode()    {}
func (*FuncType) typeNode()    {}
func (*CodeType) typeNode()    {}
func (*ElType) typeNode()      {}
func (*AnyType) typeNode()     {}

func (*BlockStmt) stmtNode()    {}
func (*DeclStmt) stmtNode()     {}
func (*ShortVarDecl) stmtNode() {}
func (*AssignStmt) stmtNode()   {}
func (*IncDecStmt) stmtNode()   {}
func (*ExprStmt) stmtNode()     {}
func (*IfStmt) stmtNode()       {}
func (*MatchStmt) stmtNode()    {}
func (*ForStmt) stmtNode()      {}
func (*ScopeStmt) stmtNode()    {}
func (*DeferStmt) stmtNode()    {}
func (*ReturnStmt) stmtNode()   {}
func (*BreakStmt) stmtNode()    {}
func (*ContinueStmt) stmtNode() {}

func (*ConstDecl) declNode() {}
func (*TypeDecl) declNode()  {}
func (*VarDecl) declNode()   {}
func (*FuncDecl) declNode()  {}

func (*WildcardPattern) patternNode() {}
func (*CtorPattern) patternNode()     {}
func (*LiteralPattern) patternNode()  {}
func (*OrPattern) patternNode()       {}
