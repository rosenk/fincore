package ast

import (
	"strings"
	"testing"
)

func id(name string) *Ident { return &Ident{Name: name} }

func TestExprAsType(t *testing.T) {
	cases := []struct {
		e    Expr
		want string
	}{
		{id("u8"), "u8"},
		{&SelectorExpr{X: id("pkg"), Sel: id("T")}, "pkg.T"},
		{&IndexExpr{X: id("Vec"), Indices: []Expr{&BasicLit{Raw: "4"}, id("u8")}}, "Vec[4, u8]"},
		{&IndexExpr{X: &SelectorExpr{X: id("p"), Sel: id("Slot")}, Indices: []Expr{id("T")}}, "p.Slot[T]"},
		{&UnaryExpr{Op: OpDeref, X: id("T")}, "*T"},
		{&ParenExpr{X: id("T")}, "T"},
		{&TypeExpr{Type: &SliceType{Elem: &NamedType{Name: id("byte")}}}, "[]byte"},
	}
	for _, c := range cases {
		ty, ok := ExprAsType(c.e)
		if !ok || Format(ty) != c.want {
			t.Errorf("ExprAsType(%s) = %v, %v", Format(c.e), ty, ok)
		}
	}
	for _, e := range []Expr{
		&BasicLit{Raw: "1"},
		&UnaryExpr{Op: OpNeg, X: id("x")},
		&SelectorExpr{X: &SelectorExpr{X: id("a"), Sel: id("b")}, Sel: id("c")},
		&CallExpr{Fun: id("f")},
	} {
		if _, ok := ExprAsType(e); ok {
			t.Errorf("%s should not be a type", Format(e))
		}
	}
}

func TestInspectSkip(t *testing.T) {
	e := &BinaryExpr{Op: OpAdd, X: &CallExpr{Fun: id("f"), Args: []Expr{id("a")}}, Y: id("b")}
	var seen []string
	Inspect(e, func(n Node) bool {
		switch n := n.(type) {
		case *Ident:
			seen = append(seen, n.Name)
		case *CallExpr:
			return false
		}
		return true
	})
	if strings.Join(seen, ",") != "b" {
		t.Fatal(seen)
	}
	if got := ExprString(e); got != "(f(a) + b)" {
		t.Fatal(got)
	}
}

func TestDumpOmitsZeroFields(t *testing.T) {
	var ids IDAlloc
	x := id("x")
	Init(x, ids.Next(), x.Span())
	r := &ReturnStmt{Results: []Expr{x}}
	Init(r, ids.Next(), r.Span())
	got := DumpString(r)
	want := "ReturnStmt #2 @0:0-0:0\n  Results:\n    - Ident #1 @0:0-0:0 Name=\"x\"\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	if ids.Count() != 2 {
		t.Fatal(ids.Count())
	}
}
