# Changelog

Each `## vX.Y.Z` section is the release notes of that tag (the release
workflow publishes it; docs/release.md).

## v0.3.0

Language v0.3, implemented only in the self-hosted compiler (`compiler/`,
`finc version` prints `language v0.3`); finc0 stays frozen at v0.2. Details
in [docs/v0_3.md](docs/v0_3.md), decisions 140–157 in
[docs/semantics_decisions.md](docs/semantics_decisions.md).

### Language

- **`min` and `max`** are predeclared: `min(x, y, ...)` / `max(...)` with
  two or more operands of one ordered type (integers, strings), typed like
  comparison operands. All-constant calls are constants
  (`const Lo = min(7, 3)`, array lengths). Shadowable like `len`: programs
  that declare their own `min`/`max` keep working.
- **`match` on `[]byte`**: string literals and string constants are
  patterns for `[]byte` positions (compared by bytes) in every pattern
  position and in `is`; strings keep their v0.2 rules. Lowered to
  `switch string(b)` / `string(path) == "..."`.
- **Multi-value `match` expressions**:
  `return match v { Int(h, l) -> h, l, true; _ -> 0, 0, false }`,
  `a, b := match ...`, `a, b = match ...`. Every arm gives the same number
  of values (a value list, a multi-result call or a nested multi-value
  match); untyped values take the result/variable types. A `,` after an
  arm value continues the value list unless the rest of the arm is another
  arm, so multi-value arms are separated by `;` or newlines. As the only
  argument of a call a multi-value match supplies one argument per value,
  like a multi-result call, typed by the parameters like assignment
  targets (`add(match n { 0 -> 1, 2; _ -> 3, 4 })` with `u32` parameters).

### Compiler and tooling

- `LangVersion` 0.3; `finc test` runs the `// SINCE: v0.3` tests.
- compiler/ is formatted with `finc fmt` (like std/ and examples/), and
  `make fmt-fc-check` (CI) fails if a file of compiler/, std/ or
  examples/ is not formatted.
- The TCP echo integration tests listen on ports picked by the kernel
  (`127.0.0.1:0`, the program reports its address) instead of fixed
  ports 18101–18104, so concurrent runs of the suite no longer collide.
- Totality: `min`/`max` of loop-invariant operands are loop-invariant
  bounds.
- `make diff-prev` now `go vet`s the emitted Go of programs only the
  current compiler builds and runs those with a `.out` file. Mutants in
  `make diff-prev-fuzz` inherit their source's `// SINCE:` version.
- `make diff-check-totality`/`diff-check-all` and `make diff-prev`: the
  compiler/ and std/ mutants now see the std/ packages (symlinked into
  their temporary trees). Before, every compiler/ mutant stopped at E2001
  `cannot find package "std/..."` in both compilers and never reached the
  totality checker.
- Fixed: a string constant pattern on a `[]byte` scrutinee was accepted
  by the v0.2 checker but produced Go that did not compile.
- Fixed: a variant constructor whose only argument has several values,
  `Shape.Rect(pair())` (accepted by the v0.2 checker, but its Go did not
  compile) or `Shape.Rect(match ...)`, is lowered like a call `f(g())`
  (decision 153). A folded comptime multi-result call as a variant
  argument or as a multi-value match arm (`Shape.Rect(cpair())`,
  `0 -> cpair()`) no longer stops the code generator with `multi-value
  constant in single-value context`.
- `ErrReported` (decision 156): a predeclared sentinel error meaning "the
  failure has already been reported". A driver `main` returning it exits
  with status 1 without printing; any other error still prints
  `error: <err>`. The generated Go `main` is now
  `fincrt.MainExit(_fc_main(...))`. compiler/ itself switches to it (and
  loses its trailing `error: fincore: invalid number` line) once the seed
  is v0.3.0.
- The driver calls itself `finc` (`finc help`, `finc version` prints
  `finc language v0.3`, `finc: ...` messages).
- Clearer messages, same codes and positions (decision 157): `c >>= 1`
  and the other compound assignments Go has and FinCore lacks, unary `^`,
  `var a, b T`, and E3002 between two parameters at the same depth (states
  decision 44 instead of "(depth 0) ... (depth 0), which outlives it").
  The `--context` hint of a slice-shrink E4002 explains that `n+1` can
  wrap to 0.
- `make diff-check*` and `make diff-prev` allowlist such rewordings by
  rule, `E#### "old text" => "new text" # reason` (scripts/diagallow;
  scripts/diffcheck/allow.txt, scripts/diffprev/allow.txt).

### Tooling and release

- Release archives `fincore-<version>-<os>-<arch>.tar.gz` (`make dist`,
  `make dist-test`) and a GitHub release workflow; see docs/release.md.
- An installed `finc` finds the runtime and `std/` relative to its own
  executable (`Env.GetEnv("FINCRT_EXECUTABLE")` in fincrt). `FINCORE_ROOT`
  and `FINCROOT` still override.
- Bootstrap: finc0 is frozen at v0.2; the chain is finc0 → finc-seed
  (compiler/ at `v0.2.0`, bootstrap/SEED) → finc1 → finc2 → finc3.
  `make diff-prev` compares the current compiler/ with the seed. Tests
  of newer versions say `// SINCE: vX.Y`.

## v0.2.0

Language v0.2 (docs/v0_2.md; decisions 58–138 in
docs/semantics_decisions.md), implemented in both finc0 (Go) and the
self-hosted compiler/. This tag is the bootstrap seed: finc0 is frozen
at v0.2 after it.

Language:

- **Standard library `std/`**: `vec`, `maps`, `bytes`, `strconv`, `path`,
  `sort`. They are total, bounded and allocation-free (docs/stdlib.md).
  `import "std/..."` resolves against `$FINCROOT` when it is set.
- **`formatTo(dst, format, args...)`**: a predeclared builtin for
  formatting with compile-time checks (`%d %x %s %q %c %v`, width and
  flags, truncation). Types can format themselves with a static `Format`
  method. It runs in comptime too, and Go's `fmt` is never used at run
  time.
- **Sum types**: exhaustive `match` statements and expressions, or-patterns
  and a compact enum layout.
- **Ergonomics**: auto-`&` for pointer-receiver method calls, constant
  strings as `[]byte`, and constant string `+`.
- **Patterns**: nested, tuple and rest patterns, binding or-patterns,
  guards, constants in every position, `name @ pattern` bindings, and
  `is` tests with flow-scoped bindings (`&&` chains, if/else outcomes,
  let-else, `for` conditions, `T(A || B)` / `F(A && B)` intersections).
  Exhaustiveness and reachability are usefulness-based and report
  witnesses (E2012).
- Assignability follows Go: basic types count as named.

Compiler and runtime:

- compiler/ uses v0.2 itself (std containers, `formatTo` diagnostics,
  sum types, patterns), and the bootstrap still reaches its fixed point
  (stage2.go == stage3.go).
- fincrt: the `Fmt` writer for `formatTo`. `strBytes` returns a writable
  copy (decision 90), so writing to it no longer crashes.
- Fixes: a soundness bug in the let-else check, and the conversion of
  untyped `match` results.

Since the M0–M8 milestones (v0.1): parser, checker (types, comptime,
scope/linearity, totality), the fincrt runtime, deterministic Go codegen,
`tcp_echo.fc` end to end, and the self-hosted compiler with the
fixed-point bootstrap gate.
