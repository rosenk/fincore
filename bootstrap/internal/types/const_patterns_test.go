package types_test

import (
	"fmt"
	"strings"
	"testing"
)

// v0.2 constant patterns and bindings in every pattern position: a bare
// identifier is a variant of the expected enum, else a constant in scope,
// else a binding. These sources are also diffcheck snippets (finc0 vs
// finc1).

const constPatDecls = `package main
type Col enum {
	Red
	Green
	Blue
}
type Opt[T any] enum {
	Some(T)
	None
}
type Val enum {
	Int(u32, u32)
	Bool(bool)
	Str(string)
}
const K = 2
const Hello = "hello"
const Yes = true
const Def = Col.Red
const Wrapped = Opt[u32].Some(1)
`

// constPatDiags checks src (which starts with constPatDecls) and returns
// its diagnostics as "E####@line: message", lines counted after
// constPatDecls.
func constPatDiags(t *testing.T, src string) []string {
	t.Helper()
	if !strings.HasPrefix(src, constPatDecls) {
		t.Fatal("source does not start with constPatDecls")
	}
	off := strings.Count(constPatDecls, "\n")
	_, d := checkSrc(t, src, 1000)
	var out []string
	for _, x := range d {
		out = append(out, fmt.Sprintf("%s@%d: %s", x.Code, x.Span.Start.Line-off, x.Message))
	}
	return out
}

func TestConstPatterns(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"positive", `package main
type Col enum {
	Red
	Green
	Blue
}
type Opt[T any] enum {
	Some(T)
	None
}
type Val enum {
	Int(u32, u32)
	Bool(bool)
	Str(string)
}
const K = 2
const Hello = "hello"
const Yes = true
const Def = Col.Red
const Wrapped = Opt[u32].Some(1)
func f(v Val, c Col, o Opt[u32], n u32) u32 {
	const Local u32 = 7
	const Red = 9
	r := match v {
		Int(K, Local) -> 1
		Int(K, x) if x > K -> x
		Bool(Yes) -> 2
		Bool(false) -> 3
		Str(Hello) -> 4
		Int(..) | Str(_) -> 5
	}
	r += match c {
		Def -> 1
		Green -> 2
		Blue -> 3
	}
	r += match (c, n) {
		(Red, Red) -> 1
		(Green, K) | (Blue, K) -> 2
		(d, m) if d == Def -> m
		(_, m) -> m + 1
	}
	r += match o {
		Some(K) -> 1
		Some(x) -> x
		None -> 0
	}
	return match n {
		Local -> r
		m if m > r -> m
		m -> m + r
	}
}
`, nil},
		{"wrong type", `package main
type Col enum {
	Red
	Green
	Blue
}
type Opt[T any] enum {
	Some(T)
	None
}
type Val enum {
	Int(u32, u32)
	Bool(bool)
	Str(string)
}
const K = 2
const Hello = "hello"
const Yes = true
const Def = Col.Red
const Wrapped = Opt[u32].Some(1)
func f(v Val, n u32, c Col, b bool) u32 {
	match v {
		Int(Hello, _) -> return 1
		_ -> return 0
	}
	match n {
		Yes -> return 1
		Def -> return 2
		_ -> return 0
	}
	match b {
		K -> return 1
		_ -> return 0
	}
	match c {
		K -> return 1
		Wrapped -> return 2
		_ -> return 0
	}
}
`, []string{
			"E2002@3: cannot use Hello (untyped string constant) as type u32",
			"E2002@7: cannot use Yes (untyped bool constant) as type u32",
			"E2002@8: cannot use Def (type Col) as type u32 in match pattern",
			"E2002@12: cannot use K (untyped int constant) as type bool",
			"E2002@16: cannot use K (untyped int constant) as type Col",
			"E2002@17: cannot use Wrapped (type Opt[u32]) as type Col in match pattern",
		}},
		{"enum constant with payload", `package main
type Col enum {
	Red
	Green
	Blue
}
type Opt[T any] enum {
	Some(T)
	None
}
type Val enum {
	Int(u32, u32)
	Bool(bool)
	Str(string)
}
const K = 2
const Hello = "hello"
const Yes = true
const Def = Col.Red
const Wrapped = Opt[u32].Some(1)
func f(o Opt[u32], c Col) u32 {
	match c {
		Def(x) -> return 0
		_ -> return 1
	}
	return match o {
		Wrapped -> 1
		_ -> 0
	}
}
`, []string{
			"E2001@3: undefined: Def (no constant or variant of Col)",
			"E2002@7: constant Wrapped cannot be a pattern: its variant Some has a payload (match the variant instead)",
		}},
		{"unreachable after binding", `package main
type Col enum {
	Red
	Green
	Blue
}
type Opt[T any] enum {
	Some(T)
	None
}
type Val enum {
	Int(u32, u32)
	Bool(bool)
	Str(string)
}
const K = 2
const Hello = "hello"
const Yes = true
const Def = Col.Red
const Wrapped = Opt[u32].Some(1)
func f(n u32, c Col) u32 {
	r := match n {
		x -> x
		K -> 0
	}
	r += match c {
		Red | Green | Blue -> 1
		other -> 2
	}
	r += match n {
		K -> 1
		K -> 2
		_ -> 3
	}
	return match c {
		Def -> 1
		Red -> 2
		_ -> 3
	}
}
`, []string{
			"E2012@4: unreachable match arm: it follows the catch-all binding x",
			"E2012@8: unreachable binding other: all variants of Col are covered",
			"E2012@12: unreachable pattern: duplicate case K",
			"E2012@17: unreachable pattern: variant Red is already covered",
		}},
		{"constants do not cover", `package main
type Col enum {
	Red
	Green
	Blue
}
type Opt[T any] enum {
	Some(T)
	None
}
type Val enum {
	Int(u32, u32)
	Bool(bool)
	Str(string)
}
const K = 2
const Hello = "hello"
const Yes = true
const Def = Col.Red
const Wrapped = Opt[u32].Some(1)
func f(c Col, b bool, v Val, o Opt[u32]) u32 {
	r := match c {
		Def -> 1
		Green -> 2
	}
	r += match (b, c) {
		(Yes, _) -> 1
		(false, Def) -> 2
	}
	r += match v {
		Bool(Yes) -> 1
		Int(..) | Str(_) -> 2
	}
	return match o {
		Some(K) -> 1
		None -> 0
	}
}
`, []string{
			"E2012@2: non-exhaustive match on Col: missing Blue",
			"E2012@6: non-exhaustive match on (bool, Col): missing (false, Green), (false, Blue)",
			"E2012@10: non-exhaustive match on Val: missing Bool(false)",
			"E2012@14: non-exhaustive match on Opt[u32]: missing Some(_)",
		}},
		{"bindings", `package main
type Col enum {
	Red
	Green
	Blue
}
type Opt[T any] enum {
	Some(T)
	None
}
type Val enum {
	Int(u32, u32)
	Bool(bool)
	Str(string)
}
const K = 2
const Hello = "hello"
const Yes = true
const Def = Col.Red
const Wrapped = Opt[u32].Some(1)
func f(a u32, b u32) u32 {
	match (a, b) {
		t -> return 1
	}
	match (a, b) {
		(x, 0) | (0, y) -> return 1
		_ -> return 2
	}
	match (a, b) {
		(x, x) -> return x
	}
	match (a, b) {
		(x, 1) | (true, x) -> return x
		_ -> return 2
	}
}
`, []string{
			"E2002@3: cannot bind t to a tuple of type (u32, u32) (use '_' or a tuple pattern)",
			"E2002@6: or-pattern alternatives must bind the same variables (x is not bound here)",
			"E2002@6: or-pattern alternatives must bind the same variables (y is not bound in the first alternative)",
			"E2002@10: variable x is bound more than once in a pattern",
			"E2002@13: cannot use true (untyped bool constant) as type u32",
		}},
		{"variant names shadow nothing", `package main
type Col enum {
	Red
	Green
	Blue
}
type Opt[T any] enum {
	Some(T)
	None
}
type Val enum {
	Int(u32, u32)
	Bool(bool)
	Str(string)
}
const K = 2
const Hello = "hello"
const Yes = true
const Def = Col.Red
const Wrapped = Opt[u32].Some(1)
func f(c Col, o Opt[u32]) u32 {
	if c is Red && o is Some(Red) {
		return Red
	}
	return 0
}
`, []string{
			"E2001@3: undefined: Red",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := constPatDiags(t, tc.src)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(tc.want, "\n\t"))
			}
		})
	}
}
