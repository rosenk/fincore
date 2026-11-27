package totality

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/comptime"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
	"fincore/scripts/langver"
)

var markerRE = regexp.MustCompile(`//\s*ERROR\s+(E\d{4})`)

const mainFunc = "\ndriver func main(io Stdio) error {\n\treturn nil\n}\n"

// load type-checks src (a package main without main) and returns the
// program and typed info; it fails the test on earlier errors.
func load(t *testing.T, src string) (*resolve.Program, *types.Info) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "t.fc")
	if err := os.WriteFile(path, []byte("package main\n"+src+mainFunc), 0o644); err != nil {
		t.Fatal(err)
	}
	prog, diags, err := resolve.Load(path, resolve.Options{ModuleRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Fatalf("resolve errors:\n%v", diags)
	}
	info, diags := types.Check(prog, types.Config{Evaluator: comptime.New(0)})
	if len(diags) > 0 {
		t.Fatalf("type errors:\n%v", diags)
	}
	return prog, info
}

// run checks src and compares the diagnostics with the `// ERROR E####`
// markers (code and line; line 1 is `package main`).
func run(t *testing.T, src string) {
	t.Helper()
	prog, info := load(t, src)
	var want []string
	for i, line := range strings.Split(src, "\n") {
		if m := markerRE.FindStringSubmatch(line); m != nil {
			want = append(want, m[1]+"@"+strconv.Itoa(i+2))
		}
	}
	d := Check(prog, info)
	d.Sort()
	var got []string
	for _, x := range d {
		got = append(got, x.Code.String()+"@"+strconv.Itoa(x.Span.Start.Line))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v\n%v", got, want, d)
	}
}

type tcase struct{ name, src string }

func runAll(t *testing.T, cases []tcase) {
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { run(t, tc.src) })
	}
}

func TestEffects(t *testing.T) {
	runAll(t, []tcase{
		{"total calls driver", `
driver func side() u32 {
	return 1
}
func pure() u32 {
	return side() // ERROR E5001
}`},
		{"driver calls driver", `
driver func side() u32 {
	return 1
}
driver func d() u32 {
	return side()
}`},
		{"total calls total and comptime", `
comptime func K(n usize) usize {
	return n + 1
}
func g(x u32) u32 {
	return x + 1
}
func f(x u32) u32 {
	return g(x) + u32(K(2))
}`},
		{"driver for in total", `
func spin() {
	driver for { // ERROR E5001
		break
	}
}`},
		{"driver for in driver", `
driver func spin() {
	driver for {
		break
	}
}`},
		{"panic in total", `
func f(x u32) u32 {
	if x == 0 {
		panic("zero") // ERROR E5001
	}
	return x
}`},
		{"comptime calls driver", `
driver func side() usize {
	return 1
}
comptime func C(n usize) usize {
	return n + side() // ERROR E5001
}`},
		{"driver func value called in total", `
func apply(f driver func(n u32) u32, x u32) u32 {
	return f(x) // ERROR E5001
}`},
		{"total func value called in total", `
func apply(f func(n u32) u32, x u32) u32 {
	return f(x)
}`},
		{"driver literal in total is a value", `
driver func side() u32 {
	return 1
}
func mk() u32 {
	f := driver func() u32 {
		return side()
	}
	_ = f
	return 0
}`},
		{"calling driver literal in total", `
driver func side() u32 {
	return 1
}
func mk() u32 {
	f := driver func() u32 {
		return side()
	}
	return f() // ERROR E5001
}`},
		{"total literal calling driver", `
driver func side() u32 {
	return 1
}
driver func d() u32 {
	f := func() u32 {
		return side() // ERROR E5001
	}
	return f()
}`},
	})
}

func TestT1(t *testing.T) {
	runAll(t, []tcase{
		{"up", `
func sum(a []u32) u64 {
	var s u64
	for i := usize(0); i < len(a); i++ {
		s += u64(a[i])
	}
	return s
}`},
		{"step 2 against len", `
func sum(a []u32) u64 {
	var s u64
	for i := usize(0); i < len(a); i += 2 {
		s += u64(a[i])
	}
	return s
}`},
		{"down", `
func f(dst []byte) usize {
	k := usize(0)
	for i := usize(9); i > 0; i -= 1 {
		if k < len(dst) {
			dst[k] = byte(i) + '0'
			k++
		}
	}
	for i := usize(9); i > 0; i-- {
		k++
	}
	return k
}`},
		{"nonzero init, reversed cond, break, continue, element writes", `
func f(a []u32, n usize) usize {
	lim := n
	for i := usize(3); lim > i; i++ {
		if a[i] == 0 {
			continue
		}
		if a[i] == 7 {
			break
		}
		a[i] = 0
	}
	return lim
}`},
		{"u8 counter with constant bound", `
const Top = 200
func f() u32 {
	s := u32(0)
	for k := u8(10); k < Top+1; k++ {
		s += u32(k)
	}
	return s
}`},
		{"field bound through pointer", `
type Arena struct {
	items [16]u32
	len   usize
}
func (a *Arena) Sum() u64 {
	var s u64
	for i := usize(0); i < a.len; i++ {
		s += u64(a.items[i])
		a.items[i] = 0
	}
	return s
}`},
		{"field bound through pointer with method call", `
type Arena struct {
	items [16]u32
	len   usize
}
func (a *Arena) Pop() {
	a.len = a.len - 1
}
func (a *Arena) Drain() {
	for i := usize(0); i < a.len; i++ { // ERROR E4002
		a.Pop()
	}
}`},
		{"field bound written", `
type Arena struct {
	items [16]u32
	len   usize
}
func (a *Arena) Grow() {
	for i := usize(0); i < a.len; i++ { // ERROR E4002
		a.len = a.len + 1
	}
}`},
		{"bound modified", `
func f(n usize) usize {
	s := usize(0)
	for i := usize(0); i < n; i++ { // ERROR E4002
		n++
		s++
	}
	return s
}`},
		{"counter modified", `
func f(n usize) usize {
	s := usize(0)
	for i := usize(0); i < n; i++ { // ERROR E4002
		i = 0
		s++
	}
	return s
}`},
		{"counter addressed", `
func g(p *usize) {
}
func f(n usize) {
	for i := usize(0); i < n; i++ { // ERROR E4002
		g(&i)
	}
}`},
		{"wrong direction", `
func f(n usize) {
	for i := usize(0); i < n; i-- { // ERROR E4002
	}
}`},
		{"down step 2 may wrap", `
func f() {
	for i := usize(9); i > 0; i -= 2 { // ERROR E4002
	}
}`},
		{"down step 2 constant bound", `
func f() {
	for i := usize(9); i > 1; i -= 2 {
	}
}`},
		{"le with variable bound may wrap", `
func f(n u8) {
	for i := u8(0); i <= n; i++ { // ERROR E4002
	}
}`},
		{"le with constant bound", `
func f() {
	for i := u8(0); i <= 254; i++ {
	}
}`},
		{"step 2 on u8 to 255 wraps", `
func f() {
	for i := u8(0); i < 255; i += 2 { // ERROR E4002
	}
}`},
		{"no post", `
func f(n usize) {
	i := usize(0)
	for i < n { // ERROR E4002
		i++
	}
}`},
		{"bare for", `
func spin() {
	for { // ERROR E4002
	}
}`},
		{"plain for in driver func is checked", `
driver func spin() {
	for { // ERROR E4002
	}
}`},
		{"comptime funcs are not checked", `
comptime func Spin(n usize) usize {
	for {
	}
	return n
}`},
		{"loop in literal", `
func f() u32 {
	g := func(n u32) u32 {
		for { // ERROR E4002
		}
		return n
	}
	return g(1)
}`},
		{"bound written by a closure", `
func f(n usize) usize {
	s := usize(0)
	inc := func() {
		n++
	}
	for i := usize(0); i < n; i++ { // ERROR E4002
		inc()
		s++
	}
	return s
}`},
		{"bound aliased", `
func f(n usize) usize {
	p := &n
	s := usize(0)
	for i := usize(0); i < n; i++ { // ERROR E4002
		*p = *p + 1
		s++
	}
	return s
}`},
		{"const type parameter bound", `
type Ring[N usize, T any] struct {
	data [N]T
	n    usize
}

func (r *Ring[N, T]) Clear() {
	var z T
	for i := usize(0); i < N; i++ {
		r.data[i] = z
		r.Clear2()
	}
	for i := N; i > 0; i-- {
		r.n = i
	}
	for i := usize(0); i < N*2+r.n; i++ { // ERROR E4002
		r.n++
	}
}

func (r *Ring[N, T]) Clear2() {
}

func count[N usize](v *Ring[N, u8]) usize {
	k := usize(0)
	for i := usize(1); i < N-1; i++ {
		k++
	}
	return k
}`},
	})
}

func TestT2(t *testing.T) {
	runAll(t, []tcase{
		{"zero check", `
type Sink struct {
	n usize
}
func (s *Sink) Write(p []byte) (usize, error) {
	return len(p), nil
}
func writeAll(s *Sink, data []byte) error {
	for rem := data; len(rem) > 0; {
		n, err := s.Write(rem)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrZeroWrite
		}
		rem = rem[n:]
	}
	return nil
}`},
		{"missing zero check", `
type Sink struct {
	n usize
}
func (s *Sink) Write(p []byte) (usize, error) {
	return len(p), nil
}
func writeAll(s *Sink, data []byte) error {
	for rem := data; len(rem) > 0; { // ERROR E4002
		n, err := s.Write(rem)
		if err != nil {
			return err
		}
		rem = rem[n:]
	}
	return nil
}`},
		{"constant steps and both ends", `
func f(data []byte) usize {
	c := usize(0)
	for rem := data; len(rem) != 0; {
		if rem[0] == 'a' {
			rem = rem[1:]
			continue
		}
		if len(rem) > 3 {
			rem = rem[:len(rem)-2]
		} else {
			rem = rem[1:len(rem)]
		}
		c++
	}
	return c
}`},
		{"positive branch", `
func f(data []byte, k usize) {
	for rem := data; len(rem) > 0; {
		if k > 0 {
			rem = rem[k:]
		} else {
			break
		}
	}
}`},
		{"continue without shrink", `
func f(data []byte) {
	for rem := data; len(rem) > 0; { // ERROR E4002
		if rem[0] == ' ' {
			continue
		}
		rem = rem[1:]
	}
}`},
		{"lengthened", `
func f(data []byte) {
	for rem := data; len(rem) > 0; { // ERROR E4002
		rem = rem[1:]
		if len(rem) == 1 {
			rem = data
		}
	}
}`},
		{"guard killed by reassignment", `
func f(data []byte, k usize) {
	for rem := data; len(rem) > 0; { // ERROR E4002
		if k == 0 {
			return
		}
		k = k - 1
		rem = rem[k:]
	}
}`},
		{"addressed", `
func g(p *[]byte) {
}
func f(data []byte) {
	for rem := data; len(rem) > 0; { // ERROR E4002
		g(&rem)
		rem = rem[1:]
	}
}`},
	})
}

func TestT3Loops(t *testing.T) {
	runAll(t, []tcase{
		{"binary search", `
func search(a []u32, key u32) (usize, bool) {
	lo := usize(0)
	hi := len(a)
	for lo < hi decreases(hi - lo) {
		mid := lo + (hi-lo)/2
		if a[mid] == key {
			return mid, true
		}
		if a[mid] < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return 0, false
}`},
		{"binary search without progress", `
func search(a []u32, key u32) (usize, bool) {
	lo := usize(0)
	hi := len(a)
	for lo < hi decreases(hi - lo) { // ERROR E4002
		mid := lo + (hi-lo)/2
		if a[mid] < key {
			lo = mid
		} else {
			hi = mid
		}
	}
	return 0, false
}`},
		{"lexer position", `
func count(src []byte) usize {
	pos := usize(0)
	n := usize(0)
	for pos < len(src) decreases(len(src) - pos) {
		if src[pos] == ' ' {
			pos++
			continue
		}
		n++
		pos += 1
	}
	return n
}`},
		{"position not advanced on continue", `
func count(src []byte) usize {
	pos := usize(0)
	n := usize(0)
	for pos < len(src) decreases(len(src) - pos) { // ERROR E4002
		if src[pos] == ' ' {
			continue
		}
		pos++
	}
	return n
}`},
		{"countdown measure", `
func f(n usize) usize {
	s := usize(0)
	for n > 0 decreases(n) {
		n--
		s++
	}
	return s
}`},
		{"countdown without guard may wrap", `
func f(n usize, m usize) usize {
	s := usize(0)
	for s < m decreases(n) { // ERROR E4002
		n--
		s++
	}
	return s
}`},
		{"measure not implied non-negative", `
func f(lo usize, hi usize) {
	for lo != hi decreases(hi - lo) { // ERROR E4002
		lo++
	}
}`},
	})
}

func TestRecursion(t *testing.T) {
	runAll(t, []tcase{
		{"self recursion with guard", `
func sum(a []u32, i usize, depth usize) u64 decreases(depth) {
	if depth == 0 {
		return 0
	}
	if i >= len(a) {
		return 0
	}
	return u64(a[i]) + sum(a, i+1, depth-1)
}`},
		{"self recursion without measure", `
func f(n u32) u32 { // ERROR E4002
	if n == 0 {
		return 0
	}
	return f(n - 1)
}`},
		{"self recursion without guard", `
func f(n u32, depth usize) u32 decreases(depth) {
	return f(n, depth-1) // ERROR E4002
}`},
		{"self recursion passing the same measure", `
func f(n u32, depth usize) u32 decreases(depth) {
	if depth == 0 {
		return 0
	}
	return f(n, depth) // ERROR E4002
}`},
		{"halving", `
func f(depth usize) u32 decreases(depth) {
	if depth == 0 {
		return 0
	}
	return f(depth/2) + f(depth>>1) + 1
}`},
		{"guard in condition with &&", `
func f(depth usize) bool decreases(depth) {
	return depth > 0 && f(depth-1)
}`},
		{"mutual with measure", `
func isEven(n u32, depth usize) (bool, error) decreases(depth) {
	if depth == 0 {
		return false, ErrMaxDepth
	}
	if n == 0 {
		return true, nil
	}
	return isOdd(n-1, depth-1)
}
func isOdd(n u32, depth usize) (bool, error) decreases(depth) {
	if depth == 0 {
		return false, ErrMaxDepth
	}
	if n == 0 {
		return false, nil
	}
	return isEven(n-1, depth-1)
}`},
		{"mutual without measure: once, at the first function", `
func isEven(n u32) bool { // ERROR E4004
	if n == 0 {
		return true
	}
	return isOdd(n - 1)
}
func isOdd(n u32) bool {
	if n == 0 {
		return false
	}
	return isEven(n - 1)
}`},
		{"mutual with one measure missing", `
func a(depth usize) u32 decreases(depth) { // ERROR E4004
	if depth == 0 {
		return 0
	}
	return b(depth - 1)
}
func b(depth usize) u32 {
	return a(depth)
}`},
		{"mutual call not smaller", `
func a(depth usize) u32 decreases(depth) {
	if depth == 0 {
		return 0
	}
	return b(depth - 1)
}
func b(depth usize) u32 decreases(depth) {
	return a(depth) // ERROR E4004
}`},
		{"methods resolved by receiver type", `
type P struct {
	n u32
}
type Q struct {
	n u32
}
func (p *P) walk(depth usize) u32 decreases(depth) {
	if depth == 0 {
		return 0
	}
	return p.walk(depth - 1)
}
func (q *Q) walk(depth usize) u32 {
	return q.n
}`},
		{"measure assigned", `
func f(depth usize) u32 decreases(depth) { // ERROR E4002
	if depth == 0 {
		return 0
	}
	depth = depth + 1
	return f(depth - 1)
}`},
		{"recursion through a function value", `
func f(n u32) u32 { // ERROR E4002
	g := f
	return g(n)
}`},
		{"driver recursion is not checked", `
driver func f(n u32) u32 {
	return f(n)
}`},
	})
}

// TestCompilerSCCs checks the call-graph shape of compiler/ (spec 13.4,
// docs/compiler_requirements.md item 17) and pins its counts, which the
// docs do not repeat: compilerParserDecreases *Parser methods carry
// decreases(depth), compilerParserSCC of them form one SCC (typeParams and
// typeParam are only called from non-recursive declarations with
// ParseDepth), and the *Printer methods form a 4-member SCC. Later
// milestones add further SCCs. History (docs/semantics_decisions.md 57,
// 85, 109): 33 methods / SCC of 31 at M6, 35 / 33 with the v0.2 matchExpr
// and patternAlt, 40 / 38 with the v0.2 pattern methods scrutinee, guard,
// patterns, payloadListPush and patternAltBody.
func TestCompilerSCCs(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "compiler")); err != nil {
		t.Skip("no compiler/")
	}
	if !finc0Can(t, root, "compiler") {
		t.Skip("compiler/ needs a newer language version than finc0 (// SINCE:)")
	}
	t.Chdir(root)
	prog, diags, err := resolve.Load("compiler", resolve.Options{})
	if err != nil || len(diags) > 0 {
		t.Fatalf("load: %v %v", err, diags)
	}
	info, diags := types.Check(prog, types.Config{Evaluator: comptime.New(0)})
	if len(diags) > 0 {
		t.Fatalf("types: %v", diags)
	}
	if d := Check(prog, info); len(d) > 0 {
		t.Fatalf("unexpected diagnostics:\n%v", d)
	}
	c := &checker{info: info, roots: map[*types.FuncInfo]*rootFacts{}}
	g := c.buildGraph()
	var sizes []int
	for _, scc := range g.tarjan() {
		self := false
		for _, e := range g.edges[scc[0]] {
			self = self || e.to == scc[0]
		}
		if len(scc) > 1 || self {
			sizes = append(sizes, len(scc))
		}
	}
	slices.Sort(sizes)
	// The M6 front end contributes the Parser SCC and the 4-member Printer
	// SCC; the semantic phases added since (M7: resolver, checker +
	// comptime interpreter, value and type helpers) add their own SCCs, so
	// only require the M6 ones to be intact.
	if !slices.Contains(sizes, compilerParserSCC) || !slices.Contains(sizes, 4) {
		t.Errorf("nontrivial SCC sizes = %v, want the Parser (%d) and Printer (4) SCCs among them", sizes, compilerParserSCC)
	}
	n := 0
	for _, pkg := range prog.Packages {
		for _, f := range pkg.AST.Files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && fd.Recv.TypeName.Name == "Parser" && fd.Decreases != nil {
					n++
				}
			}
		}
	}
	if n != compilerParserDecreases {
		t.Errorf("%d *Parser methods carry decreases, want %d", n, compilerParserDecreases)
	}
}

// The counts pinned by TestCompilerSCCs; update them together with
// compiler/parser.fc.
const (
	compilerParserDecreases = 40 // *Parser methods with decreases(depth)
	compilerParserSCC       = 38 // members of the Parser SCC
)

func TestDiagFormat(t *testing.T) {
	prog, info := load(t, "\nfunc spin() {\n\tfor {\n\t}\n}\n")
	d := Check(prog, info)
	if len(d) != 1 || d[0].Code != diag.E4002 || d[0].Span.Start.Line != 4 || d[0].Span.Start.Col != 2 {
		t.Fatalf("got %v", d)
	}
}

// TestCorpus runs the checker alone (without the scope pass) over the
// conformance corpus: pass programs, examples and integration copies give
// no diagnostics; fail programs give exactly their totality/effect markers.
func TestCorpus(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var ins []string
	for _, pat := range []string{"examples/*.fc", "testdata/pass/*.fc", "testdata/fail/*.fc", "testdata/integration/*.fc"} {
		m, _ := filepath.Glob(pat)
		for _, in := range m {
			if finc0Can(t, ".", in) {
				ins = append(ins, in)
			}
		}
	}
	ents, _ := os.ReadDir("testdata/pass")
	for _, e := range ents {
		if e.IsDir() && finc0Can(t, ".", "testdata/pass/"+e.Name()) {
			ins = append(ins, "testdata/pass/"+e.Name())
		}
	}
	mine := map[string]bool{"E4002": true, "E4004": true, "E5001": true}
	for _, in := range ins {
		t.Run(in, func(t *testing.T) {
			prog, diags, err := resolve.Load(in, resolve.Options{})
			if err == nil && len(diags) > 0 && strings.HasPrefix(in, "testdata/fail/") {
				return // a resolver fail test
			}
			if err != nil || len(diags) > 0 {
				t.Fatalf("load: %v %v", err, diags)
			}
			info, diags := types.Check(prog, types.Config{Evaluator: comptime.New(1000)})
			if len(diags) > 0 {
				return // a type-checker fail test
			}
			var want []string
			if src, err := os.ReadFile(in); err == nil {
				for i, line := range strings.Split(string(src), "\n") {
					if m := markerRE.FindStringSubmatch(line); m != nil && mine[m[1]] {
						want = append(want, m[1]+"@"+strconv.Itoa(i+1))
					}
				}
			}
			d := Check(prog, info)
			d.Sort()
			var got []string
			for _, x := range d {
				got = append(got, x.Code.String()+"@"+strconv.Itoa(x.Span.Start.Line))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("got %v, want %v\n%v", got, want, d)
			}
		})
	}
}

// v0.2 sum types: match-expression arms are branches of the flow walker.
func TestMatchExprTotality(t *testing.T) {
	runAll(t, []tcase{
		{"driver call in arm", `
driver func side() u32 {
	return 1
}
func pure(n u32) u32 {
	return match n { 0 -> 1; _ -> side() } // ERROR E5001
}`},
		{"recursion in arm after guard", `
type T enum {
	Leaf(u32)
	Pair(u32, u32)
}
func size(t T, d usize) usize decreases(d) {
	if d == 0 {
		return 0
	}
	return match t {
		Leaf(_) -> 1
		Pair(a, b) -> size(T.Leaf(a), d-1) + size(T.Leaf(b), d-1)
	}
}`},
		{"recursion in arm without guard", `
func f(n usize) usize decreases(n) {
	return match n { 0 -> 1; _ -> f(n - 1) } // ERROR E4002
}`},
		{"loop bound from match expr", `
func f(k u32) u32 {
	var s u32
	lim := match k { 0 -> u32(4); _ -> k }
	for i := u32(0); i < lim; i++ {
		s += match i { 1 | 3 -> i; _ -> 0 }
	}
	return s
}`},
	})
}

// v0.2 ergonomics: x.M() with a pointer receiver takes &x, exactly like an
// explicit (&x).M(): the local becomes unstable for loop invariance.
func TestAutoAddrTotality(t *testing.T) {
	runAll(t, []tcase{
		{"local bound through implicit &", `
type Arena struct {
	items [16]u32
	len   usize
}
func (a *Arena) Pop() {
	a.len = a.len - 1
}
func f() {
	var a Arena
	a.len = 4
	for i := usize(0); i < a.len; i++ { // ERROR E4002
		a.Pop()
	}
}`},
		{"local bound through explicit &", `
type Arena struct {
	items [16]u32
	len   usize
}
func (a *Arena) Pop() {
	a.len = a.len - 1
}
func f() {
	var a Arena
	a.len = 4
	for i := usize(0); i < a.len; i++ { // ERROR E4002
		(&a).Pop()
	}
}`},
		{"copied bound with implicit &", `
type Arena struct {
	items [16]u32
	len   usize
}
func (a *Arena) Pop() {
	a.len = a.len - 1
}
func f() {
	var a Arena
	a.len = 4
	n := a.len
	for i := usize(0); i < n; i++ {
		a.Pop()
	}
}`},
		{"value receiver keeps the bound stable", `
type Arena struct {
	items [16]u32
	len   usize
}
func (a Arena) Peek() u32 {
	return a.items[0]
}
func f() u32 {
	var a Arena
	a.len = 4
	s := u32(0)
	for i := usize(0); i < a.len; i++ {
		s += a.Peek()
	}
	return s
}`},
	})
}

// finc0Can reports whether the input in (relative to the module root
// root) needs at most the language version finc0 implements (v0.2, frozen):
// tests of newer versions carry `// SINCE: vX.Y` and are skipped here
// (testdata/README.md).
func finc0Can(t *testing.T, root, in string) bool {
	t.Helper()
	v, ok, err := langver.RequiredIn(root, os.Getenv("FINCROOT"), filepath.Join(root, in))
	if err != nil {
		t.Fatal(err)
	}
	return !ok || !langver.MustParse(langver.Finc0).Less(v)
}
