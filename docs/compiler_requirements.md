# What finc0's checkers/codegen MUST support for compiler/ (self-hosted front end) to compile

Reported by the M6 author; see compiler/README.md for details.

**Which features compiler/ may use (the seed rule).** finc0 is frozen at
language v0.2 and builds only `compiler/` at the seed tag named in
`bootstrap/SEED` (`v0.2.0`); that seed compiler builds the current
`compiler/` (bootstrap/README.md). So the current `compiler/` may use
exactly the features the **seed** implements, no longer "only features both
finc0 and finc1 support". To use a new feature in `compiler/`, implement
it first, cut a release tag that contains it, then bump `bootstrap/SEED` to
that tag. The list below is what finc0 must support to build the v0.2.0
seed; it is closed (finc0 gets no new features).

1. Multi-file `package main` (6+ files).
2. Untyped int consts in grouped `const (...)` (no iota), `_` separators, const arithmetic; mixed with u8/u32/usize.
3. Large Value globals (structs of big fixed arrays, NodeArena ≈ 12 MB) accessed by field/index paths and via `&global`, never copied.
4. Pointer-receiver methods called through explicit pointers (`eb := &errBuf; eb.putStr(..)`).
5. Pointer-field chains: `p.na.alloc(..)`, `p.na.items[usize(id)].l1s = s`.
6. Scoped structs in locals built from pointers, incl. in loop bodies; a callee constructing `Printer{na: p.na, ...}` from the receiver's pointer fields must pass S2.
7. Slices of global arrays stored in struct fields and returned from methods (`return p.fsrc[a:b]`) — must not be flagged as escapes.
8. Multiple results incl. 4-tuples, `_` in `:=`, tuple assignment to existing vars, `_ = f()`.
9. Struct literals (multi-line, trailing comma), zero-value `var n Node`, struct copies, structs by value.
10. int with -1/++/--, u8 loop counters, shifts by u64 count, bit ops on u8/u32/u64, conversions u32()/usize()/u64()/byte()/u16(), char escapes.
11. Builtins len (slices), copy (usize result in arithmetic), bytesEqual, strBytes, bytesToStr, formatUint.
12. Sentinels ErrParse/ErrExec/ErrNotFound/ErrArenaFull compared with ==; `if err := f(); err != nil`.
13. string fields, `[MaxFiles]string` global arrays, `fileNames[:]` to fs.ListSourceFiles.
14. Caps: fs.IsDir/ListSourceFiles/ReadFileInto, env.ArgsCount/Arg, io.WriteStdout/WriteStderr; unused `exec` param.
15. Plain `for` in driver funcs must pass T1.
16. T1 shapes: `for i := usize(<init>); i < <bound>; i++` with invariant local/param/const bound, non-zero init, break/continue in body.
17. T3 SCCs: the *Parser SCC (every recursive *Parser method; the counts are pinned by `TestCompilerSCCs` in bootstrap/internal/totality/totality_test.go), 4-method *Printer SCC, 2 self-recursive methods; each `decreases(depth)` with `if depth == 0 { return ... }` guard first and `depth - 1` on in-SCC calls; entry calls pass constants (ParseDepth=65536, WalkDepth=MaxNodes+1). Call graph needs method resolution by receiver type.

## Added by M7 part A (semantic phases: sema/resolve/types/values/checker/check_expr/check_stmt/comptime.fc)

18. Free total functions that read and write package-level Value arenas directly (`syms.items[usize(s)].typ = t`, `typs.items[usize(t)].flag = typs.items[usize(t)].flag | 2`); about 40 large globals (NodeArena ≈ 50 MB at MaxNodes = 524_288, value slots ≈ 100 MB virtual), never copied whole.
19. Struct values passed and returned by value (`Val` 32 B, `Operand` ≈ 72 B, `TypeRec`, `Call`, `FuncInfo`), `var z Val` zero values, struct field copies `r := typs.items[usize(t)]`.
20. Pointer parameters into global arrays and locals: `assignment(&opStack.items[i], t, c)`, `singleValue(&x)`, `putType(o *OutBuf, ...)` with `o := &fmtBuf` (scope checker: origins at depth 0 or the caller's frame).
21. Package-level `var` with a constant initializer (`var evalFuel u64 = DefaultFuel`); local fixed arrays (`var seen [NumBuiltins]bool`, `var digits [48]byte`, `var imps [MaxPkgs]u32` in a driver).
22. `int` results and comparisons (`bytesCompare`, `cmp128`, `codeLevel`), 128-bit arithmetic on `u64` pairs with `^`, `&`, `|`, `<<`, `>>` by `u64` counts, wrapping `u64` multiplication.
23. Recursion SCCs: one ≈ 80-function checker/interpreter SCC (`dp usize`, `decreases(dp)`, guard `if dp == 0 { tooDeep(); return ... }`, `dp-1` on every in-SCC call, entry with `SemaDepth`), plus smaller SCCs (binder, putType, classOf, subst/instantiate/completeInstance, copyVal, persistVal, equalVals, writeExpr, isTerminating, containsCall, ...). Recursion between driver funcs (`visitPackage` ↔ `loadImport`) is not checked.
24. Multi-line conditions (`&&` at line end), `continue` in T1 loops, loops whose bound is a local copy of a global (`top := typs.top`), `for g := u64(0); g < lim; g++` with `lim := inBudget + 2`.
25. Codegen: the generated `main` must keep printing `error: <err>` and exiting 1 for a non-nil error. Since v0.3 the runtime leaves the line out when the program wrote to stderr (decision 156), so a failing finc prints only its diagnostics (scripts/diffcheck and diffprev still strip a trailing `error: fincore:` line).
