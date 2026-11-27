package resolve

import (
	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
)

// Resolve builds the universe, declares the package-level symbols of every
// package and binds every identifier use to its symbol (Program.Defs/Uses).
// It is called by Load; it is exported for tests that construct programs
// from in-memory ASTs (see NewProgram).
func Resolve(p *Program) diag.List {
	if p.Captures == nil {
		p.Captures = map[ast.NodeID][]*Symbol{}
	}
	p.Universe = newUniverse(p)
	b := &binder{prog: p, declOf: map[*ast.Ident]*Symbol{}}
	for _, pkg := range p.Packages {
		b.declarePackage(pkg)
	}
	n := p.IDs.Count() + 1
	p.Defs = make([]*Symbol, n)
	p.Uses = make([]*Symbol, n)
	for _, pkg := range p.Packages {
		b.bindPackage(pkg)
	}
	b.diags.Sort()
	return b.diags
}

// NewProgram wraps already-parsed packages (in dependency order, root last)
// into a Program without touching the file system; import specs are matched
// against the packages' Path fields. Used by tests.
func NewProgram(ids *ast.IDAlloc, pkgs []*Package) (*Program, diag.List) {
	p := &Program{IDs: ids, Packages: pkgs, Captures: map[ast.NodeID][]*Symbol{}}
	if len(pkgs) > 0 {
		p.Root = pkgs[len(pkgs)-1]
	}
	byPath := map[string]*Package{}
	for _, pkg := range pkgs {
		if pkg.Name == "" && pkg.AST != nil {
			pkg.Name = pkg.AST.Name
		}
		byPath[pkg.Path] = pkg
	}
	var diags diag.List
	for _, pkg := range pkgs {
		for _, f := range pkg.AST.Files {
			for _, imp := range f.Imports {
				for _, spec := range imp.Specs {
					dep := byPath[spec.Path.Str]
					if dep == nil || spec.Path.Str == "" {
						diags.Addf(diag.E2001, spec.Path.Range, "cannot find package %q", spec.Path.Str)
						continue
					}
					pkg.Imports = append(pkg.Imports, dep)
				}
			}
		}
	}
	if len(diags) > 0 {
		return p, diags
	}
	return p, Resolve(p)
}

type binder struct {
	prog  *Program
	pkg   *Package
	diags diag.List
	scope *Scope
	funcs []ast.Node // enclosing functions (FuncDecl / FuncLit), innermost last
	// declOf maps the defining identifier of each package-level symbol
	// (including methods and redeclared duplicates) to its symbol.
	declOf map[*ast.Ident]*Symbol
}

func (b *binder) errorf(code diag.Code, sp diag.SourceSpan, format string, args ...any) {
	b.diags.Addf(code, sp, format, args...)
}

// declarePackage creates the package and file scopes and the package-level
// symbols.
func (b *binder) declarePackage(pkg *Package) {
	pkg.Scope = NewScope(b.prog.Universe, ScopePackage, nil)
	add := func(kind SymKind, id *ast.Ident, decl ast.Node) *Symbol {
		sym := b.prog.NewSymbol(kind, id.Name, id, decl, pkg)
		if kind == SymVar {
			sym.VarKind = VarGlobal
		}
		pkg.Decls = append(pkg.Decls, sym)
		b.declOf[id] = sym
		if id.Name == "_" {
			return sym
		}
		if old := pkg.Scope.Insert(sym); old != nil {
			b.errorf(diag.E2001, id.Range, "%s redeclared in this package (previous declaration at %s)", id.Name, old.Ident.Range)
		}
		return sym
	}
	for _, f := range pkg.AST.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.ConstDecl:
				for _, s := range d.Specs {
					add(SymConst, s.Name, s)
				}
			case *ast.TypeDecl:
				add(SymType, d.Name, d)
			case *ast.VarDecl:
				add(SymVar, d.Name, d)
			case *ast.FuncDecl:
				if d.Recv != nil {
					sym := b.prog.NewSymbol(SymFunc, d.Name.Name, d.Name, d, pkg)
					sym.RecvType = d.Recv.TypeName.Name
					if old := pkg.LookupMethod(sym.RecvType, sym.Name); old != nil {
						b.errorf(diag.E2001, d.Name.Range, "method %s.%s redeclared (previous declaration at %s)", sym.RecvType, sym.Name, old.Ident.Range)
					} else {
						pkg.Methods = append(pkg.Methods, sym)
					}
					pkg.Decls = append(pkg.Decls, sym)
					b.declOf[d.Name] = sym
					continue
				}
				add(SymFunc, d.Name, d)
			}
		}
	}
	for _, f := range pkg.AST.Files {
		fs := NewScope(pkg.Scope, ScopeFile, f)
		pkg.FileScopes = append(pkg.FileScopes, fs)
	}
}

func (b *binder) bindPackage(pkg *Package) {
	b.pkg = pkg
	// Import names (file scope). Imports were matched in order by the
	// loader; match them again by import path.
	byPath := map[string]*Package{}
	for _, dep := range pkg.Imports {
		byPath[dep.Path] = dep
	}
	for i, f := range pkg.AST.Files {
		fs := pkg.FileScopes[i]
		for _, imp := range f.Imports {
			for _, spec := range imp.Specs {
				dep := byPath[spec.Path.Str]
				if dep == nil {
					continue
				}
				name, id := dep.Name, spec.Name
				if id != nil {
					name = id.Name
				}
				if name == "_" {
					continue
				}
				sym := b.prog.NewSymbol(SymPackage, name, id, spec, pkg)
				sym.Imported = dep
				if id != nil {
					b.prog.Defs[id.ID()] = sym
				}
				if old := fs.Insert(sym); old != nil {
					b.errorf(diag.E2001, spec.Range, "%s redeclared in this file", name)
				} else if pkg.Scope.LookupLocal(name) != nil {
					b.errorf(diag.E2001, spec.Range, "import name %s conflicts with a package-level declaration", name)
				}
			}
		}
	}
	for i, f := range pkg.AST.Files {
		b.scope = pkg.FileScopes[i]
		for _, d := range f.Decls {
			b.topDecl(d)
		}
	}
	b.scope = nil
}

func (b *binder) def(sym *Symbol) {
	if sym.Ident != nil {
		b.prog.Defs[sym.Ident.ID()] = sym
	}
}

func (b *binder) topDecl(d ast.Decl) {
	switch d := d.(type) {
	case *ast.ConstDecl:
		for _, s := range d.Specs {
			b.def(b.declOf[s.Name])
			b.bindType(s.Type)
			b.bindExpr(s.Value)
		}
	case *ast.VarDecl:
		b.def(b.declOf[d.Name])
		b.bindType(d.Type)
		b.bindExpr(d.Value)
	case *ast.TypeDecl:
		b.def(b.declOf[d.Name])
		outer := b.scope
		if d.TypeParams != nil {
			b.scope = NewScope(outer, ScopeTypeDecl, d)
			b.typeParams(d.TypeParams)
		}
		b.bindType(d.Type)
		b.scope = outer
	case *ast.FuncDecl:
		b.funcDecl(d)
	}
}

func (b *binder) typeParams(l *ast.TypeParamList) {
	for _, tp := range l.Params {
		b.bindType(tp.Constraint)
	}
	for _, tp := range l.Params {
		for _, id := range tp.Names {
			b.declare(SymTypeParam, VarNone, id, tp)
		}
	}
}

// declare defines a new local symbol in the current scope.
func (b *binder) declare(kind SymKind, vk VarKind, id *ast.Ident, decl ast.Node) *Symbol {
	sym := b.prog.NewSymbol(kind, id.Name, id, decl, b.pkg)
	sym.VarKind = vk
	if len(b.funcs) > 0 {
		sym.Func = b.funcs[len(b.funcs)-1]
	}
	if id.Name == "_" {
		return sym
	}
	b.def(sym)
	if old := b.scope.Insert(sym); old != nil {
		b.errorf(diag.E2001, id.Range, "%s redeclared in this block", id.Name)
	}
	return sym
}

func (b *binder) funcDecl(d *ast.FuncDecl) {
	sym := b.declOf[d.Name]
	b.def(sym)
	outer := b.scope
	b.scope = NewScope(outer, ScopeFunc, d)
	b.funcs = append(b.funcs, d)
	if d.Recv != nil {
		r := d.Recv
		tsym := b.pkg.Scope.LookupLocal(r.TypeName.Name)
		if tsym == nil || tsym.Kind != SymType {
			b.errorf(diag.E2001, r.TypeName.Range, "undefined receiver type %s (must be a type declared in package %s)", r.TypeName.Name, b.pkg.Name)
		} else {
			b.prog.Uses[r.TypeName.ID()] = tsym
		}
		for _, id := range r.TypeParams {
			b.declare(SymTypeParam, VarNone, id, r)
		}
	}
	if d.TypeParams != nil {
		b.typeParams(d.TypeParams)
	}
	b.signatureTypes(d.Sig)
	if d.Recv != nil {
		b.declare(SymVar, VarRecv, d.Recv.Name, d.Recv)
	}
	b.params(d.Sig)
	if d.Decreases != nil {
		b.bindExpr(d.Decreases.Measure)
	}
	if d.Body != nil {
		b.stmts(d.Body.Stmts)
	}
	b.funcs = b.funcs[:len(b.funcs)-1]
	b.scope = outer
}

func (b *binder) signatureTypes(s *ast.Signature) {
	for _, p := range s.Params {
		b.bindType(p.Type)
	}
	for _, t := range s.Results {
		b.bindType(t)
	}
}

func (b *binder) params(s *ast.Signature) {
	for _, p := range s.Params {
		for _, id := range p.Names {
			b.declare(SymVar, VarParam, id, p)
		}
	}
}

func (b *binder) bindType(t ast.Type) {
	if t == nil {
		return
	}
	switch t := t.(type) {
	case *ast.NamedType:
		b.namedType(t)
	case *ast.ArrayType:
		b.bindExpr(t.Len)
		b.bindType(t.Elem)
	case *ast.SliceType:
		b.bindType(t.Elem)
	case *ast.PointerType:
		b.bindType(t.Elem)
	case *ast.StructType:
		for _, f := range t.Fields {
			b.bindType(f.Type)
		}
	case *ast.EnumType:
		for _, v := range t.Variants {
			for _, p := range v.Payload {
				b.bindType(p)
			}
		}
	case *ast.FuncType:
		b.signatureTypes(t.Sig)
	case *ast.CodeType:
		b.bindExpr(t.Level)
	case *ast.ElType:
		b.bindExpr(t.Code)
	case *ast.AnyType:
	}
}

func (b *binder) namedType(t *ast.NamedType) {
	if t.Pkg != nil {
		b.qualified(t.Pkg, t.Name)
	} else {
		b.ident(t.Name)
	}
	for _, a := range t.Args {
		b.bindExpr(a)
	}
}

// qualified resolves pkg.Name; returns false if pkg is not a package name.
func (b *binder) qualified(pkg, name *ast.Ident) bool {
	ps := b.lookup(pkg)
	if ps == nil {
		return true // error reported
	}
	if ps.Kind != SymPackage {
		return false
	}
	member := ps.Imported.Scope.LookupLocal(name.Name)
	switch {
	case member == nil:
		b.errorf(diag.E2001, name.Range, "undefined: %s.%s", pkg.Name, name.Name)
	case !IsExported(name.Name):
		b.errorf(diag.E2001, name.Range, "%s.%s is not exported", pkg.Name, name.Name)
	default:
		b.prog.Uses[name.ID()] = member
	}
	return true
}

// lookup resolves an identifier use, reporting E2001 if undefined.
func (b *binder) lookup(id *ast.Ident) *Symbol {
	if id.Name == "_" {
		b.errorf(diag.E2001, id.Range, "cannot use _ as value")
		return nil
	}
	sym := b.scope.Lookup(id.Name)
	if sym == nil {
		b.errorf(diag.E2001, id.Range, "undefined: %s", id.Name)
		return nil
	}
	b.prog.Uses[id.ID()] = sym
	b.noteCapture(sym)
	return sym
}

func (b *binder) ident(id *ast.Ident) { b.lookup(id) }

// noteCapture records sym as captured by every function literal between the
// current function and the function declaring sym.
func (b *binder) noteCapture(sym *Symbol) {
	if sym.Func == nil || len(b.funcs) == 0 || sym.Kind == SymTypeParam {
		return
	}
	for i := len(b.funcs) - 1; i >= 0; i-- {
		fn := b.funcs[i]
		if fn == sym.Func {
			return
		}
		lit, ok := fn.(*ast.FuncLit)
		if !ok {
			return
		}
		caps := b.prog.Captures[lit.ID()]
		dup := false
		for _, c := range caps {
			if c == sym {
				dup = true
				break
			}
		}
		if !dup {
			b.prog.Captures[lit.ID()] = append(caps, sym)
		}
	}
}

func (b *binder) bindExpr(e ast.Expr) {
	if e == nil {
		return
	}
	switch e := e.(type) {
	case *ast.Ident:
		b.ident(e)
	case *ast.BasicLit, *ast.BoolLit, *ast.NilLit:
	case *ast.BinaryExpr:
		b.bindExpr(e.X)
		b.bindExpr(e.Y)
	case *ast.UnaryExpr:
		b.bindExpr(e.X)
	case *ast.ParenExpr:
		b.bindExpr(e.X)
	case *ast.CallExpr:
		b.bindExpr(e.Fun)
		for _, a := range e.Args {
			b.bindExpr(a)
		}
	case *ast.IndexExpr:
		b.bindExpr(e.X)
		for _, a := range e.Indices {
			b.bindExpr(a)
		}
	case *ast.SliceExpr:
		b.bindExpr(e.X)
		b.bindExpr(e.Lo)
		b.bindExpr(e.Hi)
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok && x.Name != "_" {
			// Resolves x; if x is a package name, also resolves Sel.
			b.qualified(x, e.Sel)
			return
		}
		b.bindExpr(e.X)
	case *ast.FuncLit:
		outer := b.scope
		b.scope = NewScope(outer, ScopeFunc, e)
		b.signatureTypes(e.Sig)
		b.funcs = append(b.funcs, e)
		b.params(e.Sig)
		b.stmts(e.Body.Stmts)
		b.funcs = b.funcs[:len(b.funcs)-1]
		b.scope = outer
	case *ast.StructLit:
		b.namedType(e.Type)
		for _, f := range e.Fields {
			b.bindExpr(f.Value)
		}
	case *ast.TypeExpr:
		b.bindType(e.Type)
	case *ast.MatchExpr:
		b.bindExpr(e.X)
		for _, arm := range e.Arms {
			b.matchArm(arm)
		}
	}
}

// ctorPattern resolves the names of a constructor pattern.
func (b *binder) ctorPattern(cp *ast.CtorPattern) {
	switch {
	case cp.Qual != nil:
		// Qual.Name: a package member, or Type.Variant (the variant is
		// resolved by the type checker).
		b.qualified(cp.Qual, cp.Name)
	case !cp.HasParens:
		// A constant, or an unqualified variant of the scrutinee's enum.
		if sym := b.scope.Lookup(cp.Name.Name); sym != nil && sym.Kind == SymConst {
			b.prog.Uses[cp.Name.ID()] = sym
		}
	}
}

func (b *binder) openScope(n ast.Node) func() {
	outer := b.scope
	b.scope = NewScope(outer, ScopeBlock, n)
	return func() { b.scope = outer }
}

func (b *binder) stmts(list []ast.Stmt) {
	for _, s := range list {
		b.stmt(s)
	}
}

func (b *binder) block(bl *ast.BlockStmt) {
	if bl == nil {
		return
	}
	defer b.openScope(bl)()
	b.stmts(bl.Stmts)
}

func (b *binder) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case nil:
	case *ast.BlockStmt:
		b.block(s)
	case *ast.DeclStmt:
		switch d := s.Decl.(type) {
		case *ast.ConstDecl:
			for _, sp := range d.Specs {
				b.bindType(sp.Type)
				b.bindExpr(sp.Value)
				b.declare(SymConst, VarNone, sp.Name, sp)
			}
		case *ast.VarDecl:
			b.bindType(d.Type)
			b.bindExpr(d.Value)
			b.declare(SymVar, VarLocal, d.Name, d)
		}
	case *ast.ShortVarDecl:
		for _, v := range s.Values {
			b.bindExpr(v)
		}
		fresh := 0
		for _, id := range s.Names {
			if id.Name == "_" {
				continue
			}
			if old := b.scope.LookupLocal(id.Name); old != nil {
				b.prog.Uses[id.ID()] = old
				continue
			}
			fresh++
			b.declare(SymVar, VarLocal, id, s)
		}
		if fresh == 0 {
			b.errorf(diag.E2002, s.Range, "no new variables on left side of :=")
		}
	case *ast.AssignStmt:
		for _, l := range s.LHS {
			if id, ok := l.(*ast.Ident); ok && id.Name == "_" && s.Op == ast.Assign {
				continue
			}
			b.bindExpr(l)
		}
		for _, r := range s.RHS {
			b.bindExpr(r)
		}
	case *ast.IncDecStmt:
		b.bindExpr(s.X)
	case *ast.ExprStmt:
		b.bindExpr(s.X)
	case *ast.DeferStmt:
		b.bindExpr(s.X)
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			b.bindExpr(r)
		}
	case *ast.IfStmt:
		defer b.openScope(s)()
		b.stmt(s.Init)
		b.bindExpr(s.Cond)
		b.block(s.Then)
		b.stmt(s.Else)
	case *ast.ForStmt:
		defer b.openScope(s)()
		b.stmt(s.Init)
		b.bindExpr(s.Cond)
		b.stmt(s.Post)
		if s.Decreases != nil {
			b.bindExpr(s.Decreases.Measure)
		}
		b.block(s.Body)
	case *ast.MatchStmt:
		b.bindExpr(s.X)
		for _, arm := range s.Arms {
			b.matchArm(arm)
		}
	case *ast.ScopeStmt:
		b.bindExpr(s.Value)
		defer b.openScope(s)()
		b.declare(SymVar, VarScope, s.Name, s)
		b.block(s.Body)
	case *ast.BreakStmt, *ast.ContinueStmt:
	}
}

func (b *binder) matchArm(arm *ast.MatchArm) {
	defer b.openScope(arm)()
	switch p := arm.Pattern.(type) {
	case *ast.CtorPattern:
		b.ctorPattern(p)
		for _, id := range p.Bindings {
			b.declare(SymVar, VarBinding, id, p)
		}
	case *ast.OrPattern:
		// Alternatives bind nothing (bindings are rejected by the type
		// checker and are not declared).
		for _, a := range p.Alts {
			if cp, ok := a.(*ast.CtorPattern); ok {
				b.ctorPattern(cp)
			}
		}
	}
	if arm.Expr != nil {
		b.bindExpr(arm.Expr)
	}
	if arm.Block != nil {
		b.stmts(arm.Block.Stmts)
	}
}
