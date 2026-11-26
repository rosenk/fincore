// Package totality is the finc0 Layer-2 effect and totality checker
// (spec 6, milestone M2): E5001 (effect containment), E4002 (unproven
// termination of a loop or direct recursion) and E4004 (mutually recursive
// group without a decreasing measure). It runs after the type checker
// (check.Passes) on a well-typed program and only reads types.Info.
//
// # Effects (E5001)
//
// Comptime < Total < Driver. In a total or comptime function (or total
// function literal) every call whose Call.Effect is Driver is E5001, at
// the call: driver functions and methods, calls of `driver func` values
// (including driver literals), Exec.Run and panic. `driver for` in a total
// or comptime function is E5001 at the loop. Creating a driver literal
// inside a total function is allowed; calling it is the violation. A
// literal has its own mode: a total literal inside a driver function is
// checked, a driver literal inside a total function is not.
//
// # Loops (T1, T2, T3, T4)
//
// Every plain `for` in total and driver functions and in all non-comptime
// literals is checked (decision 8); `driver for` is exempt; comptime
// functions are fuel-bounded and not checked (decision 4). A loop with
// `decreases(M)` is checked by T3 only; a loop whose condition is
// len(s) > 0 by T2; any other loop by T1. Failure is E4002 at `for`.
//
// T1 counting loops: `for [init]; C; P { body }` where
//   - P is i++, i += c (counting up) or i--, i -= c (down), c a positive
//     constant, i an integer local of this function that is never
//     addressed (&i) or written by a function literal;
//   - init is absent, `i := e` or `i = e` (e arbitrary: usize(0), l.off,
//     ...);
//   - C is i < B or B > i (up), i > B or B < i (down). i <= B / i >= B are
//     accepted only for a constant B where i + c (i - c) cannot overflow
//     the counter's type. With step c > 1 and a strict comparison the bound
//     must be a constant with B + c - 1 <= max (B - c + 1 >= min), or, for
//     a 64-bit unsigned counter counting up, a len/cap expression;
//   - the body writes no storage rooted at i (i = .., i++, ...);
//   - B is loop-invariant: built from constants, arithmetic, identity
//     conversions, len/cap and access paths x.f.g, and no write in the body
//     or post statement may change it (see invariant.go): a local atom
//     (non-addressed local/param, no pointer on the path) only conflicts
//     with writes rooted at the same variable on an overlapping field
//     path (element writes s[i] = v keep len(s)); a heap atom (global,
//     pointer indirection, addressed or captured root) additionally
//     conflicts with every call that may write memory (user functions,
//     methods, function values, capability methods) and with writes that
//     may alias it by type or field name. Copy fields to locals before
//     loops that call methods (`lim := p.ntok; for g := usize(0); g < lim;
//     g++ { ... }`).
//
// break, continue and return are allowed anywhere in a T1 body.
//
// T2 slice shrink: `for [rem := e | rem = e]; len(rem) > 0; { body }`
// (also len(rem) != 0, 0 < len(rem), len(rem) >= 1; no post statement)
// where rem is a slice local of this function that is never addressed or
// written by a literal, and
//   - every assignment to rem in the body (including nested loops) is
//     rem = rem[k:], rem = rem[k:len(rem)] or rem = rem[:len(rem)-k]
//     (rem[0:len(rem)-k]) for some k: rem is never lengthened;
//   - on every path to the end of the body and to every `continue` of the
//     loop, such an assignment happened with k > 0 proven at that point: k
//     a positive constant, or a variable with a proven lower bound >= 1.
//     Lower bounds come from conditions (n != 0 for unsigned n, n > c,
//     n >= c, n == c, and negations: `if n == 0 { return/break/continue }`,
//     `if n > 0 { .. } else { .. }`, `&&`/`||`), hold until n is assigned,
//     and are intersected at joins. Shrinks inside nested loops do not
//     count (they may run zero times).
//
// T3 loops: `for [init;] [C] [; P] decreases(M)` where M is X or X - Y and
// each leaf is a constant, len(s) of a local slice s not written in the
// loop, or a non-addressed local integer variable of this function. Every
// path to the end of an iteration (end of body followed by P, and every
// continue) must prove X' <= X and Y' >= Y with at least one strict, and C
// must imply Y <= X (for M = X, X must be unsigned), so M is a natural
// number that strictly decreases. The proof uses bounds of variables
// relative to the iteration-start values (terms) of the measure variables:
//   - C and branch conditions add x < y, x <= y, x == y, x != 0 facts;
//   - x + 1 gets bounds only if x < t is known for a term t (no overflow);
//     x - 1 only if x > t is known (no underflow);
//   - a + (b - a) / k (k >= 2) and a + ((b - a) >> k) (k >= 1) with a < b
//     known satisfy a <= r < b (binary search midpoint). (lo + hi) / 2 is
//     not accepted (it may overflow);
//   - after each write of a measure variable the checker records whether
//     M is already proven smaller; joins keep that flag only if it holds
//     on both sides, so `if .. { lo = mid + 1 } else { hi = mid }` passes.
//
// # Recursion (T3, T4)
//
// The call graph has one node per total function, method and literal.
// Edges: static calls (methods resolved by receiver type via Call.Callee)
// to total functions, and for every call through a total function value an
// edge to every escaping total function (all literals and every declared
// function used other than as a callee). Tarjan's algorithm yields the
// SCCs; for every SCC with more than one member or a self edge:
//   - every member needs `decreases(d)`, else E4002 at the function (single
//     self-recursive function) or E4004 once, at the first member in source
//     order (decision 5);
//   - d must be an unsigned, non-variadic parameter (not the receiver) that
//     the function never assigns, addresses or lets a literal write;
//   - every call to a member inside the SCC must pass, at the callee's
//     measure position, d - c (c >= 1) with d >= c proven at the call
//     (`if d == 0 { return ... }` before it gives d >= 1), or d / c (c >= 2)
//     or d >> c (c >= 1) with d >= 1 proven. Facts come from the same
//     lower-bound analysis as T2 (including `d > 0 && f(d-1)`). Violations
//     are E4002 (self recursion) or E4004 (mutual recursion) at the call;
//     calls through function values inside an SCC are always rejected.
//
// Entry calls from outside the SCC may pass any value (ParseDepth).
//
// # Known limits
//
// Measures of recursive functions must be parameters; T3 loop measures
// must be local variables, constants and len(s) of locals (no fields).
// Integer overflow of the update expressions is excluded only for the
// forms listed above.
package totality
