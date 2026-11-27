# A tour of FinCore for Go programmers

FinCore looks like Go and compiles to Go, but it makes three promises that
Go does not:

- **every function terminates**, unless it is explicitly marked as talking
  to the outside world;
- **nothing grows without a bound**: there is no `make`, `append`, `new` or
  `map`, and every buffer, table and task pool has a capacity known at
  compile time;
- **resources cannot leak or escape**: connections, listeners, slots and
  cancel functions are closed exactly once, and borrowed memory never
  outlives its owner.

This tour shows how these promises feel in practice. Each section introduces
one idea with a short snippet and points to a complete program in
[`examples/`](../examples). All snippets that start with `// file:` are
complete programs. `make tutorial-check` compiles each of them with the
current compiler and compares the shown compiler output with the real one.

The reference is the specification
([`fincore_language_specification_v0_1.md`](../fincore_language_specification_v0_1.md),
in Bulgarian), the v0.2 changes in [`v0_2.md`](v0_2.md), the resolved
ambiguities in [`semantics_decisions.md`](semantics_decisions.md) and the
library reference in [`stdlib.md`](stdlib.md).

## Contents

1. [Install, build, run](#1-install-build-run)
2. [Three layers](#2-three-layers)
3. [Totality: why every loop needs a bound](#3-totality-why-every-loop-needs-a-bound)
4. [Recursion with `decreases`](#4-recursion-with-decreases)
5. [Value, Scoped and Linear](#5-value-scoped-and-linear)
6. [Borrowed memory and the scope checker](#6-borrowed-memory-and-the-scope-checker)
7. [Fixed-capacity containers](#7-fixed-capacity-containers)
8. [Sum types and patterns](#8-sum-types-and-patterns)
9. [Formatting with `formatTo`](#9-formatting-with-formatto)
10. [Compile-time code: `comptime`, `Code` and `El`](#10-compile-time-code-comptime-code-and-el)
11. [Capabilities and `driver func main`](#11-capabilities-and-driver-func-main)
12. [Structured concurrency](#12-structured-concurrency)
13. [Tooling: tests, `SINCE` and the gates](#13-tooling-tests-since-and-the-gates)
14. [Where to go next](#14-where-to-go-next)

## 1. Install, build, run

You need Go 1.27 or later and a git checkout with tags (the compiler
bootstraps from its previous release). From the repository root:

```sh
make finc1      # bin/finc0 (Go) -> .stage/finc-seed -> bin/finc1 (FinCore)
```

`bin/finc1` is the self-hosted compiler, written in FinCore. It has the
same commands as the Go bootstrap compiler `bin/finc0`:

```text
finc1 parse [--dump-ast] <file.fc | dir>
finc1 check [--comptime-fuel=N] <file.fc | dir>
finc1 build [-o out_bin] [--emit-go=out.go] [--race] <file.fc | dir>
finc1 run   [--race] <file.fc | dir> [-- args...]
finc1 test  <dir>
finc1 version
```

The compiler translates the program to one Go file and calls `go build`.
Run it from the repository root, so that `import "std/..."` and the Go
runtime package are found (or set `FINCROOT` and `FINCORE_ROOT` to the
repository). The first program:

```go
// file: hello.fc
package main

driver func main(io Stdio) error {
	return io.WriteStdout("hello, finite world\n")
}
```

```text
$ finc1 run hello.fc
hello, finite world
```

There is no `fmt.Println`. Output goes through the `Stdio` capability that
`main` asks for in its parameter list (section 11). A string constant can be
passed where a `[]byte` is expected.

## 2. Three layers

Every function lives in one of three layers, and a layer may only call
itself or the layers below it:

| Layer | Declared as | Runs | Guarantee |
| :--- | :--- | :--- | :--- |
| 1. comptime | `comptime func` | inside the compiler | stops within a fuel budget |
| 2. total | `func` | at run time | proven to terminate |
| 3. driver | `driver func`, `driver for` | at run time | may block, loop forever and do I/O |

```go
// file: layers.fc
package main

// Layer 1: evaluated by the compiler; the binary contains the result.
comptime func pow2(k usize) usize {
	r := usize(1)
	for i := usize(0); i < k; i++ {
		r *= 2
	}
	return r
}

const TableSize = pow2(4)

// Layer 2: total. The compiler proves that the loop ends.
func sum(xs []u64) u64 {
	s := u64(0)
	for i := usize(0); i < len(xs); i++ {
		s += xs[i]
	}
	return s
}

// Layer 3: the only place where the program touches the world.
driver func main(io Stdio) error {
	var table [TableSize]u64
	for i := usize(0); i < len(table); i++ {
		table[i] = u64(i)
	}
	var b [32]byte
	n := formatTo(b[:], "sum = %d\n", sum(table[:]))
	return io.WriteStdout(b[:n])
}
```

```text
$ finc1 run layers.fc
sum = 120
```

A total `func` that calls a `driver func` or contains a `driver for` is
rejected with E5001. Most of a FinCore program is total code. The driver
layer is a thin shell around it: in [`examples/tcp_echo.fc`](../examples/tcp_echo.fc)
only the accept loop and the per-connection loop are `driver`.

## 3. Totality: why every loop needs a bound

A total function must terminate, and the compiler has to see why, without a
solver. It accepts three loop shapes:

- **T1, counting:** `for i := a; i < n; i++` (or `i += 2`, or counting
  down with `i > 0; i--`) where `n` does not change in the loop.
- **T2, slice shrinking:** `for rem := data; len(rem) > 0; { ... }` where
  every path to the next iteration does `rem = rem[k:]` with `k > 0`.
- **T3, a measure:** `for cond decreases(m) { ... }` where `m` is an
  unsigned expression that gets smaller on every path.

Here is a loop a Go programmer writes without thinking:

```go
// file: index.fc
package main

// indexOf returns the position of c in s, or len(s).
func indexOf(s []byte, c byte) usize {
	i := usize(0)
	for i < len(s) && s[i] != c {
		i++
	}
	return i
}

driver func main(io Stdio) error {
	var b [32]byte
	return io.WriteStdout(b[:formatTo(b[:], "%d\n", indexOf("hello", 'l'))])
}
```

```text
$ finc1 check index.fc
index.fc:7:2: error[E4002]: cannot prove termination of for loop: not a counting loop (no post statement), slice-shrink loop, or loop with decreases clause
error: fincore: invalid number
```

(The last line is the compiler's exit status. It always reads `invalid
number` after diagnostics; see the note in section 13.) Two fixes: make it
a counting loop with an early exit, or name the measure.

```go
// file: index2.fc
package main

func indexOf(s []byte, c byte) usize {
	for i := usize(0); i < len(s); i++ { // T1
		if s[i] == c {
			return i
		}
	}
	return len(s)
}

func indexOf2(s []byte, c byte) usize {
	i := usize(0)
	for i < len(s) && s[i] != c decreases(len(s) - i) { // T3
		i++
	}
	return i
}

// countWords drops a non-empty prefix of rem on every round: T2.
func countWords(text []byte) usize {
	n := usize(0)
	for rem := text; len(rem) > 0; {
		k := indexOf(rem, ' ')
		if k == 0 {
			rem = rem[1:]
			continue
		}
		n++
		rem = rem[k:] // k > 0 on this path
	}
	return n
}

driver func main(io Stdio) error {
	var b [32]byte
	k := formatTo(b[:], "%d %d %d\n", indexOf("hello", 'l'), indexOf2("hello", 'o'), countWords("to be  or not"))
	return io.WriteStdout(b[:k])
}
```

```text
$ finc1 run index2.fc
2 4 4
```

Things that surprise Go programmers:

- `i < n && more` is not a T1 condition. Keep `i < n` in the header and
  `break` inside the loop, or use `decreases`.
- A bound read through a pointer or a global can change when the loop calls
  a function that writes memory. Copy it to a local first: `lim := p.n`.
- `driver for { ... }` is the only unbounded loop. It is allowed only in
  driver code, which is where servers and event loops live.

[`examples/wordfreq.fc`](../examples/wordfreq.fc) has a T2 tokenizer and T1
loops everywhere else; [`examples/calc.fc`](../examples/calc.fc) bounds its
parser loops by the input length.

## 4. Recursion with `decreases`

Recursion is a loop through the call graph, so it needs a measure too. Every
function in a recursive group (a strongly connected component of the call
graph) declares `decreases(depth)` with an unsigned parameter, and every
call inside the group passes something provably smaller, usually `depth - 1`
after a `depth == 0` check:

```go
// file: depth.fc
package main

const MaxDepth = 64

func isEven(n u32, depth usize) (bool, error) decreases(depth) {
	if depth == 0 {
		return false, ErrMaxDepth
	}
	if n == 0 {
		return true, nil
	}
	return isOdd(n-1, depth-1)
}

func isOdd(n u32, depth usize) (bool, error) decreases(depth) {
	if depth == 0 {
		return false, ErrMaxDepth
	}
	if n == 0 {
		return false, nil
	}
	return isEven(n-1, depth-1)
}

driver func main(io Stdio) error {
	a, _ := isEven(10, MaxDepth)
	_, err := isEven(1000, MaxDepth)
	var b [64]byte
	k := formatTo(b[:], "isEven(10) = %v, isEven(1000): %v\n", a, err == ErrMaxDepth)
	return io.WriteStdout(b[:k])
}
```

```text
$ finc1 run depth.fc
isEven(10) = true, isEven(1000): true
```

The depth is not a formality. It is how a recursive-descent parser proves
that it terminates, and it also bounds the stack: deeply nested hostile
input produces an error instead of a stack overflow. The self-hosted
compiler's parser is one recursive group of 33 functions that all carry
`decreases(depth)`. [`examples/calc.fc`](../examples/calc.fc) does the same
on a small scale, and prints `nested deeper than 80` for a pathological
line. A recursive group without measures is rejected with E4002 (one
function) or E4004 (mutual recursion).

## 5. Value, Scoped and Linear

Every type belongs to one of two classes:

- **Value:** numbers, `bool`, `string`, `error`, arrays, slices, structs and
  enums of values. Values are copied freely, can be returned and stored in
  globals, and have a zero value.
- **Scoped:** capabilities (`Net`, `FS`, `Stdio`, ...), `Context`, `Scope`,
  pointers, and structs containing any of them. A Scoped value has no zero
  value and cannot outlive the scope it was created in.

Some Scoped types are also **Linear**: `Listener`, `Conn`, `Slot` and
`CancelFunc` must be used up exactly once, by closing, releasing, calling,
or moving them to a task. The usual tool is `defer`, as in Go. Forgetting
it on one path is a compile error:

```go
// file: leak.fc
package main

driver func main(net Net, io Stdio) error {
	ln, err := net.ListenTCP("127.0.0.1:0")
	if err != nil {
		return err
	}
	if err := io.WriteStdout("listening\n"); err != nil {
		return err
	}
	return ln.Close()
}
```

```text
$ finc1 check leak.fc
leak.fc:5:2: error[E3005]: Linear resource ln (Listener) is not closed, released or moved on every path
error: fincore: invalid number
```

The early `return err` leaks the listener. Note what the checker does *not*
complain about: on the `err != nil` path after `ListenTCP` there is no
listener to close. Write `defer ln.Close()` right after the error check
and the program compiles. The family of scope and linearity errors:

| Code | Meaning | Typical cause |
| :--- | :--- | :--- |
| E3001 | Scoped value or pointer in a global | `var conn Conn` at package level |
| E3002 | scope escape | returning `&local`, storing a borrowed slice (section 6) |
| E3003 | use after move | using `conn` after handing it to `s.Go(...)`, closing twice |
| E3004 | discarded Linear value | `_ = ln` |
| E3005 | Linear resource leak | a path that neither closes nor moves it |

## 6. Borrowed memory and the scope checker

There is no garbage collector to keep memory alive, so FinCore tracks where
every pointer and slice points. A slice of a local array is valid while that
array is; a function parameter is *borrowed* from the caller. Storing a
borrowed slice somewhere that lives longer than the call is the mistake
Go lets you make and FinCore does not:

```go
// file: borrow.fc
package main

type Lexer struct {
	src []byte
	pos usize
}

// reset points the lexer at new input.
func (l *Lexer) reset(src []byte) {
	l.src = src
	l.pos = 0
}

var lastLine []byte

func remember(line []byte) {
	lastLine = line
}

driver func main(io Stdio) error {
	var buf [64]byte
	n := copy(buf[:], "x := 1")
	var lx Lexer
	lx.reset(buf[:n])
	remember(buf[:n])
	return io.WriteStdout(lx.src)
}
```

```text
$ finc1 check borrow.fc
borrow.fc:11:2: error[E3002]: scope escape: a value referring to the storage of parameter src cannot be stored in the storage of parameter l: each parameter is its own region, and memory reached through one parameter must not refer to another parameter's storage
borrow.fc:18:2: error[E3002]: scope escape: a value referring to the storage of parameter line (depth 0) cannot be stored in package-level storage, which outlives it
error: fincore: invalid number
```

Each parameter is its own region: the checker cannot know that `lx` and
`buf` live equally long, so it rejects the store. The fix is to build the
struct where both are visible, or to copy the bytes into storage you own:

```go
// file: borrow2.fc
package main

type Lexer struct {
	src []byte
	pos usize
}

var lastLine [64]byte
var lastLen usize

func remember(line []byte) {
	lastLen = copy(lastLine[:], line) // a copy, not a reference
}

driver func main(io Stdio) error {
	var buf [64]byte
	n := copy(buf[:], "x := 1")
	lx := Lexer{src: buf[:n]} // lx has the lifetime of buf
	remember(buf[:n])
	if err := io.WriteStdout(lx.src); err != nil {
		return err
	}
	return io.WriteStdout(lastLine[:lastLen])
}
```

The rules (S1 to S5 in spec section 7) are simple: each variable has a
depth, `scope` blocks and function bodies go one level deeper, and a value
may only be stored where it does not outlive what it points to. A pointer
receiver may change its fields but not make them point at the arguments.
[`examples/calc.fc`](../examples/calc.fc) builds `Parser{src: line}` with a
struct literal for exactly this reason.

## 7. Fixed-capacity containers

The standard library in `std/` replaces Go's growing slices and maps with
containers whose capacity is a const type parameter. They are plain Value
structs: declare one as a local or a global and call its methods.

```go
// file: containers.fc
package main

import (
	"std/maps"
	"std/vec"
)

var ages maps.StrMap[16, 256, 4, u32] // 16 buckets, 256 key bytes, 4 entries

driver func main(io Stdio) error {
	var v vec.Vec[3, u32]
	pushed := 0
	for i := u32(1); i < 6; i++ {
		if v.Push(i * 10) {
			pushed++
		}
	}
	_ = ages.PutStr("ada", 36)
	_ = ages.PutStr("alan", 41)
	_ = ages.PutStr("ada", 37) // overwrites
	a, ok := ages.GetStr("ada")

	var b [96]byte
	k := formatTo(b[:], "pushed %d of 5, len %d, full %v; ada=%d (%v), %d keys\n",
		pushed, v.Len(), v.Full(), a, ok, ages.Len())
	return io.WriteStdout(b[:k])
}
```

```text
$ finc1 run containers.fc
pushed 3 of 5, len 3, full true; ada=37 (true), 2 keys
```

What to remember:

- **Overflow is a value.** `Push`, `Put` and `Intern` return `false` when
  there is no room; the arena-like types also keep a sticky `Full()` flag
  so you can do a batch of work and check once.
- **Nothing panics and nothing allocates.** Lookups take `[]byte` keys, so
  a subslice of an input buffer is a key without copying.
- **Iteration order is insertion order**, never hash order.
- `std/sort` sorts slices with a `less` function value; `std/bytes` has a
  bounded `Builder`; `std/strconv` parses and formats numbers.

[`examples/wordfreq.fc`](../examples/wordfreq.fc) counts words in a
`StrMap`, sorts the entry IDs with `sort.Sort` and reports how many words
were dropped when the table is full.

## 8. Sum types and patterns

Enums carry payloads, and `match` takes them apart. A match on an enum must
cover every variant, so adding a variant turns every place that needs to
handle it into a compile error.

```go
// file: shapes.fc
package main

type Shape enum {
	Circle(u32)       // radius
	Rect(u32, u32)    // width, height
	Triangle(u32, u32) // base, height
	Empty
}

func area(s Shape) u64 {
	return match s {
		Circle(r) -> 3 * u64(r) * u64(r)
		Rect(w, h) -> u64(w) * u64(h)
		Empty -> 0
	}
}

driver func main(io Stdio) error {
	var b [32]byte
	return io.WriteStdout(b[:formatTo(b[:], "%d\n", area(Shape.Rect(3, 4)))])
}
```

```text
$ finc1 check shapes.fc
shapes.fc:12:9: error[E2012]: non-exhaustive match on Shape: missing Triangle
error: fincore: invalid number
```

Variants are constructed qualified (`Shape.Rect(3, 4)`) and matched
unqualified. Patterns nest and combine:

```go
// file: patterns.fc
package main

type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
}

type Option[T any] enum {
	Some(T)
	None
}

func describe(o Option[Shape]) u32 {
	return match o {
		Some(Rect(w, h)) if w == h -> 1                  // nested pattern, guard
		Some(Circle(x) | Rect(x, _)) if x > 100 -> 2     // or-pattern binding x
		Some(s @ (Circle(_) | Rect(..))) -> 10 + kind(s) // `@` binds the shape
		Some(Empty) | None -> 0
	}
}

func kind(s Shape) u32 {
	return match s { Circle(_) -> 1, Rect(..) -> 2, Empty -> 0 }
}

func width(o Option[Shape]) u32 {
	if !(o is Some(Rect(w, _))) { // let-else: w is bound after the if
		return 0
	}
	return w
}

driver func main(io Stdio) error {
	var b [64]byte
	k := formatTo(b[:], "%d %d %d %d %d\n", describe(Option[Shape].Some(Shape.Rect(2, 2))),
		describe(Option[Shape].Some(Shape.Circle(200))), describe(Option[Shape].Some(Shape.Circle(1))),
		describe(Option[Shape].None), width(Option[Shape].Some(Shape.Rect(7, 1))))
	return io.WriteStdout(b[:k])
}
```

```text
$ finc1 run patterns.fc
1 2 11 0 7
```

The pattern language, all of it checked for exhaustiveness and unreachable
arms: `_`, bindings, literals and constants, nested variants, or-patterns
(`A | B`, binding the same names), `..` for the remaining payloads, `name @
pattern`, guards (`if cond`, which do not count for exhaustiveness), tuple
scrutinees `match (a, b) { ... }`, and `x is Pattern` as a boolean
expression whose bindings are visible where it is known to be true. A match
is a statement or an expression.

Enums are values, so they fit into fixed arrays. A tree is an array of
nodes that refer to each other by a `u32` index. Look at the `Node` arena of
[`examples/calc.fc`](../examples/calc.fc), its tuple match on `(op, y)` in
`apply`, and the transition table of
[`examples/state_machine.fc`](../examples/state_machine.fc), a single match
over `(State, Cmd)` without a catch-all arm.

## 9. Formatting with `formatTo`

```go
func formatTo(dst []byte, format string, args...) usize
```

`formatTo` writes into a buffer you provide, truncates at `len(dst)`, and
returns the number of bytes written. The format must be a constant; it is
checked against the arguments at compile time and expanded into straight
calls, so there is no reflection and no allocation at run time. Verbs:
`%d`, `%x`, `%s`, `%c`, `%q`, `%v`, with widths, `-` and `0`. A named type
with a total method `Format(dst []byte) usize` prints with `%v`:

```go
// file: format.fc
package main

type Celsius i32

func (c Celsius) Format(dst []byte) usize {
	return formatTo(dst, "%d.%d C", c/10, c%10)
}

driver func main(io Stdio) error {
	var b [80]byte
	n := formatTo(b[:], "[%-6s|%6s] %05d %x %q %v\n", "ab", "cd", 42, 255, "a\tb", Celsius(215))
	if err := io.WriteStdout(b[:n]); err != nil {
		return err
	}
	var small [8]byte
	k := formatTo(small[:], "truncated at %d bytes", len(small))
	if err := io.WriteStdout(small[:k]); err != nil {
		return err
	}
	return io.WriteStdout("\n")
}
```

```text
$ finc1 run format.fc
[ab    |    cd] 00042 ff "a\tb" 21.5 C
truncate
```

A wrong format is a compile error, not a `%!d(string=...)` at run time:
`cannot use ok (type bool) as argument for %d in formatTo`. Every example
program prints with `formatTo`; [`examples/state_machine.fc`](../examples/state_machine.fc)
pads enum values that have a Format method with `%-12v`.

## 10. Compile-time code: `comptime`, `Code` and `El`

A `comptime func` runs inside the compiler. It may loop as it likes, because
a fuel budget stops it: every loop iteration, call and index costs one unit,
and the default budget is 1,000,000 per evaluation. Constants, array sizes
and package-level `var` initializers may call comptime functions:

```go
// file: table.fc
package main

comptime func squares() [16]u32 {
	var t [16]u32
	for i := usize(0); i < 16; i++ {
		t[i] = u32(i * i)
	}
	return t
}

// Code 0 is a type code; El turns it into the type [64]u8.
comptime func BufCode(n usize) Code 0 {
	return CodeArray(n, CodeU8())
}

type Buf = El(BufCode(64))

var sq [16]u32 = squares() // computed by the compiler

driver func main(io Stdio) error {
	var b Buf
	k := formatTo(b[:], "sq[15] = %d, len(Buf) = %d\n", sq[15], len(b))
	return io.WriteStdout(b[:k])
}
```

```text
$ finc1 run table.fc
sq[15] = 225, len(Buf) = 64
```

The generated Go contains the finished array literal and the type
`[64]uint8`. When the budget runs out, the error names the evaluation root
and the call stack:

```go
// file: fuel.fc
package main

comptime func spin(n u64) u64 {
	s := u64(0)
	for i := u64(0); i < n; i++ {
		s += i
	}
	return s
}

const Big = spin(10_000_000)

driver func main(io Stdio) error {
	return nil
}
```

```text
$ finc1 check fuel.fc
fuel.fc:12:13: error[E4001]: comptime evaluation ran out of fuel (budget 1000000 steps); call stack (innermost first): spin
error: fincore: invalid number
```

`Code n` / `El` are FinCore's answer to "types as values" without
`Type : Type`: a comptime function computes a *code* for a type at universe
level `n`, and `El` turns it back into a type before code generation.
Mixing levels is E2010. [`examples/comptime_tables.fc`](../examples/comptime_tables.fc)
builds the CRC-32 table at compile time, checks it against the same
algorithm at run time, and sizes its read buffer with
`El(ChunkCode(Pow2(16)))`; `examples/tcp_echo.fc` sizes its connection
buffer pool the same way.

## 11. Capabilities and `driver func main`

A FinCore program has no ambient authority: no `os.Open`, no `net.Dial`,
no `os.Getenv`. The only way to reach the outside world is through a
capability value, and the only source of capabilities is `main`'s parameter
list:

```go
driver func main(fs FS, env Env, io Stdio) error
```

`main` takes any subset of `Net`, `Signals`, `Clock`, `FS`, `Env`, `Stdio`
and `Exec` (each at most once), in any order, and returns `error`; a
non-nil error is printed as `error: <text>` on stderr and the process exits
with status 1. A program that has already reported its failure (say, on
stderr) returns the predeclared `ErrReported` (v0.3): the process then
exits with status 1 without printing anything. Capabilities are
Scoped: they cannot be stored in globals, so a function can only do I/O if
a caller passed it the capability. A function's signature tells you what
it can touch:

| Capability | Methods (v0.2) |
| :--- | :--- |
| `Stdio` | `WriteStdout`, `WriteStderr` |
| `Env` | `ArgsCount`, `Arg(i)`, `GetEnv(key)` |
| `FS` | `ReadFileInto(path, dst)`, `WriteFile`, `IsDir`, `ListSourceFiles`, `MkdirTemp`, `RemoveAll` |
| `Net` | `ListenTCP(addr)`, then `Accept`, `Read`, `Write`, `Close` on the results |
| `Signals` | `NotifyContext(SIGINT, SIGTERM)` |
| `Exec` | `Run(ctx, bin, args, env, dir, stderrBuf)` (driver only) |

Reading a file means providing the buffer: `fs.ReadFileInto(path, buf[:])`
returns `ErrArenaFull` when the file does not fit. There is no stdin
capability in v0.2; [`examples/wordfreq.fc`](../examples/wordfreq.fc) reads
`/dev/stdin` through `FS` when its argument is `-`.

## 12. Structured concurrency

There is no `go f()`. Tasks run inside a `scope` block, which waits for all
of them before it ends, and each task gets a *slot*: exclusive use of one
element of a buffer pool, for the lifetime of the task. The number of
concurrent tasks is the scope's capacity.

```go
// file: squares.fc
package main

const Tasks = 3

var results [8]u64 // task i writes only results[i]

driver func main(io Stdio) error {
	ctx, cancel := NewContext()
	defer cancel()
	var pool [Tasks][16]u64 // per-task scratch, one element per slot

	scope s := NewScope(Tasks) {
		for i := usize(0); i < len(results); i++ {
			slot, err := s.AcquireSlot(ctx, &pool) // blocks while all 3 are busy
			if err != nil {
				break
			}
			n := u64(i) // captured by value
			s.Go(slot, driver func(scratch *[16]u64) {
				for j := usize(0); j < len(scratch); j++ {
					scratch[j] = n * n
				}
				results[n] = scratch[15]
			})
		}
	} // every task has finished here

	var b [96]byte
	k := formatTo(b[:], "%d %d %d ... %d\n", results[0], results[1], results[2], results[7])
	return io.WriteStdout(b[:k])
}
```

```text
$ finc1 run squares.fc
0 1 4 ... 49
```

The checker enforces the shape: the slot is Linear (passed to `Go` or
released with `ReleaseSlot`), a `Conn` captured by the task moves into it,
and the `scratch` pointer cannot escape the task (E3002). Cancellation goes
through `Context`: `NewContext()` returns a context and its Linear
`CancelFunc`, `ctx.WithTimeout(d)` derives a deadline, and
`sig.NotifyContext(SIGINT, SIGTERM)` gives a context that is cancelled by a
signal. Blocking operations (`Accept`, `Read`, `Write`, `AcquireSlot`)
take a context and return when it is cancelled, and `ctx.Done()` is a total
function, so long computations can check it.
[`examples/worker_pool.fc`](../examples/worker_pool.fc) runs a batch of
jobs on four workers, stops accepting work on a signal, and waits a bounded
`DrainHorizon` before it cancels the stragglers;
[`examples/tcp_echo.fc`](../examples/tcp_echo.fc) does the same for network
connections.

## 13. Tooling: tests, `SINCE` and the gates

`finc1 test <dir>` runs a conformance directory: `pass/` programs are built
and run, and their stdout is compared with the `.out` file next to them;
`fail/` programs must produce exactly the diagnostics marked in the source:

```go
// fail_store_param.fc: storing a borrowed slice in a global.
package main

var saved []byte

func keep(b []byte) {
	saved = b // ERROR E3002
}
```

Directives go in the leading comment block: `// FLAGS: --comptime-fuel=100`
passes flags, `// BUILD-ONLY` builds a pass program without running it (a
server, say), and `// SINCE: v0.3` marks a test that needs a newer language
version than v0.2: compilers that are older skip it and count it as
skipped. `finc1 version` prints the version a compiler implements.
[`testdata/README.md`](../testdata/README.md) has the details.

The targets you will use:

| Target | What |
| :--- | :--- |
| `make finc1` | build the current compiler |
| `make examples-test` | build every example; run those with a `.out` file and compare |
| `make tutorial-check` | compile every complete snippet of this tutorial and compare the shown output |
| `make diff-prev` | current compiler against the previous release on the whole corpus |
| `make bootstrap` | the fixed point: the compiler builds itself twice to identical Go |

The examples are part of the corpus: `make diff-prev` compares their
output with the `.out` files under both compilers.

A note on the output of a failed `finc1 check`: the diagnostics are followed
by `error: fincore: invalid number`. That line is the compiler's own exit
error (it reports every failed check with the runtime's `ErrParse`), not
a problem in your program. `bin/finc0` does not print it. The compiler
will return `ErrReported` instead, and the line will go away, once the
bootstrap seed is a release that has `ErrReported` (v0.3.0): until then
compiler/ may not use it.

## 14. Where to go next

| Example | Shows |
| :--- | :--- |
| [`wordfreq.fc`](../examples/wordfreq.fc) | FS/Env capabilities, `StrMap`, `sort.Sort`, overflow handling, T1/T2 loops |
| [`calc.fc`](../examples/calc.fc) | AST as an enum arena, recursive descent with `decreases`, every kind of pattern |
| [`comptime_tables.fc`](../examples/comptime_tables.fc) | CRC-32 and character-class tables built by the compiler, `Code`/`El`, fuel |
| [`worker_pool.fc`](../examples/worker_pool.fc) | `scope`, slots, contexts, signals, graceful shutdown |
| [`state_machine.fc`](../examples/state_machine.fc) | an SMTP session as an exhaustive `match` over `(State, Cmd)` |
| [`tcp_echo.fc`](../examples/tcp_echo.fc) | the reference server: interruptible I/O, drain and hard deadlines |

The self-hosted compiler in [`compiler/`](../compiler) is the largest
FinCore program: fixed arenas for tokens, nodes and types, a parser whose
33 functions share one `decreases(depth)` measure, and no allocation.
