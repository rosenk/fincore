package resolve

import (
	"fincore/bootstrap/internal/ast"
)

// SymKind classifies symbols.
type SymKind uint8

const (
	SymBad       SymKind = iota
	SymPackage           // imported package name (file scope)
	SymConst             // constant (package-level, local, or predeclared)
	SymType              // named type, alias, or predeclared type
	SymVar               // variable: global, local, parameter, receiver, binding, scope name
	SymFunc              // declared function or method
	SymBuiltin           // predeclared function (len, copy, CodeArray, ...)
	SymTypeParam         // type or const parameter ([T any], [N usize])
)

func (k SymKind) String() string {
	return [...]string{"bad", "package", "const", "type", "var", "func", "builtin", "type parameter"}[k]
}

// VarKind refines SymVar symbols.
type VarKind uint8

const (
	VarNone    VarKind = iota
	VarGlobal          // package-level `var`
	VarLocal           // `var x` / `x :=` inside a function
	VarParam           // function or func-literal parameter
	VarRecv            // method receiver
	VarBinding         // match-arm payload binding
	VarScope           // name introduced by `scope s := ...`
)

func (k VarKind) String() string {
	return [...]string{"none", "global", "local", "param", "receiver", "binding", "scope"}[k]
}

// SymID is a dense, 1-based symbol index (0 = none), unique per Program.
// Later passes may index side tables by SymID.
type SymID uint32

// Symbol is a named entity.
type Symbol struct {
	ID      SymID
	Kind    SymKind
	VarKind VarKind // for SymVar
	Name    string
	// Ident is the defining identifier (nil for predeclared symbols).
	Ident *ast.Ident
	// Decl is the declaring node:
	//   package-level: *ast.ConstSpec, *ast.TypeDecl, *ast.VarDecl, *ast.FuncDecl
	//   imports:       *ast.ImportSpec
	//   locals:        *ast.ConstSpec, *ast.VarDecl, *ast.ShortVarDecl,
	//                  *ast.Param, *ast.Receiver, *ast.CtorPattern, *ast.ScopeStmt
	//   type params:   *ast.TypeParam
	Decl ast.Node
	// Pkg is the declaring package (nil for predeclared symbols).
	Pkg *Package
	// Builtin is non-zero for predeclared symbols.
	Builtin Builtin
	// Func is the innermost enclosing function (*ast.FuncDecl or
	// *ast.FuncLit) for locals, parameters, receivers and the type
	// parameters of generic functions/methods; nil for package-level symbols.
	Func ast.Node
	// Imported is the package an import name refers to (SymPackage).
	Imported *Package
	// RecvType is the receiver type name for methods (SymFunc); "" for
	// plain functions. Methods are not in any scope; see Package.Methods.
	RecvType string
}

// IsMethod reports whether s is a method.
func (s *Symbol) IsMethod() bool { return s.Kind == SymFunc && s.RecvType != "" }

// IsGlobal reports whether s is declared at package level (or predeclared).
func (s *Symbol) IsGlobal() bool { return s.Func == nil }

// IsExported reports whether the name starts with an upper-case ASCII letter.
func (s *Symbol) IsExported() bool { return IsExported(s.Name) }

// IsExported reports whether name is exported (decision 15).
func IsExported(name string) bool { return name != "" && name[0] >= 'A' && name[0] <= 'Z' }

func (s *Symbol) String() string {
	if s.Pkg != nil && s.Func == nil {
		return s.Pkg.Name + "." + s.Name
	}
	return s.Name
}
