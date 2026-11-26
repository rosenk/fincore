// Package types is the finc0 type checker (milestone M1). It consumes a
// name-resolved program (package resolve) and produces the typed model
// (*Info) that every later pass reads: the scope checker (S1-S5), the
// totality/effect checker (T1-T4, E5001), IR lowering and Go codegen.
//
// # Pipeline
//
//	prog, diags, err := resolve.Load(path, resolve.Options{})          // parse, imports, E2001/E2006
//	info, diags := types.Check(prog, types.Config{Evaluator: comptime.New(fuel)})
//
// or simply check.Run (package check), which also runs the registered
// later passes. Passes after the type checker only run on programs without
// type errors, so they may assume: every expression has a valid type,
// every CallExpr has a *Call, every identifier use has a symbol.
//
// # Identity: symbols and nodes
//
// Symbols (*resolve.Symbol, dense SymID) come from the resolver; the
// checker never creates them (except dummy symbols for blank `_`
// parameters/vars, with ID 0). For every identifier:
//
//	info.SymbolOf(id)            // def or use (Prog.Defs / Prog.Uses)
//	sym.Kind, sym.VarKind        // SymVar + VarGlobal/VarLocal/VarParam/VarRecv/VarBinding/VarScope, ...
//	sym.Func                     // enclosing *ast.FuncDecl / *ast.FuncLit of locals (nil for globals)
//	sym.Builtin                  // predeclared entity (resolve.TConn, resolve.FLen, resolve.CErrEOF, ...)
//	info.SymType(sym)            // declared/inferred type
//	info.SymValue(sym)           // value of a constant
//
// Not symbols: struct field names (see Selection.Field), enum variant names
// (Selection.Variant, VariantOf), and method names in selectors
// (Selection.Method). Package-qualified names pkg.X are resolved by the
// resolver: Prog.Uses[sel.Sel.ID()] is the member symbol and there is no
// Selection.
//
// # Per-node side tables (indexed by ast.NodeID)
//
//	info.TypeOf(n)    // expressions: final type (untyped constants get the
//	                  //   type they were converted to, else their default);
//	                  //   *Tuple for multi-result calls; nil for calls without
//	                  //   results and builtin/package names.
//	                  // type nodes and type-denoting expressions: the type;
//	                  //   El(...) is folded, e.g. [1024][4096]u8.
//	                  // a method selector used as callee: the instantiated
//	                  //   method signature.
//	info.KindOf(n)    // KindValue, KindVar (addressable), KindConst, KindType,
//	                  //   KindNoValue, KindBuiltin, KindFunc, KindPackage, ...
//	info.ValueOf(n)   // constant value (also folded comptime calls: TupleVal
//	                  //   for multi-result ones)
//	info.CallOf(call) // *Call, see below
//	info.SelectionOf(sel)  // *Selection: field (Indirect = through *T),
//	                       //   method, or enum variant (T.V)
//	info.InstanceOf(n)     // generic instantiation at an IndexExpr/NamedType
//	info.VariantOf(pat)    // enum variant matched by a constructor pattern
//
// # Calls
//
// Call.Kind is one of CallFunc (static call; Callee = func symbol,
// TypeArgs for generic instantiations), CallMethod (user method; Method,
// Recv expression, RecvType, Callee = method symbol, TypeArgs = receiver
// type arguments), CallBuiltinMeth (capability/resource method;
// Method.Builtin identifies it, e.g. ConnRead, ScopeAcquireSlot),
// CallBuiltin (Builtin = resolve.FLen, FCopy, FNewScope, FCodeArray, ...),
// CallConversion (Conv), CallVariant (Variant, VariantType) and CallValue
// (call through a func-typed value, including CancelFunc values).
// Call.Sig is the instantiated signature (nil for conversions, len/cap,
// panic, Code constructors).
//
// Effects: Call.Effect is the callee's mode (Comptime < Total < Driver):
// declared functions/methods their modifier, function values their type's
// effect (`driver func(...)` types are Driver), builtins Total except
// panic (Driver) and Code constructors (Comptime), capability methods Total
// except Exec.Run (Driver). Call.Folded marks comptime calls evaluated at
// compile time (all comptime calls outside comptime function bodies, and
// those in constant contexts inside them).
//
// Scope-checker hints, fixed by the universe tables:
//
//	Call.ResourceCtor   fresh Scoped/Linear results (S3 exemption): NewContext,
//	                    NewScope, Net.ListenTCP, Listener.Accept,
//	                    Scope.AcquireSlot, Context.WithTimeout, Signals.NotifyContext
//	Call.ConsumesRecv   Conn.Close, Listener.Close; calling a CancelFunc value
//	Call.ConsumesArgs   Scope.ReleaseSlot [0], Scope.Go [0] (the slot)
//
// Scope methods are typed per call: AcquireSlot(ctx Context, pool *[N]T)
// (Slot[T], error) with TypeArgs [ConstArg N, T]; ReleaseSlot(Slot[T]);
// Go(Slot[T], driver func(*T)) with TypeArgs [T]. Codegen maps them to
// fincrt.AcquireSlot(conns, ctx, pool[:]) etc. (spec 9.5).
//
// # Functions and the call graph
//
// info.Funcs lists a *FuncInfo for every function, method and function
// literal (declarations in package/source order, each followed by its
// literals depth-first); info.FuncOf(decl or lit), info.FuncOfSym(sym),
// info.Main. A FuncInfo carries Effect, Sig, Method/RecvSym, Params,
// TypeParams, Decreases (measure expression), Body, Lits, Captures
// (literals: outer locals referenced, in first-use order) and Calls: every
// CallExpr in the body in source order, excluding calls inside nested
// literals (those are in the literal's FuncInfo; Outer links back). Call
// graph edges for SCCs: for each fi.Calls entry whose Call.Kind is
// CallFunc or CallMethod, the callee is info.FuncOfSym(call.Callee) (method
// calls are resolved by receiver type). Calls in package-level
// initializers belong to no FuncInfo.
//
// # Types and classes
//
// Types: *Basic (Typ[U8] etc.; byte is u8), *Named (declared, predeclared
// like Duration/Conn/Scope, or an instance such as Vec[4, u32] /
// Slot[[4096]u8]; instances are canonical pointers with Origin/TypeArgs),
// *Array (Len, or LenParam inside generic code), *Slice, *Pointer,
// *Struct, *Enum, *Signature, *Code (Level), *TypeParam (Const != nil for
// [N usize]), *ConstArg (constant type argument), *Tuple, *Opaque
// (underlying type of predeclared resources). Compare with Identical.
//
// ClassOf(t) gives Value / Scoped / Linear (spec 4.1): pointers, function
// values and Net/Signals/Clock/Context/Scope/FS/Env/Stdio/Exec are Scoped;
// Listener/Conn/Slot[T]/CancelFunc are Linear; arrays/slices inherit the
// element class; structs/enums take the maximum over fields (a struct
// with a pointer field is Scoped). Type parameters are Value: type
// arguments must be Value types.
//
// # Generics
//
// Generic functions must be instantiated explicitly (id[u64](x)). Every
// distinct instantiation (functions and named types, including those with
// type-parameter arguments inside generic code, flagged Generic) is listed
// once in info.InstanceList in first-use order. Methods of generic types
// are instantiated through their receiver: Call.TypeArgs and Call.Sig;
// Method.RecvTypeParams are the method's names for the receiver type's
// parameters. Const parameters ([N usize]) are values of their constraint
// type inside the body and must be monomorphized by codegen.
//
// # Errors reported here
//
// E2001 (unknown field/method/variant), E2002, E2003, E2004 (nil is only
// valid for error and slices), E2005 (uninitialized Scoped local or Scoped
// struct field), E2010 (Code level mismatch, non-constant levels), E2011
// (operators/conversions on type-parameter values), E3001 (package-level
// variable of Scoped type, including pointers; reported instead of
// E2005/E2004), E4001 (comptime fuel / depth / evaluation cycle, reported
// at the evaluation root), E4003 (non-constant or negative array lengths,
// constant index out of bounds, comptime index errors), E5002 (main must
// be `driver func main(<distinct subset of Net, Signals, Clock, FS, Env,
// Stdio, Exec>) error`). The scope and totality checkers must not
// re-report E3001.
package types
