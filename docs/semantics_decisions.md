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
