package types_test

import (
	"strings"
	"testing"

	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// v0.2 sum types: exhaustiveness, reachability, or-patterns, unqualified
// patterns and match expressions.

const sumDecls = "package main\ntype S enum {\n\tA(u32)\n\tB(u32, u32)\n\tC\n}\ntype Col enum {\n\tRed\n\tGreen\n\tBlue\n}\n"

func TestSumTypesNegative(t *testing.T) {
	cases := []struct {
		name, src, want, msg string
	}{
		{"non-exhaustive stmt", "package main\ntype S enum {\n\tA(u32)\n\tB\n\tC\n}\nfunc f(s S) {\n\tmatch s {\n\t\tB -> {}\n\t}\n}\n",
			"E2012@8", "non-exhaustive match on S: missing A, C"},
		{"non-exhaustive expr", "package main\ntype S enum {\n\tA\n\tB\n}\nfunc f(s S) u32 {\n\treturn match s { A -> 1 }\n}\n",
			"E2012@7", "non-exhaustive match on S: missing B"},
		{"int expr needs wildcard", "package main\nfunc f(n u32) u32 {\n\treturn match n {\n\t\t0 -> 1\n\t\t1 | 2 -> 2\n\t}\n}\n",
			"E2012@3", "non-exhaustive match expression on u32: missing '_' arm"},
		{"arm after wildcard", "package main\nfunc f(n u32) {\n\tmatch n {\n\t\t_ -> {}\n\t\t1 -> {}\n\t}\n}\n",
			"E2012@5", "unreachable match arm: it follows the '_' arm"},
		{"variant twice", "package main\ntype S enum {\n\tA\n\tB\n}\nfunc f(s S) {\n\tmatch s {\n\t\tA | B -> {}\n\t\tS.A -> {}\n\t}\n}\n",
			"E2012@9", "unreachable pattern: variant A is already covered"},
		{"duplicate literal", "package main\nconst K = 2\nfunc f(n u32) u32 {\n\treturn match n {\n\t\t1 | K -> 1\n\t\t2 -> 2\n\t\t_ -> 3\n\t}\n}\n",
			"E2012@6", "unreachable pattern: duplicate case 2"},
		{"useless wildcard", "package main\ntype S enum {\n\tA\n}\nfunc f(s S) {\n\tmatch s {\n\t\tA -> {}\n\t\t_ -> {}\n\t}\n}\n",
			"E2012@8", "unreachable '_' arm: all variants of S are covered"},
		{"arm types", "package main\nfunc f(n u32) u32 {\n\tv := match n { 0 -> u32(1); _ -> \"x\" }\n\treturn v\n}\n",
			"E2002@3", ""},
		{"arm typed mismatch", "package main\nfunc f(n u32, a u8, b u16) u16 {\n\treturn match n {\n\t\t0 -> a\n\t\t_ -> b\n\t}\n}\n",
			"E2002@5", "match arm has type u16, want u8"},
		{"or binds", "package main\ntype S enum {\n\tA(u32)\n\tB(u32)\n}\nfunc f(s S) u32 {\n\treturn match s {\n\t\tA(x) | B(_) -> 1\n\t}\n}\n",
			"E2002@8", "or-pattern alternatives must bind the same variables (x is not bound here)"},
		{"or wildcard", "package main\nfunc f(n u32) u32 {\n\treturn match n {\n\t\t1 | _ -> 1\n\t}\n}\n",
			"E2002@4", "'_' cannot be an alternative of an or-pattern"},
		{"const on enum", "package main\ntype S enum {\n\tA\n\tB\n}\nconst K = S.A\nfunc f(s S) {\n\tmatch s {\n\t\tK -> {}\n\t\t_ -> {}\n\t}\n}\n",
			"E2002@9", "constant K cannot be a pattern for enum type S"},
		{"overflow untyped arm", "package main\ntype S enum {\n\tA\n\tB\n}\nfunc f(s S) u8 {\n\treturn match s {\n\t\tA -> 1\n\t\tB -> 300\n\t}\n}\n",
			"E2002@9", ""},
		{"void arm", "package main\nfunc g() {}\nfunc f(n u32) u32 {\n\treturn match n { _ -> g() }\n}\n",
			"E2002@4", ""},
		{"recursive enum", "package main\ntype E enum {\n\tLeaf\n\tNode(E, E)\n}\n",
			"E2002@2", "invalid recursive type E (contains itself by value)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := checkSrc(t, tc.src, 1000)
			got := codes(d)
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("got %v, want [%s]\n%v", got, tc.want, d)
			}
			if tc.msg != "" && d[0].Message != tc.msg {
				t.Errorf("message %q, want %q", d[0].Message, tc.msg)
			}
		})
	}
}

func TestSumTypesPositive(t *testing.T) {
	src := sumDecls + `
type Opt[T any] enum {
	Some(T)
	None
}

const K = match Col.Green {
	Red -> 1
	Green | Blue -> 2
}

const L u8 = match 7 { 1 | 2 -> 10; _ -> 20 }

comptime func code(s S) u32 {
	return match s {
		A(x) -> x
		B(x, y) -> x * y
		C -> 0
	}
}

const M = code(S.B(6, 7))

func area(s S) u64 {
	return match s {
		A(r) -> u64(r) * 3
		B(w, h) -> u64(w) * u64(h)
		C -> 0
	}
}

func exhaustive(c Col) u32 {
	match c {
		Red | Green -> {
			return 1
		}
		Col.Blue -> {
			return 2
		}
	}
}

func get[T any](o Opt[T], d T) T {
	return match o {
		Opt.Some(x) -> x
		None -> d
	}
}

func small(c Col) u8 {
	var b u8 = match c { Red -> 1; _ -> 255 }
	return b
}
` + mainStub
	info, d := checkSrc(t, src, 1000)
	if len(d) > 0 {
		t.Fatalf("unexpected diagnostics: %v", d)
	}
	vals := map[string]string{}
	for _, sym := range info.Prog.Packages[0].Decls {
		if v := info.SymValue(sym); v != nil {
			vals[sym.Name] = v.String()
		}
	}
	for name, want := range map[string]string{"K": "2", "L": "20", "M": "42"} {
		if vals[name] != want {
			t.Errorf("%s = %q, want %s", name, vals[name], want)
		}
	}
	if typ := info.SymType(findSym(t, info, "L")); typ.String() != "u8" {
		t.Errorf("L has type %s", typ)
	}
}

func findSym(t *testing.T, info *types.Info, name string) *resolve.Symbol {
	t.Helper()
	for _, sym := range info.Prog.Packages[0].Decls {
		if sym.Name == name {
			return sym
		}
	}
	t.Fatalf("no symbol %s", name)
	return nil
}

func TestSumTypesMessages(t *testing.T) {
	// The missing variants are listed in declaration order.
	src := "package main\ntype E enum {\n\tV1\n\tV2\n\tV3\n\tV4\n}\nfunc f(e E) {\n\tmatch e {\n\t\tV3 -> {}\n\t}\n}\n"
	_, d := checkSrc(t, src, 1000)
	if len(d) != 1 || !strings.HasSuffix(d[0].Message, "missing V1, V2, V4") {
		t.Fatalf("got %v", d)
	}
}
