# FinCore conformance corpus (`testdata/`)

Conformance tests from spec section 13 ([fincore_language_specification_v0_2.md](../fincore_language_specification_v0_2.md)). Every compiler must pass every test
of its language version: `finc0` (frozen at v0.2) and the self-hosted
`finc1`/`finc2` (the version in `compiler/main.fc`, `LangVersion`). Tests of
newer versions are marked `// SINCE: vX.Y` and skipped by older compilers.
Three runners implement the conventions below:
`testdata/integration/corpus_test.go` (the reference implementation),
`finc0 test` (`bootstrap/cmd/finc0/test.go`) and `finc1 test`
(`compiler/build.fc`). See [docs/testing.md](../docs/testing.md) for the
testing strategy as a whole.

```
testdata/
├── fail/          negative tests (spec 13.1), one expected diagnostic each
├── pass/          positive tests (spec 13.2 + pass_extra_*), stdout compared with .out
└── integration/   Go E2E tests for tcp_echo (spec 13.3) + corpus driver
```

## Running

From the module root:

```sh
go test ./testdata/integration/ -v
```

Go's `./...` pattern skips directories named `testdata`, so the package must
be named explicitly. The same applies to vet: `go vet ./testdata/integration/`.

Environment variables:

| Variable | Effect |
| :--- | :--- |
| `FINC0=/path/to/finc0` | Use a prebuilt compiler instead of `go build ./bootstrap/cmd/finc0`. |
| `FINC_RACE=0` | Only static builds (`CGO_ENABLED=0`). |
| `FINC_RACE=1` | Only `--race` builds. |
| unset | Both, as `static` and `race` subtests. |

The tests skip rather than fail when:
- `./bootstrap/cmd/finc0` does not build,
- `finc0` exits non-zero and its output contains `not implemented`, or
- the test's `// SINCE: vX.Y` is newer than the compiler's language version.

`FINC0` may name any FinCore compiler: `scripts/bootstrap_verify.go` runs the
suite with `finc2`; `FINC0=$PWD/bin/finc1` tests the current compiler/,
including the tests of language versions finc0 does not implement.

Run one group with `-run`, for example `-run TestCorpusFail`, `-run TestCorpusPass`
or `-run TestEcho`.

## File conventions

All directives are line comments. A `// ERROR` marker goes at the end of a
code line. The other directives go in the leading comment block, before
`package`.

### `// ERROR E####` (fail tests)

```go
	saved = buf // ERROR E3002
```

- The marked line is where the primary diagnostic must point. Diagnostics use
  the spec 11 format: `file.fc:LINE:COL: error[E####]: message`.
- A test passes when all of the following hold:
  1. `finc check [FLAGS] <file>` exits non-zero.
  2. For every marker there is a diagnostic with the same file base name, the
     same line and the same code. The column is not compared.
  3. There is no diagnostic that does not match a marker. Each fail program is
     valid apart from the marked violation, so a conforming checker reports
     exactly the marked error(s) and no cascades.
- A file may contain several markers (`fail_named_assign.fc` has several;
  the others have exactly one).

### `// FLAGS: <args...>`

Extra CLI flags for `finc check` (fail tests) and `finc build` (pass tests),
split on whitespace. Example: `// FLAGS: --comptime-fuel=1000`.

### `// BUILD-ONLY` (pass tests)

The program is compiled with `finc build` but never run, and it has no `.out`
file. `pass_tcp_echo.fc` uses this: it listens on `0.0.0.0:8080` and runs until
it gets a signal.

### `// SINCE: vX.Y` (versioned tests)

```go
// SINCE: v0.3
// pass_foo: what the test covers.
package main
```

The file needs language version `vX.Y` (exactly this spelling: `v`, major,
`.`, minor, each 1 to 4 decimal digits). Every test of a feature added after
v0.2 must carry it. The directive is the first line starting with
`// SINCE:` in the file's leading comment block (the lines, trimmed of
ASCII white space, that are empty or start with `//`, up to the first other
line).

- Each compiler knows the version it implements: `finc0` is frozen at v0.2
  (`LangVersion` in `bootstrap/cmd/finc0/main.go`); `compiler/` declares
  `LangVersion`/`LangMajor`/`LangMinor` in `compiler/main.fc`. Both print it
  with `finc version` (or `--version`): `finc1 language v0.2`.
- A runner **skips** a test whose version is newer than the compiler's and
  counts it: `finc0 test` and `finc1 test` end with
  `N tests, P passed, F failed, S skipped (needs vX.Y)` (the newest version
  needed; `-v` lists each skipped test); the Go runner marks the subtest
  SKIP and logs `S skipped (needs vX.Y)`. It asks the compiler under test
  (`$FINC0 version`) and assumes v0.2 for a compiler without the command.
- A malformed value (`// SINCE: 0.3`) in any file of the closure below
  fails the test; the message names the file.
- `make diff-parse`/`diff-check*`/`diff-build*` (finc0 vs finc1) skip inputs
  newer than v0.2; `make diff-prev` classifies them against the previous
  release (see below).

**Required version of an input** (a test, or any file or package directory
given to the diff tools): the highest SINCE of the files of its import
closure, i.e.

- every `.fc` file of the input's own package: the file itself, or every
  `.fc` file directly in the directory (not in subdirectories); and,
- transitively, every package those files import, found by the scan below.
  An import path is resolved like the compilers resolve it, from the module
  root (the working directory; decisions 15, 41): a local path names
  `<moduleRoot>/<path>`, and `std`/`std/...` names `$FINCROOT/<path>` when
  `FINCROOT` is set and non-empty, else `<moduleRoot>/<path>` (decision
  58). Paths the compilers reject as invalid (empty, starting with `/` or
  `..`, containing `\`, not clean) and paths that are not a directory are
  ignored (the compiler reports them). A package (its resolved directory)
  is read once, so import cycles stop.

So a directive may sit in any file of a directory test or in a package it
imports (a subpackage of the test, a local package, a std package), and a
test that imports a package needing v0.3 needs v0.3 itself without a
marker of its own.

Imports are found by a textual scan of each file, not by the parser:

1. Comments are removed: `//` to the end of the line and `/* ... */` (not
   nested; its newlines are kept). String literals are not recognized.
2. The text is split into lines, each trimmed of ASCII white space; empty
   lines are skipped.
3. A line starting with the word `package` is skipped. (A word is followed
   by the end of the line, white space, `(` or `"`.)
4. A line that is exactly `import (` opens a block; until a line starting
   with `)`, each line holds at most one spec.
5. Any other line starting with the word `import` holds one spec after the
   keyword.
6. Any other line is the first top-level declaration and ends the scan:
   later `import` lines are not followed.
7. A spec is an optional name (one word without white space) followed by a
   quoted path: the path is the text between the first `"` and the next
   `"` (no escapes). A line of a block, or the rest of an `import` line,
   that does not have this form is ignored.

gofmt-style imports (`import "p"`, `import name "p"`, a block with one spec
per line) are always found. The three implementations, `scripts/langver`
(`RequiredIn`; the diff tools and the Go runner), `finc0 test`
(`requiredVersion` in `bootstrap/cmd/finc0/test.go`) and `finc test` of
compiler/ (`requiredVersion` in `compiler/build.fc`), are kept identical.
compiler/ bounds the scan by fixed capacities (256 packages per test, 128
files per package, 1 MiB per file) and fails the test with a message when
one is exceeded.

Unit tests of the runners use dummy `// SINCE: v9.9` tests in a temporary
directory that is also the module root of the run (`TestTestSince` and
`TestTestSinceClosure` in `bootstrap/cmd/finc0`, `TestRequiredClosure` in
`scripts/langver`, `TestSinceSkip` here, which also runs the compiler's own
`test` command on directory tests with the directive in a non-first file
and in an imported subpackage); the corpus has no such test.

### `.out` files (pass tests)

- Single-file test `pass/<name>.fc` → expected stdout in `pass/<name>.out`.
- Directory test `pass/<name>/` (a multi-file package; all `*.fc` directly in
  that directory, sorted by name) → expected stdout in `pass/<name>.out`, next
  to the directory. The `FLAGS` and `BUILD-ONLY` directives are read from
  the first `.fc` file in sorted order (`SINCE`: see above).
- The program is built with `finc build -o <bin> <src>` and run with no
  arguments. It must exit 0, and its stdout must equal the `.out` file byte for
  byte, including the final newline. stderr is ignored.
- `finc` is always invoked with the **module root** as the working directory,
  and paths are relative to it (for example `testdata/pass/multifile`). Local
  import paths are resolved against this root (see `pass_extra_import`), and
  so are `std/...` imports unless `FINCROOT` is set (`pass_std_*` use the
  repository's `std/`; see docs/stdlib.md).
- Pass programs write output only through `io.WriteStdout(...)` of a
  `driver func main(io Stdio) error`. Numbers are printed with `formatUint`.

## Testing a new language feature

finc0 is frozen, so a feature of v0.3 or later has no second
implementation to be compared with. It is tested by
([docs/testing.md](../docs/testing.md)):

1. **Conformance tests** with `// SINCE: vX.Y`: pass tests for every form of
   the feature (with `.out`) and fail tests for every diagnostic it adds
   (one `// ERROR` marker each). `finc1 test testdata` and
   `make integration` (with `FINC0=bin/finc1`) run them.
2. **`make diff-prev`** (`scripts/diffprev`): the current compiler against
   the previous release (`.stage/finc-seed`, `make seed`) on the whole
   corpus, the unit-test snippets and mutants: parse dumps, check
   diagnostics in all pass modes, emitted Go, `go vet`, and run output for
   cases with a `.out`. Old programs must behave identically (REGRESSION
   and DIAG-CHANGED fail); the new tests show up as EXPECTED. Intentional
   changes go into `scripts/diffprev/allow.txt` with a reason.
   `make diff-prev-fuzz` adds random mutants (`FUZZ_SEED=n`).
3. **Go compile and vet of the emitted Go**: every pass test is built (a
   program finc accepts but `go build` rejects is a type checker bug), and
   diff-prev vets every program both compilers build (`make diff-build-vet`
   does the same for the v0.2 subset).
4. **The fixed point** (`make bootstrap`): compiler/ built by the seed
   builds itself twice to identical Go, and finc2 passes `testdata` and the
   integration suite.

## Integration (`integration/`)

- `echo_*.fc` are copies of `examples/tcp_echo.fc` that change only the listen
  address (each has a fixed `127.0.0.1:181xx` port) and, where named,
  `MaxConns = 4` or `DrainHorizon = 300 * Millisecond`.
- `echo_test.go` implements the five E2E tests of spec 13.3. It also has
  `TestBuildExample`, which builds `examples/tcp_echo.fc`. Every test runs once
  for each build mode (`static`, `race`).
- `harness_test.go` builds `finc0` and the binaries, and manages the server
  processes. A test fails if a server prints `DATA RACE`.
- The tests run in sequence because the ports are fixed. Do not use `-parallel`
  with other runs that use the 18101–18104 ports.
