# What finc0's checkers/codegen MUST support for compiler/ (self-hosted front end) to compile

Reported by the M6 author; see compiler/README.md for details.

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
17. T3 SCCs: 33-method *Parser SCC, 4-method *Printer SCC, 2 self-recursive methods; each `decreases(depth)` with `if depth == 0 { return ... }` guard first and `depth - 1` on in-SCC calls; entry calls pass constants (ParseDepth=65536, WalkDepth=MaxNodes+1). Call graph needs method resolution by receiver type.
