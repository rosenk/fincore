# FinCore v0.7 Language & Standard Library Guide

FinCore compiles directly to **standalone static Linux x86_64 ELF64 machine code** — **zero `libc`, zero garbage collector, zero runtime VM**.

## 1. CLI Commands

```sh
finc version                            # Print compiler version
finc build -o app main.fc               # Compile to standalone static ELF64
finc build --emit-asm=app.s -o app ...  # Also save generated x86_64 GAS assembly
finc run main.fc                        # Build & run immediately
finc check [--pretty] main.fc           # Type, scope, totality, and where-clause check
finc fmt [-w | --check] file.fc         # Canonical tab-indented source formatter
finc test [-v] testdata/                # Run conformance suite
```

## 2. Core Language Features

### Three Layers (Totality Without Ceremony)
- **Layer 1 (`comptime func`):** fuel-bounded compile-time evaluation and `Code n` / `El(...)` intensional type constructors.
- **Layer 2 (`func`, default):** provably terminating total functions (counting loops T1, slice-shrink loops T2, structural/measured recursion `decreases(...)`).
- **Layer 3 (`driver func` / `driver for`):** explicit OS/event-loop boundary receiving OS capabilities (`Net`, `Signals`, `Clock`, `FS`, `Env`, `Stdio`, `Exec`).

### Sum Types & Exhaustive Pattern Matching
```go
type Result[T any] enum {
	Ok(T)
	Err(error)
}

func unwrapOr(r Result[u32], fallback u32) u32 {
	return match r {
		Ok(v) -> v
		Err(_) -> fallback
	}
}
```

### Algebraic Effects & Linear One-Shot Continuations
```go
effect Yield[T any] { yield(v T) }

func scanLines(data []byte) with Yield[[]byte] { ... }

func count(data []byte) usize {
	n := usize(0)
	handle scanLines(data) {
		yield(_) -> { n++; resume() }
	}
	return n
}
```

### Dependent Dimensions, `where` Proofs & Zero-Check Indexing
```go
func concat[m usize, n usize, T any](a [m]T, b [n]T) [m + n]T { ... }

// Emits ZERO runtime bounds checks!
func at[n usize, T any](a *[n]T, i usize where i < n) T {
	return a[i]
}
```

### Immutable Views, Compile-Time `embed`, Bounded `Arena`, `f32`/`f64`, `Atomic[T]`, Generic Constraints
- `const []T`, `const *T`, zero-copy `strView(s string) const []byte`
- Compile-time file embedding directly into `.rodata` (FinCore's equivalent of Go `//go:embed`, zero heap, zero syscalls, relative paths only — `..` and `/...` rejected at compile time):
  ```go
  const indexHTML = embed("assets/index.html")       // string (works in comptime len/index & const structs)
  const favicon   = embedBytes("assets/favicon.ico") // const []byte (zero-copy .rodata view)
  ```
- `scope a := NewArena(65536) { buf, err := a.AllocBytes(256); ... }` (O(1) bump + O(1) `munmap`)
- Hardware SSE2 `f32`/`f64` + `%f` formatting
- Lock-free `Atomic[T]` (`Load`, `Store`, `Add`, `Swap`, `CompareAndSwap`)
- Shallow static constraints: `Equal`, `Ordered`, `Numeric` (0 vtables, 100% monomorphized)

### M:N `epoll` Fiber Concurrency (`NewFiberScope`)
```go
scope conns := NewFiberScope(4, 4096) {
	driver for {
		if err := conns.Ready(drainCtx); err != nil { break }
		conn, err := ln.Accept(drainCtx)
		if err != nil { continue }
		_ = conns.Spawn(drainCtx, driver func() { handleConn(conn) })
	}
}
```

## 3. Standard Library (`std/`)

| Package | Purpose |
| :--- | :--- |
| `std/vec` | Bounded `Vec[N, T]` and `ListArena` |
| `std/maps` | Bounded open-addressed hash tables |
| `std/bytes` | Mutable/immutable byte-slice utilities |
| `std/strings` | Zero-copy `const []byte` / `string` helpers (`Cut`, `TrimSpace`, `EqualFold`, `SplitByte`) |
| `std/strconv` | Allocation-free integer formatting & parsing |
| `std/sort` | Total in-place sorting |
| `std/path` | Zero-allocation path manipulation |
| `std/json` | Bounded `json.Builder[N]` encoder + zero-copy `GetString`/`GetInt`/`GetUint`/`GetBool` |
| `std/http` | Zero-GC M:N `epoll` HTTP/1.1 Keep-Alive server (`http.Server`) + HTTP/1.1 client (`http.Get`, `http.Post`, `http.Do`) |
| `std/sql` | Driver-agnostic zero-GC SQL core (`Param` enum, zero-copy `Row`, `RowBuf`, `Result`, `DBError` SQLSTATE) |
| `std/pg` | Pure-FinCore PostgreSQL v3.0 wire-protocol driver (`SCRAM-SHA-256`/MD5/cleartext/trust, pipelined Extended Query, scoped `Open` & `WithTx`) |
| `std/encoding/hex` | Zero-alloc hexadecimal `Encode` / `Decode` (`EncodedLen`, `DecodedLen`) |
| `std/encoding/base64` | Zero-alloc RFC 4648 base64 `Encode` / `Decode` (`EncodedLen`, `DecodedLen`) |
| `std/crypto/sha256` | Layer-2 total FIPS 180-4 SHA-256 (`Sum256` one-shot + streaming `Hasher`) |
| `std/log` | Zero-alloc structured logger over `Stdio` + `Clock` (`Debug`/`Info`/`Warn`/`Error`/`InfoKV`/`ErrorKV`) |
| `std/chan` | Bounded zero-GC MPMC CSP channels (`Chan[N, T]`: `Send`, `Recv`, `TrySend`, `TryRecv`, `Close` over `NewFiberScope`) |
| `std/datalog` | Finitist stratified & Flix-style Lattice Datalog (`Unary`, `Binary`, `Join`, `TransitiveClosure`, `AntiJoin`, `LatticeMap`, `ShortestPaths`, `PropagateBits`) |
