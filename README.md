# FinCore

**FinCore** is an experimental systems programming language with **Go ergonomics** built on the principles of **mathematical finitism**. It has a Go backend and is designed to compile itself.

The three classic hard limits of type systems become three simple compiler rules:

| Limit | FinCore rule |
| :--- | :--- |
| Girard–Hurkens paradox (`Type : Type`) | Strict split between **total functions** (`func`, provably terminating), a **fuel-bounded `comptime`**, and an explicit **OS layer** (`driver func` / `driver for`). |
| Cantor-style diagonal limits | **Intensional type codes** `Code n` / `El` stratified by level, and **statically bounded capacity** (fixed arenas, `NewScope(MaxConns)`). |
| Parametricity vs. uniformity | **Second-class scoped resources** (`Conn`, `Listener`, `Scope`, `FS`, `Exec`, …) that cannot escape their scope, and linear resources that must be closed exactly once. |

In practice this means:

- **No ambient authority.** `main` declares the capabilities it uses, e.g. `driver func main(net Net, sig Signals) error`.
- **No `nil` resources.**
- **No unbounded allocation.** There is no `make`, `append`, `new` or `map`.
- **Structured concurrency only.** There is no free `go f()`.
- **Every non-`driver` loop and recursion must be proven to terminate.** This uses rules T1–T3 plus `decreases(...)`.

The full specification (in Bulgarian) is [`fincore_language_specification_v0_1.md`](fincore_language_specification_v0_1.md).

## Example

```go
// Layer 2: the compiler proves this terminates (slice-shrink rule T2)
func writeAll(ctx Context, conn Conn, data []byte) error {
	for rem := data; len(rem) > 0; {
		n, err := conn.Write(ctx, rem)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrZeroWrite
		}
		rem = rem[n:]
	}
	return nil
}
```

The full TCP echo server with graceful shutdown is in [`examples/tcp_echo.fc`](examples/tcp_echo.fc).

## Install

Download the archive for your platform from the
[GitHub Releases](https://github.com/rosenk/fincore/releases) page, e.g.
`fincore-0.2.0-linux-amd64.tar.gz` (also `linux-arm64`, `darwin-arm64`,
and `darwin-amd64` when available), check it against `SHA256SUMS`, unpack
it anywhere and put its `bin/` on `PATH`:

```sh
sha256sum --ignore-missing -c SHA256SUMS      # macOS: shasum -a 256 --ignore-missing -c SHA256SUMS
tar -xzf fincore-0.2.0-linux-amd64.tar.gz -C "$HOME/.local"   # creates ~/.local/fincore
export PATH="$HOME/.local/fincore/bin:$PATH"

finc run hello.fc                              # from any directory
finc build -o echo examples/tcp_echo.fc
```

**finc needs a Go toolchain** (Go 1.27 or newer, `go` on `PATH`): it
compiles FinCore to Go and runs `go build` and gofmt. The archive holds
`bin/finc`, the runtime (`go.mod` + `runtime/fincrt`), `std/`, the
examples and the docs; finc finds the runtime and `std/` next to its own
executable (also through a symlink), so no environment variables are
needed. `FINCORE_ROOT` (runtime) and `FINCROOT` (std) still override.
Layout, reproducibility and the release workflow:
[`docs/release.md`](docs/release.md); changes: [`CHANGELOG.md`](CHANGELOG.md).

To build from source instead, see Bootstrap and Build and test below.

## Bootstrap

```text
bootstrap/ (Go) ──go build──► finc0                         (frozen at language v0.2)
finc0     build compiler/ @ seed tag (bootstrap/SEED) ──► finc-seed   (v0.2.0)
finc-seed build ./compiler (current) ──► finc1
finc1     build ./compiler ──► finc2
finc2     build ./compiler ──► finc3
fixed point: sha256(stage2.go) == sha256(stage3.go)
```

- **finc0 is frozen at v0.2.** New language features are implemented only
  in `compiler/`. finc0 gets only the bug fixes it needs to keep building
  the seed (see [`bootstrap/README.md`](bootstrap/README.md)).
- **compiler/ may use a feature once the seed tag has it.** To use one, a
  release tag that implements it must exist first; then `bootstrap/SEED` is
  bumped to that tag. Go and Rust bootstrap from their previous releases
  the same way.
- The seed is built from `git archive <seed>`, so the bootstrap needs a git
  checkout with tags (`git fetch --tags`). `make seed` builds
  `.stage/finc-seed`.
- `make diff-*` (finc0 vs finc1) are regression checks of the frozen v0.2
  subset; inputs marked `// SINCE: v0.3` (or later) are skipped.

## Status

| Milestone | What | Status |
| :--- | :--- | :--- |
| M0 | Lexer, AST, parser (Go) — `finc0 parse` | ✅ |
| M1 | Resolver, fuel-bounded comptime, type checker — `finc0 check` | ✅ |
| M2 | Scope/linearity checker (S1–S5) and totality/effect checker (T1–T4) | ✅ |
| M3 | Go runtime `runtime/fincrt` (interruptible I/O, structured concurrency, OS caps) | ✅ |
| M4 | IR + deterministic Go codegen — `finc0 build/run/test` | ✅ |
| M5 | `tcp_echo.fc` end-to-end (static and `--race`) | ✅ |
| M6 | Self-hosted front end in FinCore (`compiler/`: arenas, lexer, parser, AST dump); `finc1 parse` byte-identical to finc0 | ✅ |
| M7 | Self-hosted checker (resolve, types, comptime, scope, totality) + Go codegen in FinCore; `finc1 check/build` byte-identical to finc0 | ✅ |
| M8 | Fixed-point bootstrap gate: `sha256(stage2.go) == sha256(stage3.go)`, finc2 passes all tests | ✅ |
| v0.2 | Language v0.2 (sum types, patterns, `is`, ergonomics; [`docs/v0_2.md`](docs/v0_2.md)) in both compilers; release tag `v0.2.0` | ✅ |
| Seed | finc0 frozen at v0.2; bootstrap goes finc0 → finc-seed (`v0.2.0`) → current `compiler/` | ✅ |

Reproduce the bootstrap (about 15 seconds):

```sh
make bootstrap   # go run ./scripts/bootstrap_verify.go
```

```text
==> 2. finc0 build compiler/@v0.2.0 → finc-seed
...
==> 6. fixed point: sha256(stage2.go) == sha256(stage3.go)
   stage2.go dff7efd8e43b7ce8d8975e5577ab871c16bc81ad15d11286ad0137456ccecf02
   stage3.go dff7efd8e43b7ce8d8975e5577ab871c16bc81ad15d11286ad0137456ccecf02
BOOTSTRAP OK: finc0 → finc-seed (v0.2.0) → finc1 → finc2 → finc3; fixed point reached (stage2.go == stage3.go)
```

## Layout

```text
bootstrap/        Stage 0 compiler in Go (finc0, frozen at v0.2): diag, lexer, ast, parser,
                  resolve, comptime, types, check, scope, totality, ir, codegen/golang;
                  SEED names the seed tag
compiler/         Self-hosting compiler written in FinCore (data-oriented, fixed arenas)
runtime/fincrt/   Go runtime used by generated code
std/              Finitist standard library in FinCore: vec, maps, bytes, strconv, path,
                  sort (total, bounded, allocation-free; see docs/stdlib.md)
examples/         tcp_echo.fc — the reference program
testdata/         pass/ fail/ integration/ conformance suite (see testdata/README.md)
docs/             Resolved spec ambiguities, runtime API, stdlib reference, self-hosting requirements
```

## Build and test

Requires Go 1.27+ and has no external dependencies.

```sh
make            # vet + test + build bin/finc0
make race       # unit tests with the race detector
make check-corpus
make integration
make seed       # .stage/finc-seed: finc0 builds compiler/ at the bootstrap/SEED tag
make bootstrap  # finc0 → finc-seed → finc1 → finc2 → finc3, fixed-point gate
make dist       # release archive dist/fincore-<version>-<os>-<arch>.tar.gz (finc2)
make dist-test  # smoke-test it from another directory, no env vars
make help       # all targets

bin/finc0 parse --dump-ast examples/tcp_echo.fc
bin/finc0 check examples/tcp_echo.fc
```

## Design notes

- [`docs/semantics_decisions.md`](docs/semantics_decisions.md) lists every place where the spec was ambiguous and how it was resolved. The compilers and tests follow it.
- [`docs/runtime_api.md`](docs/runtime_api.md) describes the `fincrt` API that generated code targets.
- [`docs/stdlib.md`](docs/stdlib.md) is the reference for the `std/` packages (`import "std/maps"`; resolved against `$FINCROOT` when set, else the module root; an installed finc uses its install directory).
- [`docs/compiler_requirements.md`](docs/compiler_requirements.md) lists the language features the self-hosted compiler relies on.
