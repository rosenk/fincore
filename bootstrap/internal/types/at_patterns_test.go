package types_test

import (
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
)

// v0.2 `name @ pattern` bindings: the name binds the value at its
// position, which must also match the pattern. For usefulness `x @ P` is
// P. These sources are also diffcheck snippets (finc0 vs finc1).
func TestAtPatterns(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"valid forms", patDecls + `const K = 2
func f(v Val, s Shape, o Opt[Opt[u32]], n u32) u32 {
	match v {
		Int(h @ K, l) | Int(l, h @ K) -> return h + l
		w @ Bool(b @ true) -> return g(w, b)
		Bool(_) -> return 1
		x @ Str(_) -> return g(x, false)
		Int(..) -> return 2
	}
	match (s, n) {
		(c @ Circle(r), m @ (1 | 2)) -> return r + m + area(c)
		((e @ Empty) | (e @ Rect(_, _)), _) -> return area(e)
		(Circle(_), _) -> return 3
	}
	if o is a @ Some(b @ Some(c)) && c > 0 {
		return flat(a) + flat2(b) + c
	}
	if !(s is r2 @ (Rect(_, _) | Circle(_))) {
		return 0
	}
	return area(r2)
}
func g(v Val, b bool) u32 {
	return 0
}
func area(s Shape) u32 {
	return 0
}
func flat(o Opt[Opt[u32]]) u32 {
	return 0
}
func flat2(o Opt[u32]) u32 {
	return 0
}
` + mainStub, nil},
		{"usefulness", patDecls + `func f(s Shape, o Opt[u32]) u32 {
	match s {
		all @ _ -> return 1
		Empty -> return 2
	}
	match o {
		Some(x @ _) -> return x
		y @ None -> return 0
		z @ _ -> return 9
	}
}
func h(s Shape) u32 {
	return match s {
		c @ (Circle(_) | Empty) -> 1
		r @ Rect(_, _) -> 2
	}
}
func k(s Shape) u32 {
	return match s {
		c @ Circle(_) -> 1
		r @ Empty -> 2
	}
}
` + mainStub, []string{
			"E2012@4: unreachable match arm: it follows the catch-all binding all",
			"E2012@9: unreachable binding z: all variants of Opt[u32] are covered",
			"E2012@19: non-exhaustive match on Shape: missing Rect",
		}},
		{"type errors", patDecls + `func f(v Val, a u32, b u32) u32 {
	match v {
		x @ Int(x, _) -> return 1
		(w @ Bool(_)) | Str(_) -> return 2
		Int(n @ 1, _) | Int(_, n @ true) -> return n
		_ -> return 0
	}
	match (a, b) {
		t @ (p, _) -> return p
	}
}
` + mainStub, []string{
			"E2002@3: variable x is bound more than once in a pattern",
			"E2002@4: or-pattern alternatives must bind the same variables (w is not bound here)",
			"E2002@5: cannot use true (untyped bool constant) as type u32",
			"E2002@9: cannot bind t to a tuple of type (u32, u32) (use '_' or a tuple pattern)",
		}},
		{"resolver errors", patDecls + `const K = 2
func f(v Val) u32 {
	match v {
		K @ Int(..) -> return 1
		Empty @ Bool(_) -> return 2
		_ @ Str(_) -> return 3
		_ -> return 0
	}
}
` + mainStub, []string{
			"E2002@4: cannot bind K with '@': K is a constant",
			"E2002@5: cannot bind Empty with '@': Empty is the name of a variant",
			"E2002@6: cannot bind _ with '@' (write the pattern alone)",
		}},
		{"comptime", patDecls + `comptime func pick(o Opt[u32]) u32 {
	return match o {
		w @ Some(x @ 3) -> unwrap(w) + x
		Some(x) -> x
		n @ None -> unwrap(n)
	}
}
comptime func unwrap(o Opt[u32]) u32 {
	return match o {
		Some(x) -> x
		None -> 100
	}
}
const A = pick(Opt[u32].Some(3))
const B = pick(Opt[u32].None)
const C = A * 1000 + B
` + mainStub, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := patDiags(t, tc.src)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestAtPatternConstValue checks that comptime binds '@' names at every
// position (the value of C is 3 + 3 = 6 and 100: 6100).
func TestAtPatternConstValue(t *testing.T) {
	src := patDecls + `comptime func pick(o Opt[u32]) u32 {
	return match o {
		w @ Some(x @ 3) -> unwrap(w) + x
		Some(x) -> x
		n @ None -> unwrap(n)
	}
}
comptime func unwrap(o Opt[u32]) u32 {
	return match o {
		Some(x) -> x
		None -> 100
	}
}
const C = pick(Opt[u32].Some(3)) * 1000 + pick(Opt[u32].None)
` + mainStub
	info, d := checkSrc(t, src, 100000)
	if len(d) > 0 {
		t.Fatalf("diagnostics: %v", d)
	}
	for _, sym := range info.Prog.Defs {
		if sym == nil || sym.Name != "C" {
			continue
		}
		spec, ok := sym.Decl.(*ast.ConstSpec)
		if !ok {
			t.Fatalf("C declared by %T", sym.Decl)
		}
		if v := info.ValueOf(spec.Value); v == nil || v.String() != "6100" {
			t.Fatalf("C = %v, want 6100", v)
		}
		return
	}
	t.Fatal("C not found")
}
