package golang

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// v0.2 sum types: compact enum layout and match lowering.

func TestSumTypeLowering(t *testing.T) {
	root := moduleRoot(t)
	cases := []struct {
		name     string
		src      string
		patterns []string
	}{
		{
			name: "shared-slots",
			src: `package main

type ID u32

type E enum {
	A(u8, byte, u32)
	B([]byte, ID, ID)
	C(u32, u8, u8, u8, [2]u8)
	D
}

func f(e E) u32 {
	return match e {
		A(x, _, z) -> u32(x) + z
		B(s, i, _) -> u32(len(s)) + u32(i)
		C(w, _, _, last, _) -> w + u32(last)
		D -> 0
	}
}

driver func main() error {
	var two [2]u8
	e := E.C(1, 2, 3, 4, two)
	if f(e) == 0 {
		return ErrNotFound
	}
	return nil
}
`,
			patterns: []string{
				`type E struct \{ _tag uint8 _u8_0 uint8 _u8_1 uint8 _u8_2 uint8 _u32_0 uint32 _2_0 \[\]uint8 _ID_0 ID _ID_1 ID _4_0 \[2\]uint8 \}`,
				`func f\(e E\) uint32 \{ switch e\._tag \{ case 0: x := e\._u8_0 z := e\._u32_0 return uint32\(x\) \+ z`,
				`case 2: w := e\._u32_0 last := e\._u8_2 return w \+ uint32\(last\) default: return 0 \} \}`,
			},
		},
		{
			name: "match-expr-iife-and-or",
			src: `package main

type Col enum {
	Red
	Green
	Blue
}

func g(c Col, n u32) u32 {
	v := match c { Red | Green -> n; Blue -> 0 }
	return v + match n { 1 | 2 -> 10; _ -> 20 }
}

driver func main() error {
	if g(Col.Red, 1) == 0 {
		return ErrNotFound
	}
	return nil
}
`,
			patterns: []string{
				`v := func\(\) uint32 \{ switch c\._tag \{ case 0, 1: return n default: return 0 \} \}\(\)`,
				`return v \+ func\(\) uint32 \{ switch n \{ case uint32\(1\), uint32\(2\): return 10 default: return 20 \} \}\(\)`,
			},
		},
		{
			// v0.2: constant patterns (folded ==, enum constants as tag
			// tests) and top-level / tuple-element bindings assigned from
			// the scrutinee temporaries.
			name: "const-patterns-and-bindings",
			src: `package main

type Col enum {
	Red
	Green
}

type Op enum {
	Builtin(u8, u32)
	Paint(Col)
}

const BFPanic u8 = 7
const Def = Col.Red

func f(o Op, n u32) u32 {
	r := match o {
		Builtin(BFPanic, k) -> k
		Paint(Def) -> 1
		_ -> 0
	}
	return r + match (n, r) {
		(x, 0) | (0, x) -> x
		(x, y) if x > y -> x
		_ -> 2
	}
}

driver func main() error {
	if f(Op.Paint(Col.Red), 1) == 0 {
		return ErrNotFound
	}
	return nil
}
`,
			patterns: []string{
				`if o\._tag == 0 && o\._u8_0 == uint8\(7\) \{ k := o\._u32_0 return k \} else if o\._tag == 1 && o\._Col_0\._tag == 0 \{ return 1 \}`,
				`_fc_t0 := n _fc_t1 := r if _fc_t1 == uint32\(0\) \|\| _fc_t0 == uint32\(0\) \{ var x uint32 if _fc_t1 == uint32\(0\) \{ x = _fc_t0 \} else \{ x = _fc_t1 \} return x`,
				`\} else if func\(\) bool \{ x := _fc_t0 y := _fc_t1 return x > y \}\(\) \{ x := _fc_t0 return x \}`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeProg(t, tc.src)
			out := generate(t, root, path)
			expect(t, out, tc.patterns...)
			if testing.Short() {
				return
			}
			goVet(t, root, out)
		})
	}
}

// TestWideEnumTag: more than 256 variants need a uint16 tag.
func TestWideEnumTag(t *testing.T) {
	root := moduleRoot(t)
	var b strings.Builder
	b.WriteString("package main\n\ntype W enum {\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "\tV%d\n", i)
	}
	b.WriteString("\tLast(u64)\n}\n\ndriver func main() error {\n\tw := W.Last(7)\n\tif w == W.V299 {\n\t\treturn ErrNotFound\n\t}\n\treturn nil\n}\n")
	out := generate(t, root, writeProg(t, b.String()))
	expect(t, out, `type W struct \{ _tag uint16 _u64_0 uint64 \}`, `w := W\{_tag: 300, _u64_0: uint64\(7\)\}`)
	if !bytes.Contains(out, []byte("W{_tag: 299}")) {
		t.Error("missing W{_tag: 299}")
	}
}
