package types

import (
	"strconv"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
)

// formatTo (v0.2): formatTo(dst []byte, format string, args...) usize.
// The format is a constant string; it is parsed at compile time
// (ParseFormat) and every argument is matched against its directive
// (Checker.formatCall). See docs/semantics_decisions.md, "v0.2 additions".

// MaxFormatWidth is the largest width of a formatTo directive.
const MaxFormatWidth = 64

// FormatPiece is one piece of a parsed format string: literal text (Verb ==
// 0; `%%` contributes a '%') or a directive.
type FormatPiece struct {
	Lit   string
	Verb  byte // 'd', 'x', 's', 'c', 'q', 'v'
	Left  bool // flag '-'
	Zero  bool // flag '0'
	Width int  // 0 = none
}

// ParseFormat parses a formatTo format string. Consecutive literal text
// (including `%%`) is merged into one piece. On error it returns the
// message of the first malformed directive.
func ParseFormat(s string) ([]FormatPiece, string) {
	var out []FormatPiece
	lit := []byte{}
	flush := func() {
		if len(lit) > 0 {
			out = append(out, FormatPiece{Lit: string(lit)})
			lit = []byte{}
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '%' {
			lit = append(lit, c)
			i++
			continue
		}
		start := i
		i++
		var p FormatPiece
		for i < len(s) && (s[i] == '-' || s[i] == '0') {
			if s[i] == '-' {
				p.Left = true
			} else {
				p.Zero = true
			}
			i++
		}
		hasWidth := false
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			hasWidth = true
			if p.Width <= MaxFormatWidth {
				p.Width = p.Width*10 + int(s[i]-'0')
			}
			i++
		}
		if i >= len(s) {
			return nil, "incomplete directive " + strconv.Quote(s[start:]) + " at the end of the format"
		}
		v := s[i]
		i++
		dir := strconv.Quote(s[start:i])
		switch v {
		case 'd', 'x', 's', 'c', 'q', 'v':
		case '%':
			if i-start != 2 {
				return nil, "directive " + dir + " cannot have flags or a width"
			}
			lit = append(lit, '%')
			continue
		default:
			return nil, "unknown verb in directive " + dir
		}
		if hasWidth && p.Width > MaxFormatWidth {
			return nil, "width in directive " + dir + " exceeds 64"
		}
		if p.Zero && v != 'd' && v != 'x' {
			return nil, "flag 0 in directive " + dir + " is only valid with %d and %x"
		}
		p.Verb = v
		flush()
		out = append(out, p)
	}
	flush()
	return out, ""
}

// FormatWidth returns the signed width passed to the runtime writer
// (negative = left-justify).
func (p FormatPiece) FormatWidth() int {
	if p.Left {
		return -p.Width
	}
	return p.Width
}

// FormatOp is how one formatTo argument is written (the fincrt.Fmt method
// the call is lowered to).
type FormatOp uint8

const (
	FmtNone       FormatOp = iota
	FmtInt                 // signed integer, decimal (%d, %v)
	FmtUint                // unsigned integer, decimal (%d, %v)
	FmtHexInt              // signed integer, hex (%x)
	FmtHexUint             // unsigned integer, hex (%x)
	FmtStr                 // string (%s, %v)
	FmtBytes               // []byte (%s, %v)
	FmtQuoteStr            // string (%q)
	FmtQuoteBytes          // []byte (%q)
	FmtByte                // u8 (%c)
	FmtBool                // bool (%v)
	FmtFormat              // named type with a Format method (%v)
)

// FormatArg describes one formatTo argument (after dst and format).
type FormatArg struct {
	Op FormatOp
	// Method is the Format method of FmtFormat.
	Method *Method
}

// FormatInfo is recorded in Call.Format for a successfully checked
// formatTo call.
type FormatInfo struct {
	Pieces []FormatPiece
	Args   []FormatArg // aligned with the directives of Pieces
}

// isByteSlice reports whether t's underlying type is []u8 (a slice of a
// named type over u8, e.g. []Signal, is not).
func isByteSlice(t Type) bool {
	s, ok := t.Underlying().(*Slice)
	if !ok {
		return false
	}
	b, ok := s.Elem.(*Basic)
	return ok && b.Kind == U8
}

func intOp(t Type, hex bool) FormatOp {
	switch {
	case hex && IsUnsigned(t):
		return FmtHexUint
	case hex:
		return FmtHexInt
	case IsUnsigned(t):
		return FmtUint
	}
	return FmtInt
}

// formatMethodOK reports whether m is a value-receiver, total method
// `Format(dst []byte) usize`.
func formatMethodOK(m *Method) bool {
	if m.PtrRecv || m.Builtin != BMNone || m.Sig == nil {
		return false
	}
	s := m.Sig
	return s.Effect == Total && !s.Variadic && len(s.Params) == 1 && len(s.Results) == 1 &&
		Identical(s.Params[0], bytesT) && basicKind(s.Results[0]) == Usize && !isNamedType(s.Results[0])
}

func isNamedType(t Type) bool {
	_, ok := t.(*Named)
	return ok
}

// formatCall checks a formatTo call.
func (c *Checker) formatCall(e *ast.CallExpr, ci *Call) operand {
	const name = "formatTo"
	if len(e.Args) < 2 {
		c.errorf(diag.E2003, e.Range, "not enough arguments in call to formatTo: have %d, want at least 2", len(e.Args))
		c.useArgs(e)
		return invalidOp(e)
	}
	ok := true
	d := c.exprValue(e.Args[0])
	if d.invalid() || !c.assignment(&d, bytesT, "argument to "+name) {
		ok = false
	}
	fx := c.exprValue(e.Args[1])
	format := ""
	if fx.invalid() || !c.assignment(&fx, Typ[String], "argument to "+name) {
		ok = false
		fx.mode = KindNone
	} else if fx.mode != KindConst || fx.val == nil {
		c.errorf(diag.E2002, e.Args[1].Span(), "format argument %s to formatTo must be a constant string", exprString(e.Args[1]))
		ok = false
		fx.mode = KindNone
	} else {
		format = string(fx.val.(StringVal))
	}
	args := make([]operand, len(e.Args)-2)
	for i, a := range e.Args[2:] {
		args[i] = c.exprValue(a)
	}
	var pieces []FormatPiece
	var verbs []FormatPiece
	if fx.mode != KindNone {
		var msg string
		pieces, msg = ParseFormat(format)
		if msg != "" {
			c.errorf(diag.E2002, e.Args[1].Span(), "invalid format for formatTo: %s", msg)
			fx.mode = KindNone
		} else {
			for _, p := range pieces {
				if p.Verb != 0 {
					verbs = append(verbs, p)
				}
			}
			if len(verbs) != len(args) {
				c.errorf(diag.E2003, e.Args[1].Span(), "wrong number of arguments for format %s in call to formatTo: have %d, want %d", strconv.Quote(format), len(args), len(verbs))
				fx.mode = KindNone
			}
		}
	}
	if fx.mode == KindNone {
		for i := range args {
			if !args[i].invalid() {
				c.defaultType(&args[i], "argument to "+name)
			}
		}
		return operand{mode: KindValue, typ: Typ[Usize]}
	}
	fargs := make([]FormatArg, len(args))
	for i := range args {
		x := &args[i]
		if x.invalid() {
			ok = false
			continue
		}
		v := verbs[i].Verb
		if IsUntyped(x.typ) {
			if v == 'c' {
				c.convertUntyped(x, Typ[U8])
			} else {
				c.defaultType(x, "argument to "+name)
			}
			if IsUntyped(x.typ) || IsInvalid(x.typ) {
				ok = false
				continue
			}
		}
		fa, good := c.formatArg(e.Args[2+i], x.typ, v)
		if !good {
			ok = false
			continue
		}
		fargs[i] = fa
	}
	if ok {
		ci.Format = &FormatInfo{Pieces: pieces, Args: fargs}
	}
	return operand{mode: KindValue, typ: Typ[Usize]}
}

// formatArg matches one argument of type t against verb v.
func (c *Checker) formatArg(a ast.Expr, t Type, v byte) (FormatArg, bool) {
	if ClassOf(t) != ClassValue {
		c.errorf(diag.E2002, a.Span(), "cannot format %s (type %s): formatTo arguments must be Value types", exprString(a), t)
		return FormatArg{}, false
	}
	op := FmtNone
	switch v {
	case 'd', 'x':
		if IsInteger(t) {
			op = intOp(t, v == 'x')
		}
	case 's', 'q':
		if IsString(t) {
			op = FmtStr
		} else if isByteSlice(t) {
			op = FmtBytes
		}
		if v == 'q' && op != FmtNone {
			op += FmtQuoteStr - FmtStr
		}
	case 'c':
		if basicKind(t) == U8 {
			op = FmtByte
		}
	case 'v':
		if n, isNamed := t.(*Named); isNamed && n.Builtin == 0 {
			if m := c.lookupMethod(n, "Format"); m != nil {
				if !formatMethodOK(m) {
					c.errorf(diag.E2002, a.Span(), "cannot use %s (type %s) as argument for %%v in formatTo: method %s.Format must be a total value-receiver method func(dst []byte) usize", exprString(a), t, t)
					return FormatArg{}, false
				}
				return FormatArg{Op: FmtFormat, Method: m}, true
			}
		}
		switch {
		case IsBoolean(t):
			op = FmtBool
		case IsInteger(t):
			op = intOp(t, false)
		case IsString(t):
			op = FmtStr
		case isByteSlice(t):
			op = FmtBytes
		}
	}
	if op == FmtNone {
		c.errorf(diag.E2002, a.Span(), "cannot use %s (type %s) as argument for %%%c in formatTo", exprString(a), t, v)
		return FormatArg{}, false
	}
	return FormatArg{Op: op}, true
}
