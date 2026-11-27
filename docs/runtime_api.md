# fincrt runtime API (codegen target)

Authoritative source: runtime/fincrt/*.go (see doc.go for FinCore→Go name mapping).

Key deviations from the v0.1 spec (all folded into section 8 of [fincore_language_specification_v0_2.md](../fincore_language_specification_v0_2.md)):
- `NewScope(maxTasks uint) *Scope` (pointer). Generated: `conns := fincrt.NewScope(N)` + `defer conns.Wait()`.
- `fincrt.AcquireSlot(conns, ctx, pool[:])`, `fincrt.ReleaseSlot(conns, slot)`, `fincrt.Go(conns, slot, func(buf *T){...})`.
- `Context.DoneChan()` returns `(<-chan struct{}, stop func())`.
- Extra sentinel `ErrParse` (returned by `ParseUint`) — must be in the FinCore universe.
- Builtins exported as `fincrt.Copy`, `BytesEqual`, `StrBytes`, `BytesToStr`, `FormatUint`, `ParseUint`. `StrBytes` returns a fresh, writable copy (decision 90); it used to return a read-only zero-copy view, which made writes to it crash.
- `Duration uint64`, `Signal uint8`, `ExitStatus{Code int}`; usize → `uint`.
- `ListSourceFiles` returns base names (sorted); `Exec.Run` env entries extend the parent env.
- Zero-value Conn/Listener behave as closed; zero Context never cancelled.
- `Clock` (no methods in v0.1) is `fincrt.Clock`; generated `main()` injects `fincrt.DefaultClock()`.
- `finc0 build --race` adds a race-only init file (not part of `--emit-go` output) that re-executes the program once with `GORACE=atexit_sleep_ms=0`: the race runtime otherwise sleeps 1 s at exit because os/signal's loop goroutine never ends, which breaks the spec 13.3 shutdown deadlines.

## v0.2: `fincrt.Fmt` (builtin `formatTo`)

Source: runtime/fincrt/fmt.go (tests: fmt_test.go, also run under `-race`). Both compilers expand every `formatTo(dst, format, args...)` call at compile time into an immediately called function literal. Its parameters receive `dst` and the converted arguments, so everything is evaluated before anything is written; the body drives a `fincrt.Fmt` writer. Go's `fmt` is never used at run time. Example (`formatTo(dst, "n=%5d %q %-8v", n, s, p)` with `p Point` having a Format method; gofmt later reflows the line):

```go
func(_fc_d []byte, _fc_a0 int64, _fc_a1 string, _fc_a2 Point) uint { _fc_w := fincrt.NewFmt(_fc_d); _fc_w.Lit("n="); _fc_w.Int(_fc_a0, 5, false); _fc_w.Lit(" "); _fc_w.Quote(_fc_a1, 0); _fc_w.Lit(" "); _fc_w.Advance(_fc_a2._fc_m_Format(_fc_w.Dst(-8)), -8); return _fc_w.N() }(dst, int64(n), string(s), p)
```

| Method | Used for |
| :--- | :--- |
| `NewFmt(dst []byte) Fmt`, `(*Fmt).N() uint` | the writer over dst / bytes written so far |
| `Lit(s string)` | literal text (`%%` already unescaped) |
| `Int(v int64, w int, zero bool)`, `Uint(v uint64, w int, zero bool)` | `%d`, and `%v` on integers |
| `HexInt(v int64, w int, zero bool)`, `Hex(v uint64, w int, zero bool)` | `%x` (signed / unsigned) |
| `Str(s string, w int)`, `Bytes(b []byte, w int)` | `%s`, and `%v` on strings / `[]u8` |
| `Quote(s string, w int)`, `QuoteBytes(b []byte, w int)` | `%q` |
| `Byte(c byte, w int)`, `Bool(v bool, w int)` | `%c`; `%v` on bool |
| `Dst(w int) []byte`, `Advance(n uint, w int)` | `%v` with a Format method: `w.Advance(x.Format(w.Dst(w)), w)` |

- Integer arguments are converted to `int64` (signed types) or `uint64` (unsigned types), strings to `string`, byte slices to `[]byte` (no copy), `u8` to `byte`; Format receivers are passed with their own Go type.
- `w` is the signed width: 0 none, > 0 right-justify, < 0 left-justify (`|w| <= MaxFmtWidth = 64`). `zero` pads numbers with `0` after the sign when right-justifying.
- Every method drops bytes beyond `len(dst)`, never allocates (checked by `testing.AllocsPerRun`) and never panics, so the written bytes are a prefix of the untruncated output.
- `Dst(w)` returns `dst[n:len:len]`, or a `w`-byte scratch array inside the writer when right-justifying into less room than `w`; `Advance` clamps `n` to the length of that buffer and applies the padding (from the scratch buffer, the padded text is copied with truncation).
- A `Fmt` is a plain local value; it is never shared between goroutines. finc0's comptime evaluator runs the same type on a copy of `dst`.
