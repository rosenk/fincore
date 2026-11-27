package parser

import (
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
)

// v0.2: match expressions and or-patterns.

func TestMatchExpr(t *testing.T) {
	src := `package p

func f(s S, n u32) u32 {
	v := match s {
		Circle(r) -> r
		Rect(w, h) -> w * h
		Empty -> 0
	}
	w := match n { 1 | 2 | 3 -> 1, 4 -> 2, _ -> 3 }
	x := match n {
		0 -> 1,
		_ -> match s { A | S.B -> 2; _ -> 3 },
	}
	if match n { 0 -> true; _ -> false } {
		return 1
	}
	g(match n { _ -> 0 })
	return v + w + x
}
`
	f, ids := mustParse(t, "m.fc", src)
	checkInvariants(t, f, ids)
	var kinds []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.MatchExpr:
			kinds = append(kinds, "M"+string(rune('0'+len(n.Arms))))
		case *ast.OrPattern:
			kinds = append(kinds, "O"+string(rune('0'+len(n.Alts))))
		}
		return true
	})
	want := "M3 M3 O3 M2 M2 O2 M2 M1"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	// A statement-position match stays a MatchStmt; or-patterns work there too.
	stmts := parseBody(t, "match c {\n\tRed | Green -> f()\n\t_ -> {}\n}")
	m, ok := stmts[0].(*ast.MatchStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if op, ok := m.Arms[0].Pattern.(*ast.OrPattern); !ok || len(op.Alts) != 2 {
		t.Fatalf("arm 0 pattern %T", m.Arms[0].Pattern)
	}
	if got := ast.Format(m.Arms[0]); got != "Red | Green -> f()" {
		t.Errorf("format %q", got)
	}
	e := parseExpr(t, "match x { A(_) | B -> 1; _ -> 2 }")
	if got := ast.Format(e); got != "match x { A(_) | B -> 1; _ -> 2 }" {
		t.Errorf("format %q", got)
	}
}

func TestMatchExprErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{"package p\nvar x = match y {}", "2:18: error[E1001]: expected pattern ('_', identifier, variant or literal), found '}'"},
		{"package p\nvar x = match y { A -> { return } }", "2:24: error[E1001]: expected expression, found '{'"},
		{"package p\nvar x = match y { A -> 1 B -> 2 }", "2:26: error[E1001]: unexpected identifier B after match arm; expected ';', ',' or newline"},
		{"package p\nvar x = match y\n{ A -> 1 }", "2:16: error[E1001]: unexpected newline; the '{' of a match expression must be on the same line"},
		{"package p\nvar x = match y { A | -> 1 }", "2:23: error[E1001]: expected pattern"},
	}
	for _, tc := range cases {
		_, d := ParseFile("t.fc", []byte(tc.src), nil)
		if len(d) != 1 || !strings.HasPrefix(d[0].String(), "t.fc:"+tc.want) {
			t.Errorf("%q:\n got %v\nwant t.fc:%s", tc.src, d, tc.want)
		}
	}
}

// v0.2: `name @ pattern` bindings in every position; '@' takes a single
// alternative, so an or-pattern after it (or an '@' pattern inside an
// or-pattern) needs parentheses.
func TestAtPattern(t *testing.T) {
	stmts := parseBody(t, "match v {\n\tw @ B(b @ (K | 2), ..) -> f()\n\t(x @ C) | (x @ D(_)) -> f()\n\t(a @ _, x @ y @ Some(z @ 1)) -> {}\n\tn @ (1 | 2) -> {}\n}")
	m, ok := stmts[0].(*ast.MatchStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	want := []string{
		"w @ B(b @ (K | 2), ..) -> f()",
		"(x @ C) | (x @ D(_)) -> f()",
		"(a @ _, x @ y @ Some(z @ 1)) -> {}",
		"n @ (1 | 2) -> {}",
	}
	for i, a := range m.Arms {
		if got := ast.Format(a); got != want[i] {
			t.Errorf("arm %d: format %q, want %q", i, got, want[i])
		}
	}
	at, ok := m.Arms[0].Pattern.(*ast.AtPattern)
	if !ok || at.Name.Name != "w" {
		t.Fatalf("arm 0 pattern %T", m.Arms[0].Pattern)
	}
	cp, ok := at.Pattern.(*ast.CtorPattern)
	if !ok || len(cp.Args) != 2 {
		t.Fatalf("arm 0 inner %T", at.Pattern)
	}
	if inner, ok := cp.Args[0].(*ast.AtPattern); !ok || inner.Name.Name != "b" {
		t.Fatalf("payload %T", cp.Args[0])
	} else if _, ok := inner.Pattern.(*ast.OrPattern); !ok {
		t.Fatalf("payload inner %T", inner.Pattern)
	}
	if _, ok := m.Arms[1].Pattern.(*ast.OrPattern); !ok {
		t.Fatalf("arm 1 pattern %T", m.Arms[1].Pattern)
	}
	errs := []struct{ src, want string }{
		{"package p\nvar x = match y { a @ A | B -> 1 }", "2:19: error[E1001]: ambiguous '@' pattern in an or-pattern: write a @ (P | Q) or (a @ P) | Q"},
		{"package p\nvar x = match y { A | a @ B -> 1 }", "2:23: error[E1001]: ambiguous '@' pattern in an or-pattern: write a @ (P | Q) or (a @ P) | Q"},
		{"package p\nvar x = match y { S(a @ 1 | 2) -> 1 }", "2:21: error[E1001]: ambiguous '@' pattern in an or-pattern"},
		{"package p\nvar x = match y { a @ -> 1 }", "2:23: error[E1001]: expected pattern"},
		{"package p\nvar x = match y { A.b @ C -> 1 }", "2:23: error[E1001]: expected '->', found '@'"},
	}
	for _, tc := range errs {
		_, d := ParseFile("t.fc", []byte(tc.src), nil)
		if len(d) != 1 || !strings.HasPrefix(d[0].String(), "t.fc:"+tc.want) {
			t.Errorf("%q:\n got %v\nwant t.fc:%s", tc.src, d, tc.want)
		}
	}
}
