package lexer

import (
	"strings"
	"testing"

	"fincore/bootstrap/internal/diag"
)

// render produces a compact token stream: literals/idents by spelling,
// explicit ';' as ";", auto-inserted semicolons as "⏎" (newline) or "⏚" (EOF).
func render(toks []Token) string {
	var parts []string
	for _, t := range toks {
		switch t.Kind {
		case EOF:
			continue
		case SEMICOLON:
			switch t.Lit {
			case ";":
				parts = append(parts, ";")
			case "\n":
				parts = append(parts, "⏎")
			default:
				parts = append(parts, "⏚")
			}
		case ILLEGAL:
			parts = append(parts, "ILLEGAL")
		default:
			parts = append(parts, t.Lit)
		}
	}
	return strings.Join(parts, " ")
}

func lex(t *testing.T, src string) []Token {
	t.Helper()
	toks, diags := Tokenize("t.fc", []byte(src))
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics for %q: %v", src, diags)
	}
	return toks
}

func TestKeywords(t *testing.T) {
	kws := Keywords()
	want := "package import const type var func driver comptime scope for if else return break continue defer struct enum match true false nil any decreases is"
	if got := strings.Join(kws, " "); got != want {
		t.Fatalf("keywords:\n got %s\nwant %s", got, want)
	}
	for _, k := range kws {
		if Lookup(k) == IDENT {
			t.Errorf("%s not a keyword", k)
		}
	}
	for _, id := range []string{"Code", "El", "u8", "usize", "main", "_", "len", "panic"} {
		if Lookup(id) != IDENT {
			t.Errorf("%s should be IDENT", id)
		}
	}
}

func TestOperators(t *testing.T) {
	src := "+ - * / % & | ^ << >> == != < <= > >= && || ! = := += -= *= /= ++ -- ( ) [ ] { } , . : ; -> ..."
	toks := lex(t, src)
	want := []Kind{ADD, SUB, MUL, QUO, REM, AND, OR, XOR, SHL, SHR, EQL, NEQ, LSS, LEQ, GTR, GEQ, LAND, LOR, NOT,
		ASSIGN, DEFINE, ADD_ASSIGN, SUB_ASSIGN, MUL_ASSIGN, QUO_ASSIGN, INC, DEC,
		LPAREN, RPAREN, LBRACK, RBRACK, LBRACE, RBRACE, COMMA, PERIOD, COLON, SEMICOLON, ARROW, ELLIPSIS}
	var got []Kind
	for _, tk := range toks {
		if tk.Kind == EOF || tk.Kind == SEMICOLON && tk.Lit != ";" {
			continue
		}
		got = append(got, tk.Kind)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d: got %s want %s", i, got[i], want[i])
		}
		if got[i].String() != strings.Fields(src)[i] {
			t.Errorf("String(%d) = %q", i, got[i].String())
		}
	}
}

func TestLongestMatch(t *testing.T) {
	cases := map[string]string{
		"a<<=b":    "a << = b ⏚",
		"x->y":     "x -> y ⏚",
		"a--b":     "a -- b ⏚",
		"a- -b":    "a - - b ⏚",
		"x....":    "x ... .",
		"a..b":     "a .. b ⏚",
		"a:=b":     "a := b ⏚",
		"!==":      "!= =",
		"a&&&b":    "a && & b ⏚",
		"a/b/*c*/": "a / b ⏚",
	}
	for src, want := range cases {
		if got := render(lex(t, src)); got != want {
			t.Errorf("%q: got %q want %q", src, got, want)
		}
	}
}

func TestSemicolonInsertion(t *testing.T) {
	cases := []struct{ src, want string }{
		{"x\n", "x ⏎"},
		{"x", "x ⏚"},
		{"42\n", "42 ⏎"},
		{"\"s\"\n", "\"s\" ⏎"},
		{"'c'\n", "'c' ⏎"},
		{"true\nfalse\nnil\n", "true ⏎ false ⏎ nil ⏎"},
		{"break\ncontinue\nreturn\n", "break ⏎ continue ⏎ return ⏎"},
		{"i++\ni--\n", "i ++ ⏎ i -- ⏎"},
		{"f()\na[1]\n{}\n", "f ( ) ⏎ a [ 1 ] ⏎ { } ⏎"},
		// no insertion after operators, opening brackets, most keywords
		{"a +\nb\n", "a + b ⏎"},
		{"f(\na,\nb)\n", "f ( a , b ) ⏎"},
		{"func f() {\n}\n", "func f ( ) { } ⏎"},
		{"if x {\n", "if x {"},
		{"package\nmain\n", "package main ⏎"},
		{"x :=\n1\n", "x := 1 ⏎"},
		{"a ->\nb\n", "a -> b ⏎"},
		{"defer\nx\n", "defer x ⏎"},
		// blank lines do not produce extra semicolons
		{"x\n\n\ny\n", "x ⏎ y ⏎"},
		// comments
		{"x // comment\ny", "x ⏎ y ⏚"},
		{"x /* inline */ y", "x y ⏚"},
		{"x /* multi\nline */ y", "x ⏎ y ⏚"},
		{"x + /* multi\nline */ y", "x + y ⏚"},
		{"return // done\n", "return ⏎"},
		{"x\r\ny\r\n", "x ⏎ y ⏎"},
		{"// only a comment", ""},
		{"", ""},
		// explicit semicolons
		{"a; b;\n", "a ; b ;"},
		{"for i := 0; i < n; i++ {\n}\n", "for i := 0 ; i < n ; i ++ { } ⏎"},
	}
	for _, c := range cases {
		if got := render(lex(t, c.src)); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.src, got, c.want)
		}
	}
}

func TestAutoSemicolonSpan(t *testing.T) {
	toks := lex(t, "ab\ncd")
	semi := toks[1]
	if !semi.IsAutoSemicolon() || semi.Span.Start.Line != 1 || semi.Span.Start.Col != 3 {
		t.Fatalf("bad semicolon %+v", semi)
	}
	if semi.Describe() != "newline" {
		t.Fatal(semi.Describe())
	}
	cd := toks[2]
	if cd.Span.Start.Line != 2 || cd.Span.Start.Col != 1 || cd.Span.Start.Offset != 3 || cd.Span.End.Col != 3 {
		t.Fatalf("bad span %+v", cd.Span)
	}
}

func TestIntLiterals(t *testing.T) {
	cases := map[string]uint64{
		"0":                    0,
		"7":                    7,
		"1024":                 1024,
		"10_000":               10000,
		"1_000_000":            1000000,
		"0xFF":                 255,
		"0Xff":                 255,
		"0x_FF":                255,
		"0xDEAD_BEEF":          0xDEADBEEF,
		"0b1010":               10,
		"0B1_0":                2,
		"18446744073709551615": ^uint64(0),
		"0xFFFFFFFFFFFFFFFF":   ^uint64(0),
		"4194304":              4194304,
	}
	for src, want := range cases {
		toks := lex(t, src)
		if toks[0].Kind != INT || toks[0].Lit != src {
			t.Errorf("%q: got %v %q", src, toks[0].Kind, toks[0].Lit)
			continue
		}
		v, msg := ParseInt(src)
		if msg != "" || v != want {
			t.Errorf("ParseInt(%q) = %d, %q; want %d", src, v, msg, want)
		}
	}
}

func TestStringAndCharLiterals(t *testing.T) {
	cases := []struct {
		src  string
		kind Kind
		val  string
	}{
		{`"0.0.0.0:8080"`, STRING, "0.0.0.0:8080"},
		{`""`, STRING, ""},
		{`"a\nb\rc\td"`, STRING, "a\nb\rc\td"},
		{`"q\"q\\"`, STRING, "q\"q\\"},
		{`"\x41\x7a\xFF"`, STRING, "Az\xff"},
		{`"héllo"`, STRING, "héllo"},
		{`'a'`, CHAR, "a"},
		{`'\n'`, CHAR, "\n"},
		{`'\''`, CHAR, "'"},
		{`'\\'`, CHAR, "\\"},
		{`'\x00'`, CHAR, "\x00"},
		{`'"'`, CHAR, "\""},
	}
	for _, c := range cases {
		toks := lex(t, c.src)
		if toks[0].Kind != c.kind || toks[0].Value != c.val || toks[0].Lit != c.src {
			t.Errorf("%s: got %v %q %q", c.src, toks[0].Kind, toks[0].Lit, toks[0].Value)
		}
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		src  string
		want string // "line:col code" + message fragment
		frag string
	}{
		{`"abc`, "1:1 E1002", "unterminated string"},
		{"\"abc\nx", "1:1 E1002", "unterminated string"},
		{`'a`, "1:1 E1002", "unterminated char"},
		{"'", "1:1 E1002", "unterminated char"},
		{"x /* never closed", "1:3 E1002", "unterminated block comment"},
		{"x @ y", "1:3 E1001", "invalid character '@'"},
		{"a $", "1:3 E1001", "invalid character"},
		{"a # b", "1:3 E1001", "invalid character"},
		{"π", "1:1 E1001", "U+03C0"},
		{"\xff", "1:1 E1001", "invalid UTF-8"},
		{"`raw`", "1:1 E1001", "invalid character"},
		{"x?", "1:2 E1001", "invalid character"},
		{"''", "1:1 E1001", "empty char"},
		{"'ab'", "1:1 E1001", "exactly one byte"},
		{"'é'", "1:1 E1001", "single byte"},
		{`"a\qb"`, "1:3 E1001", `invalid escape sequence \q`},
		{`"\x4"`, "1:2 E1001", `\x escape`},
		{`'\"'`, "1:2 E1001", `invalid escape`},
		{`"\'"`, "1:2 E1001", `invalid escape`},
		{"0x", "1:1 E1001", "no digits"},
		{"0b102", "1:1 E1001", "invalid digit"},
		{"0xFG", "1:1 E1001", "invalid digit"},
		{"123abc", "1:1 E1001", "invalid digit"},
		{"1__0", "1:1 E1001", "'_' must separate digits"},
		{"10_", "1:1 E1001", "'_' must separate digits"},
		{"012", "1:1 E1001", "leading zero"},
		{"18446744073709551616", "1:1 E1001", "overflows u64"},
		{"\n\n  @", "3:3 E1001", "invalid character"},
	}
	for _, c := range cases {
		toks, diags := Tokenize("t.fc", []byte(c.src))
		if len(diags) == 0 {
			t.Errorf("%q: expected error", c.src)
			continue
		}
		d := diags[0]
		got := strings.TrimPrefix(d.String(), "t.fc:")
		pos := strings.SplitN(got, ": ", 2)[0]
		wantPos, wantCode, _ := strings.Cut(c.want, " ")
		if pos != wantPos || d.Code.String() != wantCode || !strings.Contains(d.Message, c.frag) {
			t.Errorf("%q: got %s; want %s %s containing %q", c.src, d, wantPos, wantCode, c.frag)
		}
		found := false
		for _, tk := range toks {
			if tk.Kind == ILLEGAL && tk.Err != nil && *tk.Err == d {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: no ILLEGAL token carrying the diagnostic", c.src)
		}
	}
}

func TestRecoveryAfterError(t *testing.T) {
	toks, diags := Tokenize("t.fc", []byte("a @ b\n\"x\ny"))
	if len(diags) != 2 {
		t.Fatalf("want 2 diags, got %v", diags)
	}
	if diags[0].Code != diag.E1001 || diags[1].Code != diag.E1002 {
		t.Fatalf("codes: %v", diags)
	}
	if got := render(toks); got != "a ILLEGAL b ⏎ ILLEGAL y ⏚" {
		t.Fatalf("got %q", got)
	}
}

func TestBOM(t *testing.T) {
	toks := lex(t, "\xEF\xBB\xBFpackage main")
	if toks[0].Kind != PACKAGE || toks[0].Span.Start.Col != 1 {
		t.Fatalf("got %+v", toks[0])
	}
}

func TestDescribe(t *testing.T) {
	toks := lex(t, "x 1 \"s\" 'c' if { ;")
	want := []string{"identifier x", "integer literal 1", "string literal \"s\"", "char literal 'c'", "keyword if", "'{'", "';'", "EOF"}
	for i, w := range want {
		if toks[i].Describe() != w {
			t.Errorf("%d: %q want %q", i, toks[i].Describe(), w)
		}
	}
}
