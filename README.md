# FinCore

**FinCore** is a finitist systems programming language with **Go ergonomics** that compiles directly to **standalone static machine code** — **no `libc`, no garbage collector, no runtime VM**.

Read the full language manifesto, mathematical foundations, and roadmap in [`VISION.md`](VISION.md).

## Core Principles

| Classical limit | FinCore rule |
| :--- | :--- |
| Girard–Hurkens paradox (`Type : Type`) | Strict split between **total functions** (`func`, provably terminating), **fuel-bounded `comptime`**, and an explicit **OS/effect layer** (`driver func` / `driver for`). |
| Cantor-style diagonalization | **Stratified intensional type codes** (`Code n` / `El`) and **statically bounded capacity** (fixed arenas, `NewScope(MaxConns)`). |
| Parametricity vs. ambient authority | **Second-class scoped resources** (`Conn`, `Listener`, `Scope`, `Context`, `FS`, `Exec`, …) that cannot escape their scope, and **linear resources** closed exactly once. |

In practice:

- **Direct static ELF64 machine code (Linux x86_64):** compiles directly to GAS assembly (`as` + `ld -static`) against a tiny raw Linux syscall runtime (`runtime/linux_amd64/`). Zero `libc`, zero `libpthread`, zero GC, zero `malloc`. A full concurrent multicore TCP echo server with graceful shutdown compiles to **~24 KB** (`not a dynamic executable`).
- **No ambient authority:** `main` declares every OS capability it uses (e.g. `driver func main(net Net, sig Signals) error`).
- **No `nil` resources, no unbounded heap allocation, no free `go f()`.**
- **Provable termination:** every non-`driver` loop and recursion terminates (rules T1–T3 + `decreases(...)`).

## Clear Stage-0 Go Bootstrap Boundary & Self-Hosting Fixed Point

`bootstrap/` (`finc0`, written in Go) exists **strictly to bootstrap FinCore v0.1 Native** and is **permanently frozen**.

`compiler/` (`finc`, FinCore v0.7) is a **100% FinCore self-hosted native compiler** (~19.5k LOC across 19 files) that compiles directly to standalone static Linux x86_64 ELF64 binaries — **zero `libc`, zero GC, zero Go runtime**, including **algebraic effects (`effect`/`with`/`handle`) & linear one-shot continuations (`resume`)**, **indexed dependent types (`[m + n]T`) with `where` refinement proofs & zero-overhead bounds-check elimination**, **immutable views (`const []T`, `const *T`, zero-copy `strView`)**, **compile-time file embedding (`embed` / `embedBytes` into `.rodata`)**, **bounded scoped `Arena` allocation**, **M:N `epoll` Fiber Scheduler (`NewFiberScope` + `Spawn`/`Ready`)**, **SSE2 `f32`/`f64`**, **lock-free `Atomic[T]`**, **shallow static generic constraints (`Equal`/`Ordered`/`Numeric`)**, **x86_64 peephole optimizer** (self-compiles in **~0.35s**), **`finc init` + `finc.mod` module manifests**, **opt-in System V AMD64 C FFI (`extern driver func` + `link <lib>`, keeping static zero-libc default)**, **`finc fmt` + `--pretty` caret diagnostics**, and a **Go-style zero-GC standard library (`std/http` server, Router, live SSE & client, `std/net/dns`, `std/time`, `std/flag`, `std/sql`, `std/pg` PostgreSQL v3 + SCRAM-SHA-256, `std/chan` bounded CSP channels, `std/sync` Semaphore/CircuitBreaker/TokenBucket, `std/retry` composable Schedules, `std/datalog` stratified & lattice Datalog, `std/json`, `std/strings`, `std/encoding/hex`, `std/encoding/base64`, `std/crypto/sha256`, `std/log`, `std/vec`, `std/maps`, `std/bytes`, `std/strconv`, `std/sort`, `std/path`)**:

```text
bootstrap/ (Go, FROZEN at v0.1 Native) ──► finc0 ──► static Linux x86_64 ELF64 (no libc, no GC)
compiler/  (100% FinCore, v0.7 Native) ──► finc1 ──► finc2 == finc3 (bit-for-bit fixed point in ~0.35s!)
```

**No more Go is written beyond Stage 0.** See [`bootstrap/README.md`](bootstrap/README.md) and [`docs/guide.md`](docs/guide.md).

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

See the complete concurrent TCP echo server with backpressure and graceful `SIGTERM` shutdown in [`examples/tcp_echo.fc`](examples/tcp_echo.fc).

## Layout

```text
VISION.md            Language manifesto, type-theoretic foundations, and roadmap
compiler/            Stage 1+ self-hosted native compiler in 100% FinCore (v0.7, fixed point finc2 == finc3)
bootstrap/           Stage 0 compiler in Go (finc0, FROZEN at v0.1 Native)
runtime/linux_amd64/ Pure x86_64 Linux syscall runtime (sys, fmt, net, scope, fiber, effect — zero libc, zero GC)
std/                 Zero-GC standard library in FinCore (http, net/dns, time, flag, sql, pg, chan, sync, retry, datalog, json, strings, encoding/hex, encoding/base64, crypto/sha256, log, vec, maps, bytes, strconv, path, sort)
examples/            Native example programs (tcp_echo, calc, wordfreq, state_machine, worker_pool, comptime_tables)
testdata/            Conformance suite (pass/, fail/, integration/)
docs/                Design decisions, stdlib reference, runtime API
```

## Build and Test

Requires Linux x86_64, GNU binutils (`as`, `ld`), and Go 1.24+ (to build Stage-0 `finc0` once).

```sh
make                # vet + unit tests + build bin/finc0 and self-hosted bin/finc
make bootstrap      # 3-stage native fixed point (finc1 -> finc2 == finc3) + full integration suite
make test-corpus    # run finc0 test -v testdata/ (all 58 pass/fail tests as static ELF64)
make test-examples  # build and verify all examples/*.fc
make integration    # end-to-end native TCP echo + corpus integration tests
make ci             # run everything (including self-hosting fixed point!)

# Build and run a static native ELF64 binary with the self-hosted FinCore compiler:
bin/finc build -o /tmp/echo examples/tcp_echo.fc
file /tmp/echo      # ELF 64-bit LSB executable, x86-64, statically linked
bin/finc run examples/calc.fc
```
