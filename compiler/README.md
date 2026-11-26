# compiler/ — the self-hosted FinCore compiler (Stage 1+, `finc1`)

Milestone M6 (front half): arenas, diagnostics, lexer, parser, AST dump and
the `finc` driver, written in FinCore. It is a single package `main`
spread over several files (spec 9.5.6); there are no local imports.

| File | Contents |
| :--- | :--- |
| `arena.fc` | Capacities, `SourceArena`, `Token`/`TokenArena`, `Node`/`NodeArena`, `ListStack`, `OutBuf` |
| `diag.fc` | Diagnostic codes and the `file:line:col: error[E####]: msg` header; UTF-8 decoding and Go-compatible quoting (`strconv.Quote`, `%q`, `%c`) |
| `lexer.fc` | Token kinds, keyword lookup, total lexer with semicolon insertion, literal/escape validation, E1001/E1002 messages |
| `ast.fc` | Node kinds, the per-kind slot `layout`, post-order NodeID assignment, the `--dump-ast` dumper and the source printer |
| `parser.fc` | Recursive-descent parser for the full grammar of spec section 3 |
| `main.fc` | `driver func main(fs FS, env Env, io Stdio, exec Exec) error`: `parse [--dump-ast] <file.fc \| dir>`; the other subcommands print "not implemented" |

## Architecture

**Arenas (spec 9.4).** All bulk storage is in package-level `var`s holding
Value structs with fixed arrays (`sources`, `tokens`, `nodes`,
`listStack`, `outBuf`, `errBuf`, `tmpBuf`, `fileNames`). Globals may not be
pointers or Scoped (S1), so the driver takes their addresses (`np :=
&nodes`) and passes the pointers to Scoped helper structs (`Parser`,
`Dumper`, `Printer`) that live in the driver's frame or in a callee's
frame. Capacities:

| Arena | Capacity |
| :--- | :--- |
| `SourceArena` | `MaxFiles = 64` files, `MaxSourceBytes = 4 MiB` in total |
| `TokenArena` | `MaxTokens = 262_144` tokens (reset per file) |
| `NodeArena` | `MaxNodes = 131_072` nodes, `MaxListItems = 262_144` list items (whole package) |
| `ListStack` | `MaxListItems` (scratch for lists under construction) |
| `OutBuf` | `MaxOutputBytes = 8 MiB` (stdout dump per file, stderr diagnostics, string scratch) |

Overflow never panics: `alloc`/`push`/`commit`/`put*` set a sticky `full`
flag (writes go to the reserved slot 0 or are dropped) and the driver
reports `finc1: <file>: compiler capacity exceeded (...)` and returns
`ErrArenaFull`. `compiler/` itself parses to about 25k nodes and a 1.7 MB
dump.

**IDs.** `NodeID`, list starts and lengths, token offsets and positions are
`u32`; arena index 0 means "none". A `Node` has six single-child slots
(`a`..`f`), two list slots (`l1`, `l2`, ranges in `NodeArena.lists`), two
flag bits, a small `op` field (operator, modifier or literal kind), a text
span `tOff/tLen` (absolute offset into `SourceArena.bytes`: identifier name
or literal spelling) and `ival` (integer or char value). The meaning of the
slots per kind is in the header comment of `ast.fc`. Lists are built
on the `ListStack` (lists nest in LIFO order) and committed into
`NodeArena.lists` in one piece when complete.

**Dump identical to finc0.** `finc0 --dump-ast` prints finc0's Go structs by
reflection: fields in declaration order, scalars inline in the header, nil
and empty fields omitted, NodeIDs dense in post-order of `ast.Children`
across all files of the package. `ast.fc`'s `layout(kind, i)` lists each
kind's node fields with their finc0 field names in exactly that order, so it
drives both `assignIDs` (post-order numbering, run after each successful
file like finc0's `assignIDs`) and `Dumper.dumpNode`. `Dumper.scalars`
reproduces the scalar fields (`Name="x"`, `Op=+`, `Kind=int`, `Int=255`,
`Str="..."`, `Modifier=driver`, bool flags); strings are quoted with a
port of `strconv.Quote`. Nodes that finc0 builds and then discards (the
operand of a struct literal that becomes its `NamedType`) stay orphaned in
the arena and never get an ID, exactly as in finc0.

**Diagnostics identical to finc0.** The parser mirrors finc0 function by
function, including `exprLev` (struct literals in control clauses), the
`nest` counter (`MaxNesting = 1000`), every message text and every error
position. finc0 stops at the first error of a file by panicking. Here the
first error is written to `errBuf`, `failed` is set and the current token
becomes a sticky ILLEGAL token, so every expectation after it fails
silently and every loop exits on `failed`. Both parsers execute the same
steps up to the first error, so they report the same diagnostic. Messages
that embed source (`non-name a.b on left side of :=`, `cannot use x := 1 as
value in if condition`) use `Printer`, a port of finc0's `ast.Format`. The
lexer stores a reason code on ILLEGAL tokens, and the message is rendered
when the parser reaches the token, as finc0 does. Directory handling
follows finc0's `ParseFiles`: files are parsed in sorted order with one
diagnostic per failing file, and a file with a mismatched `package` clause
is reported and left out, but it still consumes NodeIDs.

**Validation.** `go run ./bootstrap/cmd/finc0 parse compiler/` exits 0.
During development the sources were also transliterated mechanically to Go
(drop `driver`/`decreases`, alias the scalar types, map the builtins to
`fincrt`) and run against finc0. `parse --dump-ast` output and diagnostics
were byte-identical on all of `testdata/`, `examples/`, finc0's parser
fixtures, `compiler/` itself, all 50 error cases from finc0's
`TestErrors`, about 65 extra lexer and grammar edge cases, nesting-limit
cases, directory cases (package mismatch, several failing files, no `.fc`
files) and 3,000 randomly mutated sources.

## Language subset used (conservative on purpose)

- Only `func` (total) and `driver func`; no `comptime`, generics, enums,
  `match`, closures, `defer` or `scope`.
- No `iota`: every constant is spelled out. Token, node and operator kinds
  are untyped integer constants compared against `u8` fields.
- No array literals and no lookup tables: name tables are `if` chains that
  return string literals.
- Every loop is T1: `for g := usize(0); g < lim; g++` where `lim` is a
  local (or a constant) that the body never assigns. Loops that consume
  input use a "fuel" counter bounded by the input length (source bytes for
  the lexer, token count for the parser) and `break` when done.
- Every recursive function carries `decreases(depth)`, opens with
  `if depth == 0 { return ... }` and passes `depth - 1` (or `depth-1`)
  on every call inside its SCC (T3). There are three SCCs: the parser
  (33 methods on `*Parser`), the printer (4 methods on `*Printer`), and
  two directly recursive methods (`NodeArena.assignIDs`,
  `Dumper.dumpNode`). Parser entry points start the measure at
  `ParseDepth = 65_536`, far above the frames reachable within
  `MaxNesting`. Tree walks start at `WalkDepth = MaxNodes + 1`, because a
  tree is never deeper than its node count. Recursive method names are
  unique across receiver types, so even a name-based call graph gives the
  same SCCs.
- Methods with pointer receivers are always called through a pointer
  variable (decision 10). For globals this means `eb := &errBuf;
  eb.putStr(...)`.
- No results are discarded silently: `_ = f()` is used where needed.

## Where finc0's future checker may struggle

1. **Large Value globals.** `NodeArena` is about 12 MB and `OutBuf` 8 MB.
   They are only ever accessed through `&global` or by field and index
   paths (`nodes.items[i].a`, `sources.files[i].path = ...`). The type
   checker and codegen must not copy them by value.
2. **Slices of arena memory.** `sources.bytes[st:st+n]` is stored in
   `Lexer.src`, `Parser.src`/`fsrc` and `Dumper.src`, and methods return
   sub-slices such as `p.fsrc[a:b]`. Spec 4.1 calls `[]T` a Value. A scope
   checker that tracks slices of arrays as borrowed pointers must still
   accept this: every slice points into a global (origin depth 0).
3. **Scoped structs built from pointer fields of a receiver.** In
   `Parser.putNode`, `Printer{na: p.na, out: p.diag, ...}` is built in a
   callee from pointer fields of `*Parser`. This is a downward assignment
   (S2) and must be accepted.
4. **Field writes through pointer chains.**
   `p.na.items[usize(id)].l1s = s` writes through a pointer field, then an
   array element, then a struct field.
5. **`int` with negative constants.** `exprLev int` uses `-1`, `++` and
   `--`.
6. **Shift counts of type `u64`** (`v >> shift`) and arithmetic on
   `copy`'s `usize` result (`k += copy(...)`).
7. **`string` values** in struct fields and global arrays
   (`SourceFile.path`, `fileNames [MaxFiles]string`), and passing
   `fileNames[:]` to `FS.ListSourceFiles`.
8. **Multi-value methods inside the SCC.** `simpleStmtOrExpr` returns
   `(u32, u32)`, and its guard is `return p.tooDeep(), 0`.
9. **A loop counter of type `u8`** with a constant bound
   (`for k := u8(TkPackage); k < TkDecreases+1; k++`).
10. **Error sentinels.** Syntax errors return `ErrParse` (the runtime
    extension) and unimplemented subcommands return `ErrExec`. How a
    returned error maps to the process exit status is up to the generated
    `main` (finc0 uses 1 for errors and 2 for usage).

## Known gaps and differences from finc0

- **Capacity limits.** A single file whose dump exceeds 8 MiB fails with
  "capacity exceeded (output buffer)". For example, a 20,000-term `1+1+...`
  chain dumps to about 800 MB because indentation grows quadratically.
  finc0 has no limits.
- **`strconv.IsPrint` is approximated** for non-ASCII runes in quoted dump
  strings: control, format, separator and private-use ranges are escaped,
  everything else is printed raw. This only matters for unusual Unicode in
  string literals.
- **I/O error texts are approximations** of Go's `os` errors (for example
  `finc1: stat x.fc: no such file or directory`, `I/O error`).
- **Flag parsing** handles only `-dump-ast` / `--dump-ast[=true|false]`
  and `--`. Go's `flag` package accepts more spellings.
- **Path cleaning.** Directory entries are joined with a port of
  `filepath.Join` (lexical Clean). finc0 also skips non-regular entries
  that are not symlinks. `fincrt.ListSourceFiles` only skips directories.
- **Usage texts** say `finc1` where finc0 says `finc0`.
