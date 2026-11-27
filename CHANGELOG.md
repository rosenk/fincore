# Changelog

## Unreleased (v0.3)

Language v0.3, implemented only in the self-hosted compiler (`compiler/`,
`finc version` prints `language v0.3`); finc0 stays frozen at v0.2. Details
in [docs/v0_3.md](docs/v0_3.md), decisions 140–155 in
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
  arm, so multi-value arms are separated by `;` or newlines.

### Compiler and tooling

- `LangVersion` 0.3; `finc test` runs the `// SINCE: v0.3` tests.
- Totality: `min`/`max` of loop-invariant operands are loop-invariant
  bounds.
- `make diff-prev` now `go vet`s the emitted Go of programs only the
  current compiler builds and runs those with a `.out` file. Mutants in
  `make diff-prev-fuzz` inherit their source's `// SINCE:` version.
- Fixed: a string constant pattern on a `[]byte` scrutinee was accepted
  by the v0.2 checker but produced Go that did not compile.
