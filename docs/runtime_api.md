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
