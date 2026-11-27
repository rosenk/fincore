# FinCore v0.1 — resolved spec ambiguities (binding for finc0 and compiler/)

The test corpus in testdata/ is written against these decisions. Conventions for tests: testdata/README.md.

1. **Linear parameters.** A Linear param of a `driver func` is OWNED by the callee (must be consumed on all paths; the caller's argument is moved). A Linear param of a total `func` is BORROWED (caller keeps ownership; callee must not consume it).
2. **E3005 location:** at the binding site of the leaked resource (`:=` line, or the func header line for a parameter).
3. **Error-path refinement:** for `(Linear, error)` results (ListenTCP, Accept, AcquireSlot, NotifyContext, NewContext...), the Linear value is not live on the `err != nil` path.
4. **comptime funcs** are not totality-checked; bounded by fuel. E4001 points at the evaluation root (e.g. the `const X = F(0)` line).
5. **E4004** reported once per SCC, at the first function in source order.
6. **E3001 beats E2005** for package-level `var g Conn`.
7. Assignment from a `conns.Go` closure to a captured outer variable is a write to that variable → S2 applies (E3002).
8. Plain `for` loops are totality-checked even inside `driver` funcs; only `driver for` is exempt.
9. Totality forms that must be accepted: `for i := usize(0); i < b; i++` / `i += k` / `i > 0; i -= k`; `for lo < hi decreases(hi - lo)` with `lo = mid + 1` / `hi = mid`; slice shrink with `if n == 0 { return ... }` before `rem = rem[n:]`.
10. No automatic `&` for pointer-receiver method calls (`ap := &arena; ap.Push(..)`).
11. Generics require explicit type args (`id[u64](42)`). Types with const params (`Vec[N usize, T any]`) are monomorphized in Go output.
12. Enums: `Shape.Circle(2)`, `Shape.Empty`; patterns `Shape.Rect(w, h)`, int literals, `_`. Expr arms are expressions only.
13. No array literals (grammar has none).
14. Go-like: untyped int consts, `:=` may redeclare `err` if ≥1 new var, `if init; cond`, errors compared with `==`, defer args evaluated at defer, `bytesToStr` copies.
15. Local imports resolve relative to the module root (cwd of finc); exported names are capitalised.
16. `main` params must be a subset of {Net, Signals, Clock, FS, Env, Stdio, Exec}; unused caps allowed.
17. `--race` requires cgo: finc0 sets CGO_ENABLED=1 for race builds only.
18. Runtime deviations: see docs/runtime_api.md (`ErrParse` sentinel is part of the universe).

## Parser decisions (M0)
19. Keywords: exactly the 24 listed in §2.3 (the "27" count is a typo). `Code`/`El` are predeclared identifiers, contextual keywords in type position.
20. Go-style: no `T{` composite literal at top level of `if/for/match/scope` headers; multi-line arg lists need trailing commas; semicolons may be omitted before `)`/`}`.
21. Match arm body must be an Expr or Block (`x -> pos++` is invalid; use `x -> { pos++ }`).
22. Variadic `xs ...T` allowed for the last parameter only.
23. No decimal literals with leading zero; ASCII identifiers only; compound assign needs exactly one value per side; `for` post clause cannot be `:=`.
24. AST nodes have dense post-order NodeIDs (shared IDAlloc across files); later passes annotate via side tables indexed by NodeID (see bootstrap/internal/ast/doc.go).

## Type checker decisions (M1)
25. Untyped int defaults to `int`; `len`/`cap` return `usize`; char literals are untyped, default `u8`.
26. Typed constant overflow is E2002; comptime arithmetic wraps to the static type. No string `+`.
27. Package-level `var` initializers must be constant expressions (struct literals of consts and payload-less variants count).
28. Comptime calls outside comptime bodies are folded and need constant args; const initializers may call only comptime funcs.
29. Fuel: fresh budget per evaluation root; call/iteration/index cost 1; recursion cap 10_000 frames (E4001); E4003 index OOB; E5001 for driver/panic in comptime.
30. Code constructors are level-polymorphic; CodeUniv(n) needs const n; bare `Code` is E2010. Extension: `CodeField(name, c) FieldCode`. CodeEmpty → `enum{}`, CodeUnit → `struct{}`.
31. Type params range over Value types only; operators/conversions on type-param values are E2011; no method type params.
32. Value-receiver methods callable on `*T`; pointer-receiver methods need `*T`; no method values; no `&T{}`.
33. nil: pointer/Scoped → E2004; other non-error/non-slice → E2002; `x := nil` → E2004.
34. Missing Scoped field in struct literal / uninitialized Scoped local (incl. pointers) → E2005.
35. Total func value assignable to `driver func` type, not vice versa.
36. All capability methods are Total except `Exec.Run` (Driver); `panic` is Driver.
37. Missing return is E2003 (Go terminating-statement rules; exhaustive `match` terminates).
38. Expression statements / defer / expr match arms must be calls.
39. Redeclaration E2001; `:=` without new var and stray break/continue E2002; `decreases` measure assignable to usize.
40. `main` checked (E5002) only when the root package is `main`; capabilities distinct; no variadics.
41. Imports: clean relative paths; local name = package clause name unless aliased.
42. `Clock` has no methods; fincrt needs a `Clock` type for main injection.

## Scope checker decisions (M2a)
43. Slices are tracked by where their memory lives: slices of global arrays are depth 0; `buf[:]` of a local carries the local's depth (`return buf[:]` → E3002).
44. Each parameter is its own region at caller level: returning a parameter (or derived value) is allowed; storing one param into memory reached via another (`p.f = q`) or `&local` through a receiver is E3002.
45. Resource-constructor calls tie the result only to their arguments (this is how the S3 exemption extends to user code).
46. Consuming a borrowed Linear (total-func param, receiver, match binding, non-Go literal capture) is E3003.
47. After `defer x.Close()` / `defer cancel()`, explicit closes and later uses remain allowed; double close without defer is E3003.
48. Immediately-called literals are summarised (consume / borrow / E3005 if closed on some paths only); stored literals only borrow Linear captures.
49. A Linear struct is tracked as one unit. E3004 covers `_ = x`, `_` in `:=`/`=`, Linear-valued expression statements and match-arm expressions.
50. Error-path refinement recognises only bare `err != nil` / `err == nil`. `panic` ends a path without leak checks.
51. Pass order: scope, then totality; the pipeline stops at the first pass reporting errors.

## Totality checker decisions (M2b) — see bootstrap/internal/totality/doc.go for exact accepted shapes
52. Missing measure: E4002 at the func (self-recursion) / E4004 once per SCC (mutual). Non-decreasing in-SCC call: E4002/E4004 at the call. Bad measure (not an unassigned unsigned param) at the `decreases` clause.
53. Loops with `decreases` → T3 only; `len(s) > 0` loops → T2 only; others → T1.
54. T1: `<=`/`>=` only with overflow-safe constant bound; step > 1 needs constant/len/cap bound; `i > 0; i -= 2` on unsigned rejected. Bounds reached through pointers/globals are invalidated by any memory-writing call — copy to a local first (`lim := p.ntok`).
55. T3 loops accept `x++`/`x--`/midpoint `a + (b-a)/k` updates; `(lo+hi)/2` rejected (overflow).
56. Calls through function values add edges to every escaping total func; inside a cycle they are rejected.
57. compiler/: 33 Parser methods carry decreases; the Parser SCC has 31 members.

## v0.2 additions — see docs/v0_2.md and docs/stdlib.md

58. **Standard library imports.** An import path that is `std` or starts with `std/` resolves to `$FINCROOT/<path>` when the environment variable `FINCROOT` is set and non-empty (a relative value is relative to the working directory), otherwise to `<moduleRoot>/<path>` like every local import (15, 41). The directory is joined and cleaned lexically; files of FINCROOT packages are named `$FINCROOT/<path>/<file>.fc` in diagnostics (never made relative to the working directory, so finc0 and finc1 print the same names). A missing package gives the usual E2001 messages naming that directory. `FINCORE_ROOT` (the fincore Go module used by `build`) is unrelated. API reference: docs/stdlib.md.
59. **std conventions.** std packages are ordinary packages: every function Total (T1-T3), no capabilities, no `driver`, no panic. Capacities are const type parameters. Overflow is reported by a `false`/`ok` result and, for arena-like types, a sticky flag (`Full`, `Truncated`), never by growing or panicking. IDs and run starts handed out by std containers start at 1 (0 = none / empty run). Hash tables store entries densely in insertion order and buckets hold entry numbers only, so iteration order is independent of hashing. `bytes.Builder` writes are all-or-nothing. Keys are concrete types (`U64Map`, `StrMap`, `Interner`, caller-keyed `Table`) because operators on type parameters are E2011 (31).
60. **Const type parameters are invariant T1 bounds.** In `for i := usize(0); i < N; i++` (also `i < N*2`, `for i := N; i > 0; i--`) a const type parameter `N` counts as a constant of every instance, like a named constant. The other T1 rules are unchanged: `<=`/`>=` and steps > 1 still need a constant (not a parameter) or a len/cap bound.
61. **Function types need parameter names** (grammar: `ParamDecl = IDENT {"," IDENT} Type`): write `less func(a T, b T) bool`, not `func(T, T) bool`. Function values (named functions, literals, variables) may be passed to generic functions; by 56 the call through the value adds edges to every escaping total function, so a comparison function that (transitively) calls the sort it is passed to closes an unmeasured cycle (E4004).
62. **`formatTo(dst []byte, format string, args...) usize`** is a predeclared builtin (universe, inserted after `panic`; shadowable like `len`). It writes the formatted text into `dst` from index 0, silently truncates at `len(dst)` and returns the number of bytes written (always `<= len(dst)`); the written bytes are always a prefix of the untruncated output. It is Total: no allocation, no panic. Like `copy` it may be used as an expression statement.
63. **Format string.** `format` must be a constant expression assignable to `string`, else E2002 at the format argument (`format argument f to formatTo must be a constant string`). A directive is `%` [flags] [width] verb: flags are any sequence of `-` (left-justify) and `0` (zero pad; ignored together with `-`), width is a decimal number `<= 64`, verbs are `d x s c q v`; `%%` is a literal `%` and takes no flags or width. Malformed directives — incomplete at the end of the format, unknown verb, `%5%`, width > 64, flag `0` with a verb other than `d`/`x` — are E2002 at the format argument (`invalid format for formatTo: ...`, first one only; the arguments are still checked and default-typed). Width pads with spaces on the left (right-justify) or, with `-`, on the right; with `0`, numbers are padded with `0` after the sign. Widths count bytes. A flag without width has no effect.
64. **Arguments.** The number of arguments after `format` must equal the number of directives: E2003 (ARITY_MISMATCH, consistent with every other builtin) at the format argument; fewer than 2 arguments in total is E2003 at the call. Each argument must be of class Value (E2002 `cannot format x (type T): formatTo arguments must be Value types`) and accepted by its verb (E2002 `cannot use x (type T) as argument for %d in formatTo`); both are reported at the argument, and every argument is checked. Untyped constants get their default type (decision 25), for `%c` they are converted to `u8`; failures of that conversion are the usual E2002 (overflow, wrong kind) / E2004 (nil).
65. **Verbs.** `%d`: any integer (underlying integer type, incl. named types like `Duration`, `Celsius i32`), decimal. `%x`: integers, lowercase hex without prefix; negative values print `-` and the hex magnitude. `%s`: underlying `string` or `[]u8` (not `[]T` for a named `T` such as `[]Signal`), raw bytes. `%c`: underlying `u8`, one raw byte. `%q`: `string`/`[]u8`, double-quoted with `\n \r \t \\ \"` and `\xHH` (lowercase) for every other byte `< 0x20`, `0x7f` and every byte `>= 0x80`; the result is always a valid FinCore and Go string literal. `%v`: a user named type (or generic instance) that has a method named `Format` is printed by that method; otherwise `bool` (`true`/`false`), integers (decimal), `string`/`[]u8` (raw). The method must be a Total, value-receiver, non-variadic `Format(dst []byte) usize` (exactly these types), else E2002 at the argument — there is no fallback to the underlying type. Resolution is static (no dynamic dispatch); a struct field named `Format` is not a method.
66. **Format methods.** A Format method receives the unwritten rest of `dst` (`len == cap ==` remaining room) and must obey the same contract (write from index 0, return the count; a larger result is clamped). With a width it is padded according to the returned count. When right-justifying into less room than the width, the method writes into a width-byte scratch buffer instead, so the padding (and therefore the truncated prefix) is computed from the untruncated text. The method is called exactly once per directive.
67. **Evaluation order.** `dst`, `format` and all arguments (including Format receivers, which are copied) are evaluated left to right before anything is written. If a `[]byte` argument, or memory reachable from a Format receiver, aliases `dst`, the output is unspecified.
68. **Totality.** A `%v` directive resolved to a Format method is a call edge from the calling function to that method: it takes part in SCC / mutual-recursion detection exactly like a direct call (a Format method formatting its own type with `%v` is E4002 at the method; `A.Format` <-> `B.Format` is E4004 once, at the first member). A Format method has no unsigned parameter, so it can never carry a valid measure. For loop invariance (decision 54), formatTo writes memory like `copy` into `dst`, and with a Format method it counts as a user call.
69. **Scope.** `dst` is only borrowed; formatTo retains nothing and its result (a byte count) has no origin.
70. **Comptime.** formatTo is supported inside comptime functions with identical semantics (finc0 runs the runtime's `fincrt.Fmt`, finc1 a port of it); every formatted argument costs one unit of fuel, a Format method is interpreted like any other call (one more unit, depth-bounded).
71. **Codegen.** Each call is expanded at compile time into an immediately called Go function literal over `fincrt.Fmt` (docs/runtime_api.md); Go's `fmt` is never used at run time. finc0 and finc1 emit it byte for byte identically.
