package check

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// formatCases exercise the v0.2 builtin formatTo through the whole front
// end (types, comptime, scope, totality). Each src is a complete package;
// `// ERROR E####` markers give the expected diagnostics (code and line).
// scripts/diffcheck (formatSnippetCases) and scripts/diffbuild run every
// src through finc0 and finc1 as well.
var formatCases = []struct {
	name string
	src  string
}{
	{"ok all verbs", `package main

type Name string

type Celsius i16

type P struct {
	x int
}

func (p P) Format(dst []byte) usize {
	return formatTo(dst, "P(%d)", p.x)
}

func fill(b []byte, n Name, c Celsius, p P, ok bool, raw []byte) usize {
	k := formatTo(b, "%d %x %s %c %q %v %v %v %v %%", c, c, n, byte('a'), raw, ok, p, n, raw)
	k += formatTo(b[k:], "%-4d|%04x|%8s|%-3c|%5q|%3v", 1, 2, "s", 'c', "q", p)
	return k
}

driver func main(io Stdio) error {
	var b [128]byte
	n := fill(b[:], "n", -3, P{x: 1}, true, strBytes("r"))
	formatTo(b[n:], "!")
	return io.WriteStdout(b[:n+1])
}
`},
	{"ok untyped constants", `package main

const K = 7
const S = "s"

driver func main(io Stdio) error {
	var b [64]byte
	n := formatTo(b[:], "%d %x %v %v %c %d %v %s %q", K, 255, -1, 'x', 65, 'y', true, S, S)
	return io.WriteStdout(b[:n])
}
`},
	{"ok shadowed", `package main

func formatTo(a int, b int) int {
	return a + b
}

driver func main(io Stdio) error {
	if formatTo(1, 2) != 3 {
		return ErrIO
	}
	return nil
}
`},
	{"ok shadowed local", `package main

driver func main(io Stdio) error {
	formatTo := 3
	if formatTo != 3 {
		return ErrIO
	}
	return nil
}
`},
	{"ok comptime", `package main

type T struct {
	a u8
}

func (t T) Format(dst []byte) usize {
	return formatTo(dst, "T%03d", t.a)
}

comptime func width(v u64) usize {
	var b [40]byte
	return formatTo(b[:], "%x|%-6v|%q|%v|%5v", v, true, "\t", T{a: 7}, T{a: 1})
}

const W = width(4096)

var table [W]u8

driver func main(io Stdio) error {
	var b [16]byte
	n := formatTo(b[:], "%d", len(table))
	return io.WriteStdout(b[:n])
}
`},
	{"ok comptime truncation", `package main

comptime func cut(k usize) usize {
	var b [8]byte
	return formatTo(b[:k], "%5d%s", 42, "abcdef")
}

const C0 = cut(0)
const C3 = cut(3)
const C8 = cut(8)

var arr [C0 + C3 + C8 + 1]u8

driver func main(io Stdio) error {
	return nil
}
`},
	{"comptime fuel", `// FLAGS: --comptime-fuel=1000
package main

comptime func many(n usize) usize {
	var b [8]byte
	t := usize(0)
	for i := usize(0); i < n; i++ {
		t += formatTo(b[:], "%d%d%d%d", i, i, i, i)
	}
	return t
}

const M0 = many(150)
const M = many(300) // ERROR E4001

driver func main(io Stdio) error {
	return nil
}
`},
	{"too few args", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:]) // ERROR E2003
	return nil
}
`},
	{"no args", `package main

driver func main(io Stdio) error {
	_ = formatTo() // ERROR E2003
	return nil
}
`},
	{"missing arg", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%d %s", 1) // ERROR E2003
	return nil
}
`},
	{"extra arg no verbs", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "100%%", 1) // ERROR E2003
	return nil
}
`},
	{"incomplete", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "abc%") // ERROR E2002
	_ = formatTo(b[:], "abc%-05", 1) // ERROR E2002
	return nil
}
`},
	{"unknown verbs", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%u", 1) // ERROR E2002
	_ = formatTo(b[:], "%€", 1) // ERROR E2002
	_ = formatTo(b[:], "%\n", 1) // ERROR E2002
	_ = formatTo(b[:], "%X", 1) // ERROR E2002
	return nil
}
`},
	{"bad percent", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%5%") // ERROR E2002
	_ = formatTo(b[:], "%-%") // ERROR E2002
	return nil
}
`},
	{"width", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%64d", 1)
	_ = formatTo(b[:], "%65d", 1) // ERROR E2002
	_ = formatTo(b[:], "%-99999999999999999999s", "x") // ERROR E2002
	return nil
}
`},
	{"zero flag", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%05s", "x") // ERROR E2002
	_ = formatTo(b[:], "%0v", 1) // ERROR E2002
	_ = formatTo(b[:], "%-08d|%0d", 1, 2)
	return nil
}
`},
	{"format not constant", `package main

func f(b []byte, s string) usize {
	return formatTo(b, s) // ERROR E2002
}

driver func main(io Stdio) error {
	return nil
}
`},
	{"format wrong type", `package main

type Fmt string

const F Fmt = "%d"

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], 42) // ERROR E2002
	_ = formatTo(b[:], F, 1) // ERROR E2002
	_ = formatTo(b[:], string(F), 1)
	_ = formatTo(b[:], strBytes("x")) // ERROR E2002
	return nil
}
`},
	{"dst wrong type", `package main

driver func main(io Stdio) error {
	var b [8]byte
	s := "abc"
	_ = formatTo(b, "x") // ERROR E2002
	_ = formatTo(s, "%d", 1) // ERROR E2002
	_ = formatTo("abc", "%d", 1) // a constant string is an implicit []byte (v0.2)
	return nil
}
`},
	{"bad format keeps arg errors", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%z", 1<<70, nil) // ERROR E2002, E2002, E2004
	_ = formatTo(b[:], "%d", 1, -1<<70) // ERROR E2003, E2002
	_ = formatTo(b[:], 7, 1<<70) // ERROR E2002, E2002
	return nil
}
`},
	{"undefined arg", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%d", undefinedName) // ERROR E2001
	return nil
}
`},
	{"arg types", `package main

type S struct {
	n int
}

type Sig []Signal

driver func main(io Stdio) error {
	var b [8]byte
	var w u16 = 1
	var arr [2]u8
	var sigs Sig
	_ = formatTo(b[:], "%d", "x") // ERROR E2002
	_ = formatTo(b[:], "%x", true) // ERROR E2002
	_ = formatTo(b[:], "%s", 1) // ERROR E2002
	_ = formatTo(b[:], "%q", w) // ERROR E2002
	_ = formatTo(b[:], "%c", w) // ERROR E2002
	_ = formatTo(b[:], "%c", 300) // ERROR E2002
	_ = formatTo(b[:], "%c", "a") // ERROR E2002
	_ = formatTo(b[:], "%v", S{n: 1}) // ERROR E2002
	_ = formatTo(b[:], "%v", arr) // ERROR E2002
	_ = formatTo(b[:], "%s", sigs) // ERROR E2002
	_ = formatTo(b[:], "%d", nil) // ERROR E2004
	_ = formatTo(b[:], "%v", 1<<64) // ERROR E2002
	_ = formatTo(b[:], "%v", ErrIO) // ERROR E2002
	return nil
}
`},
	{"several bad args", `package main

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%d %s %v", "x", // ERROR E2002
		2, // ERROR E2002
		true)
	return nil
}
`},
	{"generic type param", `package main

func show[T any](b []byte, v T) usize {
	return formatTo(b, "%v", v) // ERROR E2002
}

driver func main(io Stdio) error {
	return nil
}
`},
	{"non-value args", `package main

type H struct {
	p *int
}

driver func main(io Stdio) error {
	var b [8]byte
	x := 1
	f := func() {}
	_ = formatTo(b[:], "%v", &x) // ERROR E2002
	_ = formatTo(b[:], "%v", f) // ERROR E2002
	_ = formatTo(b[:], "%d", H{p: &x}) // ERROR E2002
	ctx, cancel := NewContext()
	defer cancel()
	_ = formatTo(b[:], "%v", ctx) // ERROR E2002
	return nil
}
`},
	{"bad Format methods", `package main

type A u8
type B u8
type C u8
type D u8
type E u8

func (a *A) Format(dst []byte) usize {
	return 0
}

driver func (b B) Format(dst []byte) usize {
	return 0
}

func (c C) Format(dst []byte) int {
	return 0
}

func (d D) Format(dst []byte, x int) usize {
	return 0
}

func (e E) Format(dst string) usize {
	return 0
}

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%v", A(1)) // ERROR E2002
	_ = formatTo(b[:], "%v", B(1)) // ERROR E2002
	_ = formatTo(b[:], "%v", C(1)) // ERROR E2002
	_ = formatTo(b[:], "%v", D(1)) // ERROR E2002
	_ = formatTo(b[:], "%v", E(1)) // ERROR E2002
	_ = formatTo(b[:], "%d %x", A(1), B(2))
	return nil
}
`},
	{"field named Format", `package main

type F struct {
	Format int
}

driver func main(io Stdio) error {
	var b [8]byte
	_ = formatTo(b[:], "%v", F{Format: 1}) // ERROR E2002
	return nil
}
`},
	{"not used", `package main

driver func main(io Stdio) error {
	var b [8]byte
	formatTo(b[:], "x")
	defer formatTo(b[:], "y")
	len(b) // ERROR E2002
	return nil
}
`},
	{"total self recursion", `package main

type T struct {
	n u32
}

func (t T) Format(dst []byte) usize { // ERROR E4002
	return formatTo(dst, "[%v]", t)
}

driver func main(io Stdio) error {
	return nil
}
`},
	{"total recursion through func", `package main

type T struct {
	n u32
}

func (t T) Format(dst []byte) usize { // ERROR E4004
	return show(dst, usize(t.n))
}

func show(dst []byte, n usize) usize decreases(n) {
	if n == 0 {
		return 0
	}
	return formatTo(dst, "%v", T{n: u32(n - 1)})
}

driver func main(io Stdio) error {
	return nil
}
`},
	{"total no recursion", `package main

type Inner struct {
	n u32
}

type Outer struct {
	in Inner
}

func (i Inner) Format(dst []byte) usize {
	return formatTo(dst, "%d", i.n)
}

func (o Outer) Format(dst []byte) usize {
	return formatTo(dst, "<%v>", o.in)
}

func f(b []byte) usize {
	return formatTo(b, "%v %v", Outer{}, Inner{})
}

driver func main(io Stdio) error {
	return nil
}
`},
	{"total loop bound through pointer", `package main

type Buf struct {
	data [64]u8
	n    usize
}

type T struct {
	k u8
}

func (t T) Format(dst []byte) usize {
	return formatTo(dst, "%d", t.k)
}

func plain(p *Buf) {
	for i := usize(0); i < p.n; i++ {
		_ = formatTo(p.data[:], "%d", i)
	}
}

func withMethod(p *Buf) {
	for i := usize(0); i < p.n; i++ { // ERROR E4002
		_ = formatTo(p.data[:], "%v", T{k: 1})
	}
}

driver func main(io Stdio) error {
	return nil
}
`},
	{"driver call in total via format arg", `package main

driver func g() int {
	return 1
}

func f(b []byte) usize {
	return formatTo(b, "%d", g()) // ERROR E5001
}

driver func main(io Stdio) error {
	return nil
}
`},
	{"scope escape unrelated", `package main

func f() []byte {
	var buf [8]byte
	n := formatTo(buf[:], "%d", 7)
	return buf[:n] // ERROR E3002
}

driver func main(io Stdio) error {
	return nil
}
`},
	{"scope ok global dst", `package main

var out [32]byte

func f(x int) []byte {
	n := formatTo(out[:], "%d", x)
	return out[:n]
}

driver func main(io Stdio) error {
	return io.WriteStdout(f(3))
}
`},
}

// formatMarkerRE matches `// ERROR E2002` and `// ERROR E2002, E2004`.
var formatMarkerRE = regexp.MustCompile(`//\s*ERROR\s+(E\d{4}(?:,\s*E\d{4})*)`)

func TestFormatTo(t *testing.T) {
	for _, tc := range formatCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "t.fc")
			if err := os.WriteFile(path, []byte(tc.src), 0o644); err != nil {
				t.Fatal(err)
			}
			var want []string
			var opts Options
			for i, line := range strings.Split(tc.src, "\n") {
				if m := formatMarkerRE.FindStringSubmatch(line); m != nil {
					for _, code := range strings.Split(m[1], ",") {
						want = append(want, strings.TrimSpace(code)+"@"+itoa(i+1))
					}
				}
				if f, ok := strings.CutPrefix(line, "// FLAGS: --comptime-fuel="); ok {
					n, err := strconv.ParseUint(f, 10, 64)
					if err != nil {
						t.Fatal(err)
					}
					opts.Fuel = n
				}
			}
			opts.ModuleRoot = dir
			res, err := Run(path, opts)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range res.Diags {
				got = append(got, d.Code.String()+"@"+itoa(d.Span.Start.Line))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("got %v, want %v\n%v", got, want, res.Diags)
			}
		})
	}
}

// TestFormatToMessages pins the diagnostic texts (finc1 must match them
// byte for byte; see scripts/diffcheck).
func TestFormatToMessages(t *testing.T) {
	cases := []struct{ call, want string }{
		{`formatTo(b[:])`, `not enough arguments in call to formatTo: have 1, want at least 2`},
		{`formatTo(b[:], "%d %d", 1)`, `wrong number of arguments for format "%d %d" in call to formatTo: have 1, want 2`},
		{`formatTo(b[:], "a%")`, `invalid format for formatTo: incomplete directive "%" at the end of the format`},
		{`formatTo(b[:], "%-3", 1)`, `invalid format for formatTo: incomplete directive "%-3" at the end of the format`},
		{`formatTo(b[:], "%3y", 1)`, `invalid format for formatTo: unknown verb in directive "%3y"`},
		{`formatTo(b[:], "%3%")`, `invalid format for formatTo: directive "%3%" cannot have flags or a width`},
		{`formatTo(b[:], "%100d", 1)`, `invalid format for formatTo: width in directive "%100d" exceeds 64`},
		{`formatTo(b[:], "%05q", "x")`, `invalid format for formatTo: flag 0 in directive "%05q" is only valid with %d and %x`},
		{`formatTo(b[:], s)`, `format argument s to formatTo must be a constant string`},
		{`formatTo(b[:], "%d", true)`, `cannot use true (type bool) as argument for %d in formatTo`},
		{`formatTo(b[:], "%v", &s)`, `cannot format &s (type *string): formatTo arguments must be Value types`},
	}
	for _, tc := range cases {
		src := "package main\n\nfunc f(s string) usize {\n\tvar b [8]byte\n\treturn " + tc.call + "\n}\n\ndriver func main(io Stdio) error {\n\treturn nil\n}\n"
		dir := t.TempDir()
		path := filepath.Join(dir, "t.fc")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Run(path, Options{ModuleRoot: dir})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Diags) != 1 || res.Diags[0].Message != tc.want {
			t.Errorf("%s:\ngot  %v\nwant %s", tc.call, res.Diags, tc.want)
		}
	}
}
