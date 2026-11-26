package types

import (
	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
)

// ExprKind classifies what an expression (or type) node denotes.
type ExprKind uint8

const (
	KindNone        ExprKind = iota // not an expression / not checked / erroneous
	KindValue                       // a non-addressable value
	KindVar                         // an addressable variable (local, global, field/index/deref of one)
	KindConst                       // a constant; Info.Values holds its value
	KindType                        // a type (type names, instantiated generic types, TypeExpr, type nodes)
	KindNoValue                     // a call without results
	KindBuiltin                     // a builtin function name (len, CodeArray, ...)
	KindFunc                        // a declared function (or generic function instantiation)
	KindPackage                     // an imported package name
	KindVariantCtor                 // an enum variant with payload, not yet applied (Shape.Circle)
)

// Info is the typed model produced by Check. Dense side tables are indexed
// by ast.NodeID (len = Prog.IDs.Count()+1) or by resolve.SymID
// (len = len(Prog.Syms)); prefer the accessor methods.
type Info struct {
	Prog *resolve.Program

	// Types records, by NodeID:
	//   - for expressions: the final type (untyped constants are recorded
	//     with the type they were converted to; constants that stayed
	//     untyped get their default type), a *Tuple for multi-result calls,
	//     nil for calls without results;
	//   - for type nodes (ast.Type) and type-denoting expressions: the
	//     resolved type. El(...) nodes and aliases of them hold the folded
	//     type, e.g. [1024][4096]u8.
	Types []Type
	// Kinds classifies each expression node (see ExprKind).
	Kinds []ExprKind
	// Values holds the constant value of constant expressions (and of
	// comptime calls folded at compile time), by NodeID.
	Values []Value

	// Calls describes every CallExpr, by NodeID.
	Calls []*Call
	// Selections describes every SelectorExpr except package-qualified
	// identifiers (pkg.Name; see Prog.Uses), by NodeID.
	Selections []*Selection
	// Instances describes every IndexExpr / NamedType that instantiates a
	// generic function or type, by NodeID.
	Instances []*Instance
	// Variants maps match-arm patterns (*ast.CtorPattern) to the enum
	// variant they match, by NodeID.
	Variants []*Variant

	// SymTypes is the type of each symbol by SymID: variables and constants
	// (their declared/inferred type), functions (*Signature), types (the
	// declared type; *Named for type declarations), type parameters
	// (*TypeParam).
	SymTypes []Type
	// SymValues is the value of each constant symbol, by SymID.
	SymValues []Value

	// Funcs lists every function declaration, method and function literal
	// of all packages, in dependency/source order (literals follow their
	// enclosing function).
	Funcs []*FuncInfo
	// Named lists all declared named types in source order.
	Named []*Named
	// InstanceList lists the distinct instantiations of generic functions
	// and types (deduplicated by Key) in first-use order. Instantiations
	// whose arguments mention type parameters (inside generic code) are
	// included with Generic set.
	InstanceList []*Instance
	// Main is the entry point of the root package (nil if absent).
	Main *FuncInfo

	funcOf   map[ast.Node]*FuncInfo // keyed by *ast.FuncDecl / *ast.FuncLit (lookups only)
	methodOf map[*resolve.Symbol]*Method
}

// TypeOf returns the recorded type of an expression or type node (nil if
// none).
func (in *Info) TypeOf(n ast.Node) Type {
	if n == nil {
		return nil
	}
	if id := int(n.ID()); id < len(in.Types) {
		return in.Types[id]
	}
	return nil
}

// KindOf returns the ExprKind of a node.
func (in *Info) KindOf(n ast.Node) ExprKind {
	if id := int(n.ID()); id < len(in.Kinds) {
		return in.Kinds[id]
	}
	return KindNone
}

// ValueOf returns the constant value of an expression, or nil.
func (in *Info) ValueOf(n ast.Node) Value {
	if id := int(n.ID()); id < len(in.Values) {
		return in.Values[id]
	}
	return nil
}

// SymbolOf returns the symbol an identifier defines or refers to (nil for
// field/variant/selector names; see Selections and Variants).
func (in *Info) SymbolOf(id *ast.Ident) *resolve.Symbol { return in.Prog.SymbolOf(id) }

// SymType returns the type of a symbol.
func (in *Info) SymType(s *resolve.Symbol) Type {
	if s == nil || int(s.ID) >= len(in.SymTypes) {
		return nil
	}
	return in.SymTypes[s.ID]
}

// SymValue returns the value of a constant symbol (nil if unknown).
func (in *Info) SymValue(s *resolve.Symbol) Value {
	if s == nil || int(s.ID) >= len(in.SymValues) {
		return nil
	}
	return in.SymValues[s.ID]
}

// CallOf returns the call description of a CallExpr.
func (in *Info) CallOf(c *ast.CallExpr) *Call {
	if id := int(c.ID()); id < len(in.Calls) {
		return in.Calls[id]
	}
	return nil
}

// SelectionOf returns the selection of a SelectorExpr (nil for pkg.Name).
func (in *Info) SelectionOf(s *ast.SelectorExpr) *Selection {
	if id := int(s.ID()); id < len(in.Selections) {
		return in.Selections[id]
	}
	return nil
}

// InstanceOf returns the instantiation recorded for an IndexExpr or
// NamedType node (nil if it is not a generic instantiation).
func (in *Info) InstanceOf(n ast.Node) *Instance {
	if id := int(n.ID()); id < len(in.Instances) {
		return in.Instances[id]
	}
	return nil
}

// VariantOf returns the enum variant matched by a constructor pattern.
func (in *Info) VariantOf(p *ast.CtorPattern) *Variant {
	if id := int(p.ID()); id < len(in.Variants) {
		return in.Variants[id]
	}
	return nil
}

// FuncOf returns the FuncInfo of a *ast.FuncDecl or *ast.FuncLit.
func (in *Info) FuncOf(n ast.Node) *FuncInfo { return in.funcOf[n] }

// FuncOfSym returns the FuncInfo of a function or method symbol.
func (in *Info) FuncOfSym(s *resolve.Symbol) *FuncInfo {
	if s == nil || s.Kind != resolve.SymFunc {
		return nil
	}
	if d, ok := s.Decl.(*ast.FuncDecl); ok {
		return in.funcOf[d]
	}
	return nil
}

// MethodOf returns the Method of a method symbol.
func (in *Info) MethodOf(s *resolve.Symbol) *Method { return in.methodOf[s] }

// ---------------------------------------------------------------------------

// FuncInfo describes a function declaration, method, or function literal.
type FuncInfo struct {
	Sym  *resolve.Symbol // nil for literals
	Decl *ast.FuncDecl   // nil for literals
	Lit  *ast.FuncLit    // nil for declarations
	// Outer is the enclosing function of a literal (nil for declarations).
	Outer *FuncInfo
	// Effect: Comptime (comptime func), Total (func, func literal), or
	// Driver (driver func, driver func literal).
	Effect Effect
	// Sig is the signature (generic: with TypeParams set).
	Sig *Signature
	// Method is set for methods.
	Method *Method
	// RecvSym is the receiver variable (methods only).
	RecvSym *resolve.Symbol
	// Params are the parameter symbols in order (blank `_` params included).
	Params []*resolve.Symbol
	// TypeParams of a generic function, or the receiver type parameters of a
	// method on a generic type.
	TypeParams []*TypeParam
	// Decreases is the measure of a `decreases(M)` clause (nil if absent).
	Decreases ast.Expr
	// Body is the function body.
	Body *ast.BlockStmt
	// Calls lists the call expressions in the body in source order,
	// excluding those inside nested function literals (see Lits).
	Calls []*ast.CallExpr
	// Lits are the function literals directly nested in this function.
	Lits []*FuncInfo
	// Captures (literals only) are the outer locals referenced by the
	// literal, in order of first reference.
	Captures []*resolve.Symbol
}

// Name returns a printable name ("f", "T.m", or "func literal").
func (f *FuncInfo) Name() string {
	switch {
	case f.Sym == nil:
		return "func literal"
	case f.Sym.RecvType != "":
		return f.Sym.RecvType + "." + f.Sym.Name
	}
	return f.Sym.Name
}

// Node returns the declaring node (*ast.FuncDecl or *ast.FuncLit).
func (f *FuncInfo) Node() ast.Node {
	if f.Decl != nil {
		return f.Decl
	}
	return f.Lit
}

// Method is a method of a named type: a user method (Sym/Decl set) or a
// predeclared capability/resource method (Builtin set).
type Method struct {
	Name string
	// Recv is the (origin) receiver type.
	Recv *Named
	// PtrRecv: declared with a pointer receiver `(p *T)`. Such methods
	// can only be called on values of type *T (no automatic &, decision 10).
	PtrRecv bool
	// RecvTypeParams are the method's own names for the receiver type's
	// parameters (`func (v *Vec[N, T]) ...`), aligned with Recv.TypeParams.
	RecvTypeParams []*TypeParam
	// Sig is the signature without the receiver; it may mention
	// RecvTypeParams. Builtin generic methods (Scope.AcquireSlot/
	// ReleaseSlot/Go) have Sig == nil; their instantiated signature is in
	// Call.Sig.
	Sig  *Signature
	Sym  *resolve.Symbol // user methods
	Decl *ast.FuncDecl   // user methods
	// Builtin identifies predeclared methods (Recv is a predeclared type).
	Builtin BuiltinMethod
}

// Effect returns the effect mode of the method.
func (m *Method) Effect() Effect {
	if m.Sig != nil {
		return m.Sig.Effect
	}
	return Total
}

// CallKind classifies call expressions.
type CallKind uint8

const (
	CallInvalid     CallKind = iota // erroneous call
	CallFunc                        // static call of a declared function (Callee set)
	CallMethod                      // call of a user method (Method, Recv set)
	CallBuiltinMeth                 // call of a capability/resource method (Method.Builtin set)
	CallBuiltin                     // call of a builtin function (Builtin set)
	CallConversion                  // type conversion T(x) (Conv set)
	CallVariant                     // enum variant construction Shape.Circle(2) (Variant set)
	CallValue                       // call through a function value (local func var, CancelFunc, ...)
)

func (k CallKind) String() string {
	return [...]string{"invalid", "func", "method", "builtin-method", "builtin", "conversion", "variant", "value"}[k]
}

// Call describes a CallExpr.
type Call struct {
	Kind CallKind
	// Callee is the called function or method symbol (CallFunc,
	// CallMethod) or the builtin's universe symbol (CallBuiltin).
	Callee *resolve.Symbol
	// Builtin identifies the builtin function (CallBuiltin).
	Builtin resolve.Builtin
	// Method is the called method (CallMethod, CallBuiltinMeth).
	Method *Method
	// Recv is the receiver expression and RecvType its type (method calls).
	// For pointer-receiver methods RecvType is *T.
	Recv     ast.Expr
	RecvType Type
	// TypeArgs are the type arguments: explicit ones of a generic function
	// instantiation, the receiver's type arguments for methods of generic
	// types, or the inferred [N, T] / [T] of Scope.AcquireSlot / ReleaseSlot
	// / Go and builtin copy.
	TypeArgs []Type
	// Sig is the instantiated signature (nil for conversions and builtins
	// whose signature is not expressible, e.g. len).
	Sig *Signature
	// Effect is the effect mode of the callee: user functions their
	// modifier; func values their type's effect; builtins Total except
	// panic (Driver) and Code constructors (Comptime); capability methods
	// Total except Exec.Run (Driver).
	Effect Effect
	// Folded: a comptime call evaluated at compile time; its value is in
	// Info.Values[call.ID()].
	Folded bool
	// Conv is the target type of a conversion.
	Conv Type
	// Variant is the constructed enum variant (CallVariant), of enum type
	// VariantType.
	Variant     *Variant
	VariantType Type
	// ResourceCtor: the callee is a builtin resource constructor whose
	// Scoped/Linear results are fresh (S3 exemption): NewContext, NewScope,
	// Net.ListenTCP, Listener.Accept, Scope.AcquireSlot, Context.WithTimeout,
	// Signals.NotifyContext.
	ResourceCtor bool
	// ConsumesRecv: the call finalizes its Linear receiver
	// (Conn.Close, Listener.Close) or, for CallValue of a CancelFunc, the
	// called value itself.
	ConsumesRecv bool
	// ConsumesArgs lists argument indices whose Linear values the call
	// consumes (Scope.ReleaseSlot: 0; Scope.Go: 0, the slot).
	ConsumesArgs []int
}

// SelKind classifies selector expressions.
type SelKind uint8

const (
	SelField   SelKind = iota // x.f (struct field; through one pointer if Indirect)
	SelMethod                 // x.m (method; only valid as the Fun of a call)
	SelVariant                // T.V (enum variant; T a type)
)

// Selection describes a SelectorExpr x.f.
type Selection struct {
	Kind SelKind
	// Recv is the type of x (for SelVariant: the enum type).
	Recv Type
	// Indirect: x is a pointer that is automatically dereferenced (field
	// access through *T, or a value-receiver method called on *T).
	Indirect bool
	Field    *Field   // SelField
	Method   *Method  // SelMethod
	Variant  *Variant // SelVariant
}

// Instance is an instantiation of a generic function or type.
type Instance struct {
	// Sym is the generic function/type symbol (universe symbol for Slot).
	Sym *resolve.Symbol
	// Named is the instance type for type instantiations (nil for funcs).
	Named *Named
	// TypeArgs are the arguments (types, *ConstArg for const params, or
	// type parameters inside generic code).
	TypeArgs []Type
	// Sig is the instantiated signature for function instantiations.
	Sig *Signature
	// Key identifies the instantiation ("id[u64]", "Vec[4, u32]").
	Key string
	// Generic: some argument mentions a type parameter.
	Generic bool
}
