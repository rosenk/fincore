# FinCore conformance corpus (`testdata/`)

Conformance tests from spec section 13. `finc0` and the self-hosted
`finc1`/`finc2` must both pass all of them. A future `finc test <dir>` must
implement the conventions below exactly. `testdata/integration/corpus_test.go`
is the reference implementation.

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
- `./bootstrap/cmd/finc0` does not build, or
- `finc0` exits non-zero and its output contains `not implemented`.

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
- A file may contain several markers. All files in `fail/` currently have
  exactly one.

### `// FLAGS: <args...>`

Extra CLI flags for `finc check` (fail tests) and `finc build` (pass tests),
split on whitespace. Example: `// FLAGS: --comptime-fuel=1000`.

### `// BUILD-ONLY` (pass tests)

The program is compiled with `finc build` but never run, and it has no `.out`
file. `pass_tcp_echo.fc` uses this: it listens on `0.0.0.0:8080` and runs until
it gets a signal.

### `.out` files (pass tests)

- Single-file test `pass/<name>.fc` → expected stdout in `pass/<name>.out`.
- Directory test `pass/<name>/` (a multi-file package; all `*.fc` directly in
  that directory, sorted by name) → expected stdout in `pass/<name>.out`, next
  to the directory. Header directives are read from the first `.fc` file in
  sorted order.
- The program is built with `finc build -o <bin> <src>` and run with no
  arguments. It must exit 0, and its stdout must equal the `.out` file byte for
  byte, including the final newline. stderr is ignored.
- `finc` is always invoked with the **module root** as the working directory,
  and paths are relative to it (for example `testdata/pass/multifile`). Local
  import paths are resolved against this root (see `pass_extra_import`).
- Pass programs write output only through `io.WriteStdout(...)` of a
  `driver func main(io Stdio) error`. Numbers are printed with `formatUint`.

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
