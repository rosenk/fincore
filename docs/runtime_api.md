# fincrt runtime API (codegen target)

Authoritative source: runtime/fincrt/*.go (see doc.go for FinCore→Go name mapping).

Key deviations from the spec:
- `NewScope(maxTasks uint) *Scope` (pointer). Generated: `conns := fincrt.NewScope(N)` + `defer conns.Wait()`.
- `fincrt.AcquireSlot(conns, ctx, pool[:])`, `fincrt.ReleaseSlot(conns, slot)`, `fincrt.Go(conns, slot, func(buf *T){...})`.
- `Context.DoneChan()` returns `(<-chan struct{}, stop func())`.
- Extra sentinel `ErrParse` (returned by `ParseUint`) — must be in the FinCore universe.
- Builtins exported as `fincrt.Copy`, `BytesEqual`, `StrBytes`, `BytesToStr`, `FormatUint`, `ParseUint`.
- `Duration uint64`, `Signal uint8`, `ExitStatus{Code int}`; usize → `uint`.
- `ListSourceFiles` returns base names (sorted); `Exec.Run` env entries extend the parent env.
- Zero-value Conn/Listener behave as closed; zero Context never cancelled.
- `Clock` (no methods in v0.1) is `fincrt.Clock`; generated `main()` injects `fincrt.DefaultClock()`.
- `finc0 build --race` adds a race-only init file (not part of `--emit-go` output) that re-executes the program once with `GORACE=atexit_sleep_ms=0`: the race runtime otherwise sleeps 1 s at exit because os/signal's loop goroutine never ends, which breaks the spec 13.3 shutdown deadlines.
