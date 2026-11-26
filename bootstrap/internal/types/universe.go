package types

import (
	"fincore/bootstrap/internal/resolve"
)

// BuiltinMethod identifies a predeclared capability/resource method
// (spec 10.2-10.6).
type BuiltinMethod uint8

const (
	BMNone BuiltinMethod = iota
	ContextDone
	ContextErr
	ContextWithTimeout
	SignalsNotifyContext
	NetListenTCP
	ListenerAccept
	ListenerAddr
	ListenerClose
	ConnRead
	ConnWrite
	ConnCloseWrite
	ConnClose
	ScopeAcquireSlot
	ScopeReleaseSlot
	ScopeGo
	ScopeWaitTimeout
	ScopeWait
	FSReadFileInto
	FSWriteFile
	FSIsDir
	FSListSourceFiles
	FSMkdirTemp
	FSRemoveAll
	EnvArgsCount
	EnvArg
	EnvGetEnv
	StdioWriteStdout
	StdioWriteStderr
	ExecRun
)

// universe holds the predeclared named types and their methods.
type universe struct {
	named   map[resolve.Builtin]*Named // lookups only
	methods map[*Named][]*Method
	slotT   *TypeParam
}

func (c *Checker) newUniverse() *universe {
	u := &universe{named: map[resolve.Builtin]*Named{}, methods: map[*Named][]*Method{}}
	prog := c.info.Prog
	mk := func(b resolve.Builtin, under Type, class Class) *Named {
		n := &Named{Name: b.String(), Sym: prog.Builtin(b), Builtin: b, underlying: under, class: class, classSet: true}
		u.named[b] = n
		return n
	}
	duration := mk(resolve.TDuration, Typ[U64], ClassValue)
	signal := mk(resolve.TSignal, Typ[U8], ClassValue)
	exit := mk(resolve.TExitStatus, &Struct{Fields: []*Field{{Name: "Code", Type: Typ[Int]}}}, ClassValue)
	_ = exit
	for _, b := range []resolve.Builtin{resolve.TNet, resolve.TSignals, resolve.TClock, resolve.TContext,
		resolve.TScope, resolve.TFS, resolve.TEnv, resolve.TStdio, resolve.TExec} {
		mk(b, &Opaque{Name: b.String()}, ClassScoped)
	}
	for _, b := range []resolve.Builtin{resolve.TListener, resolve.TConn} {
		mk(b, &Opaque{Name: b.String()}, ClassLinear)
	}
	mk(resolve.TCancelFunc, &Signature{Effect: Total}, ClassLinear)
	slot := mk(resolve.TSlot, &Opaque{Name: "Slot"}, ClassLinear)
	u.slotT = &TypeParam{Name: "T"}
	slot.TypeParams = []*TypeParam{u.slotT}
	mk(resolve.TFieldCode, &Opaque{Name: "FieldCode"}, ClassValue)

	T := func(b resolve.Builtin) Type { return u.named[b] }
	bytes := &Slice{Elem: Typ[U8]}
	str, usize, boolean, errT := Typ[String], Typ[Usize], Typ[Bool], Typ[Error]
	add := func(recv resolve.Builtin, name string, id BuiltinMethod, params []Type, results []Type, variadic bool, eff Effect) {
		n := u.named[recv]
		var sig *Signature
		if id != ScopeAcquireSlot && id != ScopeReleaseSlot && id != ScopeGo {
			sig = &Signature{Params: params, Results: results, Variadic: variadic, Effect: eff}
		}
		u.methods[n] = append(u.methods[n], &Method{Name: name, Recv: n, Sig: sig, Builtin: id})
	}
	ts := func(t ...Type) []Type { return t }
	ctx := T(resolve.TContext)
	add(resolve.TContext, "Done", ContextDone, nil, ts(boolean), false, Total)
	add(resolve.TContext, "Err", ContextErr, nil, ts(errT), false, Total)
	add(resolve.TContext, "WithTimeout", ContextWithTimeout, ts(duration), ts(ctx), false, Total)
	add(resolve.TSignals, "NotifyContext", SignalsNotifyContext, ts(signal), ts(ctx, T(resolve.TCancelFunc)), true, Total)
	add(resolve.TNet, "ListenTCP", NetListenTCP, ts(str), ts(T(resolve.TListener), errT), false, Total)
	add(resolve.TListener, "Accept", ListenerAccept, ts(ctx), ts(T(resolve.TConn), errT), false, Total)
	add(resolve.TListener, "Addr", ListenerAddr, nil, ts(str), false, Total)
	add(resolve.TListener, "Close", ListenerClose, nil, ts(errT), false, Total)
	add(resolve.TConn, "Read", ConnRead, ts(ctx, bytes), ts(usize, errT), false, Total)
	add(resolve.TConn, "Write", ConnWrite, ts(ctx, bytes), ts(usize, errT), false, Total)
	add(resolve.TConn, "CloseWrite", ConnCloseWrite, nil, ts(errT), false, Total)
	add(resolve.TConn, "Close", ConnClose, nil, ts(errT), false, Total)
	// Generic Scope methods: signatures are computed per call.
	add(resolve.TScope, "AcquireSlot", ScopeAcquireSlot, nil, nil, false, Total)
	add(resolve.TScope, "ReleaseSlot", ScopeReleaseSlot, nil, nil, false, Total)
	add(resolve.TScope, "Go", ScopeGo, nil, nil, false, Total)
	add(resolve.TScope, "WaitTimeout", ScopeWaitTimeout, ts(duration), ts(boolean), false, Total)
	add(resolve.TScope, "Wait", ScopeWait, ts(), ts(), false, Total)
	add(resolve.TFS, "ReadFileInto", FSReadFileInto, ts(str, bytes), ts(usize, errT), false, Total)
	add(resolve.TFS, "WriteFile", FSWriteFile, ts(str, bytes), ts(errT), false, Total)
	add(resolve.TFS, "IsDir", FSIsDir, ts(str), ts(boolean), false, Total)
	add(resolve.TFS, "ListSourceFiles", FSListSourceFiles, ts(str, &Slice{Elem: str}), ts(usize, errT), false, Total)
	add(resolve.TFS, "MkdirTemp", FSMkdirTemp, ts(str), ts(str, errT), false, Total)
	add(resolve.TFS, "RemoveAll", FSRemoveAll, ts(str), ts(errT), false, Total)
	add(resolve.TEnv, "ArgsCount", EnvArgsCount, nil, ts(usize), false, Total)
	add(resolve.TEnv, "Arg", EnvArg, ts(usize), ts(str), false, Total)
	add(resolve.TEnv, "GetEnv", EnvGetEnv, ts(str), ts(str, boolean), false, Total)
	add(resolve.TStdio, "WriteStdout", StdioWriteStdout, ts(bytes), ts(errT), false, Total)
	add(resolve.TStdio, "WriteStderr", StdioWriteStderr, ts(bytes), ts(errT), false, Total)
	strs := &Slice{Elem: str}
	add(resolve.TExec, "Run", ExecRun, ts(ctx, str, strs, strs, str, bytes),
		ts(T(resolve.TExitStatus), usize, errT), false, Driver)
	return u
}

// predeclaredType returns the type denoted by a predeclared type symbol
// (nil for Code, El and generic Slot, which need special handling).
func (c *Checker) predeclaredType(b resolve.Builtin) Type {
	switch b {
	case resolve.TBool:
		return Typ[Bool]
	case resolve.TU8, resolve.TByte:
		return Typ[U8]
	case resolve.TU16:
		return Typ[U16]
	case resolve.TU32:
		return Typ[U32]
	case resolve.TU64:
		return Typ[U64]
	case resolve.TI8:
		return Typ[I8]
	case resolve.TI16:
		return Typ[I16]
	case resolve.TI32:
		return Typ[I32]
	case resolve.TI64:
		return Typ[I64]
	case resolve.TInt:
		return Typ[Int]
	case resolve.TUsize:
		return Typ[Usize]
	case resolve.TString:
		return Typ[String]
	case resolve.TError:
		return Typ[Error]
	case resolve.TCode, resolve.TEl:
		return nil
	}
	if n := c.univ.named[b]; n != nil {
		return n
	}
	return nil
}

// isCapability reports whether t is one of the main() capabilities.
func isCapability(t Type) bool {
	switch BuiltinOf(t) {
	case resolve.TNet, resolve.TSignals, resolve.TClock, resolve.TFS, resolve.TEnv, resolve.TStdio, resolve.TExec:
		return true
	}
	return false
}

// predeclaredConst returns the type and value of a predeclared constant.
func (c *Checker) predeclaredConst(b resolve.Builtin) (Type, Value) {
	switch b {
	case resolve.CNanosecond:
		return c.univ.named[resolve.TDuration], MakeInt(1)
	case resolve.CMicrosecond:
		return c.univ.named[resolve.TDuration], MakeInt(1_000)
	case resolve.CMillisecond:
		return c.univ.named[resolve.TDuration], MakeInt(1_000_000)
	case resolve.CSecond:
		return c.univ.named[resolve.TDuration], MakeInt(1_000_000_000)
	case resolve.CSIGINT:
		return c.univ.named[resolve.TSignal], MakeInt(2)
	case resolve.CSIGTERM:
		return c.univ.named[resolve.TSignal], MakeInt(15)
	}
	if b.IsErrorSentinel() {
		return Typ[Error], ErrorVal{Name: b.String()}
	}
	return Typ[Invalid], nil
}
