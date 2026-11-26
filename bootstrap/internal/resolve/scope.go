package resolve

import "fincore/bootstrap/internal/ast"

// ScopeKind classifies lexical scopes.
type ScopeKind uint8

const (
	ScopeUniverse ScopeKind = iota
	ScopePackage
	ScopeFile     // import names
	ScopeTypeDecl // type parameters of a generic type declaration
	ScopeFunc     // receiver, type params, params and the top-level body block
	ScopeBlock    // { ... }, if/for headers, match arms, scope bodies
)

// Scope is a lexical scope. Symbols are kept in insertion order (Syms) for
// deterministic iteration; the map is only used for lookups.
type Scope struct {
	Parent *Scope
	Kind   ScopeKind
	Node   ast.Node // the node that opened the scope (nil for universe/package)
	Syms   []*Symbol
	names  map[string]*Symbol
}

// NewScope creates a scope.
func NewScope(parent *Scope, kind ScopeKind, node ast.Node) *Scope {
	return &Scope{Parent: parent, Kind: kind, Node: node}
}

// LookupLocal finds name in s only.
func (s *Scope) LookupLocal(name string) *Symbol {
	return s.names[name]
}

// Lookup finds name in s or its parents.
func (s *Scope) Lookup(name string) *Symbol {
	for sc := s; sc != nil; sc = sc.Parent {
		if sym := sc.names[name]; sym != nil {
			return sym
		}
	}
	return nil
}

// Insert adds sym unless the name is already declared in s, in which case
// the existing symbol is returned and sym is not inserted.
func (s *Scope) Insert(sym *Symbol) *Symbol {
	if old := s.names[sym.Name]; old != nil {
		return old
	}
	if s.names == nil {
		s.names = map[string]*Symbol{}
	}
	s.names[sym.Name] = sym
	s.Syms = append(s.Syms, sym)
	return nil
}
