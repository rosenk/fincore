package types_test

import (
	"strings"
	"testing"
)

// v0.2 flow-scoped `is` bindings (decisions 118-125): outcome sets, the
// positions where bindings are visible, let-else and the errors. These
// sources are also diffcheck snippets (finc0 vs finc1).
func TestIsFlowBindings(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"valid forms", `package main
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
func chain(v Val, w Val) bool {
	ok := v is Int(a, _) && a > 0 && w is Int(_, b) && b > a
	return ok || !(v is Bool(c)) || c
}
func elseBranch(v Val) u32 {
	if !(v is Int(h, l)) {
		return 0
	} else if h > 0 {
		return h + l
	} else {
		return l
	}
}
func letElse(v Val, o Opt[u32]) u32 {
	if !(v is Int(h, _)) || !(o is Some(k)) {
		return 0
	}
	return h + k
}
func loops(vs []Val) u32 {
	var n u32 = 0
	for i := usize(0); i < len(vs); i++ {
		if !(vs[i] is Int(_, l)) {
			continue
		}
		if !(vs[i] is Int(h, _)) || h > l {
			break
		}
		n += l - h
	}
	return n
}
func guard(s Shape, o Opt[u32]) u32 {
	return match s {
		Circle(r) if o is Some(k) && k > r -> k
		Rect(w, _) if !(o is None) -> w
		_ -> 0
	}
}
func exits(v Val) u32 {
	if !(v is Str(s)) {
		match v {
			Int(..) -> return 1
			_ -> return 2
		}
	}
	return u32(len(s))
}
`, nil},
		{"or true side", `package main
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
func f(v Val, w Val) bool {
	return (v is Int(a, _) || w is Bool(_)) && a > 0
}
`, []string{"E2001@2: undefined: a"}},
		{"else of a positive test", `package main
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
	if v is Int(a, _) {
		return a
	} else {
		return a
	}
}
`, []string{"E2001@5: undefined: a"}},
		{"then block of a negative test", `package main
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
	if !(v is Int(a, _)) {
		return a
	}
	return 0
}
`, []string{"E2001@3: undefined: a"}},
		{"no leak", `package main
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
	ok := v is Int(a, _)
	for v is Int(_, b) {
		return b
	}
	if !(v is Bool(c)) {
		return 1
	} else {
	}
	if ok && c {
		return a + b
	}
	return 0
}
`, []string{"E2001@10: undefined: c", "E2001@11: undefined: a", "E2001@11: undefined: b"}},
		{"let-else falls through", `package main
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
	var n u32 = 0
	if !(v is Int(a, _)) {
		n = 1
	}
	if !(v is Int(_, b)) {
		if n > 0 {
			return 0
		}
	}
	return n + a + b
}
`, []string{"E2002@11: cannot use a: the then block of the if statement that binds it can complete normally", "E2002@11: cannot use b: the then block of the if statement that binds it can complete normally"}},
		{"duplicate in one outcome set", `package main
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
	if v is Int(a, _) && w is Int(_, a) {
		return a
	}
	if !(v is Bool(b)) || !(w is Bool(b)) {
		return 0
	}
	ok := !(v is Int(_, c)) && (w is Int(c, _))
	if ok && b {
		return 1
	}
	return 2
}
`, []string{"E2001@2: a redeclared in this condition", "E2001@5: b redeclared in this condition"}},
		{"let-else redeclares", `package main
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
	a := u32(1)
	if !(v is Int(a, _)) {
		return 0
	}
	{
		if !(v is Int(_, a)) {
			return 1
		}
		return a
	}
}
`, []string{"E2001@3: a redeclared in this block"}},
		{"guard bindings stay in their arm", `package main
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
func f(s Shape, o Opt[u32]) u32 {
	return match s {
		Circle(_) if o is Some(k) -> k
		_ -> k
	}
}
`, []string{"E2001@4: undefined: k"}},
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
