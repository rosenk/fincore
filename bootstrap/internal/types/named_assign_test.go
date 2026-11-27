package types_test

import (
	"fmt"
	"slices"
	"testing"
)

// Decision 126: assignability follows Go. Predeclared basic types count as
// named, so a value of `type Name u32` is assignable neither to u32 nor
// from it; conversions, untyped constants and unnamed composite types are
// unaffected. These sources are also diffcheck/diffbuild snippets.
func TestNamedAssignability(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string // "E####@line: message"
	}{
		{"rejected", `package main
type Name u32
type Fmt string
const F Fmt = "%d"
const K Name = 3
type R struct {
	id u32
}
func f(x u32) u32 {
	return x
}
func g(n Name) u32 {
	return n
}
func h(n Name, u u32) bool {
	_ = f(n)
	u = n
	n = u
	_ = R{id: n}
	var b [8]byte
	_ = formatTo(b[:], F, u)
	match u {
		K -> return true
		_ -> {}
	}
	return n == u
}
` + mainStub, []string{
			"E2002@13: cannot use n (type Name) as type u32 in return statement",
			"E2002@16: cannot use n (type Name) as type u32 in argument to f",
			"E2002@17: cannot use n (type Name) as type u32 in assignment",
			"E2002@18: cannot use u (type u32) as type Name in assignment",
			"E2002@19: cannot use n (type Name) as type u32 in struct literal",
			"E2002@21: cannot use F (type Fmt) as type string in argument to formatTo",
			"E2002@23: cannot use K (type Name) as type u32 in match pattern",
			"E2002@26: mismatched types Name and u32 for operator ==",
		}},
		{"accepted", `package main
type Name u32
type Fmt string
type Buf []u8
type Err error
const F Fmt = "%d"
const K Name = 3
func f(x u32) u32 {
	return x
}
func e(x error) Err {
	var y Err = x
	return y
}
func h(n Name, u u32, raw []u8) bool {
	_ = f(u32(n)) + f(7)
	n = Name(u) + 1
	var b Buf = raw
	raw = b
	_ = formatTo(b, string(F), u)
	match n {
		K -> return true
		4 -> return false
		_ -> {}
	}
	return n == Name(u) && u32(n) == u && e(nil) == nil
}
` + mainStub, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := checkSrc(t, tc.src, 1000)
			var got []string
			for _, x := range d {
				got = append(got, fmt.Sprintf("%s@%d: %s", x.Code, x.Span.Start.Line, x.Message))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
