package types_test

import (
	"fmt"
	"strings"
	"testing"
)

// v0.2 patterns: the usefulness algorithm (reachability, exhaustiveness
// and witnesses), nested/tuple/rest/or patterns with bindings, guards and
// `is`. These sources are also diffcheck snippets (finc0 vs finc1).

const patDecls = `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
`

// patDiags checks src (which starts with patDecls) and returns its
// diagnostics as "E####@line: message", lines counted after patDecls.
func patDiags(t *testing.T, src string) []string {
	t.Helper()
	if !strings.HasPrefix(src, patDecls) {
		t.Fatal("source does not start with patDecls")
	}
	off := strings.Count(patDecls, "\n")
	_, d := checkSrc(t, src, 1000)
	var out []string
	for _, x := range d {
		out = append(out, fmt.Sprintf("%s@%d: %s", x.Code, x.Span.Start.Line-off, x.Message))
	}
	return out
}

func TestPatternsUsefulness(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"nested exhaustive", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(o Opt[Shape]) u32 {
	return match o {
		Some(Circle(r)) -> r
		Some(Rect(w, _)) -> w
		Some(Empty) -> 0
		None -> 1
	}
}
`, nil},
		{"nested missing", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(o Opt[Shape]) u32 {
	return match o {
		Some(Circle(r)) -> r
		None -> 1
	}
}
`, []string{"E2012@2: non-exhaustive match on Opt[Shape]: missing Some(Rect(_, _)), Some(Empty)"}},
		{"top-level variant name only", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(o Opt[Shape]) u32 {
	return match o {
		None -> 1
	}
}
`, []string{"E2012@2: non-exhaustive match on Opt[Shape]: missing Some"}},
		{"nested literal domain", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(o Opt[u32]) u32 {
	return match o {
		Some(0) -> 1
		Some(1 | 2) -> 2
		None -> 3
	}
}
`, []string{"E2012@2: non-exhaustive match on Opt[u32]: missing Some(_)"}},
		{"tuple witnesses", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(a Val, b Val) bool {
	return match (a, b) {
		(Int(x, _), Int(y, _)) -> x == y
		(Bool(_), _) -> false
	}
}
`, []string{"E2012@2: non-exhaustive match on (Val, Val): missing (Int(_, _), Bool(_)), (Int(_, _), Str(_)), (Str(_), _)"}},
		{"tuple witnesses, second column", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(a Opt[u32], b Opt[u32]) u32 {
	return match (a, b) {
		(Some(x), Some(_)) -> x
		(None, None) -> 0
	}
}
`, []string{"E2012@2: non-exhaustive match on (Opt[u32], Opt[u32]): missing (Some(_), None), (None, Some(_))"}},
		{"tuple of bools", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(a bool, b bool) u32 {
	return match (a, b) {
		(true, _) -> 1
		(false, true) -> 2
	}
}
`, []string{"E2012@2: non-exhaustive match on (bool, bool): missing (false, false)"}},
		{"bool expr keeps message", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(b bool) u32 {
	return match b {
		true -> 1
	}
}
`, []string{"E2012@2: non-exhaustive match expression on bool: missing '_' arm"}},
		{"bool exhaustive, wildcard unreachable", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(b bool) u32 {
	return match b {
		true -> 1
		false -> 2
		_ -> 3
	}
}
`, []string{"E2012@5: unreachable '_' arm: all values of bool are covered"}},
		{"guard does not count", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(s Shape) u32 {
	return match s {
		Circle(r) if r > 2 -> r
		Rect(..) | Empty -> 0
	}
}
`, []string{"E2012@2: non-exhaustive match on Shape: missing Circle"}},
		{"guarded arm is still checked for reachability", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(s Shape) u32 {
	return match s {
		Circle(_) | Rect(..) | Empty -> 0
		Circle(r) if r > 2 -> r
	}
}
`, []string{"E2012@4: unreachable pattern: variant Circle is already covered"}},
		{"unreachable after nested wildcard", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(o Opt[Opt[u32]]) u32 {
	return match o {
		Some(_) -> 1
		Some(Some(3)) -> 2
		None -> 0
	}
}
`, []string{"E2012@4: unreachable pattern: it is covered by earlier patterns"}},
		{"or alternatives cover", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(o Opt[u32]) u32 {
	return match o {
		Some(1) | Some(2) -> 1
		Some(2) -> 2
		_ -> 3
	}
}
`, []string{"E2012@4: unreachable pattern: it is covered by earlier patterns"}},
		{"rest pattern", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(v Val) u32 {
	return match v {
		Int(..) -> 1
		Int(_, 3) -> 2
		Bool(..) | Str(..) -> 3
	}
}
`, []string{"E2012@4: unreachable pattern: it is covered by earlier patterns"}},
		{"negative literal duplicate", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(n i32) u32 {
	return match n {
		-1 -> 1
		1 | -1 -> 2
		_ -> 3
	}
}
`, []string{"E2012@4: unreachable pattern: duplicate case -1"}},
		{"or binding mismatch", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(s Shape) u32 {
	return match s {
		Circle(x) | Rect(_, _) -> 1
		Empty -> 0
	}
}
`, []string{"E2002@3: or-pattern alternatives must bind the same variables (x is not bound here)"}},
		{"or binding types", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(v Val) u32 {
	match v {
		Int(x, _) | Bool(x) -> return 1
		Str(_) -> return 0
	}
}
`, []string{"E2002@3: variable x has type bool here but u32 in the first alternative"}},
		{"binding twice", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(v Val) u32 {
	return match v {
		Int(x, x) -> x
		_ -> 0
	}
}
`, []string{"E2002@3: variable x is bound more than once in a pattern"}},
		{"is binding outside if", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(v Val) bool {
	return v is Int(a, _) && a > 0
}
`, []string{"E2001@2: undefined: a"}},
		{"is binding unused outside if", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(v Val) bool {
	return v is Int(a, _)
}
`, []string{"E2002@2: cannot bind a: an 'is' pattern can bind variables only in the condition of an if statement"}},
		{"is binding in if", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(v Val, w Val) u32 {
	if v is Int(a, _) && a > 0 && w is Int(_, b) | Str(_) {
		return a
	}
	return 0
}
`, []string{"E2002@2: or-pattern alternatives must bind the same variables (b is not bound here)"}},
		{"guard type", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(s Shape) u32 {
	return match s {
		Circle(r) if r -> r
		_ -> 0
	}
}
`, []string{"E2002@3: non-boolean guard r in match arm (type u32)"}},
		{"tuple arity", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(a u32, b u32) u32 {
	return match (a, b) {
		(1, 2, 3) -> 1
		_ -> 0
	}
}
`, []string{"E2003@3: tuple pattern has 3 elements, scrutinee has 2"}},
		{"tuple elsewhere", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(a u32, b u32) u32 {
	return match (a, b) + 1 {
		_ -> 0
	}
}
`, []string{"E2002@2: tuple (a, b) can only be the scrutinee of a match"}},
		{"too many with rest", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(s Shape) u32 {
	return match s {
		Rect(a, b, ..) -> a + b
		Circle(a, b, ..) -> a + b
		Empty -> 0
	}
}
`, []string{"E2003@4: variant Circle has 1 payload values, pattern binds 2"}},
		{"statement arms terminate", `package main
type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
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
func f(s Shape) u32 {
	match s {
		Circle(r) if r > 1 -> return r
		Circle(_) -> return 1
		Rect(w, h) -> return w * h
		Empty -> return 0
	}
}
`, nil},
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
