# Changelog

Each `## vX.Y.Z` section is the release notes of that tag (the release
workflow publishes it; docs/release.md).

## Unreleased

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
