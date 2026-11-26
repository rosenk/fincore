// Package scope is the finc0 Layer-3 scope/depth escape and linearity
// checker (spec section 7, rules S2-S5; S1/E3001 is reported by the type
// checker). It runs on a well-typed program (types.Info) and reports
// E3002 (SCOPE_ESCAPE), E3003 (USE_AFTER_MOVE), E3004 (DISCARDED_LINEAR)
// and E3005 (LINEAR_RESOURCE_LEAK).
//
// # Depths and origins (S2-S4)
//
// Depths are numbered internally as: 0 = package level, 1 = "the caller"
// of a declared function, 2 = the body of a declared function (spec
// depth 1, e.g. main's body). Every `scope s := ... { }` body and every
// function literal body is one level deeper than its context. Every
// variable has the depth of the block level that declares it.
//
// Every expression of a tracked type (Scoped/Linear, pointers, function
// values, and Value types that contain slices: a slice is a view of some
// storage) has an origin: the depth of the storage it may refer to, plus an
// optional region. Parameters of declared functions refer to the caller's
// storage: their origin is (1, region = the parameter). Two parameters
// have incomparable regions, so a function cannot store one parameter into
// storage reached through another (`p.f = q`), but may store a value of
// region p back into p's storage or return either (both outlive the
// call). Parameters of a function literal have origin (litDepth-1, region).
//
//	origin(&x)          = storage(x)   (x local: depth(x); global: 0;
//	                                    p.f / p[i] / *p through a pointer: origin(p))
//	origin(a[lo:hi])    = storage(a) for arrays, origin(a) for slices/pointers
//	origin(x.f), x[i], *p = origin(x) / origin(p)
//	origin(call)        = join of the origins of the tracked receiver and
//	                      arguments (resource constructors included: their
//	                      fresh results are tied only to their arguments)
//	origin(T{...})      = join of the field values
//	origin(func lit)    = join of the captured tracked variables
//	origin(local x)     = join of every value assigned to x (a flow-
//	                      insensitive fixpoint), or depth(x) if x's address
//	                      is taken (&x, x[:] on an array, &x.f ...)
//	scope variable s    = depth of the scope body
//
// S2: `lhs = rhs` requires origin(rhs) to fit storage(lhs): a strictly
// smaller depth, or the same depth and (storage has no region or the same
// region). This also covers writes from a closure to a captured outer
// variable (decision 7) and writes through pointer receivers (S3's
// second sentence). S3: `return e` requires depth(origin(e)) < depth of
// the function body. S4: a func literal passed to s.Go may capture Value
// variables, tracked variables whose origin fits origin(s), and Linear
// variables, which are moved into the task.
//
// # Linearity (S5, E3003, E3004)
//
// Every Linear variable (locals, parameters, captured variables of a
// function literal) is tracked by a forward dataflow analysis over the
// structured control flow of each function body. The abstract state of a
// variable is a set of possible states {Absent, Live, Pending(err), Moved,
// Consumed, DeferredClose, Borrowed}; joins are unions, loops iterate to a
// fixpoint. Pending(err) is a Live value bound together with an error
// variable from a `(Linear, error)` result (decision 3): the branches of
// `if err != nil` / `if err == nil` refine it to Absent / Live, and any
// reassignment of err makes it Live.
//
//   - Consuming: Conn.Close, Listener.Close, calling a CancelFunc
//     (Call.ConsumesRecv), Scope.ReleaseSlot / Scope.Go (ConsumesArgs),
//     passing to a driver func/method/value parameter (owned, decision
//     1), `return x`, `y := x`, storing into a struct literal, capture by
//     a func literal passed to Scope.Go. Inside `defer`, consumption makes
//     the variable DeferredClose: it stays usable and may be closed again
//     explicitly (tcp_echo's `_ = ln.Close()` after `defer ln.Close()`).
//   - Borrowing: method calls on the receiver, passing to a total func,
//     any other read. A borrow of a possibly Moved/Consumed variable is
//     E3003, as is consuming it again.
//   - Linear parameters of total functions, receivers, captures of func
//     literals not passed to Scope.Go and match bindings are Borrowed:
//     consuming them is E3003; they carry no obligation.
//   - Leaving a block (fallthrough, break, continue, return) while a
//     variable declared in it may be Live or Pending is E3005, reported once
//     per variable at its binding site (decision 2). A panic(...) statement
//     ends the path without checks.
//   - `_ = x`, `_` in `:=`/`=` and expression statements discarding a
//     Linear value are E3004 (the value counts as consumed afterwards).
package scope
