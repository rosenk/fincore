// Package ir lowers a checked FinCore program (resolve.Program + types.Info)
// into the program plan consumed by the Go code generator (package
// codegen/golang), milestone M4.
//
// The typed AST plus the types.Info side tables already are a typed IR:
// El(...) types are folded, constants are evaluated, every call is
// classified. Lowering adds what the backend needs on top of that:
//
//   - a deterministic declaration plan: packages in dependency order (root
//     last), files sorted, declarations in source order, split into types,
//     globals and functions; comptime functions and type aliases are erased;
//   - Go names for every symbol (name mangling, spec 9.5.6 package
//     prefixes `_pkg_<name>_<ident>`, Go keyword/predeclared collisions);
//   - generic classification: const parameters ([N usize]) are
//     monomorphized by the backend, ordinary type parameters become Go
//     type parameters;
//   - per-function analyses: the `scope` statements of each function (for
//     the scope-barrier safety net), local variables Go would reject as
//     "declared and not used", and the terminating-statement / dead-code
//     analysis shared with the backend.
//
// Nothing here iterates a Go map to produce output (spec 9.2).
package ir

import (
	"fmt"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// Program is the lowered program.
type Program struct {
	Prog *resolve.Program
	Info *types.Info
	// Packages in dependency order; the root package is last.
	Packages []*Package
	// Main is the entry point (nil if the root package has no main).
	Main *types.FuncInfo
	// Caps are the capability types of main's parameters, in order.
	Caps []resolve.Builtin

	names  []string // Go name by SymID ("" = not assigned yet)
	used   []bool   // by SymID: local variable is used (read) somewhere
	funcs  map[*types.FuncInfo]*Func
	prefix map[*resolve.Package]string
}

// Package is one source package.
type Package struct {
	Src *resolve.Package
	// Prefix for package-level Go names ("" for the root package,
	// "_pkg_<name>_" for imported ones).
	Prefix string
	// Types are the named type declarations (aliases erased), in source order.
	Types []*TypeDecl
	// Globals are the package-level variables in source order.
	Globals []*Global
	// Funcs are the functions and methods in source order (comptime
	// functions erased).
	Funcs []*Func
}

// TypeDecl is a declared named type.
type TypeDecl struct {
	Sym   *resolve.Symbol
	Named *types.Named
	// ConstParams / TypeParams partition Named.TypeParams.
	ConstParams []*types.TypeParam
	TypeParams  []*types.TypeParam
	// Methods declared on the type, in source order.
	Methods []*Func
}

// Mono reports whether the type has const parameters and must be
// monomorphized.
func (t *TypeDecl) Mono() bool { return len(t.ConstParams) > 0 }

// Global is a package-level variable.
type Global struct {
	Sym  *resolve.Symbol
	Decl *ast.VarDecl
	Type types.Type
}

// Func is a function, method or function literal.
type Func struct {
	Info *types.FuncInfo
	// Recv is the receiver's type declaration (methods only).
	Recv *TypeDecl
	// ConstParams / TypeParams partition Info.TypeParams.
	ConstParams []*types.TypeParam
	TypeParams  []*types.TypeParam
	// Scopes lists the `scope` statements directly in this function body
	// (not in nested literals), in source order.
	Scopes []*ast.ScopeStmt
}

// Mono reports whether the function must be monomorphized (it has const
// parameters, its own or its receiver type's).
func (f *Func) Mono() bool { return len(f.ConstParams) > 0 }

// ScopeIndex returns the index of s in f.Scopes (-1 if absent).
func (f *Func) ScopeIndex(s *ast.ScopeStmt) int {
	for i, x := range f.Scopes {
		if x == s {
			return i
		}
	}
	return -1
}

// Lower builds the program plan. info must come from a successful check.
func Lower(prog *resolve.Program, info *types.Info) (*Program, error) {
	p := &Program{
		Prog:   prog,
		Info:   info,
		names:  make([]string, len(prog.Syms)),
		used:   make([]bool, len(prog.Syms)),
		funcs:  map[*types.FuncInfo]*Func{},
		prefix: map[*resolve.Package]string{},
	}
	p.assignPrefixes()
	typeDecls := map[*types.Named]*TypeDecl{}
	for _, rp := range prog.Packages {
		pk := &Package{Src: rp, Prefix: p.prefix[rp]}
		p.Packages = append(p.Packages, pk)
		for _, sym := range rp.Decls {
			switch sym.Kind {
			case resolve.SymType:
				d, ok := sym.Decl.(*ast.TypeDecl)
				if !ok || d.Alias {
					continue
				}
				n, ok := info.SymType(sym).(*types.Named)
				if !ok {
					return nil, fmt.Errorf("ir: type %s has no named type", sym.Name)
				}
				if MentionsCode(n.Underlying()) {
					continue // only usable at compile time
				}
				td := &TypeDecl{Sym: sym, Named: n}
				td.ConstParams, td.TypeParams = splitParams(n.TypeParams)
				typeDecls[n] = td
				pk.Types = append(pk.Types, td)
			case resolve.SymVar:
				d, _ := sym.Decl.(*ast.VarDecl)
				if MentionsCode(info.SymType(sym)) {
					continue
				}
				pk.Globals = append(pk.Globals, &Global{Sym: sym, Decl: d, Type: info.SymType(sym)})
			}
		}
		for _, sym := range rp.Decls {
			if sym.Kind != resolve.SymFunc {
				continue
			}
			fi := info.FuncOfSym(sym)
			if fi == nil {
				return nil, fmt.Errorf("ir: function %s has no FuncInfo", sym.Name)
			}
			if fi.Effect == types.Comptime || MentionsCode(fi.Sig) {
				continue // evaluated at compile time; erased (spec 5.3)
			}
			f := p.newFunc(fi)
			if fi.Method != nil {
				td := typeDecls[fi.Method.Recv]
				if td == nil {
					return nil, fmt.Errorf("ir: method %s on unknown type", fi.Name())
				}
				f.Recv = td
				td.Methods = append(td.Methods, f)
			}
			pk.Funcs = append(pk.Funcs, f)
		}
	}
	if info.Main != nil && info.Main.Sym != nil && info.Main.Sym.Pkg == prog.Root {
		p.Main = info.Main
		for _, t := range info.Main.Sig.Params {
			p.Caps = append(p.Caps, types.BuiltinOf(t))
		}
	}
	for _, fi := range info.Funcs {
		if fi.Effect == types.Comptime || fi.Body == nil {
			continue
		}
		if fi.Outer == nil {
			p.markUses(fi.Body)
		}
	}
	return p, nil
}

// MentionsCode reports whether t mentions a `Code n` type: such types,
// variables and functions exist only at compile time and are erased.
func MentionsCode(t types.Type) bool { return mentionsCode(t, nil) }

func mentionsCode(t types.Type, seen []*types.Named) bool {
	switch t := t.(type) {
	case *types.Code:
		return true
	case *types.Named:
		for _, s := range seen {
			if s == t {
				return false
			}
		}
		if t.Builtin == resolve.TFieldCode {
			return true
		}
		if t.Builtin != resolve.BNone {
			return false
		}
		for _, a := range t.TypeArgs {
			if mentionsCode(a, seen) {
				return true
			}
		}
		return mentionsCode(t.Underlying(), append(seen, t))
	case *types.Array:
		return mentionsCode(t.Elem, seen)
	case *types.Slice:
		return mentionsCode(t.Elem, seen)
	case *types.Pointer:
		return mentionsCode(t.Elem, seen)
	case *types.Struct:
		for _, f := range t.Fields {
			if mentionsCode(f.Type, seen) {
				return true
			}
		}
	case *types.Enum:
		for _, v := range t.Variants {
			for _, p := range v.Payload {
				if mentionsCode(p, seen) {
					return true
				}
			}
		}
	case *types.Signature:
		for _, p := range t.Params {
			if mentionsCode(p, seen) {
				return true
			}
		}
		for _, r := range t.Results {
			if mentionsCode(r, seen) {
				return true
			}
		}
	}
	return false
}

func splitParams(tps []*types.TypeParam) (consts, typs []*types.TypeParam) {
	for _, tp := range tps {
		if tp.IsConst() {
			consts = append(consts, tp)
		} else {
			typs = append(typs, tp)
		}
	}
	return
}

func (p *Program) newFunc(fi *types.FuncInfo) *Func {
	if f := p.funcs[fi]; f != nil {
		return f
	}
	f := &Func{Info: fi}
	f.ConstParams, f.TypeParams = splitParams(fi.TypeParams)
	if fi.Body != nil {
		collectScopes(fi.Body, &f.Scopes)
	}
	p.funcs[fi] = f
	for _, lit := range fi.Lits {
		p.newFunc(lit)
	}
	return f
}

// FuncOf returns the lowered function of a FuncInfo (including literals).
func (p *Program) FuncOf(fi *types.FuncInfo) *Func {
	if f := p.funcs[fi]; f != nil {
		return f
	}
	return p.newFunc(fi)
}

// collectScopes appends the scope statements of a body, not descending
// into function literals.
func collectScopes(n ast.Node, out *[]*ast.ScopeStmt) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ScopeStmt:
			*out = append(*out, n)
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// Names

// assignPrefixes gives every imported package a unique `_pkg_<name>_`
// prefix (deterministic: in package order; duplicates get a counter).
func (p *Program) assignPrefixes() {
	var seen []string
	for _, rp := range p.Prog.Packages {
		if rp == p.Prog.Root {
			p.prefix[rp] = ""
			continue
		}
		base := rp.Name
		name := base
		for i := 2; contains(seen, name); i++ {
			name = fmt.Sprintf("%s%d", base, i)
		}
		seen = append(seen, name)
		p.prefix[rp] = "_pkg_" + name + "_"
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Name returns the Go identifier of a symbol: package-level symbols of
// imported packages get their package prefix, `main` of the root package
// becomes `_fc_main`, other names are mangled with GoIdent.
func (p *Program) Name(s *resolve.Symbol) string {
	if s == nil {
		return "_"
	}
	if s.ID == 0 || int(s.ID) >= len(p.names) {
		return GoIdent(s.Name)
	}
	if n := p.names[s.ID]; n != "" {
		return n
	}
	var n string
	switch {
	case s.Name == "_":
		n = "_"
	case s.Kind == resolve.SymTypeParam:
		n = GoIdent(s.Name)
	case s.IsMethod():
		n = MethodIdent(s.Name)
	case s.Func == nil && s.Pkg != nil && s.Kind != resolve.SymPackage:
		if pre := p.prefix[s.Pkg]; pre != "" {
			n = pre + s.Name
		} else if s.Kind == resolve.SymFunc && s.Name == "main" && s.Pkg == p.Prog.Root {
			n = "_fc_main"
		} else {
			n = GoIdent(s.Name)
		}
	default:
		n = GoIdent(s.Name)
	}
	p.names[s.ID] = n
	return n
}

var goReserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`break case chan const continue default defer else fallthrough
		for func go goto if import interface map package range return select struct switch type var
		any bool byte comparable complex64 complex128 error float32 float64 int int8 int16 int32 int64
		rune string uint uint8 uint16 uint32 uint64 uintptr true false iota nil append cap clear close
		complex copy delete imag len make max min new panic print println real recover
		init main fincrt os`) {
		goReserved[w] = true
	}
}

var goKeywords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`break case chan const continue default defer else fallthrough
		for func go goto if import interface map package range return select struct switch type var`) {
		goKeywords[w] = true
	}
}

// Method names that `go vet` (stdmethods) checks against fixed signatures.
var vetMethods = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`Format GobDecode GobEncode MarshalJSON MarshalXML Peek ReadByte
		ReadFrom ReadRune Scan Seek UnmarshalJSON UnmarshalXML UnreadByte UnreadRune WriteByte WriteTo
		Is As`) {
		vetMethods[w] = true
	}
}

// GoIdent mangles a FinCore identifier for use as a Go variable, function,
// constant or type name. Identifiers that are Go keywords, Go predeclared
// names, names reserved by the generated code (init, main, fincrt, os) or
// that start with the generator's prefixes `_fc` / `_pkg_` are prefixed
// with `_fc_u_`. The mapping is injective.
func GoIdent(name string) string {
	if name == "_" {
		return name
	}
	if goReserved[name] || strings.HasPrefix(name, "_fc") || strings.HasPrefix(name, "_pkg_") {
		return "_fc_u_" + name
	}
	return name
}

// FieldIdent mangles a struct field name (only Go keywords and the blank
// name need mangling; enum encodings use `_tag` / `_<Variant>_<i>` fields
// in structs that have no user fields).
func FieldIdent(name string) string {
	if goKeywords[name] || name == "_" {
		return "_fc_u_" + name
	}
	return name
}

// MethodIdent mangles a method name (Go keywords, and names `go vet`'s
// stdmethods check would reject for non-standard signatures).
func MethodIdent(name string) string {
	if goKeywords[name] || vetMethods[name] || strings.HasPrefix(name, "_fc") {
		return "_fc_m_" + name
	}
	return name
}

// ---------------------------------------------------------------------------
// Usage analysis (Go's "declared and not used")

// Used reports whether a local variable symbol is read somewhere (in live
// code). Go rejects local variables that are only declared or assigned.
func (p *Program) Used(s *resolve.Symbol) bool {
	if s == nil || s.ID == 0 || int(s.ID) >= len(p.used) {
		return true
	}
	return p.used[s.ID]
}

func (p *Program) use(id *ast.Ident) {
	if s := p.Prog.Uses[id.ID()]; s != nil && int(s.ID) < len(p.used) {
		p.used[s.ID] = true
	}
}

func (p *Program) markUses(b *ast.BlockStmt) { p.markStmts(b.Stmts) }

func (p *Program) markStmts(list []ast.Stmt) {
	for _, s := range LiveStmts(p.Info, list) {
		p.markStmt(s)
	}
}

func (p *Program) markStmt(s ast.Stmt) {
	switch s := s.(type) {
	case nil:
	case *ast.AssignStmt:
		for _, l := range s.LHS {
			if _, ok := l.(*ast.Ident); !ok {
				p.markExpr(l)
			}
		}
		for _, r := range s.RHS {
			p.markExpr(r)
		}
	case *ast.IncDecStmt:
		if _, ok := s.X.(*ast.Ident); !ok {
			p.markExpr(s.X)
		}
	case *ast.ShortVarDecl:
		for _, v := range s.Values {
			p.markExpr(v)
		}
	case *ast.BlockStmt:
		p.markStmts(s.Stmts)
	case *ast.IfStmt:
		p.markStmt(s.Init)
		p.markExpr(s.Cond)
		p.markStmts(s.Then.Stmts)
		p.markStmt(s.Else)
	case *ast.ForStmt:
		p.markStmt(s.Init)
		if s.Cond != nil {
			p.markExpr(s.Cond)
		}
		p.markStmt(s.Post)
		p.markStmts(s.Body.Stmts)
	case *ast.MatchStmt:
		p.markExpr(s.X)
		for _, a := range s.Arms {
			if lp, ok := a.Pattern.(*ast.LiteralPattern); ok {
				p.markExpr(lp.Lit)
			}
			if a.Expr != nil {
				p.markExpr(a.Expr)
			}
			if a.Block != nil {
				p.markStmts(a.Block.Stmts)
			}
		}
	case *ast.ScopeStmt:
		p.markExpr(s.Value)
		p.markStmts(s.Body.Stmts)
	case *ast.DeclStmt:
		if d, ok := s.Decl.(*ast.VarDecl); ok && d.Value != nil {
			p.markExpr(d.Value)
		}
	case *ast.ExprStmt:
		p.markExpr(s.X)
	case *ast.DeferStmt:
		p.markExpr(s.X)
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			p.markExpr(r)
		}
	}
}

func (p *Program) markExpr(e ast.Expr) {
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			p.use(n)
		case *ast.FuncLit:
			p.markStmts(n.Body.Stmts)
			return false
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// Terminating statements and dead code

// LiveStmts returns the prefix of list up to and including the first
// terminating statement: statements after it are unreachable and are not
// emitted (go vet's unreachable check).
func LiveStmts(info *types.Info, list []ast.Stmt) []ast.Stmt {
	for i, s := range list {
		if IsTerminating(info, s) {
			return list[:i+1]
		}
	}
	return list
}

// IsTerminatingList reports whether a statement list ends in a terminating
// statement (after dead-code removal).
func IsTerminatingList(info *types.Info, list []ast.Stmt) bool {
	for _, s := range list {
		if IsTerminating(info, s) {
			return true
		}
	}
	return false
}

// IsTerminating implements the terminating-statement rules (Go spec,
// adapted to FinCore as in decision 37). The generated Go preserves them:
// scope blocks end with their body, exhaustive matches end with a default
// case, breaks out of a match to a loop are labeled.
func IsTerminating(info *types.Info, s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.ExprStmt:
		return isPanicCall(info, s.X)
	case *ast.BlockStmt:
		return IsTerminatingList(info, s.Stmts)
	case *ast.IfStmt:
		return s.Else != nil && IsTerminatingList(info, s.Then.Stmts) && IsTerminating(info, s.Else)
	case *ast.ForStmt:
		return s.Cond == nil && !HasBreak(info, s.Body.Stmts)
	case *ast.ScopeStmt:
		return IsTerminatingList(info, s.Body.Stmts)
	case *ast.MatchStmt:
		arms := MatchArms(info, s.X, s.Arms)
		if !arms.HasDefault {
			return false
		}
		for _, a := range arms.Arms {
			if a.Expr != nil && !isPanicCall(info, a.Expr) {
				return false
			}
			if a.Block != nil && !IsTerminatingList(info, a.Block.Stmts) {
				return false
			}
		}
		return true
	}
	return false
}

func isPanicCall(info *types.Info, e ast.Expr) bool {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	ci := info.CallOf(call)
	return ci != nil && ci.Kind == types.CallBuiltin && ci.Builtin == resolve.FPanic
}

// HasBreak reports whether a (live) statement list contains a break that
// targets the enclosing loop.
func HasBreak(info *types.Info, list []ast.Stmt) bool {
	for _, s := range LiveStmts(info, list) {
		switch s := s.(type) {
		case *ast.BreakStmt:
			return true
		case *ast.BlockStmt:
			if HasBreak(info, s.Stmts) {
				return true
			}
		case *ast.IfStmt:
			if HasBreak(info, s.Then.Stmts) || (s.Else != nil && HasBreak(info, []ast.Stmt{s.Else})) {
				return true
			}
		case *ast.MatchStmt:
			for _, a := range s.Arms {
				if a.Block != nil && HasBreak(info, a.Block.Stmts) {
					return true
				}
			}
		case *ast.ScopeStmt:
			if HasBreak(info, s.Body.Stmts) {
				return true
			}
		}
	}
	return false
}

// Arms is the lowered arm list of a match statement.
type Arms struct {
	// Arms to emit, in order: arms after a wildcard and arms whose
	// pattern repeats an earlier one are dropped (first match wins).
	Arms []*ast.MatchArm
	// HasDefault: the last arm in Arms is emitted as `default` (a
	// wildcard, or the last arm of an exhaustive enum match).
	HasDefault bool
	// Enum is the scrutinee's enum type (nil for scalar matches).
	Enum *types.Enum
}

// MatchArms lowers the arms of a match statement or expression (v0.2)
// with scrutinee x.
func MatchArms(info *types.Info, x ast.Expr, arms []*ast.MatchArm) Arms {
	var out Arms
	t := info.TypeOf(x)
	if t != nil {
		out.Enum, _ = t.Underlying().(*types.Enum)
	}
	var seenVariants []int
	var seenValues []types.Value
	for _, a := range arms {
		switch pt := a.Pattern.(type) {
		case *ast.OrPattern:
			// The type checker rejects repeated alternatives (E2012).
			out.Arms = append(out.Arms, a)
		case *ast.WildcardPattern:
			out.Arms = append(out.Arms, a)
			out.HasDefault = true
			return out
		case *ast.CtorPattern:
			if v := info.VariantOf(pt); v != nil {
				if containsInt(seenVariants, v.Index) {
					continue
				}
				seenVariants = append(seenVariants, v.Index)
				out.Arms = append(out.Arms, a)
				continue
			}
			if dupValue(seenValues, info.ValueOf(pt.Name)) {
				continue
			}
			seenValues = append(seenValues, info.ValueOf(pt.Name))
			out.Arms = append(out.Arms, a)
		case *ast.LiteralPattern:
			if dupValue(seenValues, info.ValueOf(pt.Lit)) {
				continue
			}
			seenValues = append(seenValues, info.ValueOf(pt.Lit))
			out.Arms = append(out.Arms, a)
		}
	}
	// v0.2: enum matches without a wildcard are exhaustive (E2012).
	if out.Enum != nil && len(out.Arms) > 0 {
		out.HasDefault = true
	}
	return out
}

func containsInt(list []int, x int) bool {
	for _, y := range list {
		if x == y {
			return true
		}
	}
	return false
}

func dupValue(seen []types.Value, v types.Value) bool {
	if v == nil {
		return false
	}
	for _, s := range seen {
		if s == nil {
			continue
		}
		if eq, err := types.Equal(s, v); err == nil && eq {
			return true
		}
	}
	return false
}
