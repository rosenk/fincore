# bootstrap/ — Stage-0 Compiler `finc0` (Go, FROZEN at v0.1 Native)

`finc0` is the Stage-0 bootstrap compiler written in Go. It compiles FinCore **v0.1 Native** directly into deterministic GNU GAS x86_64 assembly (`bootstrap/internal/codegen/amd64/`) and links it statically (`as` + `ld -static`) against `runtime/linux_amd64/` — with **zero `libc` and zero GC**.

## The Hard Boundary: No More Go

`bootstrap/` has reached its final purpose: a clean, verified, standalone native Stage-0 compiler capable of compiling FinCore v0.1 Native programs (including `std/`, all examples, and multicore concurrent servers) into tiny static ELF64 executables.

From this point forward, **`bootstrap/` is permanently frozen**:

- **No new language features** will ever be added to `bootstrap/` (no Go implementation of QTT dependent types, algebraic effects, delimited continuations, floats, or new syntax).
- **Stage 1 (`v0.2`) and all future compilers** are written strictly in FinCore itself, bootstrapped by `finc0`.
- **Only bug fixes** needed to keep `finc0` compiling Stage 1 on Linux x86_64 (e.g. a miscompilation found while bootstrapping `v0.2`, or Go toolchain compatibility) are permitted in `bootstrap/`.

## Pipeline

```text
source (.fc)
  └─ lexer → parser → resolve → comptime (fuel-bounded) → types → scope/linearity → totality
       └─ ir.Lower
            └─ codegen/amd64 (monomorphized x86_64 GAS assembly)
                 └─ as --64 + ld -static + runtime/linux_amd64/{sys,fmt,net,scope}.s
                      └─ static ELF64 binary (zero libc, zero GC)
```

## CLI

```sh
finc0 parse [--dump-ast] <file.fc | dir>
finc0 check [--comptime-fuel=N] <file.fc | dir>
finc0 build [-o out_bin] [--emit-asm=out.s] [--comptime-fuel=N] <file.fc | dir>
finc0 run   [--comptime-fuel=N] <file.fc | dir> [-- args...]
finc0 test  [-v] <dir>
finc0 version
```
