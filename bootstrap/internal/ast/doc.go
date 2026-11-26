// Package ast defines the abstract syntax tree produced by the finc0 parser
// for the grammar in spec section 3.
//
// # Node identity and annotations
//
// Every node embeds Base, which carries
//
//   - a SourceSpan (file, start/end line:col and byte offsets), and
//   - a NodeID: a dense, 1-based integer assigned by the parser once a file
//     is complete, in post-order (Children order): every child has a smaller ID
//     than its parent, so iterating a dense side table in ID order is a
//     bottom-up traversal. IDs are unique across all files parsed
//     with the same *IDAlloc (the parser shares one allocator across all
//     files of a package, and the driver should share one across all packages
//     of a program). ID 0 means "no node".
//
// The AST itself is never mutated by later phases. Instead, each pass
// (resolver, type checker, comptime interpreter, scope/totality checkers,
// IR lowering, Go codegen) stores its results in side tables keyed by
// NodeID, typically dense slices sized with IDAlloc.Count()+1:
//
//	types := make([]TypeID, ids.Count()+1)
//	types[expr.ID()] = t
//
// or, for sparse data, map[ast.NodeID]T. Dense slices are preferred because
// they are deterministic to iterate (spec 9.2 forbids emitting code while
// iterating Go maps) and mirror the NodeArena design of the self-hosted
// compiler (spec 9.4), where nodes are addressed by NodeID = u32.
// Keying by node pointer also works but must never be iterated for output.
//
// # Syntactic ambiguities left for later phases
//
// Some distinctions cannot be made by a context-free parser and are
// represented uniformly; the resolver reclassifies them:
//
//   - IndexExpr with one index is either indexing a[i] or a generic
//     instantiation f[T]; with several indices it is always an
//     instantiation (f[N, T]). Generic arguments are TypeOrExpr: arguments
//     that are syntactically types (e.g. []byte, [4]u8, struct{...},
//     func(...), Code n, El(e)) appear as *TypeExpr; identifiers, qualified
//     names, *p and instantiations appear as ordinary expressions. Use
//     ExprAsType to view such an expression as a Type.
//   - Casts u32(x) are ordinary CallExprs whose Fun names a type.
//   - NamedType.Args are TypeOrExpr in the same representation.
//   - A CtorPattern without payload (`Red`, `Color.Red`) may name an enum
//     variant or a constant; the resolver decides.
//   - `Code` and `El` are predeclared identifiers (not keywords) but are
//     parsed contextually: in type position (and in expression position when
//     followed by an operand / '(' respectively) they produce CodeType and
//     ElType. They therefore cannot usefully be shadowed.
package ast
