// Command diffprev is the regression gate that replaces finc0 as the
// oracle for new language versions (finc0 is frozen at v0.2; see
// docs/testing.md). It compares the current compiler (built from
// compiler/, default bin/finc1) with the previous release, the bootstrap
// seed .stage/finc-seed (compiler/ at the last tag of bootstrap/SEED,
// built by `make seed`).
//
//	go run ./scripts/diffprev [-new bin/finc1] [-old .stage/finc-seed] [-allow scripts/diffprev/allow.txt]
//	                          [-v] [-keep] [-j N] [-govet=true] [-run=true] [-mutate N -seed S]
//
// Cases (run from the module root) are the union of the diff-check and
// diff-build corpora: examples/*.fc, testdata/pass (files and package
// directories), testdata/fail, testdata/integration/*.fc, the parser
// fixtures, scripts/diffcheck/cases, scripts/diffbuild/cases, the std/
// packages, compiler/, the FINCROOT cases, the compiler/ and std/ mutants
// of diff-check-totality and every FinCore snippet embedded in finc0's unit
// tests. With -mutate N (make diff-prev-fuzz) N random token-level mutants
// of every corpus file and snippet are added (the mutators of
// scripts/diffcheck). `// FLAGS:` header directives are honoured.
//
// Steps per case, each run with both compilers:
//
//   - parse --dump-ast;
//   - check --passes=types, scope, totality and all (mutants: types, all);
//   - build --emit-go: the emitted Go is compared byte for byte; when both
//     compilers build the case, `go vet` runs on the emitted Go (-govet)
//     and, when the case has a .out file and no `// BUILD-ONLY`, both
//     binaries are run and their stdout and exit status compared, also
//     with the .out file (-run).
//
// Classification (the worst step decides a case):
//
//   - REGRESSION (fails): the previous release accepted the program and the
//     current compiler rejects it; both accept but the output (AST dump,
//     emitted Go, run output) differs; the current compiler accepts a
//     `// ERROR` fail test that the previous release rejected; the current
//     compiler accepts a program that `go build` or `go vet` then rejects.
//   - DIAG-CHANGED (fails unless allowlisted): both reject the program with
//     different diagnostics.
//   - EXPECTED (ok, counted): the previous release rejected the program and
//     the current compiler accepts it (a new feature), or the case has a
//     `// SINCE: vX.Y` header newer than the previous release's language
//     version (a conformance test of a new version) and both reject it.
//
// Allowlist (-allow, default scripts/diffprev/allow.txt): one entry per
// line, `<case name> # <reason>`; blank lines and lines starting with `#`
// are ignored. An allowlisted REGRESSION or DIAG-CHANGED case does not
// fail the run; unused entries are reported. Case names are those printed
// by -v.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fincore/scripts/langver"
)

// ---------------------------------------------------------------------------
// classification

type class int

const (
	identical class = iota
	expected
	diagChanged
	regression
)

func (c class) String() string {
	return [...]string{"ok", "EXPECTED", "DIAG-CHANGED", "REGRESSION"}[c]
}

// outcome is one compiler invocation.
type outcome struct {
	out, err string
	code     int
}

func (o outcome) ok() bool { return o.code == 0 }

// classify compares the outcomes of one step of the previous release (old)
// and the current compiler (cur). newer: the case needs a language version
// newer than the previous release's; failTest: the case has `// ERROR`
// markers.
func classify(old, cur outcome, newer, failTest bool) (class, string) {
	switch {
	case old == cur:
		return identical, ""
	case old.ok() && !cur.ok():
		return regression, "accepted by the previous release, rejected now"
	case !old.ok() && cur.ok():
		if failTest && !newer {
			return regression, "fail test (// ERROR) rejected by the previous release, accepted now"
		}
		return expected, "rejected by the previous release, accepted now (new feature)"
	case old.ok() && cur.ok():
		return regression, "accepted by both, output differs"
	case newer:
		return expected, "rejected by both (test needs a newer language version)"
	default:
		return diagChanged, "rejected by both, diagnostics differ"
	}
}

// ---------------------------------------------------------------------------
// language versions (scripts/langver)

// tagVersion is the language version of a release tag vX.Y.Z.
func tagVersion(tag string) (langver.Version, bool) {
	t := strings.TrimSpace(tag)
	if strings.Count(t, ".") == 2 {
		t = t[:strings.LastIndex(t, ".")]
	}
	v, err := langver.Parse(t)
	return v, err == nil
}

// compilerVersion asks `bin version` ("<name> language vX.Y"); compilers
// without the command (the v0.2.0 seed) get fallback.
func compilerVersion(bin string, fallback langver.Version) langver.Version {
	out, err := exec.Command(bin, "version").Output()
	if err == nil {
		if f := strings.Fields(string(out)); len(f) > 0 {
			if v, err := langver.Parse(f[len(f)-1]); err == nil {
				return v
			}
		}
	}
	return fallback
}

// ---------------------------------------------------------------------------
// cases

type tcase struct {
	name     string
	dir      string // working directory
	path     string // argument to parse/check/build
	flags    []string
	env      []string // extra environment (FINCROOT cases)
	passes   []string // check --passes modes
	noBuild  bool
	outFile  string           // expected stdout (.out), "" if none
	since    *langver.Version // `// SINCE:` (directories: the highest of their files)
	failTest bool             // has `// ERROR` markers
	mutant   bool
}

var allPasses = []string{"types", "scope", "totality", "all"}

type header struct {
	flags     []string
	buildOnly bool
}

// readHeader reads the directives of the leading comment block.
func readHeader(path string) header {
	var h header
	src, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			break
		}
		if rest, ok := strings.CutPrefix(line, "// FLAGS:"); ok {
			h.flags = append(h.flags, strings.Fields(rest)...)
		}
		if line == "// BUILD-ONLY" {
			h.buildOnly = true
		}
	}
	return h
}

var errorMarkerRE = regexp.MustCompile(`//\s*ERROR\s+E\d{4}\b`)

func hasErrorMarkers(files ...string) bool {
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && errorMarkerRE.Match(b) {
			return true
		}
	}
	return false
}

func flagSuffix(flags []string) string {
	if len(flags) == 0 {
		return ""
	}
	return " [" + strings.Join(flags, " ") + "]"
}

// corpusCases: the diff-check and diff-build corpora.
func corpusCases() []tcase {
	var out []tcase
	seen := map[string]bool{}
	addFile := func(f string) {
		if seen[f] {
			return
		}
		seen[f] = true
		h := readHeader(f)
		c := tcase{name: f + flagSuffix(h.flags), dir: ".", path: f, flags: h.flags, passes: allPasses,
			since: required(f), failTest: hasErrorMarkers(f)}
		if o := strings.TrimSuffix(f, ".fc") + ".out"; !h.buildOnly && fileExists(o) {
			c.outFile = o
		}
		out = append(out, c)
	}
	addDir := func(d string) {
		if seen[d] {
			return
		}
		files, _ := filepath.Glob(filepath.Join(d, "*.fc"))
		if len(files) == 0 {
			return
		}
		seen[d] = true
		sort.Strings(files)
		h := readHeader(files[0])
		c := tcase{name: d + flagSuffix(h.flags), dir: ".", path: d, flags: h.flags, passes: allPasses,
			since: required(d), failTest: hasErrorMarkers(files...)}
		if o := d + ".out"; !h.buildOnly && fileExists(o) {
			c.outFile = o
		}
		out = append(out, c)
	}
	for _, pat := range []string{"examples/*.fc", "testdata/pass/*.fc", "testdata/fail/*.fc",
		"testdata/integration/*.fc", "bootstrap/internal/parser/testdata/*.fc",
		"scripts/diffcheck/cases/*.fc", "scripts/diffbuild/cases/*.fc"} {
		files, _ := filepath.Glob(pat)
		sort.Strings(files)
		for _, f := range files {
			addFile(f)
		}
	}
	for _, pat := range []string{"testdata/pass/*", "testdata/fail/*", "scripts/diffcheck/cases/*",
		"scripts/diffbuild/cases/*", "std/*"} {
		dirs, _ := filepath.Glob(pat)
		sort.Strings(dirs)
		for _, d := range dirs {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				addDir(d)
			}
		}
	}
	out = append(out, tcase{name: "compiler", dir: ".", path: "compiler", passes: allPasses})
	out = append(out, tcase{name: "compiler/", dir: ".", path: "compiler/", passes: allPasses, noBuild: true})
	return out
}

// required returns the `// SINCE:` version of a corpus file or package
// directory (langver.Required); a malformed directive is fatal.
func required(path string) *langver.Version {
	v, ok, err := langver.Required(path)
	if err != nil {
		fatal(err)
	}
	if !ok {
		return nil
	}
	return &v
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// snippetCases extracts the FinCore sources embedded in finc0's unit tests
// (the snippets of scripts/diffcheck and scripts/diffbuild).
func snippetCases(tmp string) ([]tcase, error) {
	var out []tcase
	n := 0
	write := func(prefix string, files map[string]string) string {
		n++
		root := filepath.Join(tmp, fmt.Sprintf("%s%04d", prefix, n))
		for name, src := range files {
			p := filepath.Join(root, filepath.FromSlash(name))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte(src), 0o644)
		}
		return root
	}
	fuelDirs := map[string]bool{"bootstrap/internal/resolve": true, "bootstrap/internal/types": true, "bootstrap/internal/comptime": true}
	for _, dir := range []string{"bootstrap/internal/codegen/golang", "bootstrap/internal/types",
		"bootstrap/internal/comptime", "bootstrap/internal/resolve", "bootstrap/internal/scope",
		"bootstrap/internal/totality", "bootstrap/internal/ir", "bootstrap/internal/check"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
		sort.Strings(files)
		for _, f := range files {
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				return nil, err
			}
			inMap := map[*ast.BasicLit]bool{}
			if fuelDirs[dir] {
				// Multi-file modules: map[string]string literals with .fc
				// keys; the checked path is the next string argument.
				ast.Inspect(af, func(nd ast.Node) bool {
					var elems []ast.Expr
					switch x := nd.(type) {
					case *ast.CompositeLit:
						elems = x.Elts
					case *ast.CallExpr:
						elems = x.Args
					default:
						return true
					}
					for i, e := range elems {
						m, ok := e.(*ast.CompositeLit)
						if !ok {
							continue
						}
						mod := map[string]string{}
						for _, kv := range m.Elts {
							kve, ok := kv.(*ast.KeyValueExpr)
							if !ok {
								continue
							}
							k, ok1 := strLit(kve.Key)
							v, ok2 := strLit(kve.Value)
							if ok1 && ok2 && strings.HasSuffix(k, ".fc") {
								mod[k] = v
								if bl, ok := kve.Value.(*ast.BasicLit); ok {
									inMap[bl] = true
								}
							}
						}
						if len(mod) == 0 {
							continue
						}
						path := ""
						for _, rest := range elems[i+1:] {
							if s, ok := strLit(rest); ok {
								path = s
								break
							}
						}
						if path == "" {
							continue
						}
						root := write("m", mod)
						label := fmt.Sprintf("%s:%d (module, %s)", f, fset.Position(m.Pos()).Line, path)
						out = append(out, tcase{name: label, dir: root, path: path, passes: allPasses})
						out = append(out, tcase{name: label + " [fuel 1000]", dir: root, path: path, passes: allPasses, flags: []string{"--comptime-fuel=1000"}})
					}
					return true
				})
			}
			// Single-file snippets.
			ast.Inspect(af, func(nd ast.Node) bool {
				bl, ok := nd.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING || inMap[bl] {
					return true
				}
				s, err := strconv.Unquote(bl.Value)
				if err != nil || !strings.HasPrefix(s, "package ") {
					return true
				}
				label := fmt.Sprintf("%s:%d", f, fset.Position(bl.Pos()).Line)
				root := write("s", map[string]string{"t.fc": s})
				out = append(out, tcase{name: label, dir: root, path: "t.fc", passes: allPasses, flags: readHeader(filepath.Join(root, "t.fc")).flags})
				if fuelDirs[dir] {
					out = append(out, tcase{name: label + " [fuel 1000]", dir: root, path: "t.fc", passes: allPasses, flags: []string{"--comptime-fuel=1000"}})
				}
				if strings.HasPrefix(s, "package main") && !strings.Contains(s, "func main(") {
					// Give library snippets an entry point so that all
					// their declarations are emitted (as diff-build does).
					root := write("s", map[string]string{"t.fc": s + mainStub})
					out = append(out, tcase{name: label + " [main]", dir: root, path: "t.fc", passes: allPasses})
				}
				return true
			})
		}
	}
	// Scope unit tests: {name, src, want} literals, wrapped like the test's
	// run helper.
	if err := eachLit("bootstrap/internal/scope/scope_test.go", 3, func(label string, el []ast.Expr) {
		src, _ := strLit(el[1])
		if _, ok := strLit(el[2]); !ok {
			return
		}
		root := write("sc", map[string]string{"t.fc": "package main\n" + src + mainStub})
		out = append(out, tcase{name: label, dir: root, path: "t.fc", passes: allPasses})
	}); err != nil {
		return nil, err
	}
	// Totality unit tests: {name, src} literals and load(t, src) calls.
	totFile := "bootstrap/internal/totality/totality_test.go"
	addTot := func(label string, lit ast.Expr) {
		src, ok := strLit(lit)
		if !ok {
			return
		}
		root := write("t", map[string]string{"t.fc": "package main\n" + src + mainStub})
		out = append(out, tcase{name: label, dir: root, path: "t.fc", passes: allPasses})
	}
	if err := eachLit(totFile, 2, func(label string, el []ast.Expr) { addTot(label, el[1]) }); err != nil {
		return nil, err
	}
	if err := eachCall(totFile, "load", 2, func(label string, args []ast.Expr) { addTot(label+" (load)", args[1]) }); err != nil {
		return nil, err
	}
	return out, nil
}

const mainStub = "\ndriver func main(io Stdio) error {\n\treturn nil\n}\n"

// eachLit calls fn for every composite literal of file with n elements
// whose first element is a string literal.
func eachLit(file string, n int, fn func(label string, el []ast.Expr)) error {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		return err
	}
	ast.Inspect(af, func(nd ast.Node) bool {
		cl, ok := nd.(*ast.CompositeLit)
		if !ok || len(cl.Elts) != n {
			return true
		}
		if _, ok := strLit(cl.Elts[0]); !ok {
			return true
		}
		if _, ok := strLit(cl.Elts[1]); !ok {
			return true
		}
		fn(fmt.Sprintf("%s:%d", file, fset.Position(cl.Pos()).Line), cl.Elts)
		return true
	})
	return nil
}

// eachCall calls fn for every call of the function name with n arguments.
func eachCall(file, name string, n int, fn func(label string, args []ast.Expr)) error {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		return err
	}
	ast.Inspect(af, func(nd ast.Node) bool {
		if x, ok := nd.(*ast.CallExpr); ok {
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == name && len(x.Args) == n {
				fn(fmt.Sprintf("%s:%d", file, fset.Position(x.Pos()).Line), x.Args)
			}
		}
		return true
	})
	return nil
}

func strLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	return s, err == nil
}

// formatSnippetCases: the formatTo unit tests ({name, src} literals with a
// complete package and an optional `// FLAGS:` header).
func formatSnippetCases(tmp string) ([]tcase, error) {
	var out []tcase
	n := 0
	err := eachLit("bootstrap/internal/check/format_test.go", 2, func(label string, el []ast.Expr) {
		src, _ := strLit(el[1])
		if !strings.Contains(src, "package main") {
			return
		}
		n++
		root := filepath.Join(tmp, fmt.Sprintf("f%03d", n))
		_ = os.MkdirAll(root, 0o755)
		p := filepath.Join(root, "t.fc")
		_ = os.WriteFile(p, []byte(src), 0o644)
		out = append(out, tcase{name: label, dir: root, path: "t.fc", passes: allPasses, flags: readHeader(p).flags})
	})
	return out, err
}

// fincRootCases: std imports resolved through $FINCROOT (as diff-check).
func fincRootCases(tmp string) ([]tcase, error) {
	files := map[string]string{
		"fr/std/hello/hello.fc":   "package hello\n\nimport \"std/inner\"\n\nfunc Twice(x u32) u32 {\n\treturn inner.Add(x, x)\n}\n",
		"fr/std/inner/inner.fc":   "package inner\n\nfunc Add(a u32, b u32) u32 {\n\treturn a + b\n}\n",
		"fr/std/broken/broken.fc": "package broken\n\nfunc Bad() u32 {\n\treturn \"x\"\n}\n",
		"fr/std/loop/loop.fc":     "package loop\n\nfunc Spin(n usize) usize {\n\tfor n > 0 {\n\t\tn--\n\t}\n\treturn n\n}\n",
		"fr/std/empty/README":     "no sources\n",
		"mod/std/hello/hello.fc":  "package hello\n\nfunc Twice(x u32) u32 {\n\treturn x\n}\n",
		"mod/ok.fc":               "package main\n\nimport \"std/hello\"\n\ndriver func main() error {\n\tif hello.Twice(2) != 4 {\n\t\treturn ErrIO\n\t}\n\treturn nil\n}\n",
		"mod/broken.fc":           "package main\n\nimport \"std/broken\"\n\ndriver func main() error {\n\t_ = broken.Bad()\n\treturn nil\n}\n",
		"mod/loop.fc":             "package main\n\nimport \"std/loop\"\n\ndriver func main() error {\n\t_ = loop.Spin(3)\n\treturn nil\n}\n",
		"mod/missing.fc":          "package main\n\nimport (\n\t\"std/nope\"\n\t\"std/empty\"\n)\n\ndriver func main() error {\n\treturn nil\n}\n",
		"mod/local.fc":            "package main\n\nimport \"std/hello\"\n\ndriver func main() error {\n\t_ = hello.Twice(1)\n\treturn nil\n}\n",
	}
	base := filepath.Join(tmp, "fincroot")
	for name, src := range files {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			return nil, err
		}
	}
	mod := filepath.Join(base, "mod")
	var out []tcase
	for _, f := range []string{"ok.fc", "broken.fc", "loop.fc", "missing.fc"} {
		for _, fr := range []string{filepath.Join(base, "fr"), "../fr", "../fr/"} {
			show := fr
			if filepath.IsAbs(fr) {
				show = "<abs>/fr"
			}
			out = append(out, tcase{name: "fincroot " + f + " FINCROOT=" + show, dir: mod, path: f, env: []string{"FINCROOT=" + fr}, passes: allPasses})
		}
	}
	out = append(out, tcase{name: "fincroot local.fc (empty FINCROOT)", dir: mod, path: "local.fc", env: []string{"FINCROOT="}, passes: allPasses})
	out = append(out, tcase{name: "fincroot local.fc (FINCROOT without std)", dir: mod, path: "local.fc", env: []string{"FINCROOT=" + filepath.Join(base, "none")}, passes: allPasses})
	return out, nil
}

// treeMutantCases writes copies of one package directory with one textual
// mutation each (the compiler/ and std/ mutants of diff-check-totality).
func treeMutantCases(tmp, prefix string, muts []mutation) []tcase {
	var out []tcase
	for i, m := range muts {
		pkg := filepath.Dir(m.file)
		files, _ := filepath.Glob(filepath.Join(pkg, "*.fc"))
		root := filepath.Join(tmp, fmt.Sprintf("%s%02d", prefix, i))
		dir := filepath.Join(root, pkg)
		_ = os.MkdirAll(dir, 0o755)
		changed := false
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			s := string(src)
			if f == m.file && strings.Contains(s, m.old) {
				s = strings.Replace(s, m.old, m.new, 1)
				changed = true
			}
			_ = os.WriteFile(filepath.Join(dir, filepath.Base(f)), []byte(s), 0o644)
		}
		if !changed {
			fmt.Printf("warning: mutation %d (%s %q) does not apply\n", i, m.file, m.old)
			continue
		}
		out = append(out, tcase{name: fmt.Sprintf("%s mutant %d: %s %q -> %q", pkg, i, m.file, m.old, m.new),
			dir: root, path: pkg, passes: allPasses, mutant: true})
	}
	return out
}

// dedupe drops cases whose inputs (sources, path, flags, env) equal those
// of an earlier case; the first name is kept.
func dedupe(cases []tcase) []tcase {
	seen := map[[32]byte]bool{}
	var out []tcase
	for _, c := range cases {
		h := sha256.New()
		fmt.Fprintf(h, "%q %q %q %v\n", c.path, c.flags, c.env, c.noBuild)
		if c.dir != "." {
			// Temporary roots: hash the sources.
			_ = filepath.Walk(c.dir, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					b, _ := os.ReadFile(p)
					rel, _ := filepath.Rel(c.dir, p)
					fmt.Fprintf(h, "%s %d\n", rel, len(b))
					h.Write(b)
				}
				return nil
			})
		}
		var k [32]byte
		copy(k[:], h.Sum(nil))
		if c.dir != "." && seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// running

var (
	newBin, oldBin string
	newVer, oldVer langver.Version
	govet, runOut  bool
	rootDir        string
	goBin          = "go"
)

const procTimeout = 5 * time.Minute

func run(bin, dir string, env []string, args []string) outcome {
	ctx, cancel := context.WithTimeout(context.Background(), procTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
			errb.WriteString(err.Error())
		}
	}
	if ctx.Err() != nil {
		errb.WriteString("\n(timed out)")
	}
	return outcome{out.String(), errb.String(), code}
}

type stepDiff struct {
	step   string
	cls    class
	reason string
	detail string
}

type result struct {
	cls   class
	diffs []stepDiff
}

func (r *result) add(step string, cls class, reason, detail string) {
	if cls == identical {
		return
	}
	if cls > r.cls {
		r.cls = cls
	}
	r.diffs = append(r.diffs, stepDiff{step, cls, reason, detail})
}

func (c tcase) newer() bool {
	return c.since != nil && oldVer.Less(*c.since)
}

// outcomeDetail shows the exit statuses and the output of both compilers
// from the first line that differs.
func outcomeDetail(old, cur outcome) string {
	a := strings.Split(old.err+old.out, "\n")
	b := strings.Split(cur.err+cur.out, "\n")
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := ""
	if i > 0 {
		from = fmt.Sprintf(", from line %d", i+1)
	}
	tail := func(l []string) string {
		if i >= len(l) {
			return ""
		}
		return strings.Join(l[i:], "\n")
	}
	return fmt.Sprintf("  previous (exit %d%s):\n%s  current (exit %d%s):\n%s",
		old.code, from, indent("    ", tail(a)), cur.code, from, indent("    ", tail(b)))
}

func runCase(c tcase, out string) result {
	var r result
	newer := c.newer()
	step := func(name string, args []string) {
		o := run(oldBin, c.dir, c.env, args)
		n := run(newBin, c.dir, c.env, args)
		cls, why := classify(o, n, newer, c.failTest)
		if cls != identical {
			r.add(name, cls, why, outcomeDetail(o, n))
		}
	}
	step("parse", []string{"parse", "--dump-ast", c.path})
	for _, p := range c.passes {
		step("check --passes="+p, append(append([]string{"check", "--passes=" + p}, c.flags...), c.path))
	}
	if c.noBuild {
		return r
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		r.add("build", regression, err.Error(), "")
		return r
	}
	gOld, gNew := filepath.Join(out, "old.go"), filepath.Join(out, "new.go")
	bOld, bNew := filepath.Join(out, "old.bin"), filepath.Join(out, "new.bin")
	o := run(oldBin, c.dir, c.env, append(append([]string{"build"}, c.flags...), "--emit-go="+gOld, "-o", bOld, c.path))
	n := run(newBin, c.dir, c.env, append(append([]string{"build"}, c.flags...), "--emit-go="+gNew, "-o", bNew, c.path))
	if !n.ok() && strings.Contains(n.err, "go build failed") {
		r.add("build", regression, "accepted by the current compiler, rejected by go build", outcomeDetail(o, n))
		return r
	}
	if !o.ok() || !n.ok() {
		// Temporary output paths differ between the two runs.
		o.err = strings.ReplaceAll(o.err, out, "$OUT")
		n.err = strings.ReplaceAll(n.err, out, "$OUT")
		o.out, n.out = "", ""
		cls, why := classify(o, n, newer, c.failTest)
		r.add("build", cls, why, outcomeDetail(o, n))
		if n.ok() {
			newOnly(&r, c, out, gNew, bNew)
		}
		return r
	}
	a, errA := os.ReadFile(gOld)
	b, errB := os.ReadFile(gNew)
	if errA != nil || errB != nil {
		r.add("build", regression, fmt.Sprintf("missing emitted Go: %v %v", errA, errB), "")
		return r
	}
	if !bytes.Equal(a, b) {
		r.add("build --emit-go", regression, "accepted by both, emitted Go differs", firstDiff(a, b))
	}
	if govet && !c.mutant {
		if msg, ok := vet(out, b); !ok {
			r.add("go vet", regression, "go vet rejects the current compiler's emitted Go", indent("    ", msg))
		}
	}
	if runOut && c.outFile != "" {
		ro := runBinary(bOld, out)
		rn := runBinary(bNew, out)
		want, _ := os.ReadFile(c.outFile)
		switch {
		case ro != rn:
			r.add("run", regression, "run output differs", outcomeDetail(ro, rn))
		case rn.out != string(want) || !rn.ok():
			// Both differ from the .out file in the same way: a
			// conformance failure the corpus runner reports.
			if ro.out == string(want) && ro.ok() {
				r.add("run", regression, "run output differs from "+c.outFile, outcomeDetail(ro, rn))
			}
		}
	}
	return r
}

// newOnly checks a program that only the current compiler builds (a new
// feature, usually EXPECTED): there is nothing to compare with, so its
// emitted Go must pass `go vet` and, if the case has a .out file, its run
// output must match it. Either failure is a REGRESSION.
func newOnly(r *result, c tcase, out, gNew, bNew string) {
	if govet && !c.mutant {
		b, err := os.ReadFile(gNew)
		if err != nil {
			r.add("build", regression, "missing emitted Go: "+err.Error(), "")
			return
		}
		if msg, ok := vet(out, b); !ok {
			r.add("go vet", regression, "go vet rejects the current compiler's emitted Go (new program)", indent("    ", msg))
		}
	}
	if runOut && c.outFile != "" {
		rn := runBinary(bNew, out)
		want, _ := os.ReadFile(c.outFile)
		if rn.out != string(want) || !rn.ok() {
			r.add("run", regression, "run output of a new program differs from "+c.outFile,
				fmt.Sprintf("  want:\n%s  current (exit %d):\n%s", indent("    ", string(want)), rn.code, indent("    ", rn.out)))
		}
	}
}

func runBinary(bin, dir string) outcome {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	// stderr is ignored (testdata/README.md), only stdout and the status.
	return outcome{out: out.String(), code: code}
}

// vet runs `go vet` on the emitted Go src in a temporary module that
// requires fincore (like finc build does).
func vet(out string, src []byte) (string, bool) {
	dir := filepath.Join(out, "vet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err.Error(), false
	}
	goVersion := "1.22"
	if data, err := os.ReadFile(filepath.Join(rootDir, "go.mod")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "go "); ok {
				goVersion = strings.TrimSpace(v)
			}
		}
	}
	mod := fmt.Sprintf("module fincprog\n\ngo %s\n\nrequire fincore v0.0.0\n\nreplace fincore => %s\n", goVersion, rootDir)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		return err.Error(), false
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), src, 0o644); err != nil {
		return err.Error(), false
	}
	cmd := exec.Command(goBin, "vet", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=0")
	b, err := cmd.CombinedOutput()
	return string(b), err == nil
}

func firstDiff(a, b []byte) string {
	la := strings.Split(string(a), "\n")
	lb := strings.Split(string(b), "\n")
	for i := 0; i < len(la) || i < len(lb); i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			return fmt.Sprintf("  first difference, line %d:\n    previous: %s\n    current:  %s\n", i+1, x, y)
		}
	}
	return "  (length differs)\n"
}

func indent(prefix, s string) string {
	if s == "" {
		return prefix + "<empty>\n"
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 12 {
		lines = append(lines[:12], "...")
	}
	var b strings.Builder
	for _, l := range lines {
		if len(l) > 300 {
			l = l[:300] + "..."
		}
		b.WriteString(prefix + l + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// seed

// seedTag reads bootstrap/SEED: release tags oldest first, `#` comments;
// the last tag is the seed (the previous release).
func seedTag() string {
	tag := "v0.2.0"
	if b, err := os.ReadFile("bootstrap/SEED"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if i := strings.Index(l, "#"); i >= 0 {
				l = l[:i]
			}
			if f := strings.Fields(l); len(f) > 0 {
				tag = f[0]
			}
		}
	}
	return tag
}

// ---------------------------------------------------------------------------
// allowlist

type allowEntry struct {
	reason string
	used   bool
}

// readAllow parses the allowlist: `<case name> # <reason>` per line.
func readAllow(r io.Reader) (map[string]*allowEntry, error) {
	allow := map[string]*allowEntry{}
	sc := bufio.NewScanner(r)
	for ln := 1; sc.Scan(); ln++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, reason, ok := strings.Cut(line, " # ")
		name, reason = strings.TrimSpace(name), strings.TrimSpace(reason)
		if !ok || name == "" || reason == "" {
			return nil, fmt.Errorf("line %d: want `<case name> # <reason>`", ln)
		}
		allow[name] = &allowEntry{reason: reason}
	}
	return allow, sc.Err()
}

// ---------------------------------------------------------------------------
// main

func main() {
	flag.StringVar(&newBin, "new", "bin/finc1", "current compiler (built from compiler/)")
	flag.StringVar(&oldBin, "old", ".stage/finc-seed", "previous release (the bootstrap seed, make seed)")
	allowPath := flag.String("allow", "scripts/diffprev/allow.txt", "allowlist of intentional output changes")
	verbose := flag.Bool("v", false, "print every case and the details of EXPECTED and allowlisted ones")
	keep := flag.Bool("keep", false, "keep temporary directories")
	jobs := flag.Int("j", 8, "parallel cases")
	mutants := flag.Int("mutate", 0, "also compare N random token-level mutants of every corpus file and snippet")
	seed := flag.Int64("seed", 1, "mutation seed")
	flag.BoolVar(&govet, "govet", true, "run go vet on the emitted Go of every case both compilers build")
	flag.BoolVar(&runOut, "run", true, "run the programs with a .out file and compare their output")
	flag.Parse()
	if _, err := os.Stat("go.mod"); err != nil {
		fatal(errors.New("run from the module root"))
	}
	rootDir, _ = filepath.Abs(".")
	if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
		goBin = filepath.Join(string(bytes.TrimSpace(out)), "bin", "go")
	}
	tag := seedTag()
	for _, b := range []string{newBin, oldBin} {
		if _, err := os.Stat(b); err != nil {
			fatal(fmt.Errorf("%v (run make finc1 seed)", err))
		}
	}
	newBin, _ = filepath.Abs(newBin)
	oldBin, _ = filepath.Abs(oldBin)
	tagVer, ok := tagVersion(tag)
	if !ok {
		tagVer = langver.MustParse(langver.Finc0)
	}
	oldVer = compilerVersion(oldBin, tagVer)
	newVer = compilerVersion(newBin, tagVer)

	var allow map[string]*allowEntry
	if f, err := os.Open(*allowPath); err == nil {
		allow, err = readAllow(f)
		f.Close()
		if err != nil {
			fatal(fmt.Errorf("%s: %v", *allowPath, err))
		}
	} else if !os.IsNotExist(err) {
		fatal(err)
	}

	tmp, err := os.MkdirTemp("", "diffprev-")
	if err != nil {
		fatal(err)
	}
	if !*keep {
		// fatal and the failing exit call cleanup too (os.Exit skips defers).
		cleanup = func() { os.RemoveAll(tmp) }
		defer cleanup()
	}
	cases := corpusCases()
	for _, gen := range []func(string) ([]tcase, error){snippetCases, formatSnippetCases, fincRootCases} {
		cs, err := gen(tmp)
		if err != nil {
			fatal(err)
		}
		cases = append(cases, cs...)
	}
	cases = append(cases, treeMutantCases(tmp, "c", compilerMutations)...)
	cases = append(cases, treeMutantCases(tmp, "d", stdMutations)...)
	cases = dedupe(cases)
	if *mutants > 0 {
		cases = append(cases, mutantCases(tmp, *mutants, *seed)...)
	}

	start := time.Now()
	results := make([]result, len(cases))
	var wg sync.WaitGroup
	sem := make(chan struct{}, *jobs)
	for i := range cases {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			out := filepath.Join(tmp, fmt.Sprintf("out%05d", i))
			results[i] = runCase(cases[i], out)
			if !*keep {
				// Binaries and vet modules: up to ~10 MB per case.
				os.RemoveAll(out)
			}
		}(i)
	}
	wg.Wait()

	var counts [4]int
	allowed := 0
	for i, c := range cases {
		r := results[i]
		counts[r.cls]++
		var a *allowEntry
		if r.cls >= diagChanged {
			a = allow[c.name]
		}
		switch {
		case r.cls == identical:
			if *verbose {
				fmt.Printf("ok            %s\n", c.name)
			}
			continue
		case a != nil:
			a.used = true
			allowed++
			fmt.Printf("%-13s %s (allowlisted: %s)\n", r.cls, c.name, a.reason)
			if !*verbose {
				continue
			}
		case r.cls == expected:
			fmt.Printf("%-13s %s\n", r.cls, c.name)
			if !*verbose {
				continue
			}
		default:
			fmt.Printf("%-13s %s\n", r.cls, c.name)
		}
		if c.dir != "." {
			fmt.Printf("  (in %s)\n", c.dir)
		}
		for _, d := range r.diffs {
			fmt.Printf("  %s: %s: %s\n%s", d.step, d.cls, d.reason, d.detail)
		}
	}
	for name, a := range allow {
		if !a.used {
			fmt.Printf("warning: unused allowlist entry %q (%s)\n", name, a.reason)
		}
	}
	fail := counts[regression] + counts[diagChanged] - allowed
	fmt.Printf("diff-prev (current %s vs previous release %s, %s): %d cases, %d identical, %d expected (new features), %d regressions, %d diag-changed, %d allowlisted (%s)\n",
		newVer, tag, oldVer, len(cases), counts[identical], counts[expected], counts[regression], counts[diagChanged], allowed,
		time.Since(start).Round(time.Second))
	if fail > 0 {
		cleanup()
		os.Exit(1)
	}
}

// cleanup removes the temporary directory (unless -keep).
var cleanup = func() {}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "diffprev:", err)
	cleanup()
	os.Exit(2)
}
