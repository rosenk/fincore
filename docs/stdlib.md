# FinCore standard library (`std/`)

The standard library is a set of ordinary FinCore packages under `std/`.
It is *finitist*: every function is total (proven terminating by T1-T3,
checked by the scope and totality passes like any user code), nothing
allocates, nothing takes a capability, and nothing panics. Capacities are
compile-time constants, given as const type parameters of the container
types and monomorphized by both compilers (decision 11).

| Package | Contents |
| :--- | :--- |
| [`std/vec`](#stdvec) | `Vec[N, T]` bounded vector; `ListArena[NI, NS, T]` store of variable-length runs with mark/commit |
| [`std/maps`](#stdmaps) | `U64Map`, `StrMap`, `Interner` (insertion-ordered open addressing), `Table` (bare bucket array), FNV-1a / mixing hashes |
| [`std/bytes`](#stdbytes) | Searching, comparing and trimming byte slices; `Builder[N]` bounded byte buffer |
| [`std/strconv`](#stdstrconv) | `FormatUint`/`FormatInt`, `ParseUint`/`ParseInt`, `Quote` with FinCore escapes |
| [`std/path`](#stdpath) | `Clean`, `Join`, `Base`, `Dir`, `Ext`, `Split`, `IsAbs` (Go `path` semantics) |
| [`std/sort`](#stdsort) | Generic heapsort `Sort` and insertion sort `Stable` with a `less` function value; `SortU32`/`SortU64`/`SortBytes`, `SearchU32` |

Tests: `testdata/pass/pass_std_<pkg>.fc` (+ `.out`). The packages are part
of the diff-check corpus (`finc0 check std/<pkg>` vs finc1, plus
`stdMutations` in scripts/diffcheck) and of diff-build through the pass
tests and `scripts/diffbuild/cases/cg_std.fc`.

## Importing

```go
import (
	"std/maps"
	"std/vec"
)
```

`import "std/<pkg>"` (any import path that is `std` or starts with
`std/`) resolves to:

1. `$FINCROOT/std/<pkg>` when the environment variable `FINCROOT` is set
   and non-empty (a relative `FINCROOT` is relative to the working
   directory);
2. otherwise `<moduleRoot>/std/<pkg>`, exactly like every other local
   import (decisions 15, 41). The module root is the directory finc runs
   in, so in this repository `std/` simply resolves to the checked-in
   packages.

finc0 and finc1 implement this identically (`resolve.StdDir` in
bootstrap/internal/resolve/load.go, `loadImport` in compiler/main.fc).
Files of packages found through `FINCROOT` are named by the lexically
cleaned path `$FINCROOT/std/<pkg>/<file>.fc` in diagnostics (they are
never made relative to the working directory). A missing package is the
usual E2001 `cannot find package "std/x" (no directory <dir>)` / `(no .fc
files in <dir>)`. `FINCROOT` is unrelated to `FINCORE_ROOT`, which only
tells `build` where the fincore Go module (the runtime) lives.

## Conventions

- **Values, used through pointers.** Containers are Value structs: put
  them in package-level `var`s (large capacities) or locals, and call
  their pointer-receiver methods through an explicit pointer (decision
  10): `vp := &v; vp.Push(x)`. A container nested in a struct field is
  reached the same way: `kp := &m.keys; kp.Lookup(b)`.
- **Explicit type arguments** for generic functions (decision 11):
  `sort.Sort[u32](xs, less)`.
- **Overflow never panics.** An operation that does not fit reports it:
  `Vec.Push`, `U64Map.Put`, `Interner.Intern` and friends return
  `false`/`ok == false`; arena-style types additionally keep a sticky flag
  (`ListArena.Full`, `U64Map.Full`, `Interner.Full`, `StrMap.Full`,
  `Builder.Truncated`) so that a caller can do a batch of work and check
  once, like the compiler/ arenas do. Formatting functions writing into a
  caller's `dst` return the length written and 0 when it does not fit.
- **IDs start at 1.** Where an operation hands out IDs or run starts (the
  `Interner`, `StrMap`, `ListArena`), 0 means "none"/"empty".
- **Slices alias.** Functions returning `[]T` return subslices of their
  argument or of the container's storage, valid until it is modified.
  Nothing is copied unless the name says so.
- **Deterministic.** The hash tables keep their entries densely in
  insertion order and buckets only hold entry numbers: iteration order
  never depends on hashes, capacities or the lookup history.

Complexities below: n = number of elements, k = key length.

## std/vec

### `Vec[N usize, T any]`

A vector of at most `N` elements stored inline; the zero value is empty.

| Method | Result | Cost |
| :--- | :--- | :--- |
| `Push(x T) bool` | appends; `false` (unchanged) when full | O(1) |
| `Pop() (T, bool)` | removes the last element; `false` and zero `T` when empty | O(1) |
| `Len() usize`, `Cap() usize` | length, `N` | O(1) |
| `Full() bool` | `Len() == Cap()` | O(1) |
| `At(i usize) (T, bool)` | element `i`; `false` when `i >= Len()` | O(1) |
| `Set(i usize, x T) bool` | replaces element `i`; `false` when out of range | O(1) |
| `Last() (T, bool)` | last element; `false` when empty | O(1) |
| `Slice() []T` | the elements (aliases the vector) | O(1) |
| `Truncate(n usize)` | shrinks to `n` (no-op if `n >= Len()`), zeroes freed slots | O(Len()-n) |
| `Reset()` | `Truncate(0)` | O(Len()) |

```go
var v vec.Vec[16, u32]
vp := &v
if !vp.Push(7) {
	return ErrArenaFull
}
xs := vp.Slice()
```

### `ListArena[NI usize, NS usize, T any]`

A flat store of `NI` items holding variable-length runs back to back, plus
a LIFO scratch stack of `NS` items on which runs are built. This is the
pattern of compiler/'s `ListStack` + `NodeArena.lists` and of types.fc's
`tpush`/`tmark`/`tcommitStack`/`tcommit`. Runs under construction may
nest, as long as marks are committed in LIFO order. Store slot 0 is
reserved: an empty run is `(0, 0)` and a non-empty run never starts at 0.

| Method | Result | Cost |
| :--- | :--- | :--- |
| `Push(x T)` | pushes onto the stack (sets `Full` when the stack is full) | O(1) |
| `Mark() usize` | current stack height = start of a new run | O(1) |
| `Pending(mark usize) []T` | `stack[mark:]`, the run being built | O(1) |
| `Discard(mark usize)` | pops back to `mark` | O(1) |
| `Commit(mark usize) (u32, u32)` | moves `stack[mark:]` into the store, pops it, returns `(start, len)`; `(0, 0)` if empty or it does not fit (sets `Full`) | O(len) |
| `Store(xs []T) (u32, u32)` | stores a copy of `xs` as a run | O(len(xs)) |
| `Run(start, n u32) []T` | the run as a slice (empty when out of range) | O(1) |
| `At(i usize) T`, `Set(i usize, x T) bool` | single store slot | O(1) |
| `Used() usize`, `Depth() usize` | store slots used (incl. slot 0), stack height | O(1) |
| `Full() bool` | sticky overflow flag | O(1) |
| `Reset()` | empties store and stack, clears `Full` | O(1) |

```go
var lists vec.ListArena[1 << 20, 1 << 16, u32]

la := &lists
mark := la.Mark()
for i := usize(0); i < n; i++ {
	la.Push(child(i)) // may itself build and commit nested runs
}
start, count := la.Commit(mark)
...
kids := la.Run(start, count)
```

## std/maps

All tables use open addressing with linear probing over `NB` buckets.
Every probe loop is bounded by `NB` steps (T1), so any `NB >= 1` works;
choose `NB >= 2*N` for short probes. Lookups are O(1) expected (O(k) for
byte-string keys), O(NB) worst case. There is no deletion; `Reset`
empties a table in O(NB). Keys are concrete types because operators on
type parameters are rejected (decision 31, E2011): hashing and equality
must be written for each key type.

### Hashes

| Function | |
| :--- | :--- |
| `HashBytes(b []byte) u64` | 64-bit FNV-1a; O(k) |
| `HashU64(x u64) u64` | splitmix64 finalizer (a bijection); O(1) |
| `Mix(h u64, x u64) u64` | fold `x` into a running hash, for composite keys: `Mix(Mix(seed, a), b)`; O(1) |

### `U64Map[NB usize, N usize, V any]`

`u64` keys, at most `N` entries; entry `i` is the `i`-th distinct key
inserted.

| Method | Result |
| :--- | :--- |
| `Put(k u64, v V) bool` | sets `k` (inserting if new); `false` and `Full` when `k` is new and there is no room |
| `Insert(k u64) (i usize, added bool, ok bool)` | entry number of `k`, adding it with a zero value if new |
| `Get(k u64) (V, bool)`, `Has(k u64) bool`, `Index(k u64) (usize, bool)` | lookups |
| `Len()`, `Cap()`, `Full()` | entries, `N`, sticky overflow flag |
| `Key(i usize) u64`, `Value(i usize) V`, `SetValue(i usize, v V) bool` | entry `i` (zero values when out of range) |
| `Keys() []u64`, `Values() []V` | entries in insertion order (alias the map) |
| `Reset()` | O(NB) |

### `Interner[NB usize, NBYTES usize, N usize]`

Maps byte strings to dense `u32` IDs `1, 2, 3, ...` in insertion order (0
= none), copying each new string into an internal `NBYTES`-byte arena.
This is compiler/sema.fc's `NameTable`/`intern`/`nameBytes`.

| Method | Result |
| :--- | :--- |
| `Intern(b []byte) (u32, bool)` | ID of `b`, adding a copy if new; `(0, false)` and `Full` when the buckets, the `N` IDs or the byte arena are exhausted (existing strings still resolve) |
| `InternStr(s string) (u32, bool)` | `Intern(strBytes(s))` |
| `Lookup(b []byte) u32`, `LookupStr(s string) u32` | ID or 0, never adds |
| `Bytes(id u32) []byte` | the string (aliases the arena; empty for 0 or unknown IDs) |
| `Len()`, `Used()`, `Full()` | IDs handed out, arena bytes used, sticky overflow flag |
| `Reset()` | O(NB) |

```go
var names maps.Interner[131_072, 1 << 20, 65_536]

func intern(b []byte) u32 {
	np := &names
	id, _ := np.Intern(b) // 0 when full; check np.Full() once at the end
	return id
}
```

### `StrMap[NB usize, NBYTES usize, N usize, V any]`

Byte-string keys (copied into an `Interner`) to values `V`. Lookups take a
`[]byte` (any subslice, e.g. of a source buffer) and never allocate; the
`*Str` variants take a `string`. Entry IDs are the interner's (1-based,
insertion order).

| Method | Result |
| :--- | :--- |
| `Put(k []byte, v V) bool`, `PutStr(k string, v V) bool` | sets `k`; `false` and `Full` when `k` is new and does not fit |
| `Get(k []byte) (V, bool)`, `GetStr(k string) (V, bool)`, `Has(k []byte) bool`, `ID(k []byte) u32` | lookups |
| `Key(id u32) []byte`, `Value(id u32) V`, `SetValue(id u32, v V) bool` | entry `id` |
| `Len()`, `Full()`, `Reset()` | |

Iterate with `for id := u32(1); id < u32(m.Len())+1; id++ { ... }`.

### `Table[NB usize]`

A bare open-addressing bucket array of non-zero `u32` entry IDs for keys
that live elsewhere (compiler/'s `(scope, name)` symbol table, the
hash-consed type table). The caller hashes, compares and drives the
bounded probe loop:

```go
func lookupOrAdd(scp u32, name u32) u32 {
	tp := &symTab
	s := tp.Start(maps.Mix(maps.HashU64(u64(scp)), u64(name)))
	lim := tp.Cap()
	for g := usize(0); g < lim; g++ {
		id := tp.At(s)
		if id == 0 {
			nid := newRecord(scp, name)
			tp.Set(s, nid)
			return nid
		}
		if recs[id].scp == scp && recs[id].name == name {
			return id
		}
		s = tp.Next(s)
	}
	return 0 // every bucket is taken
}
```

| Method | |
| :--- | :--- |
| `Start(h u64) usize`, `Next(s usize) usize` | first slot for a hash (`h % NB`), next slot (wraps) |
| `At(s usize) u32`, `Set(s usize, id u32)` | read / write a slot (0 = empty) |
| `Len()`, `Cap()` | occupied slots, `NB` (the probe-loop bound) |
| `Reset()` | O(NB) |

## std/bytes

| Function | Result | Cost |
| :--- | :--- | :--- |
| `Equal(a, b []byte) bool` | same bytes | O(k) |
| `Compare(a, b []byte) int` | -1, 0, +1 (lexicographic, a proper prefix first) | O(k) |
| `HasPrefix(s, prefix []byte) bool`, `HasSuffix(s, suffix []byte) bool` | | O(len(prefix)) |
| `Index(s, sep []byte) int` | first index of `sep` or -1 (empty `sep`: 0) | O(len(s)·len(sep)) |
| `IndexByte(s []byte, c byte) int`, `LastIndexByte(s []byte, c byte) int` | first / last index of `c` or -1 | O(len(s)) |
| `Contains(s, sep []byte) bool` | `Index(s, sep) >= 0` | |
| `Count(s, sep []byte) usize` | non-overlapping occurrences (empty `sep`: `len(s)+1`) | O(len(s)·len(sep)) |
| `IsSpace(c byte) bool` | ASCII white space: space, `\t \n \v \f \r` | O(1) |
| `TrimSpace(s []byte) []byte` | subslice without leading/trailing white space | O(len(s)) |
| `TrimPrefix(s, prefix []byte) []byte`, `TrimSuffix(s, suffix []byte) []byte` | subslice | O(len(prefix)) |

### `Builder[N usize]`

A byte buffer with capacity `N`; the zero value is empty. Writes are
**all-or-nothing**: a write that does not fit completely is dropped and
sets the sticky `Truncated` flag; what was written before stays intact
(compiler/'s `OutBuf` semantics).

| Method | |
| :--- | :--- |
| `WriteByte(c byte)`, `WriteBytes(p []byte)`, `WriteString(s string)` | append |
| `WriteUint(v u64)`, `WriteInt(v i64)` | decimal (`-` for negatives; the minimum `i64` is handled) |
| `WriteRepeat(c byte, n usize)` | `n` copies of `c` (indentation) |
| `WriteHex(v u64, width usize, upper bool)` | exactly `width` (<= 16) hex digits |
| `Bytes() []byte` | contents (aliases the buffer) |
| `Len()`, `Cap()` | |
| `Truncate(n usize)` | keep the first `n` bytes (does not clear `Truncated`) |
| `Reset()` | empty and clear `Truncated` |
| `Truncated() bool` | a write was dropped since the last `Reset` |

```go
var out bytes.Builder[1 << 16]

func report(name []byte, line u64) {
	o := &out
	o.WriteBytes(name)
	o.WriteByte(':')
	o.WriteUint(line)
	o.WriteByte('\n')
}

driver func main(io Stdio) error {
	report(strBytes("a.fc"), 3)
	op := &out
	if op.Truncated() {
		return ErrArenaFull
	}
	return io.WriteStdout(op.Bytes())
}
```

## std/strconv

Formatting functions return the number of bytes written to `dst`, or 0 if
`dst` is too small or an argument is invalid (every successful result is
at least one byte, so 0 always means failure).

| Function | |
| :--- | :--- |
| `FormatUint(dst []byte, v u64, base u32) usize` | base 2..36, lower-case digits; at most `MaxUintLen` (64) bytes |
| `FormatInt(dst []byte, v i64, base u32) usize` | with `-` for negatives; at most `MaxIntLen` (65) bytes |
| `AppendUint(dst []byte, at usize, v u64) usize` | decimal at `dst[at:]`, returns the new end (`at` if it does not fit) |
| `ParseUint(s []byte, base u32) (u64, bool)` | see below |
| `ParseInt(s []byte, base u32) (i64, bool)` | optional `+`/`-`, then as `ParseUint`, range-checked to `i64` |
| `QuotedLen(s []byte) usize` | length of `Quote`'s output |
| `Quote(dst []byte, s []byte) usize` | see below |

All are O(length of the text).

`ParseUint` accepts base 2..36 (digits `0-9`, `a-z`, `A-Z`) or base 0: then
a prefix picks the base (`0x`/`0X` 16, `0b`/`0B` 2, `0o`/`0O` 8, otherwise
decimal; leading zeros are decimal) and underscores are allowed between
digits and after a prefix, as in Go literals (`1_000`, `0x_ff`; not `_1`,
`1_`, `1__0`). No sign, no white space; `ok` is false on any invalid digit,
an empty number or overflow.

`Quote` writes `s` as a double-quoted FinCore string literal: `\n \r \t
\\ \"` for those bytes, `\xHH` (lower-case hex) for every other byte below
0x20, for 0x7f and for every byte >= 0x80; everything else is copied. The
result is valid FinCore source denoting exactly `s` (spec 2.6). Note that
this differs from Go's `strconv.Quote` (no `\a`, `\u`, UTF-8 pass-through).

## std/path

Lexical processing of slash-separated paths with the semantics of Go's
`path` package (compiler/'s `cleanPath`/`cleanJoin`).

| Function | Result |
| :--- | :--- |
| `Clean(dst, src []byte) usize` | Go `path.Clean`: one slash between elements, no `.`, `..` cancels the previous element, no `..` above a rooted path, no trailing slash, `""` becomes `"."`. Needs `len(dst) >= max(len(src), 1)` (the result is never longer), returns 0 otherwise. `dst` may be `src` itself (in-place cleaning). O(len(src)) |
| `Join(dst, a, b []byte) usize` | `Clean(a + "/" + b)` ignoring empty elements (`Join("", "")` is `""`, length 0). Needs `len(dst) >= len(a)+1+len(b)`; `dst` must not overlap `a` or `b` |
| `Base(p []byte) []byte` | last element after removing trailing slashes; `""` gives `"."`, all slashes give `"/"` |
| `Dir(dst, p []byte) usize` | `Clean` of everything up to the last slash (`Dir("a") == "."`, `Dir("/a") == "/"`); needs `len(dst) >= max(len(p), 1)` |
| `Ext(p []byte) []byte` | suffix from the final `.` of the last element, or empty |
| `Split(p []byte) (dir, file []byte)` | split after the final slash; `dir + file == p` |
| `IsAbs(p []byte) bool` | starts with `/` |

Examples: `Clean("a/../..") == ".."`, `Clean("//a//b/./c/..") == "/a/b"`,
`Clean("/../a") == "/a"`, `Join("a", "../../c") == "../c"`.

## std/sort

| Function | |
| :--- | :--- |
| `Sort[T any](xs []T, less func(a T, b T) bool)` | heapsort: not stable, O(n log n) comparisons worst case, O(1) space |
| `Stable[T any](xs []T, less func(a T, b T) bool)` | insertion sort: stable, O(n²) worst case, O(n) on sorted input |
| `IsSorted[T any](xs []T, less func(a T, b T) bool) bool` | O(n) |
| `SortU32(xs []u32)`, `SortU64(xs []u64)` | ascending |
| `SortBytes(xs [][]byte)` | lexicographic order of the byte strings |
| `SearchU32(xs []u32, x u32) usize` | first index with `xs[i] >= x` in an ascending slice (`len(xs)` if none); O(log n) |

`less` must be a total function value (`func`, not `driver func`): a named
function, a function literal or a variable holding one. It should be a
strict weak order; with any other function the sorts still terminate
after the same number of steps, only the order is unspecified. Function
types need parameter names (grammar: `func(a T, b T) bool`).

```go
type Item struct {
	key u32
	tag u8
}

func byKey(a Item, b Item) bool {
	return a.key < b.key
}

sort.Stable[Item](items[:], byKey)
sort.Sort[u32](xs, func(a u32, b u32) bool {
	return a > b
})
```

Totality: a call through a function value is an edge to every escaping
total function of the program (decision 56). A comparison function that
itself (directly or indirectly) calls `Sort`/`Stable` therefore closes a
call cycle without a measure and is rejected.
