# compiler/ — the self-hosted FinCore compiler (Stage 1+, `finc1`)

Milestone M6 (front half): arenas, diagnostics, lexer, parser, AST dump and
the `finc` driver, written in FinCore. Milestone M7 part A (semantic
foundation): name resolution, type checking and the comptime interpreter,
wired as `finc1 check`. It is a single package `main` spread over several
files (spec 9.5.6); there are no local imports.

| File | Contents |
| :--- | :--- |
| `arena.fc` | Capacities, `SourceArena`, `Token`/`TokenArena` (`vec.Vec`), `Node`/`NodeArena` with its child lists `NodeLists` (`vec.ListArena`), `OutBuf` (`bytes.Builder`) |
| `diag.fc` | Diagnostic codes and the `file:line:col: error[E####]: msg` header; UTF-8 decoding and Go-compatible quoting (`strconv.Quote`, `%q`, `%c`) |
| `lexer.fc` | Token kinds, keyword lookup, total lexer with semicolon insertion, literal/escape validation, E1001/E1002 messages |
| `ast.fc` | Node kinds, the per-kind slot `layout`, post-order NodeID assignment, the `--dump-ast` dumper and the source printer |
| `parser.fc` | Recursive-descent parser for the full grammar of spec section 3 |
| `sema.fc` | Semantic data model: interned names, symbols, scopes and the (scope, name) symbol table, packages, u32 lists and list cells, per-node resolver tables, the sorted diagnostic list, AST accessors |
| `resolve.fc` | Port of `resolve`: universe (predeclared entities), package declarations, binder (defs/uses/captures, E2001/E2002) |
| `types.fc` | Typed model: hash-consed `TypeArena`, predicates, type strings, value classes, builtin named types and capability methods, `FuncInfo`/`Call`/`Selection`/`Inst` records, per-node type tables, instantiation and substitution |
| `values.fc` | Compile-time values: 128-bit integers, value slots (persistent region + comptime heap), zero values, copying, constant arithmetic, equality, value strings, Code folding |
| `checker.fc` | Type checker part 1: operands, assignability and untyped constants, package-level declarations, type expressions, recursive types, `main`, finishing |
| `check_expr.fc` | Type checker part 2: expressions and calls (builtins, conversions, methods, capability methods, variants, generic instances, comptime folding) |
| `check_stmt.fc` | Type checker part 3: statements, match patterns, terminating statements |
| `check_match.fc` | Type checker part 4 (v0.2 sum types, finc0's types/match.go): match statements and expressions, or-patterns, exhaustiveness and reachability (E2012), untyped match expressions |
| `comptime.fc` | Fuel-bounded comptime interpreter (the checker's evaluator) |
| `totality.fc` | Totality checker part 1 (finc0's `totality`: totality.go, invariant.go, loops.go): pre-order program index, E5001 effects, root facts, loop invariance, T1, T2 setup |
| `totality_flow.fc` | Totality checker part 2 (flow.go, T2/T3 walks): abstract states, bounds, the path-sensitive walker |
| `totality_rec.fc` | Totality checker part 3 (recursion.go): call graph, iterative Tarjan, measures, E4002/E4004 |
| `main.fc` | `driver func main(fs FS, env Env, io Stdio, exec Exec) error`: `parse [--dump-ast] <file.fc \| dir>`, `check [--comptime-fuel=N] <file.fc \| dir>` (hidden flags `--stats`, `--passes=types\|all\|totality`; the loader for local imports lives here); dispatches build/run/test to build.fc |
| `codegen.fc` | Code generator part 1 (finc0's `ir` + `codegen/golang` gen.go, types.go, values.go): emission buffers, Go names, usage/scope/dead-code analyses, match arm lowering, monomorphization, types, constant values, declarations, entry point |
| `codegen_expr.fc` | Code generator part 2 (expr.go): expressions and calls |
| `codegen_stmt.fc` | Code generator part 3 (stmt.go): bodies, statements, scope barriers, labeled breaks, returns through scopes |
| `format.fc` | The v0.2 builtin `formatTo` (finc0's types/format.go plus the formatTo parts of codegen and comptime): lazy format parser, argument checking, Go lowering over `fincrt.Fmt`, comptime writer (port of runtime/fincrt/fmt.go) |
| `codegen_enum.fc` | Code generator part 4 (v0.2 sum types, enum.go): compact enum layouts (LIFO layout pools), match switches, match expressions, `return match` |
| `build.fc` | `build`, `run`, `test` (finc0's build.go / test.go): temporary module, gofmt and `go build` via Exec, test conventions of testdata/README.md |

## Architecture

**Arenas (spec 9.4).** All bulk storage is in package-level `var`s holding
Value structs with fixed arrays (`sources`, `tokens`, `nodes`, `outBuf`,
`errBuf`, `tmpBuf`, `fileNames`); where a std container fits (decision
59, docs/stdlib.md) it is used through a type alias (`type OutBuf =
bytes.Builder[MaxOutputBytes]`, `TokenArena`, `NodeLists`). Globals may not be
pointers or Scoped (S1), so the driver takes their addresses (`np :=
&nodes`) and passes the pointers to Scoped helper structs (`Parser`,
`Dumper`, `Printer`) that live in the driver's frame or in a callee's
frame. Capacities:

| Arena | Capacity |
| :--- | :--- |
| `SourceArena` | `MaxFiles = 128` files, `MaxSourceBytes = 4 MiB` in total |
| `TokenArena` | `MaxTokens = 262_144` tokens (reset per file) |
| `NodeArena` | `MaxNodes = 524_288` nodes; `lists`: `MaxListItems = 1_048_576` list items (whole program) plus a scratch stack of `MaxListItems` for lists under construction |
| `OutBuf` | `MaxOutputBytes = 8 MiB` (stdout dump per file, stderr diagnostics, string scratch) |

Overflow never panics: `alloc` and the std containers set a sticky flag
(`full`, `Full()`, `Truncated()`; writes go to the reserved slot 0 or are
dropped) and the driver
reports `finc1: <file>: compiler capacity exceeded (...)` and returns
`ErrArenaFull`. The semantic arenas are listed in "Semantic model" below.
`finc1 check --stats <path>` (hidden flag) prints the usage of every arena.

**IDs.** `NodeID`, list starts and lengths, token offsets and positions are
`u32`; arena index 0 means "none". A `Node` has six single-child slots
(`a`..`f`), two list slots (`l1`, `l2`, runs in `NodeArena.lists`), two
flag bits, a small `op` field (operator, modifier or literal kind), a text
span `tOff/tLen` (absolute offset into `SourceArena.bytes`: identifier name
or literal spelling) and `ival` (integer or char value; for a BinaryExpr
the operator position `line<<32 | col`, finc0's `OpPos`). The meaning of the
slots per kind is in the header comment of `ast.fc`. Lists are built
on the scratch stack of `NodeArena.lists` (lists nest in LIFO order) and
committed into its store in one piece when complete.

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

## Semantic model (M7): what `finc1 check` builds

The semantic phases mirror finc0's `resolve.Program` and `types.Info`, but
every table is a fixed array in a package-level `var`, indexed by u32 IDs
(0 = none), and every pass is a set of free functions over those globals
(total functions may read and write globals; no Scoped helper struct is
needed). Later passes (scope checker, totality checker, codegen) read the
same tables. Unless stated otherwise, per-node tables are indexed by the
**NodeArena index** of the node, not by finc0's post-order NodeID (that one
is still available as `Node.id`).

### Pipeline (`checkPath` in main.fc)

1. `parseSources` parses the root file/directory (like `parser.ParsePath`);
   `visitPackage` / `loadImport` load local imports depth-first relative to
   the current directory (module root), exactly like `resolve/load.go`:
   E2001 invalid/missing/empty packages, E2006 import cycles, files of
   imported packages are named `importpath/file.fc`. `std/...` imports
   resolve against `$FINCROOT` when it is set (read once by `main` into
   `fincRoot`; files are then named `<cleaned $FINCROOT/importpath>/file.fc`),
   like finc0's `resolve.StdDir` (decision 58). `pkgs.order` is the
   dependency order (root last), `pkgs.root` the root package.
2. If any diagnostic exists, print them (sorted) and stop. Otherwise
   `resolveProgram` (resolve.fc): universe, `declarePackage` for all
   packages, then `bindPackage`.
3. If the resolver reported anything, stop. Otherwise `checkProgram`
   (checker.fc): `objDecl` of every package-level symbol, `ensureBody` of
   every function, `checkRecursiveTypes`, `checkMain`, `finishInfo`.
4. Diagnostics are collected in `diags` (sema.fc) and printed sorted by
   (file path, line, column, code, message) with exact duplicates removed,
   as `diag.List.Sort` does. `--comptime-fuel=N` sets `evalFuel`.
5. If the type checker reported nothing, the semantic passes run in
   order and the first pass with diagnostics stops the pipeline
   (finc0's `check.Passes`): the scope pass, then `totalityProgram`
   (totality*.fc). The hidden flag `--passes=types|all|<pass>[,<pass>]`
   selects them like finc0's oracle flag.

The later passes (scope, totality) must run only when `diags.count == 0`
after step 3, and may then assume a well-typed program: every expression
has a type, every CallExpr has a `Call`, every identifier use a symbol.

### Names, symbols, scopes, packages (sema.fc, resolve.fc)

| Table | Contents |
| :--- | :--- |
| `names` (`maps.Interner`) | Interned spellings (`NameID`); `idName(identNode)` caches the NameID of an Ident; `nameBytes(id)`, `nameBlank`, `nameMain` |
| `syms.items[SymID]` (`Sym`) | `kind` (SymConst/SymType/SymVar/SymFunc/SymBuiltin/SymTypeParam/SymPackage), `varKind` (VarGlobal/Local/Param/Recv/Binding/Scope), `builtin` (predeclared entity, `BT*`/`BC*`/`BF*` in resolve.fc), `name`, `ident` (defining Ident node), `decl` (declaring node, as `resolve.Symbol.Decl`), `pkg`, `fn` (enclosing FuncDecl/FuncLit node of locals; 0 = package level), `imported` (SymPackage), `recvType` (NameID of the receiver type of methods) |
| checker fields of `Sym` | `typ` (SymTypes; untyped constants hold their default type), `val` (SymValues: value slot), `untyped` (UntypedConsts), `method` (MethodOf), `objState`/`body` (checker bookkeeping) |
| `scopes` (`vec.Vec` of `ScopeRec`, by ScopeID) | `parent`, `kind`, `node`; all bindings are in one open-addressing table `symTab` (`maps.Table`) keyed by (scope, name): `lookupLocal(scope, name)`, `lookupChain(scope, name)` |
| `pkgs.items[PkgID]` (`Pkg`) | `name`, `path` (import path, "" for root), `scp` (package scope), source files `[f0, f1)` (only `sources.files[i].ok` ones belong to it), `impS/impN` (imports), `declS/declN` (package-level symbols in source order, methods included), `methS/methN` (methods); `fileScope[file]`, `filePkg[file]` |
| `defs[node]`, `uses[node]` | Prog.Defs / Prog.Uses; `symbolOf(ident)` |
| `litCapHead[funcLit]` | Prog.Captures: cell list of captured outer locals, first-use order |
| `builtinSym[b]`, `universe` | Universe symbols of the predeclared entities |

Struct field names, enum variant names and method selector names are not
symbols (see `Selection`), exactly as in finc0. Blank `_` locals get a
symbol but no `defs` entry.

### Types (types.fc)

`typs.items[TypeID]` (`TypeRec`), see the field table in types.fc. Basic
types have fixed IDs `TyBool`..`TyUntypedNil` (`TyInvalid = 1`). Structural
types and generic instances are **hash-consed**, so `types.Identical(a, b)`
is `a == b`. Named types (`newNamed`), type parameters and opaque resource
types are unique records. Helpers: `underlying`, `basicKind`, `isInteger`,
`isUnsigned`, `isUntyped`, `isBoolean`, `isString`, `intInfo`, `defaultOf`,
`derefType`, `elemOf`, `nfields/fieldName/fieldType/fieldByName`,
`nvariants/variantName/variantPay*/variantByName`, `sig*` (effect,
variadic, params, results, type params, `sigResultType`), `namedOrigin`,
`namedSym`, `builtinOf`, `isResource`, `isCapability`, `comparable`,
`hasTypeParam`, `putType`/`mtype` (types.Type.String). Lists (fields as
(name, type) pairs, variants as (name, payload start, payload len) triples,
params, results, type args) live in `typs.lists`.

**Classes (spec 4.1):** `classOfT(t)` returns `ClassValue`, `ClassScoped` or
`ClassLinear` (cached on named types, like `types.ClassOf`); `isScoped`,
`isLinear`.

### Typed model records

| Table | finc0 equivalent |
| :--- | :--- |
| `ntype[node]` | `Info.TypeOf`: final type of expressions (untyped constants defaulted), Tuple types of multi-result calls, 0 for calls without results and builtin/package names; the type of type nodes; the instantiated signature for a method selector used as callee |
| `nmode[node]` | `Info.KindOf` (`KValue`, `KVar`, `KConst`, `KType`, `KNoValue`, `KBuiltin`, `KFunc`, `KPackage`, `KVariantCtor`) |
| `nval[node]` | `Info.ValueOf`: value slot of constants and folded comptime calls (`VTuple` for multi-result ones) |
| `ncall[callExpr]` → `callTab.items[CallID]` | `Info.CallOf`: `kind` (CallFunc/CallMethod/CallBuiltinMeth/CallBuiltin/CallConversion/CallVariant/CallValue), `callee` (function/method symbol, or the builtin's universe symbol), `builtin`, `method`, `recv` (receiver expression), `recvType`, `taS/taN` (TypeArgs), `sig` (instantiated signature), `effect` (EffComptime < EffTotal < EffDriver), `folded`, `conv`, `variantType` / `variantEnum` / `variant`, and the scope-checker hints `resourceCtor`, `consumesRecv`, `consumesArg0` (`ConsumesArgs == [0]`) |
| `nsel[selectorExpr]` → `selTab.items` | `Info.SelectionOf`: `kind` (SelField/SelMethod/SelVariant), `recv` (type of x), `indirect`, `ctype`+`index` (field of a struct type / variant of an enum type), `method`; no entry for package-qualified names |
| `ninst[node]` → `insts.items[InstID]` | `Info.InstanceOf` / `Info.InstanceList` (records in first-use order): `sym`, `named` (type instances), `argS/argN`, `sig` (function instances), `generic` |
| `nvarEnum[ctorPattern]`, `nvarIdx` | `Info.VariantOf` |
| `nfunc[funcDecl or funcLit]` → `funcs.items[FuncID]` | `Info.FuncOf`; `funcOfSym(sym)` = `Info.FuncOfSym` |
| `funcs.orderS/orderN` (ulists) | `Info.Funcs`: declarations in package/source order, each followed by its literals depth-first |
| `funcs.main` | `Info.Main` |
| `namedTypes[0:nNamed]` | `Info.Named` |
| `meths.items[MethodID]` | `types.Method`: `name`, `recv` (origin Named), `ptrRecv`, `builtin` (`BM*`, capability/resource methods), `rtpS/rtpN` (RecvTypeParams), `sig` (0 for the generic Scope methods), `sym`, `decl`; `methodsOf(named)` lists the methods of a type (cached on the type) |

`FuncInfo`: `sym`, `decl`, `lit`, `outer`, `effect`, `sig`, `method`,
`recvSym`, `parS/parN` (parameter **Ident nodes** in order; the symbol is
`defs[ident]`, 0 for `_`), `tpS/tpN` (TypeParams), `measure`
(the `decreases(M)` expression), `body`, `callH/callT` (cell list of the
CallExpr nodes of the body in source order, excluding nested literals),
`litH/litT` (cell list of directly nested literal FuncInfos); captures of a
literal are `litCapHead[lit]`. Call-graph edges: for each call with kind
CallFunc or CallMethod the callee is `funcOfSym(call.callee)` (methods are
resolved by receiver type).

### Values (values.fc)

`Val` is a by-value record: `VInt` (exact 128-bit two's complement in
`hi/lo`; `flag&1` marks values outside ±2^127), `VBool`, `VString`
(`strPool` span), `VError` (sentinel builtin, 0 = nil), `VNil`, `VArray`,
`VSlice`, `VStruct`, `VEnum`, `VTuple`, `VCode`, `VFieldCode`, `VPtr`.
Aggregates refer to contiguous **value slots** in `vals`: the persistent
region `[1, VtabCap)` holds every value recorded in the typed model; the
comptime heap `[VtabCap, MaxValSlots)` is reset after each evaluation root
once its result has been copied with `persistVal`. Operations:
`binaryOpVal`, `unaryOpVal`, `equalVals`, `representable`, `wrapVal`,
`valUint64`, `zeroVal`, `copyV`, `putVal`, `foldCode`, `codeLevelOf`.

### Recursion and scratch stacks

The checker, the expression and statement checkers and the interpreter form
one recursive SCC (about 80 functions). Each member takes `dp usize` with
`decreases(dp)`, returns at once when `dp == 0` (after `tooDeep()`, which
sets `semaFull`), and passes `dp-1` inside the SCC; entry points pass
`SemaDepth = 1_000_000`. Smaller SCCs (binder, type strings, classes,
substitution/instantiation, value copies, terminating statements, AST
walks) follow the same pattern. Lists under construction use LIFO scratch
stacks that every function leaves balanced: `typs.stack` (type lists,
`tpush`/`tcommitStack`/`mk*`), the `ulists` stack (`umark`/`upush`/`ucommit`), `opStack`
(operands of calls and assignments), `vstack` (comptime argument values),
`rstack` (assignment references).

### Comptime interpreter (comptime.fc)

`callComptime(fn, argMark, dp)` is `types.Evaluator.CallComptime`: a fresh
fuel budget per evaluation root, one unit per call, loop iteration and index
operation, `CtMaxDepth = 10_000` frames, E4001 messages with the collapsed
call stack, E4003/E2002/E5001 as finc0. finc0 unwinds failures with panic;
here the first failure sets `ifail` (`ifailCode`, `ifailMsg`; code 0 = already
reported) and every step returns as soon as it sees it. Nested roots (a
comptime call folded while the checker checks a body on behalf of the
interpreter) save and restore the interpreter state. Frame variables live in
`fvSym/fvSlot` ranges; a variable's cell is a heap slot and `&x` marks it so
that a redeclaration allocates a fresh cell (finc0's map semantics).

### Scope checker (scope.fc, linear.fc, passes.fc)

Port of finc0's `scope` package (S2-S5: E3002-E3005; rules in
`bootstrap/internal/scope/doc.go`). `passes.fc` holds `runPasses`, called by
`checkPath` after `checkProgram` when there are no diagnostics; it runs the
enabled passes in order and stops after the first one that reports
anything (finc0's `check.Passes`). The hidden flag
`check --passes=types|all|<pass>[,<pass>]` mirrors finc0's oracle flag
(known passes: `scope`).

- `scope.fc` (finc0 scope.go): `scopeCheck` checks every declaration with a
  body in `funcs.order`. Origins (depth, region) are packed into a u64
  (`depth << 32 | region SymID`). finc0's per-declaration maps (depth,
  content, addr, fixed) are per-symbol arrays stamped with an epoch per
  declaration; the value flows are a flat array; the content fixpoint is
  bounded by the height of the origin lattice. The walker (`wStmt`,
  `wExpr`, ...) runs twice (collect, check) with globals for depth, function
  depth and mode. finc0's one-diagnostic-per-(code, position) map is a
  scan of the recorded diagnostics (`scReport`).
- `linear.fc` (finc0 linear.go): every function body (and each nested
  literal body) is a flow record; flows nest LIFO, so their variables,
  frames and loop contexts live on shared stacks with per-flow bases. An
  abstract environment is an EnvID into `lnEnv` (dead flag + a range of
  state bytes and tied error symbols), a mutable handle like finc0's
  `*env`; the arena is reset per declaration (1 Mi envs, 4 Mi slots).
  Owned/borrowed lists and assignment operands are passed on the `lnTmp`
  scratch stack. Loop fixpoints keep finc0's 65-iteration cap.

`make diff-check-scope` runs scripts/diffcheck with `-passes scope`:
`finc0 check --passes=scope` vs `finc1 check --passes=scope` on the
diff-check corpus and snippets plus the 60 cases of finc0's
`scope_test.go` (wrapped like its `run` helper) and
`scripts/diffcheck/cases/scope_edges.fc`. `make diff-check` passes
`--passes=types` to both compilers.

### Semantic capacities

| Arena | Capacity | `compiler/` (18k lines) |
| :--- | :--- | :--- |
| `names` | `MaxNames = 65_536`, `MaxNameBytes = 1 MiB` | 1.9k |
| `syms` | `MaxSyms = 131_072`, `SymBuckets = 524_288` | 4.9k |
| `scopes` | `MaxScopes = 524_288` | 6.7k |
| `ulists` (store + stack) | `MaxULists = 1_048_576` each | 3.3k |
| `cells` | `MaxCells = 524_288` | 7.7k |
| `typs` | `MaxTypes = 131_072`, `TypeBuckets = 262_144`, `MaxTypeLists = 1_048_576` | 501 / 1.6k |
| `meths`, `insts`, `funcs` | 16_384, 8_192, 65_536 | 164, 0, 644 |
| `callTab`, `selTab` | 262_144 each | 7.7k, 5.1k |
| `vals` | `VtabCap = 1 Mi` persistent + `HeapCap = 2 Mi` heap slots | 9.4k |
| `strPool` | `MaxStrBytes = 4 MiB` | 16 KB |
| `diags` | `MaxDiags = 8_192`, `MaxDiagBytes = 2 MiB` | 0 |

`compiler/` itself uses 84k nodes (4.6 per line), so a 35k-line compiler
needs about 165k of `MaxNodes = 524_288`; every other table has more than
10x headroom. Deviations from the spec 9.4 table: MaxNodes 524_288 (spec
131_072), MaxListItems 1_048_576 (262_144), MaxFiles 128 (64), MaxTypes
131_072 (32_768), MaxSyms 131_072 (32_768) with 524_288 buckets (65_536);
there is no separate MaxFields: fields are entries of `typs.lists`.
`finc1 check compiler/` takes about 20 ms and 90 MB RSS.

### Validation

`make diff-check` (scripts/diffcheck) runs `finc1 check` and the oracle
`finc0 check --passes=types` (hidden flag: load + resolve + types +
comptime only) on examples/, testdata/{pass,fail,integration},
the parser fixtures, scripts/diffcheck/cases/*.fc (hand-written edge cases
for the interpreter, generics, enums, Code and error paths), compiler/ and
every FinCore snippet embedded in finc0's resolve/types/comptime unit tests
(single files and multi-file import modules, each with the default and a
small fuel), and requires byte-identical stdout/stderr and the same
success/failure status (finc1's generated `main` appends one
`error: fincore: ...` line and always exits 1 on failure; that line is
removed before comparing). `make diff-check-fuzz SEED=n` adds 20 random
token-level mutants per source. Both compilers get the same hidden
`--passes` value (`types` by default). The finc0 totality unit-test
snippets are part of the corpus.

`make diff-check-totality` (`diffcheck -passes=totality`) compares types +
totality on the same corpus plus about 20 copies of compiler/ with one
recursion measure, loop or decreases clause broken each. With `-mutate N`
every other mutant uses totality-oriented edits (loop conditions and
steps, measure arithmetic, guards, decreases clauses, extra writes).

### Totality checker (totality*.fc)

A line-by-line port of finc0's `bootstrap/internal/totality` (the shapes
are documented in its doc.go); the differences are representation only:

- **Tree walks.** `buildTotIndex` numbers every package file in pre-order
  once (`tord`, `tpos`, `tend`, and `tlit` = innermost enclosing FuncLit),
  so each `ast.Inspect` of finc0 is a flat loop over `tord[tpos[n]:tend[n]]`
  and `inspectBody` keeps the nodes with `tlit == fi.lit`.
- **Root facts** (`&x`, array slicing, writes from literals) are
  per-symbol flags computed in one pass: a local is only visible inside its
  root function, so finc0's per-root maps collapse into one table.
- **Loop reasons** are codes (`Why*`) rendered into the message at report
  time.
- **Abstract states** are handles into `tst`; their lower bounds and
  variable bounds are regions of two pools (relocated on growth, reset per
  analysed loop or function). A known variable has a presence entry, so
  "known with no bounds" (finc0's map entry with empty slices) is kept.
  Terms are (kind, value) pairs instead of finc0's strings; bounds values
  live on a LIFO scratch stack.
- **Callbacks** (`onCall`, `onAssign`, `onExit`) dispatch on `wMode`
  (T2, T3, recursion).
- **Call graph.** Value calls go to a hub node that has an edge to every
  escaping function (same SCCs of the real nodes, linear edge count);
  Tarjan is iterative with an explicit frame stack. Diagnostics are sorted
  afterwards, so the visiting order does not matter.

Capacities: `TotMaxStates = 262_144` states, `TotMaxLB`/`TotMaxBE =
524_288` fact entries, `TotMaxBS = 262_144` bounds, per analysed loop or
function; overflow is reported as capacity exceeded.

## Language subset used (conservative on purpose)

- Only `func` (total) and `driver func`; no `comptime`, generics, enums,
  `match`, closures, `defer` or `scope`. The M7 files use free functions
  over package-level arenas instead of methods on Scoped helper structs
  (total functions read and write globals directly); pointers appear only
  as `*Operand` / `*OutBuf` parameters (`&opStack.items[i]`, `&msgBuf`).
- No compound assignment other than `+= -= *= /=` (no `|=`); `scope`,
  `any` and `decreases` are keywords and cannot name fields or variables.
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
  (35 methods on `*Parser` since v0.2's `matchExpr` and `patternAlt`;
  the SCC has 33 members), the printer (4 methods on `*Printer`), and
  two directly recursive methods (`NodeArena.assignIDs`,
  `Dumper.dumpNode`). Parser entry points start the measure at
  `ParseDepth = 65_536`, far above the frames reachable within
  `MaxNesting`. Tree walks start at `WalkDepth = MaxNodes + 1`, because a
  tree is never deeper than its node count. Recursive method names are
  unique across receiver types, so even a name-based call graph gives the
  same SCCs. The M7 semantic SCCs use `dp usize` and `SemaDepth` (see
  "Recursion and scratch stacks"). Recursion between driver functions
  (`visitPackage` / `loadImport`) is not totality-checked.
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

## Code generation and `finc1 build` (M7b)

`cgGenerateGo` (codegen*.fc) is a function-by-function port of finc0's
`ir.Lower` + `golang.Generate` over the semantic tables; there is no
separate IR object. Emission appends to `cgBuf`; each finished top-level
declaration moves to its section (`genTypes`, `genVars`, `genFuncs`), and
`cgAssembleGo` writes header, imports and sections to `outBuf`. Where finc0
builds a string and then decides on parentheses (`primary`, `unary`,
`composite`, `convType`), the text is wrapped in place; where finc0
evaluates parts in another order than it prints them (type arguments before
an instance name, a for header's post before its condition, struct/enum
literal fields before their type, AcquireSlot's pool before its context),
the parts are rendered in evaluation order and rotated (`cgWrotate`), so
temporaries, labels and monomorphized-instance discovery happen in
finc0's order. Labels for `break` out of a match are inserted in front of
the loop after its body. Return values evaluated before scope waits and the
match scrutinee are kept on the `cgStash` stack. Capacities: 8 MiB per
section (compiler/ needs about 0.5 MB), 4096 instances, 65_536 frames.

Formatting: the raw text is written to `<tmp>/main.go` and formatted with
`go run cmd/gofmt -w` (or `$FINC_GOFMT`) through Exec; gofmt and go/format
print identically, so `--emit-go` output is byte-identical to finc0's. The
build then writes `go.mod` (`replace fincore => <root>`, go version of the
root go.mod), `zz_race.go` for `--race`, and runs `go build -C <tmp>` with
workdir = module root (so mise-style shims resolve the toolchain), env
`GOWORK=off GOFLAGS=-mod=mod CGO_ENABLED=0|1`. Root: `$FINCORE_ROOT`, else
walk up from `$PWD` (FS has no Getwd), else from Arg(0). `$FINC_GO`
overrides the go command.

`make diff-build` (scripts/diffbuild) compares `finc0 build --emit-go` and
`finc1 build --emit-go` on examples/, testdata/pass, testdata/integration,
compiler/, scripts/diffbuild/cases (hand-written codegen edge cases) and
the unit-test snippets of finc0 (library snippets get a trivial main).
`make finc2` is the fixed-point smoke test (stage2.go == stage3.go).

Differences from finc0: `run` cannot forward stdin or the child's exit
status (Exec), so a failing program makes finc1 exit 1 with
`error: fincore: exec failed`, and the child's stderr is printed after it
exits. `test` runs pass programs through `/bin/sh -c 'exec "$0" >"$1"'`
(Exec does not capture stdout), lists subdirectories with /bin/sh, runs
`finc1 check`/`build` as subprocesses (Arg(0)) and prints no timings (no
clock). Flag parsing covers the Go flag forms used in practice.

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
- **Exit status.** The generated `main` prints `error: <err>` after a failed
  driver and always exits 1, so usage errors exit 1 (finc0: 2) and every
  failure has one extra stderr line.

### `check` (M7)

- **Integer constants are exact only within ±2^127** (finc0 uses
  `math/big`). Larger intermediate values (e.g. `1 << 127 - 1`,
  `1 << 200`) are marked as overflowed: they still fail every range check,
  but their decimal form prints as `<overflow>` in messages, and `Wrap` of
  such a value is not exact. This is the only divergence found by
  `make diff-check-fuzz`.
- **The comptime heap is not garbage-collected within one evaluation
  root**: every array/struct copy allocates value slots, and a root that
  allocates more than `HeapCap = 2 Mi` slots (e.g. copying a 1000-element
  array in a 10,000-iteration loop) fails with `compiler capacity exceeded
  (comptime heap)` where finc0 succeeds. The heap is reset after every
  root, and a variable redefined in a loop reuses its cell.
- `diag.List.Sort` removes duplicates including the end of the span; finc1
  compares (file, line, column, code, message) only.
- `--comptime-fuel` accepts decimal numbers only (Go's flag package also
  accepts `0x..`); `check` has the hidden flag `--stats`.
- I/O failures while loading an imported package are reported as
  E2001 `cannot load package` without Go's error text.
