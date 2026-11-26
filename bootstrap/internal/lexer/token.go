package lexer

import "fincore/bootstrap/internal/diag"

// Kind identifies a token class.
type Kind uint8

// Token kinds.
const (
	ILLEGAL Kind = iota // lexical error; Token.Err holds the diagnostic
	EOF

	IDENT  // main, _, u8, Code, El
	INT    // 1024, 0xFF, 0b1010, 10_000
	STRING // "abc"
	CHAR   // 'a'

	keywordBeg
	PACKAGE
	IMPORT
	CONST
	TYPE
	VAR
	FUNC
	DRIVER
	COMPTIME
	SCOPE
	FOR
	IF
	ELSE
	RETURN
	BREAK
	CONTINUE
	DEFER
	STRUCT
	ENUM
	MATCH
	TRUE
	FALSE
	NIL
	ANY
	DECREASES
	keywordEnd

	operatorBeg
	ADD        // +
	SUB        // -
	MUL        // *
	QUO        // /
	REM        // %
	AND        // &
	OR         // |
	XOR        // ^
	SHL        // <<
	SHR        // >>
	EQL        // ==
	NEQ        // !=
	LSS        // <
	LEQ        // <=
	GTR        // >
	GEQ        // >=
	LAND       // &&
	LOR        // ||
	NOT        // !
	ASSIGN     // =
	DEFINE     // :=
	ADD_ASSIGN // +=
	SUB_ASSIGN // -=
	MUL_ASSIGN // *=
	QUO_ASSIGN // /=
	INC        // ++
	DEC        // --
	LPAREN     // (
	RPAREN     // )
	LBRACK     // [
	RBRACK     // ]
	LBRACE     // {
	RBRACE     // }
	COMMA      // ,
	PERIOD     // .
	COLON      // :
	SEMICOLON  // ; (explicit or automatically inserted)
	ARROW      // ->
	ELLIPSIS   // ...
	operatorEnd
)

var kindStrings = [...]string{
	ILLEGAL: "ILLEGAL",
	EOF:     "EOF",
	IDENT:   "IDENT",
	INT:     "INT",
	STRING:  "STRING",
	CHAR:    "CHAR",

	PACKAGE:   "package",
	IMPORT:    "import",
	CONST:     "const",
	TYPE:      "type",
	VAR:       "var",
	FUNC:      "func",
	DRIVER:    "driver",
	COMPTIME:  "comptime",
	SCOPE:     "scope",
	FOR:       "for",
	IF:        "if",
	ELSE:      "else",
	RETURN:    "return",
	BREAK:     "break",
	CONTINUE:  "continue",
	DEFER:     "defer",
	STRUCT:    "struct",
	ENUM:      "enum",
	MATCH:     "match",
	TRUE:      "true",
	FALSE:     "false",
	NIL:       "nil",
	ANY:       "any",
	DECREASES: "decreases",

	ADD:        "+",
	SUB:        "-",
	MUL:        "*",
	QUO:        "/",
	REM:        "%",
	AND:        "&",
	OR:         "|",
	XOR:        "^",
	SHL:        "<<",
	SHR:        ">>",
	EQL:        "==",
	NEQ:        "!=",
	LSS:        "<",
	LEQ:        "<=",
	GTR:        ">",
	GEQ:        ">=",
	LAND:       "&&",
	LOR:        "||",
	NOT:        "!",
	ASSIGN:     "=",
	DEFINE:     ":=",
	ADD_ASSIGN: "+=",
	SUB_ASSIGN: "-=",
	MUL_ASSIGN: "*=",
	QUO_ASSIGN: "/=",
	INC:        "++",
	DEC:        "--",
	LPAREN:     "(",
	RPAREN:     ")",
	LBRACK:     "[",
	RBRACK:     "]",
	LBRACE:     "{",
	RBRACE:     "}",
	COMMA:      ",",
	PERIOD:     ".",
	COLON:      ":",
	SEMICOLON:  ";",
	ARROW:      "->",
	ELLIPSIS:   "...",
}

// String returns the source spelling for keywords/operators and the class
// name for other kinds.
func (k Kind) String() string {
	if int(k) < len(kindStrings) && kindStrings[k] != "" {
		return kindStrings[k]
	}
	return "Kind(?)"
}

// IsKeyword reports whether k is a reserved word.
func (k Kind) IsKeyword() bool { return k > keywordBeg && k < keywordEnd }

// IsOperator reports whether k is an operator or punctuation token.
func (k Kind) IsOperator() bool { return k > operatorBeg && k < operatorEnd }

// IsLiteral reports whether k is a literal token class (INT, STRING, CHAR).
func (k Kind) IsLiteral() bool { return k == INT || k == STRING || k == CHAR }

var keywords map[string]Kind

func init() {
	keywords = make(map[string]Kind)
	for k := keywordBeg + 1; k < keywordEnd; k++ {
		keywords[kindStrings[k]] = k
	}
}

// Lookup maps an identifier spelling to its keyword kind, or IDENT.
func Lookup(name string) Kind {
	if k, ok := keywords[name]; ok {
		return k
	}
	return IDENT
}

// Keywords returns all reserved words in declaration order.
func Keywords() []string {
	var out []string
	for k := keywordBeg + 1; k < keywordEnd; k++ {
		out = append(out, kindStrings[k])
	}
	return out
}

// Token is a lexical token.
type Token struct {
	Kind Kind
	// Lit is the raw source text of the token. For an automatically inserted
	// semicolon it is "\n" (or "" at EOF); for an explicit one it is ";".
	Lit string
	// Value holds the decoded contents of STRING and CHAR literals
	// (escapes resolved). For CHAR it is exactly one byte.
	Value string
	Span  diag.SourceSpan
	// Err is set for ILLEGAL tokens.
	Err *diag.Diagnostic
}

// IsAutoSemicolon reports whether t is a semicolon inserted by the lexer.
func (t Token) IsAutoSemicolon() bool { return t.Kind == SEMICOLON && t.Lit != ";" }

// Describe renders the token for error messages, Go style
// ("newline", "EOF", "identifier x", "'{'").
func (t Token) Describe() string {
	switch t.Kind {
	case SEMICOLON:
		if t.Lit == "\n" {
			return "newline"
		}
		if t.Lit == "" {
			return "EOF"
		}
		return "';'"
	case EOF:
		return "EOF"
	case IDENT:
		return "identifier " + t.Lit
	case INT:
		return "integer literal " + t.Lit
	case STRING:
		return "string literal " + t.Lit
	case CHAR:
		return "char literal " + t.Lit
	case ILLEGAL:
		return "invalid token"
	}
	if t.Kind.IsKeyword() {
		return "keyword " + t.Kind.String()
	}
	return "'" + t.Kind.String() + "'"
}
