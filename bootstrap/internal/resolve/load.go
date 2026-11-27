package resolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/parser"
)

// Program is a fully loaded and name-resolved FinCore program: the root
// package plus all transitively imported local packages.
type Program struct {
	// ModuleRoot is the directory local import paths are resolved against
	// (decision 15: the working directory of finc).
	ModuleRoot string
	// Packages in dependency order: every package comes after all packages
	// it imports; the root package is last.
	Packages []*Package
	// Root is the package named on the command line.
	Root *Package
	// IDs is the NodeID allocator shared by all files of the program.
	IDs *ast.IDAlloc
	// Universe is the predeclared scope.
	Universe *Scope
	// Syms lists every symbol by SymID (Syms[0] is nil).
	Syms []*Symbol
	// Defs maps the NodeID of each defining identifier to its symbol.
	Defs []*Symbol
	// Uses maps the NodeID of each identifier use to its symbol. Not filled
	// for struct field names, selector names of non-package selectors,
	// enum variant names, or unqualified constructor patterns that do not
	// name a constant (the type checker resolves those).
	Uses []*Symbol
	// Captures maps a *ast.FuncLit NodeID to the enclosing-function locals
	// it references, in order of first reference.
	Captures map[ast.NodeID][]*Symbol
}

// Package is one loaded package.
type Package struct {
	AST *ast.Package
	// Path is the import path ("" for the root package).
	Path string
	// Key is the cleaned absolute directory (or file) path, unique per package.
	Key  string
	Name string
	// Scope is the package scope (parent: universe). It contains all
	// package-level constants, types, variables and functions (not methods).
	Scope *Scope
	// FileScopes holds the import names of each file (parent: Scope),
	// aligned with AST.Files.
	FileScopes []*Scope
	// Imports lists the directly imported packages in first-import order.
	Imports []*Package
	// Decls lists package-level symbols in source order (consts, types,
	// vars, funcs, and methods).
	Decls []*Symbol
	// Methods lists the method symbols in source order.
	Methods []*Symbol
}

// LookupMethod returns the method named name declared on the package's
// type typeName, or nil.
func (p *Package) LookupMethod(typeName, name string) *Symbol {
	for _, m := range p.Methods {
		if m.RecvType == typeName && m.Name == name {
			return m
		}
	}
	return nil
}

// NewSymbol allocates a symbol with the next SymID.
func (p *Program) NewSymbol(kind SymKind, name string, ident *ast.Ident, decl ast.Node, pkg *Package) *Symbol {
	if len(p.Syms) == 0 {
		p.Syms = append(p.Syms, nil)
	}
	s := &Symbol{ID: SymID(len(p.Syms)), Kind: kind, Name: name, Ident: ident, Decl: decl, Pkg: pkg}
	p.Syms = append(p.Syms, s)
	return s
}

// Lookup returns the universe symbol of a builtin.
func (p *Program) Builtin(b Builtin) *Symbol {
	return p.Universe.LookupLocal(b.String())
}

// SymbolOf returns the symbol an identifier defines or uses (nil if
// unresolved).
func (p *Program) SymbolOf(id *ast.Ident) *Symbol {
	n := int(id.ID())
	if n < len(p.Defs) && p.Defs[n] != nil {
		return p.Defs[n]
	}
	if n < len(p.Uses) {
		return p.Uses[n]
	}
	return nil
}

// Options configure Load.
type Options struct {
	// ModuleRoot for local imports; "" means the current directory.
	ModuleRoot string
}

// Load parses the file or directory at path and all packages it imports,
// then resolves names. I/O failures of the root path are returned as err;
// all other problems as diagnostics (parse errors E1xxx, E2001, E2006). If
// any parse or import error occurs, name resolution is skipped and prog may
// be incomplete.
func Load(path string, opts Options) (*Program, diag.List, error) {
	root := opts.ModuleRoot
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	l := &loader{
		prog:  &Program{ModuleRoot: absRoot, IDs: &ast.IDAlloc{}, Captures: map[ast.NodeID][]*Symbol{}},
		state: map[string]int{},
		byKey: map[string]*Package{},
	}
	rootAST, d, err := parser.ParsePath(path, l.prog.IDs)
	if err != nil {
		return nil, nil, err
	}
	l.diags = append(l.diags, d...)
	key, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	l.prog.Root = l.visit(rootAST, "", filepath.Clean(key))
	if len(l.diags) > 0 {
		l.diags.Sort()
		return l.prog, l.diags, nil
	}
	diags := Resolve(l.prog)
	return l.prog, diags, nil
}

type loader struct {
	prog  *Program
	diags diag.List
	state map[string]int // 1 visiting, 2 done (lookups only)
	byKey map[string]*Package
}

func (l *loader) visit(a *ast.Package, importPath, key string) *Package {
	pkg := &Package{AST: a, Path: importPath, Key: key, Name: a.Name}
	l.byKey[key] = pkg
	l.state[key] = 1
	seen := map[*Package]bool{}
	for _, f := range a.Files {
		for _, imp := range f.Imports {
			for _, spec := range imp.Specs {
				dep := l.importSpec(spec)
				if dep != nil && !seen[dep] {
					seen[dep] = true
					pkg.Imports = append(pkg.Imports, dep)
				}
			}
		}
	}
	l.state[key] = 2
	l.prog.Packages = append(l.prog.Packages, pkg)
	return pkg
}

func (l *loader) importSpec(spec *ast.ImportSpec) *Package {
	ip := spec.Path.Str
	if ip == "" || filepath.IsAbs(ip) || strings.HasPrefix(ip, "/") ||
		strings.Contains(ip, "\\") || ip != filepath.ToSlash(filepath.Clean(ip)) || strings.HasPrefix(ip, "..") {
		l.diags.Addf(diag.E2001, spec.Path.Range, "invalid import path %q", ip)
		return nil
	}
	dir := filepath.Join(l.prog.ModuleRoot, filepath.FromSlash(ip))
	// std/... resolves against $FINCROOT when it is set (and non-empty).
	// Such packages are named by the lexically cleaned path
	// $FINCROOT/<ip> in diagnostics (never made relative), so that finc1,
	// which cannot query the working directory, prints the same names.
	stdDir := StdDir(ip, os.Getenv("FINCROOT"))
	display := func(p string) string { return rel(l.prog.ModuleRoot, p) }
	if stdDir != "" {
		dir = stdDir
		display = func(p string) string { return p }
	}
	key := filepath.Clean(dir)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	switch l.state[key] {
	case 1:
		l.diags.Addf(diag.E2006, spec.Path.Range, "import cycle not allowed: %q imports itself (directly or indirectly)", ip)
		return nil
	case 2:
		return l.byKey[key]
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		l.diags.Addf(diag.E2001, spec.Path.Range, "cannot find package %q (no directory %s)", ip, display(dir))
		return nil
	}
	files, err := parser.ListSourceFiles(dir)
	if err != nil || len(files) == 0 {
		l.diags.Addf(diag.E2001, spec.Path.Range, "cannot find package %q (no %s files in %s)", ip, parser.SourceExt, display(dir))
		return nil
	}
	// Report positions relative to the module root, like the root package.
	for i, f := range files {
		files[i] = display(f)
	}
	a, d, err := parser.ParseFiles(display(dir), files, l.prog.IDs)
	if err != nil {
		l.diags.Addf(diag.E2001, spec.Path.Range, "cannot load package %q: %v", ip, err)
		return nil
	}
	l.diags = append(l.diags, d...)
	if len(a.Files) == 0 {
		return nil
	}
	return l.visit(a, ip, key)
}

// StdDir returns the directory of the standard-library import path ip
// ("std" or "std/...") under fincRoot (the value of $FINCROOT), lexically
// cleaned, or "" when ip is not a std path or fincRoot is empty (then ip
// resolves against the module root like every local import).
func StdDir(ip, fincRoot string) string {
	if fincRoot == "" || (ip != "std" && !strings.HasPrefix(ip, "std/")) {
		return ""
	}
	return filepath.Join(fincRoot, filepath.FromSlash(ip))
}

// rel returns path relative to the current directory if that is a clean
// relative path below it, otherwise path itself.
func rel(root, path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	if r, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	_ = root
	return path
}

// String renders a short description (for debugging).
func (p *Package) String() string { return fmt.Sprintf("package %s (%s)", p.Name, p.Key) }
