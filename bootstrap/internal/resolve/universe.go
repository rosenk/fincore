package resolve

// Builtin identifies a predeclared entity of the FinCore universe (spec
// 2.4, 5.1 and 10). The zero value BNone means "not predeclared".
type Builtin uint16

// Predeclared entities. The order inside each group is the order in which
// they are inserted into the universe scope.
const (
	BNone Builtin = iota

	// Scalar types (Value class).
	TBool
	TU8
	TU16
	TU32
	TU64
	TI8
	TI16
	TI32
	TI64
	TInt
	TUsize
	TByte // alias of u8
	TString
	TError
	TDuration
	TSignal
	TExitStatus
	// Capabilities and resources (Scoped / Linear).
	TNet
	TSignals
	TClock
	TListener
	TConn
	TContext
	TCancelFunc
	TScope
	TSlot
	TFS
	TEnv
	TStdio
	TExec
	// Layer-1 type functions (contextual keywords in type position).
	TCode
	TEl
	TFieldCode // extension: element type of CodeStruct's argument

	// Constants.
	CNanosecond
	CMicrosecond
	CMillisecond
	CSecond
	CSIGINT
	CSIGTERM
	CErrEOF
	CErrTimeout
	CErrCancelled
	CErrZeroWrite
	CErrClosed
	CErrIO
	CErrNotFound
	CErrMaxDepth
	CErrArenaFull
	CErrExec
	CErrParse

	// Builtin functions.
	FLen
	FCap
	FCopy
	FBytesEqual
	FStrBytes
	FBytesToStr
	FFormatUint
	FParseUint
	FNewContext
	FNewScope
	FPanic
	// Code constructors (comptime only).
	FCodeEmpty
	FCodeUnit
	FCodeBool
	FCodeU8
	FCodeU16
	FCodeU32
	FCodeU64
	FCodeI8
	FCodeI16
	FCodeI32
	FCodeI64
	FCodeArray
	FCodeSlice
	FCodeStruct
	FCodeField // extension: CodeField(name string, c Code n) FieldCode
	FCodeUniv

	numBuiltins
)

var builtinNames = [numBuiltins]string{
	TBool: "bool", TU8: "u8", TU16: "u16", TU32: "u32", TU64: "u64",
	TI8: "i8", TI16: "i16", TI32: "i32", TI64: "i64", TInt: "int", TUsize: "usize",
	TByte: "byte", TString: "string", TError: "error", TDuration: "Duration",
	TSignal: "Signal", TExitStatus: "ExitStatus",
	TNet: "Net", TSignals: "Signals", TClock: "Clock", TListener: "Listener",
	TConn: "Conn", TContext: "Context", TCancelFunc: "CancelFunc", TScope: "Scope",
	TSlot: "Slot", TFS: "FS", TEnv: "Env", TStdio: "Stdio", TExec: "Exec",
	TCode: "Code", TEl: "El", TFieldCode: "FieldCode",

	CNanosecond: "Nanosecond", CMicrosecond: "Microsecond", CMillisecond: "Millisecond",
	CSecond: "Second", CSIGINT: "SIGINT", CSIGTERM: "SIGTERM",
	CErrEOF: "ErrEOF", CErrTimeout: "ErrTimeout", CErrCancelled: "ErrCancelled",
	CErrZeroWrite: "ErrZeroWrite", CErrClosed: "ErrClosed", CErrIO: "ErrIO",
	CErrNotFound: "ErrNotFound", CErrMaxDepth: "ErrMaxDepth", CErrArenaFull: "ErrArenaFull",
	CErrExec: "ErrExec", CErrParse: "ErrParse",

	FLen: "len", FCap: "cap", FCopy: "copy", FBytesEqual: "bytesEqual",
	FStrBytes: "strBytes", FBytesToStr: "bytesToStr", FFormatUint: "formatUint",
	FParseUint: "parseUint", FNewContext: "NewContext", FNewScope: "NewScope", FPanic: "panic",
	FCodeEmpty: "CodeEmpty", FCodeUnit: "CodeUnit", FCodeBool: "CodeBool",
	FCodeU8: "CodeU8", FCodeU16: "CodeU16", FCodeU32: "CodeU32", FCodeU64: "CodeU64",
	FCodeI8: "CodeI8", FCodeI16: "CodeI16", FCodeI32: "CodeI32", FCodeI64: "CodeI64",
	FCodeArray: "CodeArray", FCodeSlice: "CodeSlice", FCodeStruct: "CodeStruct",
	FCodeField: "CodeField", FCodeUniv: "CodeUniv",
}

// String returns the FinCore spelling of the builtin.
func (b Builtin) String() string {
	if b < numBuiltins {
		return builtinNames[b]
	}
	return "?"
}

// IsType reports whether b is a predeclared type (or type function).
func (b Builtin) IsType() bool { return b >= TBool && b <= TFieldCode }

// IsConst reports whether b is a predeclared constant (Duration units,
// signals, error sentinels).
func (b Builtin) IsConst() bool { return b >= CNanosecond && b <= CErrParse }

// IsErrorSentinel reports whether b is one of the ErrXxx constants.
func (b Builtin) IsErrorSentinel() bool { return b >= CErrEOF && b <= CErrParse }

// IsFunc reports whether b is a builtin function (including Code
// constructors).
func (b Builtin) IsFunc() bool { return b >= FLen && b <= FCodeUniv }

// IsCodeCtor reports whether b is a Code constructor (CodeU8, CodeArray, ...).
func (b Builtin) IsCodeCtor() bool { return b >= FCodeEmpty && b <= FCodeUniv }

// Kind returns the symbol kind of a predeclared entity.
func (b Builtin) Kind() SymKind {
	switch {
	case b.IsType():
		return SymType
	case b.IsConst():
		return SymConst
	case b.IsFunc():
		return SymBuiltin
	}
	return SymBad
}

// GoName returns the name of the corresponding fincrt identifier for
// builtins that map 1:1 onto runtime/fincrt (see runtime/fincrt/doc.go), or
// "" if there is none (scalar types, Code constructors, len/cap/panic).
func (b Builtin) GoName() string {
	switch b {
	case FCopy:
		return "Copy"
	case FBytesEqual:
		return "BytesEqual"
	case FStrBytes:
		return "StrBytes"
	case FBytesToStr:
		return "BytesToStr"
	case FFormatUint:
		return "FormatUint"
	case FParseUint:
		return "ParseUint"
	case FNewContext:
		return "NewContext"
	case FNewScope:
		return "NewScope"
	}
	if b.IsConst() || (b >= TDuration && b <= TExec && b != TClock) {
		return b.String()
	}
	return ""
}

// newUniverse builds the universe scope. Symbols are allocated in Builtin
// order, so their IDs are stable across runs.
func newUniverse(p *Program) *Scope {
	u := NewScope(nil, ScopeUniverse, nil)
	for b := Builtin(1); b < numBuiltins; b++ {
		sym := p.NewSymbol(b.Kind(), b.String(), nil, nil, nil)
		sym.Builtin = b
		u.Insert(sym)
	}
	return u
}
