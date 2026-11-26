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
