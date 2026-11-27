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

## v0.2 additions

### Sum types (enums and `match`) — details and grammar in docs/v0_2.md, "Sum types"
58. New code **E2012 NON_EXHAUSTIVE_MATCH**, also used for unreachable match arms. Messages: `non-exhaustive match on T: missing V1, V2` (variants in declaration order, at `match`), `non-exhaustive match expression on T: missing '_' arm` (at `match`), `unreachable match arm: it follows the '_' arm`, `unreachable pattern: variant V is already covered`, `unreachable pattern: duplicate case X`, `unreachable '_' arm: all variants of T are covered` (at the pattern or or-pattern alternative).
59. Exhaustiveness: a `match` on an enum without a `_` arm must name every variant (or-pattern alternatives count). Other scrutinee types (integers, bytes, bool, string): statements need not be exhaustive (Go-like); match expressions need a `_` arm (`true | false` does not count). Coverage is checked only when every pattern is valid. For decision 37 a match on an enum without `_` counts as exhaustive even when E2012 is reported (no cascading E2003).
60. Reachability (E2012): any arm after `_`; a variant named twice (also within one or-pattern, or qualified and unqualified); a constant pattern equal by value to an earlier one (`1 | K` with `K = 1`); `_` after all variants are covered.
61. Or-patterns `P1 | P2 | ...` in statement and expression arms. Alternatives may not be `_` and may not bind names other than `_` (E2002); a payload variant is written `V(_, _)`. Alternatives declare no symbols. Codegen: one `case a, b:` list.
62. Unqualified variant patterns: if the scrutinee is an enum, an unqualified pattern name that is one of its variants is that variant, even when a constant of that name is in scope; a constant pattern on an enum scrutinee is E2002 (`constant K cannot be a pattern for enum type T`). `Type.Variant` stays valid; a generic enum's name qualifies patterns on its instances (`Option.Some(x)` on `Option[u32]`). Patterns of imported enums must be unqualified (`pkg.Type.Variant` is not in the grammar). Construction stays qualified.
63. `match` expressions (a `Primary`; a statement that starts with `match` is still a MatchStmt). Arms are expressions only (no blocks; no-value calls such as `panic(..)` are E2002) separated by newline, `;` or `,`; at least one arm. Arm types: typed arms must be identical (`match arm has type T, want U`, E2002, at the arm); untyped constant arms take the typed arms' type; if all arms are untyped the result is untyped (untyped int and char mix to untyped int) and its context converts every arm (overflow reported per arm). The result is a constant iff the scrutinee is constant and the selected arm is constant, so `const C = match Color.Red { ... }` works. In messages a match expression prints as `match expression` (exprString) or on one line (ast.Format).
64. Scope/linearity: arms of a match expression are branches; payload bindings get the scrutinee's origin and are Borrowed (decision 46); an arm that is a Linear variable moves it (like an assignment's right-hand side); origin(match expr) = join of the arms.
65. Totality: match-expression arms are branches of the flow walker (bindings are fresh). Patterns give no facts: `match n { 0 -> 1; _ -> f(n-1) }` does not prove `n >= 1`; guard with `if n == 0 { return .. }`.
66. Comptime: match expressions and or-patterns are evaluated (first matching arm; no arm is an evaluation error).
67. Compact enum layout: `struct { _tag uint8|uint16|uint32; slots }` (<= 256, <= 65536, more variants). Payload slots are shared by Go type: groups in order of first appearance (variants in declaration order, payloads left to right); a group has as many slots as the most payloads of that type in one variant; a variant's payloads of a type use the group's slots in order. Slot j of a group is `_<key>_<j>`, key = FinCore spelling of a basic type (`u8`, `u32`, `usize`, `int`, `bool`, `string`, `error`, ...), else the Go type if it is an identifier (named types, type parameters, monomorphized names), else the group index in decimal (also when an earlier group already has the key). Constructors set the tag (omitted for variant 0) and their own slots, Go zeroes the rest, so `==` on enums is variant equality. Enums are comparable iff all payload types are.
68. Lowering: `switch x._tag` (a temporary when bindings are used and x is not a local variable); the last arm of an exhaustive match becomes `default`; unused bindings are not emitted. A match expression is an immediately invoked literal `func() T { switch .. { case ..: return arm } }()`, except `return match ...` in a single-result function outside `scope` blocks, which becomes a switch whose arms return (nested `match` arms likewise). A variant construction renders the layout, then the payloads, then the type (this fixes the discovery order of monomorphized instances).
69. Generic enums use Go generics: the layout is computed on the generic declaration (type parameters are slot groups of their own: `Option[T]` has `_T_0`; `Pair[A, B]` keeps `_A_0` and `_B_0` even for `Pair[u32, u32]`). Enums with const parameters are monomorphized and laid out per instance.
70. Enums are Value iff all payloads are Value, may be stored in fixed arrays and structs and are copied by value. An enum containing itself by value (directly, through structs/arrays or generic instances) is E2002 `invalid recursive type E (contains itself by value)`; use an ID into an arena.
71. compiler/: the type checker part for sum types is check_match.fc, the codegen part codegen_enum.fc; 35 Parser methods carry decreases and the Parser SCC has 33 members (new `matchExpr`, `patternAlt`; decision 57). No compliance changes were needed in compiler/ (it uses no `match`).
