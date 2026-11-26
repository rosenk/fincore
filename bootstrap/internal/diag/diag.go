// Package diag defines source positions, spans and compiler diagnostics for
// finc0. The textual format is fixed by spec section 11 and must be identical
// in finc0 and finc1:
//
//	file.fc:line:col: error[E####]: <message>
//
// Lines and columns are 1-based; columns count bytes (not runes), like Go.
package diag

import (
	"fmt"
	"slices"
	"strings"
)

// Pos is a position inside a source file.
type Pos struct {
	Offset int // 0-based byte offset
	Line   int // 1-based line
	Col    int // 1-based byte column
}

// IsValid reports whether p denotes a real position (Line > 0).
func (p Pos) IsValid() bool { return p.Line > 0 }

// SourceSpan is a half-open byte range [Start, End) in one file.
// End is the position just past the last byte of the construct.
type SourceSpan struct {
	File  string
	Start Pos
	End   Pos
}

// Line returns the 1-based start line.
func (s SourceSpan) Line() int { return s.Start.Line }

// Col returns the 1-based start column.
func (s SourceSpan) Col() int { return s.Start.Col }

// IsValid reports whether the span has a valid start position.
func (s SourceSpan) IsValid() bool { return s.Start.IsValid() }

// To returns the span from the start of s to the end of e (same file assumed).
func (s SourceSpan) To(e SourceSpan) SourceSpan {
	return SourceSpan{File: s.File, Start: s.Start, End: e.End}
}

// String renders "file:line:col".
func (s SourceSpan) String() string {
	return fmt.Sprintf("%s:%d:%d", s.File, s.Start.Line, s.Start.Col)
}

// Code is a diagnostic code such as E1001.
type Code uint16

// All diagnostic codes from spec section 11.
const (
	E1001 Code = 1001 // SYNTAX_ERROR
	E1002 Code = 1002 // UNTERMINATED_LITERAL
	E2001 Code = 2001 // UNDEFINED_SYMBOL
	E2002 Code = 2002 // TYPE_MISMATCH
	E2003 Code = 2003 // ARITY_MISMATCH
	E2004 Code = 2004 // INVALID_NIL
	E2005 Code = 2005 // UNINITIALIZED_SCOPED
	E2006 Code = 2006 // IMPORT_CYCLE
	E2010 Code = 2010 // UNIVERSE_LEVEL_MISMATCH
	E2011 Code = 2011 // NON_PARAMETRIC_OP
	E3001 Code = 3001 // GLOBAL_SCOPED_VAR
	E3002 Code = 3002 // SCOPE_ESCAPE
	E3003 Code = 3003 // USE_AFTER_MOVE
	E3004 Code = 3004 // DISCARDED_LINEAR
	E3005 Code = 3005 // LINEAR_RESOURCE_LEAK
	E4001 Code = 4001 // COMPTIME_OUT_OF_FUEL
	E4002 Code = 4002 // UNPROVEN_TERMINATION
	E4003 Code = 4003 // ARRAY_BOUNDS_COMPTIME
	E4004 Code = 4004 // MUTUAL_RECURSION_NO_MEASURE
	E5001 Code = 5001 // DRIVER_CALL_IN_TOTAL
	E5002 Code = 5002 // INVALID_MAIN_SIGNATURE
)

// Named aliases, for readability at call sites.
const (
	SyntaxError              = E1001
	UnterminatedLiteral      = E1002
	UndefinedSymbol          = E2001
	TypeMismatch             = E2002
	ArityMismatch            = E2003
	InvalidNil               = E2004
	UninitializedScoped      = E2005
	ImportCycle              = E2006
	UniverseLevelMismatch    = E2010
	NonParametricOp          = E2011
	GlobalScopedVar          = E3001
	ScopeEscape              = E3002
	UseAfterMove             = E3003
	DiscardedLinear          = E3004
	LinearResourceLeak       = E3005
	ComptimeOutOfFuel        = E4001
	UnprovenTermination      = E4002
	ArrayBoundsComptime      = E4003
	MutualRecursionNoMeasure = E4004
	DriverCallInTotal        = E5001
	InvalidMainSignature     = E5002
)

var codeNames = map[Code]string{
	E1001: "SYNTAX_ERROR",
	E1002: "UNTERMINATED_LITERAL",
	E2001: "UNDEFINED_SYMBOL",
	E2002: "TYPE_MISMATCH",
	E2003: "ARITY_MISMATCH",
	E2004: "INVALID_NIL",
	E2005: "UNINITIALIZED_SCOPED",
	E2006: "IMPORT_CYCLE",
	E2010: "UNIVERSE_LEVEL_MISMATCH",
	E2011: "NON_PARAMETRIC_OP",
	E3001: "GLOBAL_SCOPED_VAR",
	E3002: "SCOPE_ESCAPE",
	E3003: "USE_AFTER_MOVE",
	E3004: "DISCARDED_LINEAR",
	E3005: "LINEAR_RESOURCE_LEAK",
	E4001: "COMPTIME_OUT_OF_FUEL",
	E4002: "UNPROVEN_TERMINATION",
	E4003: "ARRAY_BOUNDS_COMPTIME",
	E4004: "MUTUAL_RECURSION_NO_MEASURE",
	E5001: "DRIVER_CALL_IN_TOTAL",
	E5002: "INVALID_MAIN_SIGNATURE",
}

// AllCodes returns every known code in ascending order.
func AllCodes() []Code {
	out := make([]Code, 0, len(codeNames))
	for c := range codeNames {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// String renders the code as "E1001".
func (c Code) String() string { return fmt.Sprintf("E%04d", uint16(c)) }

// Name returns the symbolic name, e.g. "SYNTAX_ERROR" ("" if unknown).
func (c Code) Name() string { return codeNames[c] }

// Known reports whether c is one of the codes from spec section 11.
func (c Code) Known() bool { _, ok := codeNames[c]; return ok }

// ParseCode parses "E1001" into a Code.
func ParseCode(s string) (Code, bool) {
	if len(s) != 5 || s[0] != 'E' {
		return 0, false
	}
	n := 0
	for _, ch := range s[1:] {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	c := Code(n)
	return c, c.Known()
}

// Diagnostic is a single compiler error.
type Diagnostic struct {
	Code    Code
	Span    SourceSpan
	Message string
}

// New constructs a Diagnostic with a formatted message.
func New(code Code, span SourceSpan, format string, args ...any) Diagnostic {
	return Diagnostic{Code: code, Span: span, Message: fmt.Sprintf(format, args...)}
}

// String renders the canonical one-line form
// "file.fc:line:col: error[E####]: message".
func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%d:%d: error[%s]: %s", d.Span.File, d.Span.Start.Line, d.Span.Start.Col, d.Code, d.Message)
}

// Error implements error.
func (d Diagnostic) Error() string { return d.String() }

// List is an ordered collection of diagnostics.
type List []Diagnostic

// Add appends a diagnostic.
func (l *List) Add(d Diagnostic) { *l = append(*l, d) }

// Addf appends a formatted diagnostic.
func (l *List) Addf(code Code, span SourceSpan, format string, args ...any) {
	l.Add(New(code, span, format, args...))
}

// Less is the canonical deterministic ordering: file, offset, line, col,
// code, message.
func Less(a, b Diagnostic) bool { return compare(a, b) < 0 }

func compare(a, b Diagnostic) int {
	if c := strings.Compare(a.Span.File, b.Span.File); c != 0 {
		return c
	}
	if a.Span.Start.Offset != b.Span.Start.Offset {
		return cmpInt(a.Span.Start.Offset, b.Span.Start.Offset)
	}
	if a.Span.Start.Line != b.Span.Start.Line {
		return cmpInt(a.Span.Start.Line, b.Span.Start.Line)
	}
	if a.Span.Start.Col != b.Span.Start.Col {
		return cmpInt(a.Span.Start.Col, b.Span.Start.Col)
	}
	if a.Code != b.Code {
		return cmpInt(int(a.Code), int(b.Code))
	}
	return strings.Compare(a.Message, b.Message)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Sort sorts the list in canonical order (stable) and removes exact duplicates.
func (l *List) Sort() {
	slices.SortStableFunc(*l, compare)
	*l = slices.CompactFunc(*l, func(a, b Diagnostic) bool { return a == b })
}

// Len returns the number of diagnostics.
func (l List) Len() int { return len(l) }

// HasErrors reports whether the list is non-empty.
func (l List) HasErrors() bool { return len(l) > 0 }

// Err returns nil for an empty list, otherwise the list itself as an error.
func (l List) Err() error {
	if len(l) == 0 {
		return nil
	}
	return l
}

// Error renders all diagnostics, one per line (no trailing newline).
func (l List) Error() string {
	var b strings.Builder
	for i, d := range l {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(d.String())
	}
	return b.String()
}
