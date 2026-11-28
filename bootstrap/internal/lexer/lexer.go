// Package lexer converts FinCore source text into tokens (spec section 2).
//
// It implements Go-style automatic semicolon insertion (section 2.2): when a
// newline (or EOF, or a block comment spanning a newline) follows an
// identifier, a literal, true/false/nil, break/continue/return, ++/--, or a
// closing ')' ']' '}', a SEMICOLON token with Lit "\n" ("" at EOF) is emitted.
//
// Lexical errors never abort scanning: each produces an ILLEGAL token whose
// Err field holds an E1001 (bad character / malformed literal) or E1002
// (unterminated string, char or block comment) diagnostic, and scanning
// resumes after it. Identifiers are ASCII-only; non-ASCII bytes are allowed
// only inside string literals and comments.
package lexer

import (
	"fmt"
	"unicode/utf8"

	"fincore/bootstrap/internal/diag"
)

// Lexer scans one source file.
type Lexer struct {
	file       string
	src        []byte
	off        int // current offset
	line       int // current line (1-based)
	lineStart  int // offset of the first byte of the current line
	insertSemi bool
	diags      diag.List
}

// New creates a lexer for src. file is used in spans.
func New(file string, src []byte) *Lexer {
	l := &Lexer{file: file, src: src, line: 1}
	if len(src) >= 3 && src[0] == 0xEF && src[1] == 0xBB && src[2] == 0xBF {
		l.off = 3 // skip UTF-8 BOM
		l.lineStart = 3
	}
	return l
}

// Tokenize scans the whole file. The returned slice always ends with EOF.
// The diagnostic list contains the errors of all ILLEGAL tokens, in order.
func Tokenize(file string, src []byte) ([]Token, diag.List) {
	l := New(file, src)
	var toks []Token
	for {
		t := l.Next()
		toks = append(toks, t)
		if t.Kind == EOF {
			break
		}
	}
	return toks, l.diags
}

// Diagnostics returns the lexical errors reported so far.
func (l *Lexer) Diagnostics() diag.List { return l.diags }

func (l *Lexer) pos() diag.Pos {
	return diag.Pos{Offset: l.off, Line: l.line, Col: l.off - l.lineStart + 1}
}

func (l *Lexer) peekByte(k int) byte {
	if l.off+k < len(l.src) {
		return l.src[l.off+k]
	}
	return 0
}

// advance consumes one byte, tracking newlines.
func (l *Lexer) advance() {
	if l.off < len(l.src) {
		if l.src[l.off] == '\n' {
			l.line++
			l.lineStart = l.off + 1
		}
		l.off++
	}
}

func (l *Lexer) span(start diag.Pos) diag.SourceSpan {
	return diag.SourceSpan{File: l.file, Start: start, End: l.pos()}
}

func (l *Lexer) illegal(start diag.Pos, code diag.Code, format string, args ...any) Token {
	sp := l.span(start)
	d := diag.New(code, sp, format, args...)
	l.diags.Add(d)
	l.insertSemi = false
	return Token{Kind: ILLEGAL, Lit: string(l.src[start.Offset:l.off]), Span: sp, Err: &d}
}

// errAt records a diagnostic at an explicit position, returning an ILLEGAL token spanning start..current.
func (l *Lexer) illegalAt(start, at diag.Pos, code diag.Code, format string, args ...any) Token {
	sp := l.span(start)
	d := diag.New(code, diag.SourceSpan{File: l.file, Start: at, End: at}, format, args...)
	l.diags.Add(d)
	l.insertSemi = false
	return Token{Kind: ILLEGAL, Lit: string(l.src[start.Offset:l.off]), Span: sp, Err: &d}
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isHex(c byte) bool    { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// Next returns the next token.
func (l *Lexer) Next() Token {
	for {
		// skip whitespace (newline handled for semicolon insertion)
		for l.off < len(l.src) {
			c := l.src[l.off]
			if c == '\n' {
				if l.insertSemi {
					start := l.pos()
					l.advance()
					l.insertSemi = false
					return Token{Kind: SEMICOLON, Lit: "\n", Span: diag.SourceSpan{File: l.file, Start: start, End: start}}
				}
				l.advance()
				continue
			}
			if c == ' ' || c == '\t' || c == '\r' {
				l.advance()
				continue
			}
			break
		}
		if l.off >= len(l.src) {
			p := l.pos()
			sp := diag.SourceSpan{File: l.file, Start: p, End: p}
			if l.insertSemi {
				l.insertSemi = false
				return Token{Kind: SEMICOLON, Lit: "", Span: sp}
			}
			return Token{Kind: EOF, Span: sp}
		}
		// comments
		if l.src[l.off] == '/' && l.peekByte(1) == '/' {
			for l.off < len(l.src) && l.src[l.off] != '\n' {
				l.advance()
			}
			continue
		}
		if l.src[l.off] == '/' && l.peekByte(1) == '*' {
			start := l.pos()
			l.advance()
			l.advance()
			sawNewline := false
			closed := false
			for l.off < len(l.src) {
				if l.src[l.off] == '*' && l.peekByte(1) == '/' {
					l.advance()
					l.advance()
					closed = true
					break
				}
				if l.src[l.off] == '\n' {
					sawNewline = true
				}
				l.advance()
			}
			if !closed {
				return l.illegal(start, diag.E1002, "unterminated block comment")
			}
			if sawNewline && l.insertSemi {
				l.insertSemi = false
				return Token{Kind: SEMICOLON, Lit: "\n", Span: diag.SourceSpan{File: l.file, Start: start, End: start}}
			}
			continue
		}
		break
	}

	start := l.pos()
	c := l.src[l.off]
	switch {
	case isLetter(c):
		for l.off < len(l.src) && (isLetter(l.src[l.off]) || isDigit(l.src[l.off])) {
			l.advance()
		}
		lit := string(l.src[start.Offset:l.off])
		k := Lookup(lit)
		switch k {
		case IDENT, TRUE, FALSE, NIL, BREAK, CONTINUE, RETURN:
			l.insertSemi = true
		default:
			l.insertSemi = false
		}
		return Token{Kind: k, Lit: lit, Span: l.span(start)}
	case isDigit(c):
		return l.number(start)
	case c == '"':
		return l.str(start)
	case c == '\'':
		return l.char(start)
	}
	return l.operator(start)
}

func (l *Lexer) operator(start diag.Pos) Token {
	c := l.src[l.off]
	n1 := l.peekByte(1)
	n2 := l.peekByte(2)
	k := ILLEGAL
	size := 1
	switch c {
	case '+':
		k = ADD
		if n1 == '+' {
			k, size = INC, 2
		} else if n1 == '=' {
			k, size = ADD_ASSIGN, 2
		}
	case '-':
		k = SUB
		if n1 == '-' {
			k, size = DEC, 2
		} else if n1 == '=' {
			k, size = SUB_ASSIGN, 2
		} else if n1 == '>' {
			k, size = ARROW, 2
		}
	case '*':
		k = MUL
		if n1 == '=' {
			k, size = MUL_ASSIGN, 2
		}
	case '/':
		k = QUO
		if n1 == '=' {
			k, size = QUO_ASSIGN, 2
		}
	case '%':
		k = REM
		if n1 == '=' {
			k, size = REM_ASSIGN, 2
		}
	case '&':
		k = AND
		if n1 == '&' {
			k, size = LAND, 2
		} else if n1 == '=' {
			k, size = AND_ASSIGN, 2
		}
	case '|':
		k = OR
		if n1 == '|' {
			k, size = LOR, 2
		} else if n1 == '=' {
			k, size = OR_ASSIGN, 2
		}
	case '^':
		k = XOR
		if n1 == '=' {
			k, size = XOR_ASSIGN, 2
		}
	case '<':
		k = LSS
		if n1 == '<' {
			k, size = SHL, 2
			if n2 == '=' {
				k, size = SHL_ASSIGN, 3
			}
		} else if n1 == '=' {
			k, size = LEQ, 2
		}
	case '>':
		k = GTR
		if n1 == '>' {
			k, size = SHR, 2
			if n2 == '=' {
				k, size = SHR_ASSIGN, 3
			}
		} else if n1 == '=' {
			k, size = GEQ, 2
		}
	case '=':
		k = ASSIGN
		if n1 == '=' {
			k, size = EQL, 2
		}
	case '!':
		k = NOT
		if n1 == '=' {
			k, size = NEQ, 2
		}
	case ':':
		k = COLON
		if n1 == '=' {
			k, size = DEFINE, 2
		}
	case '(':
		k = LPAREN
	case ')':
		k = RPAREN
	case '[':
		k = LBRACK
	case ']':
		k = RBRACK
	case '{':
		k = LBRACE
	case '}':
		k = RBRACE
	case ',':
		k = COMMA
	case ';':
		k = SEMICOLON
	case '.':
		k = PERIOD
		if n1 == '.' && n2 == '.' {
			k, size = ELLIPSIS, 3
		}
	}
	if k == ILLEGAL {
		r, w := utf8.DecodeRune(l.src[l.off:])
		for range w {
			l.advance()
		}
		if r == utf8.RuneError && w <= 1 {
			return l.illegal(start, diag.E1001, "invalid UTF-8 byte 0x%02X", c)
		}
		return l.illegal(start, diag.E1001, "invalid character %s", quoteRune(r))
	}
	for range size {
		l.advance()
	}
	switch k {
	case RPAREN, RBRACK, RBRACE, INC, DEC:
		l.insertSemi = true
	default:
		l.insertSemi = false
	}
	return Token{Kind: k, Lit: string(l.src[start.Offset:l.off]), Span: l.span(start)}
}

func quoteRune(r rune) string {
	if r >= 0x20 && r < 0x7F {
		return fmt.Sprintf("%q (U+%04X)", r, r)
	}
	return fmt.Sprintf("U+%04X", r)
}

func (l *Lexer) number(start diag.Pos) Token {
	// consume the maximal run of identifier characters, then validate.
	for l.off < len(l.src) && (isLetter(l.src[l.off]) || isDigit(l.src[l.off])) {
		l.advance()
	}
	lit := string(l.src[start.Offset:l.off])
	if _, err := ParseInt(lit); err != "" {
		return l.illegal(start, diag.E1001, "%s", err)
	}
	l.insertSemi = true
	return Token{Kind: INT, Lit: lit, Span: l.span(start)}
}

// ParseInt decodes an integer literal (decimal, 0x hex, 0b binary, with '_'
// separators between digits). On failure it returns a non-empty message.
func ParseInt(lit string) (uint64, string) {
	digits := lit
	base := uint64(10)
	if len(lit) >= 2 && lit[0] == '0' && (lit[1] == 'x' || lit[1] == 'X') {
		base, digits = 16, lit[2:]
	} else if len(lit) >= 2 && lit[0] == '0' && (lit[1] == 'b' || lit[1] == 'B') {
		base, digits = 2, lit[2:]
	}
	if digits == "" {
		return 0, fmt.Sprintf("invalid integer literal %q: no digits", lit)
	}
	if base == 10 && len(digits) > 1 && digits[0] == '0' {
		return 0, fmt.Sprintf("invalid integer literal %q: leading zero (octal literals are not supported)", lit)
	}
	var v uint64
	prevDigit := false // an '_' directly after a 0x/0b prefix is allowed (Go rule)
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c == '_' {
			if !prevDigit && !(i == 0 && base != 10) {
				return 0, fmt.Sprintf("invalid integer literal %q: '_' must separate digits", lit)
			}
			if i+1 >= len(digits) || digits[i+1] == '_' {
				return 0, fmt.Sprintf("invalid integer literal %q: '_' must separate digits", lit)
			}
			prevDigit = false
			continue
		}
		var d uint64
		switch {
		case isDigit(c):
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			d = 99
		}
		if d >= base {
			return 0, fmt.Sprintf("invalid digit %q in base-%d literal %q", c, base, lit)
		}
		if v > (^uint64(0)-d)/base {
			return 0, fmt.Sprintf("integer literal %q overflows u64", lit)
		}
		v = v*base + d
		prevDigit = true
	}
	return v, ""
}

// escape decodes one escape sequence starting at l.off (pointing at '\\').
// It returns the decoded byte and an error message ("" on success).
func (l *Lexer) escape(quote byte) (byte, string) {
	l.advance() // backslash
	if l.off >= len(l.src) {
		return 0, "eof"
	}
	c := l.src[l.off]
	switch c {
	case 'n':
		l.advance()
		return '\n', ""
	case 'r':
		l.advance()
		return '\r', ""
	case 't':
		l.advance()
		return '\t', ""
	case '\\':
		l.advance()
		return '\\', ""
	case '"', '\'':
		if c != quote {
			return 0, fmt.Sprintf("invalid escape sequence \\%c", c)
		}
		l.advance()
		return c, ""
	case 'x':
		l.advance()
		h1, h2 := l.peekByte(0), l.peekByte(1)
		if !isHex(h1) || !isHex(h2) {
			return 0, "invalid \\x escape: need exactly two hex digits"
		}
		l.advance()
		l.advance()
		return hexVal(h1)<<4 | hexVal(h2), ""
	case '\n':
		return 0, "newline"
	}
	return 0, fmt.Sprintf("invalid escape sequence \\%c", c)
}

func hexVal(c byte) byte {
	switch {
	case isDigit(c):
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

func (l *Lexer) str(start diag.Pos) Token {
	l.advance() // opening quote
	var val []byte
	var badMsg string
	var badPos diag.Pos
	for {
		if l.off >= len(l.src) || l.src[l.off] == '\n' {
			return l.illegal(start, diag.E1002, "unterminated string literal")
		}
		c := l.src[l.off]
		if c == '"' {
			l.advance()
			break
		}
		if c == '\\' {
			p := l.pos()
			b, msg := l.escape('"')
			if msg == "eof" || msg == "newline" {
				return l.illegal(start, diag.E1002, "unterminated string literal")
			}
			if msg != "" {
				if badMsg == "" {
					badMsg, badPos = msg, p
				}
				continue
			}
			val = append(val, b)
			continue
		}
		val = append(val, c)
		l.advance()
	}
	if badMsg != "" {
		return l.illegalAt(start, badPos, diag.E1001, "%s", badMsg)
	}
	l.insertSemi = true
	return Token{Kind: STRING, Lit: string(l.src[start.Offset:l.off]), Value: string(val), Span: l.span(start)}
}

func (l *Lexer) char(start diag.Pos) Token {
	l.advance() // opening quote
	if l.off >= len(l.src) || l.src[l.off] == '\n' {
		return l.illegal(start, diag.E1002, "unterminated char literal")
	}
	c := l.src[l.off]
	var v byte
	switch {
	case c == '\'':
		l.advance()
		return l.illegal(start, diag.E1001, "empty char literal")
	case c == '\\':
		p := l.pos()
		b, msg := l.escape('\'')
		if msg == "eof" || msg == "newline" {
			return l.illegal(start, diag.E1002, "unterminated char literal")
		}
		if msg != "" {
			l.skipCharRest()
			return l.illegalAt(start, p, diag.E1001, "%s", msg)
		}
		v = b
	case c >= 0x80:
		l.skipCharRest()
		return l.illegal(start, diag.E1001, "char literal must be a single byte (ASCII character or escape)")
	default:
		v = c
		l.advance()
	}
	if l.off >= len(l.src) || l.src[l.off] == '\n' {
		return l.illegal(start, diag.E1002, "unterminated char literal")
	}
	if l.src[l.off] != '\'' {
		l.skipCharRest()
		return l.illegal(start, diag.E1001, "char literal must contain exactly one byte")
	}
	l.advance()
	l.insertSemi = true
	return Token{Kind: CHAR, Lit: string(l.src[start.Offset:l.off]), Value: string([]byte{v}), Span: l.span(start)}
}

// skipCharRest skips to just past the closing quote on the same line, if any.
func (l *Lexer) skipCharRest() {
	for i := l.off; i < len(l.src) && l.src[i] != '\n'; i++ {
		if l.src[i] == '\'' {
			for l.off <= i {
				l.advance()
			}
			return
		}
	}
}
