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
