package golang

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"fincore/bootstrap/internal/comptime"
	"fincore/bootstrap/internal/ir"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// moduleRoot returns the fincore module root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// generate runs the front end (without the later semantic passes, which
// codegen does not depend on) and the generator on path.
func generate(t *testing.T, root, path string) []byte {
	t.Helper()
	prog, diags, err := resolve.Load(path, resolve.Options{ModuleRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Fatalf("%s: %v", path, diags)
	}
	info, diags := types.Check(prog, types.Config{Evaluator: comptime.New(1_000_000)})
	if len(diags) > 0 {
		t.Fatalf("%s: %v", path, diags)
	}
	p, err := ir.Lower(prog, info)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(p)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

// corpus lists the programs of the repository that must compile.
func corpus(t *testing.T, root string) []string {
	ins := []string{"examples/tcp_echo.fc", "compiler"}
	ents, err := os.ReadDir(filepath.Join(root, "testdata", "pass"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".fc") {
			ins = append(ins, "testdata/pass/"+e.Name())
		}
	}
	ins = append(ins, "testdata/integration/echo_basic.fc")
	return ins
}

func TestDeterminism(t *testing.T) {
	root := moduleRoot(t)
	for _, in := range corpus(t, root) {
		path := filepath.Join(root, in)
		a := generate(t, root, path)
		b := generate(t, root, path) // fresh load: new symbols, new maps
		if !bytes.Equal(a, b) {
			t.Errorf("%s: two runs produced different Go code", in)
		}
		if bytes.Contains(a, []byte(root)) || bytes.Contains(a, []byte(os.TempDir())) {
			t.Errorf("%s: generated code contains a file system path", in)
		}
	}
}

func writeProg(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "prog.fc")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// expect checks that every pattern (a regexp over the gofmt-ed output with
// runs of whitespace normalised to one space) occurs.
func expect(t *testing.T, out []byte, patterns ...string) {
	t.Helper()
	norm := regexp.MustCompile(`\s+`).ReplaceAllString(string(out), " ")
	for _, p := range patterns {
		if !regexp.MustCompile(p).MatchString(norm) {
			t.Errorf("generated code does not match %q:\n%s", p, out)
		}
	}
}

func TestGolden(t *testing.T) {
	root := moduleRoot(t)
	cases := []struct {
		name     string
		src      string
		patterns []string
	}{
		{
			name: "main-and-caps",
			src: `package main

driver func main(fs FS, io Stdio, clk Clock) error {
	return nil
}
`,
			patterns: []string{
				`func _fc_main\(fs fincrt\.FS, io fincrt\.Stdio, clk fincrt\.Clock\) error \{ return nil \}`,
				`func main\(\) \{ if err := _fc_main\(fincrt\.DefaultFS\(\), fincrt\.DefaultStdio\(\), fincrt\.DefaultClock\(\)\); err != nil \{ _, _ = os\.Stderr\.WriteString\("error: " \+ err\.Error\(\) \+ "\\n"\) os\.Exit\(1\) \} \}`,
			},
		},
		{
			name: "enum-and-match",
			src: `package main

type Shape enum {
	Circle(u32)
	Rect(u32, u32)
	Empty
}

func area(s Shape) u64 {
	match s {
		Shape.Circle(r) -> {
			return 3 * u64(r) * u64(r)
		}
		Shape.Rect(w, _) -> {
			return u64(w)
		}
		Shape.Empty -> {
			return 0
		}
	}
}

driver func main(io Stdio) error {
	s := Shape.Rect(2, 3)
	if s == Shape.Empty || area(s) == 0 {
		return ErrIO
	}
	return nil
}
`,
			patterns: []string{
				`type Shape struct \{ _tag uint8 _Circle_0 uint32 _Rect_0 uint32 _Rect_1 uint32 \}`,
				`switch s\._tag \{ case 0: r := s\._Circle_0 return 3 \* uint64\(r\) \* uint64\(r\) case 1: w := s\._Rect_0 return uint64\(w\) default: return 0 \}`,
				`s := Shape\{_tag: 1, _Rect_0: uint32\(2\), _Rect_1: uint32\(3\)\}`,
				`if s == \(Shape\{_tag: 2\}\) \|\| area\(s\) == 0 \{`,
			},
		},
		{
			name: "generics-and-mono",
			src: `package main

type Vec[N usize, T any] struct {
	data [N]T
	len  usize
}

func (v *Vec[N, T]) Cap() usize {
	return N
}

func id[T any](x T) T {
	return x
}

func total[N usize](v *Vec[N, u32]) usize {
	return v.Cap() + N
}

driver func main(io Stdio) error {
	var a Vec[4, u32]
	var b Vec[8, bool]
	ap := &a
	bp := &b
	if total[4](ap) + bp.Cap() + id[usize](1) == 0 {
		return ErrIO
	}
	return nil
}
`,
			patterns: []string{
				`type _fc_3Vec_4\[T any\] struct \{ data \[4\]T len uint \}`,
				`type _fc_3Vec_8\[T any\] struct \{ data \[8\]T len uint \}`,
				`func \(v \*_fc_3Vec_4\[T\]\) Cap\(\) uint \{ return uint\(4\) \}`,
				`func id\[T any\]\(x T\) T`,
				`func _fc_5total_4\(v \*_fc_3Vec_4\[uint32\]\) uint \{ return v\.Cap\(\) \+ uint\(4\) \}`,
				`var a _fc_3Vec_4\[uint32\] var b _fc_3Vec_8\[bool\]`,
				`_fc_5total_4\(ap\) ?\+ ?bp\.Cap\(\) ?\+ ?id\[uint\]\(1\) == 0`,
			},
		},
		{
			name: "scope-lowering",
			src: `package main

driver func main(io Stdio) error {
	var pool [2]u64
	ctx, cancel := NewContext()
	defer cancel()
	driver for i := usize(0); i < 3; i++ {
		scope s := NewScope(2) {
			slot, err := s.AcquireSlot(ctx, &pool)
			if err != nil {
				return err
			}
			s.Go(slot, driver func(p *u64) {
				*p = 1
			})
			if i == 1 {
				continue
			}
			match i {
				2 -> {
					break
				}
				_ -> {
				}
			}
		}
	}
	return nil
}
`,
			patterns: []string{
				`var _fc_s0 \*fincrt\.Scope defer func\(\) \{ if _fc_s0 != nil \{ _fc_s0\.Wait\(\) \} \}\(\)`,
				`_fc_L0: for i := uint\(0\); i < 3; i\+\+ \{ \{ s := fincrt\.NewScope\(2\) _fc_s0 = s`,
				`slot, err := fincrt\.AcquireSlot\(s, ctx, pool\[:\]\)`,
				`if err != nil \{ var _fc_t0 error = err s\.Wait\(\) return _fc_t0 \}`,
				`fincrt\.Go\(s, slot, func\(p \*uint64\) \{ \*p = 1 \}\)`,
				`if i == 1 \{ s\.Wait\(\) continue \}`,
				`case uint\(2\): s\.Wait\(\) break _fc_L0`,
				`\} s\.Wait\(\) \} \} return nil`,
			},
		},
		{
			name: "names-consts-unused",
			src: `package main

const (
	Max = 10
	Ch  = 'a'
	Lim u32 = 7
)

type Point struct {
	x i32
	y i32
}

var origin Point = Point{x: 1}
var big [1000000]u64

func f(len u32, string u32, go u32) u32 {
	type_ := len + string
	return type_ + go
}

driver func main(io Stdio) error {
	c := Ch
	n := Max
	k := 5
	if v := f(1, 2, 3); k > Max {
		k = 6
	}
	big[0] = u64(c) + u64(n) + u64(origin.x)
	return nil
}
`,
			patterns: []string{
				`const Max = 10 const Ch = 97 const Lim uint32 = 7`,
				`var origin Point = Point\{x: int32\(1\)\}`,
				`var big \[1000000\]uint64 `,
				`func f\(_fc_u_len uint32, _fc_u_string uint32, _fc_u_go uint32\) uint32`,
				`c := uint8\(Ch\) n := Max k := 5 if v := f\(1, 2, 3\); k > Max \{ _ = v k = 6 \}`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeProg(t, tc.src)
			out := generate(t, root, path)
			expect(t, out, tc.patterns...)
			if !bytes.Equal(out, generate(t, root, path)) {
				t.Error("not deterministic")
			}
			if testing.Short() {
				return
			}
			goVet(t, root, out)
		})
	}
}

// TestGeneratedGoVets compiles and vets the Go generated for the whole
// corpus (spec 13.2: generated code passes go vet).
func TestGeneratedGoVets(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go vet")
	}
	root := moduleRoot(t)
	for _, in := range corpus(t, root) {
		t.Run(strings.ReplaceAll(in, "/", "_"), func(t *testing.T) {
			goVet(t, root, generate(t, root, filepath.Join(root, in)))
		})
	}
}

var (
	goOnce sync.Once
	goBin  string
)

// goVet runs `go vet` (which type-checks) on src in a temporary module
// that uses the fincore runtime.
func goVet(t *testing.T, root string, src []byte) {
	t.Helper()
	goOnce.Do(func() {
		goBin = "go"
		cmd := exec.Command("go", "env", "GOROOT")
		cmd.Dir = root
		if out, err := cmd.Output(); err == nil {
			p := filepath.Join(strings.TrimSpace(string(out)), "bin", "go")
			if _, err := os.Stat(p); err == nil {
				goBin = p
			}
		}
	})
	dir := t.TempDir()
	mod := "module fincprog\n\ngo 1.22\n\nrequire fincore v0.0.0\n\nreplace fincore => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "vet", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet: %v\n%s\n--- source ---\n%s", err, out, src)
	}
}
