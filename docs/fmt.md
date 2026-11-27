# Tooling: `finc fmt` and diagnostics with source context

Both are features of the current compiler (`compiler/`, `bin/finc1` and
later stages), not of the language: they do not change what a program
means, so `LangVersion` stays where it is (`compiler/main.fc`). finc0 is
frozen and has neither.

## `finc fmt`

```text
finc fmt [-w] [-l] [-d] [--check] <file.fc | dir>...
```

| Flag | Effect |
| :--- | :--- |
| (none) | print the formatted source to stdout |
| `-w` | rewrite the files whose formatting differs |
| `-l` | list the files whose formatting differs |
| `-d` | print a unified diff (`diff -u` through `/bin/sh`) for each such file |
| `--check` | list them and exit with status 1 if there is any |

A directory operand is formatted recursively: its `.fc` files in name
order, then its subdirectories (hidden ones are skipped). A file with a
syntax error is reported in the usual format and left alone; the command
then fails.

### How it works

The formatter re-lays out the token stream; it does not print the AST
back. Every token keeps its spelling, so literals, identifiers and the
choice between equivalent forms (`x is A | B`, a parenthesized result list,
grouped constants) stay as written. Line breaks come from the source: the
formatter decides indentation, spacing inside a line, alignment and blank
lines, and it splits the few constructs listed below. The AST is used to
classify tokens where the token alone is ambiguous: a binary versus a
unary `-`, `*` or `&`, a type prefix `[]T`/`*T`, the `{` of a struct
literal or of a match body, the `(` of a parameter list or a receiver, the
header of `if` and `for`.

### Canonical style

- **Indentation:** tabs. A line is indented one level more than the line
  that opened the innermost bracket (`(`, `[`, `{`) still open at its
  start; several brackets opened on one line count once (`foo(T{` indents
  its contents by one). A line that starts with closing brackets is
  indented like the line that opened the first of them. A statement that
  ends a line with an operator, `,`, `.`, `=`, `:=` or `->` continues on
  the next line with one extra level (as gofmt does: `if a &&` / `b {`,
  the body of the `if` is not indented twice).
- **Spacing:** gofmt's rules.
  - Binary operators follow gofmt's precedence-based spacing
    (go/printer's `binaryExpr`/`cutoff`): at the top level of an
    expression every operator has blanks, unless the expression mixes
    additive and multiplicative operators (`a*b + c`); inside call
    arguments when there are several, index expressions and nested
    operands, only operators below the additive level have blanks
    (`f(i+1, n)`, `a[i+1]`, `x == y+1`). Blanks are kept where dropping them
    would fuse tokens (`a / *p`, `a - -b`). FinCore's levels map onto Go's
    (`==`/`!=` and `<`..`>=` are both Go's comparison level).
  - A slice expression gets blanks around `:` when it is at the top level,
    has both indices and one of them is a binary expression
    (`s[i : j+1]`); otherwise none (`s[i:j]`).
  - One space after `,`, `;` and keywords, around `:=`, `=`, `+=`.., `->`,
    `@`, `|` (or-patterns) and `is`; none after `(`, `[`, unary operators,
    `...` and type prefixes, before `)`, `]`, `,`, `;`, `:`, `++`, `--`,
    around `.`, and between a name and its `(` or `[`.
  - `func (r T) m()` (receiver), `func f() (int, error)` (result list),
    `func(x int)` (literal or type), `decreases(n)`.
  - Struct literals: `T{a: 1, b: 2}`, `T{}`. Blocks on one line keep
    their spaces: `{ return x }`, empty ones are `{}`. Struct and enum
    types have a space before the brace: `struct { x int; y int }`,
    `struct {}`.
- **Line structure:**
  - explicit statement separators (`a; b`) become line breaks; `;` stays
    only in `for`/`if` headers and in one-line struct and enum types;
  - match arms go one per line: a match written on one line is split, and
    the `,`/`;` arm separators of a match expression are dropped (the
    newline separates the arms);
  - a trailing comma is never needed: a multi-line list whose closing
    bracket is on its own line already ends with `,` (the parser requires
    it), and lists are never joined or split.
- **Blank lines:** kept as written, at most one in a row; none at the start
  of the file; the file ends with exactly one newline.
- **Alignment:** gofmt's elastic tabstops (padding with spaces, one space
  minimum) over runs of consecutive lines with the same indentation that
  all have the cell:
  - the types of struct fields (`name    u8`);
  - in `const ( ... )` groups, the type and the `= value` of the specs
    (a run without types has no type column);
  - the values of `key: value` lines of a multi-line struct literal;
  - trailing `//` comments.

  A blank line, a comment-only line, a line without the cell or a change of
  indentation ends a run, as in gofmt. Unlike gofmt there is no size-ratio
  heuristic that breaks a run of very different key lengths.
- **Comments:** every comment is kept, with its text unchanged. A trailing
  comment stays at the end of its line (aligned, see above); a comment on
  its own line stays on its own line, indented like a statement at that
  point (inside a block before its `}`: like the block's contents); a block
  comment inside a line keeps one space on each side. A block comment that
  spans lines is copied verbatim and ends alignment for its line.

### Comments: the lexer's side table

The lexer skipped comments (`nextToken` in `compiler/lexer.fc` consumes
`//` to the end of the line and `/* ... */`, turning a block comment that
contains a newline into a virtual semicolon when one is due). The parser
never sees them. `finc fmt` sets `Lexer.keepComments`, and the lexer then
appends each comment to the package-level table `comments`
(`vec.Vec[MaxComments, Comment]`: byte span, line, column, end line, block
or line comment) in source order. The token stream is unchanged, so
`parse`, `check` and `build` behave exactly as before. The formatter merges
the two ordered streams by offset.

### Correctness checks

- **In the formatter:** every formatted file is lexed and parsed again, and
  its `--dump-ast` dump is compared with the original's, ignoring node IDs
  and positions. A difference, or a result that does not parse, is
  reported as an internal error and the file is not written.
  `FINC_FMT_DEBUG=1` prints the rejected output.
- **`make fmt-fc-check`** (`scripts/fmtcheck`) checks, in fresh processes,
  every `.fc` file of `compiler/`, `std/`, `examples/`, `testdata/`
  (pass, fail, integration, context), `scripts/` (diffcheck, diffbuild,
  diffprev cases) and the parser fixtures of `bootstrap/`:
  idempotence (`fmt(fmt(x)) == fmt(x)`) and AST equivalence
  (`finc parse --dump-ast` with IDs, positions and the path stripped).
  Files that do not parse (syntax-error fixtures) are skipped and counted.

Result at the time of writing: 181 files, 177 checked, 4 skipped
(deliberate syntax errors in `scripts/diffcheck/cases` and
`testdata/context/syntax.fc`), all 177 idempotent and AST-equivalent; 141
are already formatted, 36 would change.

### What is formatted in the repository

- `std/` and `examples/` are already in canonical form (`finc fmt -l`
  prints nothing).
- `compiler/`: `compiler/fmt.fc` is formatted. The other files are not
  reformatted yet: 18 of them would change, mostly one-line `match`
  expressions being split and alignment that was not quite gofmt's. Doing
  it in one sweep while several branches change compiler/ would only
  cause merge conflicts; `make fmt-fc` lists the files. A reformat changes
  line numbers in compiler/ only, not the emitted Go of any program, and
  `make bootstrap` checks it.
- `testdata/` and `scripts/*/cases` are left as they are: fail tests carry
  positional `// ERROR` markers, and the cases pin positions in
  diagnostics that the differential tests compare.

## Diagnostics with source context (`--context`)

`check`, `build`, `run` and `test` accept `--context`; the environment
variable `FINC_DIAG=context` turns it on for every command (and for the
compiler processes `finc test` starts). Each diagnostic line is then
followed by the source line, a caret span and, for the most common errors,
a one-line help:

```text
testdata/context/match.fc:14:2: error[E2012]: non-exhaustive match on Shape: missing Rect, Empty
   14 |     match s {
      |     ^^^^^^^^^
      = help: add arms for the missing cases, or a final `_ -> ...` arm
testdata/context/match.fc:29:13: error[E2002]: mismatched types u32 and u64 for operator ==
   29 |     return a+1 == b
      |                ^^
```

- The span is the range of the node the diagnostic is reported at (the
  checker records the node's end next to the position, `DiagList.eline`/
  `ecol`); at a binary operator it is the operator. A node that ends on a
  later line is underlined to the end of the code on the first line (a
  trailing `//` comment is not underlined). Syntax errors carry no range:
  one caret.
- Tabs are shown as four spaces, and the caret column counts the same way.
- Hints exist for E4002 (loops, slice-shrink loops and recursion
  separately), E4004, E3002 (stores into longer-lived storage and between
  parameters separately), E3005 and E2012 (non-exhaustive and unreachable
  separately).

The default output is unchanged, byte for byte: the spec 11 line only.
`make diff-check*`/`diff-build` (finc1 against finc0) and `make diff-prev`
(against the seed) run without the flag and stay identical.

`make context-test` runs `check --context` on `testdata/context/*.fc` and
compares stderr (only the diagnostics: since v0.3 a failing finc prints no
`error: fincore:` line, decision 156) with the `.out` file next to each
(`UPDATE=1` rewrites them). It also
checks that `FINC_DIAG=context` gives the same output and that the output
without the flag is exactly the diagnostic lines of the `.out` file. The
files are marked `// SINCE: v0.3`: they test behaviour that finc0 and the
seed do not have. No conformance runner reads `testdata/context/` (the
runners read `testdata/pass` and `testdata/fail`, the diff tools their own
lists).
