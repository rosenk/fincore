// Command diffcheck compares `finc1 check --passes=P` with the oracle
// `finc0 check --passes=P` (default P = types: load, resolve, type checking
// and comptime evaluation only) on the corpus and on the source snippets
// embedded in finc0's resolver, type checker and comptime unit tests. With
// -passes=scope (make diff-check-scope) the scope checker runs too, and the
// snippets of finc0's scope unit tests are added. Snippets of the totality
// unit tests and of the formatTo tests (bootstrap/internal/check/
// format_test.go, formatSnippetCases) are always included; any mode other
// than types also checks mutated copies of compiler/ (make
// diff-check-totality).
//
//	go run ./scripts/diffcheck [-finc0 bin/finc0] [-finc1 bin/finc1] [-passes types|scope|totality|all] [-v] [-keep]
//
// Corpus cases (run from the module root): examples/*.fc, testdata/pass
// (files and package directories), testdata/fail/*.fc,
// testdata/integration/*.fc, the parser fixtures, the std/ packages and
// compiler/. `// FLAGS:` header directives are honoured. FINCROOT cases
// (fincRootCases) run both compilers with FINCROOT set to absolute and
// relative std trees. Modes other than types also check std/ packages with
// one loop/measure/type broken each (stdMutations).
//
// Snippet cases are extracted from bootstrap/internal/{resolve,types,
// comptime}/*_test.go with go/parser: every string literal that starts with
// "package " becomes a one-file package t.fc in its own temporary module
// root, and every map[string]string literal with .fc keys (resolver tests)
// becomes a multi-file module whose checked path is the next string
// argument of the enclosing call or composite literal. Snippets run twice,
// with the default fuel and with --comptime-fuel=1000.
//
// A case matches when stdout, stderr and the exit status are identical.
// finc1's generated main prints "error: <err>" after a failing driver and
// always exits 1 on error, so its final "error: fincore: ..." line is
// removed before comparing and exit statuses are compared as 0 / non-zero.
//
// finc0 is frozen at language v0.2, so this is a regression check of the
// v0.2 subset: corpus files and packages whose leading comment block says
// `// SINCE: vX.Y` with vX.Y newer than v0.2 (scripts/langver) are
// skipped together with their mutants; when compiler/ (or a std/ package)
// declares a newer version, its cases and mutants are skipped too.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"fincore/scripts/langver"
)

type tcase struct {
	name  string
	dir   string // working directory
	path  string // argument to check
	flags []string
	env   []string // extra environment (FINCROOT cases)
}

func main() {
	finc0 := flag.String("finc0", "bin/finc0", "oracle compiler")
	finc1 := flag.String("finc1", "bin/finc1", "self-hosted compiler")
	verbose := flag.Bool("v", false, "print every case")
	keep := flag.Bool("keep", false, "keep temporary directories")
	mutants := flag.Int("mutate", 0, "also check N random token-level mutants of every corpus file")
	seed := flag.Int64("seed", 1, "mutation seed")
	passes := flag.String("passes", "types", "passes to compare (--passes= value of both compilers: types, scope, totality, all)")
	flag.Parse()
	abs0, _ := filepath.Abs(*finc0)
	abs1, _ := filepath.Abs(*finc1)

	tmp, err := os.MkdirTemp("", "diffcheck-")
	if err != nil {
		fatal(err)
	}
	if !*keep {
		defer os.RemoveAll(tmp)
	}
	cases := corpusCases()
	snips, err := snippetCases(tmp)
	if err != nil {
		fatal(err)
	}
	cases = append(cases, snips...)
	tsnips, err := totalitySnippetCases(tmp)
	if err != nil {
		fatal(err)
	}
	cases = append(cases, tsnips...)
	fr, err := fincRootCases(tmp)
	if err != nil {
		fatal(err)
	}
	cases = append(cases, fr...)
	fsnips, err := formatSnippetCases(tmp)
	if err != nil {
		fatal(err)
	}
	cases = append(cases, fsnips...)
	if *passes != "types" {
		sc, err := scopeSnippetCases(tmp)
		if err != nil {
			fatal(err)
		}
		cases = append(cases, sc...)
		cases = append(cases, mutantCompilerCases(tmp)...)
		cases = append(cases, mutantStdCases(tmp)...)
	}
	if *mutants > 0 {
		cases = append(cases, mutantCases(tmp, *mutants, *seed, *passes != "types")...)
	}

	match, mismatch := 0, 0
	var failed []string
	for _, c := range cases {
		pf := "--passes=" + *passes
		o0, e0, x0 := run(abs0, c.dir, c.env, append(append([]string{"check", pf}, c.flags...), c.path))
		o1, e1, x1 := run(abs1, c.dir, c.env, append(append([]string{"check", pf}, c.flags...), c.path))
		e1 = stripRuntimeError(e1)
		ok := o0 == o1 && e0 == e1 && (x0 == 0) == (x1 == 0)
		if ok {
			match++
			if *verbose {
				fmt.Printf("ok    %s\n", c.name)
			}
			continue
		}
		mismatch++
		failed = append(failed, c.name)
		fmt.Printf("DIFF  %s (exit %d vs %d)\n", c.name, x0, x1)
		fmt.Print(indent("finc0: ", e0+o0))
		fmt.Print(indent("finc1: ", e1+o1))
	}
	fmt.Printf("diff-check (--passes=%s): %d cases, %d match, %d mismatch", *passes, len(cases), match, mismatch)
	if skipped > 0 {
		fmt.Printf(", %d inputs skipped (// SINCE: newer than finc0's %s)", skipped, langver.Finc0)
	}
	fmt.Println()
	if mismatch > 0 {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "diffcheck:", err)
	os.Exit(2)
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

func stripRuntimeError(s string) string {
	t := strings.TrimSuffix(s, "\n")
	i := strings.LastIndex(t, "\n")
	last := t[i+1:]
	if strings.HasPrefix(last, "error: fincore: ") {
		if i < 0 {
			return ""
		}
		return t[:i+1]
	}
	return s
}

func run(bin, dir string, env []string, args []string) (string, string, int) {
	cmd := exec.Command(bin, args...)
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
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
			errb.WriteString(err.Error())
		}
	}
	return out.String(), errb.String(), code
}

// headerFlags returns the `// FLAGS:` directive of a corpus file.
func headerFlags(path string) []string {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			break
		}
		if rest, ok := strings.CutPrefix(line, "// FLAGS:"); ok {
			return strings.Fields(rest)
		}
	}
	return nil
}

// skipped counts inputs left out because they need a language version
// newer than finc0's (`// SINCE:` directive).
var skipped int

// finc0Can reports whether finc0 implements the language version path
// needs (scripts/langver); a malformed directive is fatal.
func finc0Can(path string) bool {
	ok, err := langver.Finc0Can(path)
	if err != nil {
		fatal(err)
	}
	if !ok {
		skipped++
	}
	return ok
}

func corpusCases() []tcase {
	var out []tcase
	add := func(path string, flags []string) {
		if !finc0Can(path) {
			return
		}
		out = append(out, tcase{name: path + flagSuffix(flags), dir: ".", path: path, flags: flags})
	}
	for _, pat := range []string{"examples/*.fc", "testdata/pass/*.fc", "testdata/fail/*.fc",
		"testdata/integration/*.fc", "bootstrap/internal/parser/testdata/*.fc", "scripts/diffcheck/cases/*.fc"} {
		files, _ := filepath.Glob(pat)
		sort.Strings(files)
		for _, f := range files {
			add(f, headerFlags(f))
		}
	}
	dirs, _ := filepath.Glob("testdata/pass/*")
	sort.Strings(dirs)
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			first, _ := filepath.Glob(filepath.Join(d, "*.fc"))
			sort.Strings(first)
			var fl []string
			if len(first) > 0 {
				fl = headerFlags(first[0])
			}
			add(d, fl)
		}
	}
	stdDirs, _ := filepath.Glob("std/*")
	sort.Strings(stdDirs)
	for _, d := range stdDirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			add(d, nil)
		}
	}
	add("compiler/", nil)
	add("compiler", nil)
	return out
}

// fincRootCases check std imports resolved through $FINCROOT: a module
// root (cwd) and a separate FINCROOT tree with good, broken and missing
// std packages, absolute and relative FINCROOT values, an empty FINCROOT
// (module root fallback) and a FINCROOT without the package next to a
// module root that has it (then it must not be found).
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
			out = append(out, tcase{name: "fincroot " + f + " FINCROOT=" + fr, dir: mod, path: f, env: []string{"FINCROOT=" + fr}})
		}
	}
	// FINCROOT empty: std/hello resolves against the module root.
	out = append(out, tcase{name: "fincroot local.fc (empty FINCROOT)", dir: mod, path: "local.fc", env: []string{"FINCROOT="}})
	// FINCROOT without std/hello: not found, although mod/std/hello exists.
	out = append(out, tcase{name: "fincroot local.fc (FINCROOT without std)", dir: mod, path: "local.fc", env: []string{"FINCROOT=" + filepath.Join(base, "none")}})
	return out, nil
}

func flagSuffix(flags []string) string {
	if len(flags) == 0 {
		return ""
	}
	return " [" + strings.Join(flags, " ") + "]"
}

// snippetCases extracts the FinCore sources of the unit tests.
func snippetCases(tmp string) ([]tcase, error) {
	var out []tcase
	n := 0
	for _, dir := range []string{"bootstrap/internal/resolve", "bootstrap/internal/types", "bootstrap/internal/comptime"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
		sort.Strings(files)
		for _, f := range files {
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				return nil, err
			}
			inMap := map[*ast.BasicLit]bool{}
			// Multi-file modules: map[string]string literals with .fc keys.
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
					files := map[string]string{}
					for _, kv := range m.Elts {
						kve, ok := kv.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						k, ok1 := strLit(kve.Key)
						v, ok2 := strLit(kve.Value)
						if ok1 && ok2 && strings.HasSuffix(k, ".fc") {
							files[k] = v
							if bl, ok := kve.Value.(*ast.BasicLit); ok {
								inMap[bl] = true
							}
						}
					}
					if len(files) == 0 {
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
					n++
					root := filepath.Join(tmp, fmt.Sprintf("m%03d", n))
					for name, src := range files {
						p := filepath.Join(root, filepath.FromSlash(name))
						_ = os.MkdirAll(filepath.Dir(p), 0o755)
						_ = os.WriteFile(p, []byte(src), 0o644)
					}
					pos := fset.Position(m.Pos())
					label := fmt.Sprintf("%s:%d (module, %s)", f, pos.Line, path)
					out = append(out, tcase{name: label, dir: root, path: path})
					out = append(out, tcase{name: label + " [fuel 1000]", dir: root, path: path, flags: []string{"--comptime-fuel=1000"}})
				}
				return true
			})
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
				n++
				root := filepath.Join(tmp, fmt.Sprintf("s%03d", n))
				_ = os.MkdirAll(root, 0o755)
				_ = os.WriteFile(filepath.Join(root, "t.fc"), []byte(s), 0o644)
				pos := fset.Position(bl.Pos())
				label := fmt.Sprintf("%s:%d", f, pos.Line)
				out = append(out, tcase{name: label, dir: root, path: "t.fc"})
				out = append(out, tcase{name: label + " [fuel 1000]", dir: root, path: "t.fc", flags: []string{"--comptime-fuel=1000"}})
				return true
			})
		}
	}
	return out, nil
}

// scopeSnippetCases extracts the sources of the scope checker's unit tests
// (bootstrap/internal/scope/scope_test.go): the second string of every
// {name, src, want} case literal, wrapped like the test's run helper.
func scopeSnippetCases(tmp string) ([]tcase, error) {
	const mainStub = "\ndriver func main(io Stdio) error {\n\treturn nil\n}\n"
	var out []tcase
	f := "bootstrap/internal/scope/scope_test.go"
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, f, nil, 0)
	if err != nil {
		return nil, err
	}
	n := 0
	ast.Inspect(af, func(nd ast.Node) bool {
		cl, ok := nd.(*ast.CompositeLit)
		if !ok || len(cl.Elts) != 3 {
			return true
		}
		_, ok0 := strLit(cl.Elts[0])
		src, ok1 := strLit(cl.Elts[1])
		_, ok2 := strLit(cl.Elts[2])
		if !ok0 || !ok1 || !ok2 {
			return true
		}
		n++
		root := filepath.Join(tmp, fmt.Sprintf("sc%03d", n))
		_ = os.MkdirAll(root, 0o755)
		_ = os.WriteFile(filepath.Join(root, "t.fc"), []byte("package main\n"+src+mainStub), 0o644)
		label := fmt.Sprintf("%s:%d", f, fset.Position(cl.Pos()).Line)
		out = append(out, tcase{name: label, dir: root, path: "t.fc"})
		return true
	})
	return out, nil
}

func strLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	return s, err == nil
}

// formatSnippetCases extracts the sources of the formatTo (v0.2) unit
// tests (bootstrap/internal/check/format_test.go): the second string of
// every {name, src} case literal, a complete package whose `// FLAGS:`
// header is honoured. They run in every -passes mode.
func formatSnippetCases(tmp string) ([]tcase, error) {
	f := "bootstrap/internal/check/format_test.go"
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, f, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []tcase
	n := 0
	ast.Inspect(af, func(nd ast.Node) bool {
		cl, ok := nd.(*ast.CompositeLit)
		if !ok || len(cl.Elts) != 2 {
			return true
		}
		_, ok0 := strLit(cl.Elts[0])
		src, ok1 := strLit(cl.Elts[1])
		if !ok0 || !ok1 || !strings.Contains(src, "package main") {
			return true
		}
		n++
		root := filepath.Join(tmp, fmt.Sprintf("f%03d", n))
		_ = os.MkdirAll(root, 0o755)
		path := filepath.Join(root, "t.fc")
		_ = os.WriteFile(path, []byte(src), 0o644)
		label := fmt.Sprintf("%s:%d", f, fset.Position(cl.Pos()).Line)
		out = append(out, tcase{name: label, dir: root, path: "t.fc", flags: headerFlags(path)})
		return true
	})
	return out, nil
}

// mutantCases writes n token-level mutants of every single-file corpus
// source and of the snippets (identifier swaps, literal changes, deleted
// or duplicated lines, swapped operators and types). With tot (a
// -passes mode that includes the totality checker), the totality snippets
// are mutated too and every other mutant uses mutateTotality.
func mutantCases(tmp string, n int, seed int64, tot bool) []tcase {
	rng := rand.New(rand.NewSource(seed))
	var srcs []string
	for _, pat := range []string{"examples/*.fc", "testdata/pass/*.fc", "testdata/fail/*.fc",
		"testdata/pass/*/*.fc", "testdata/pass/*/*/*.fc", "scripts/diffcheck/cases/*.fc", "std/*/*.fc", "compiler/*.fc"} {
		files, _ := filepath.Glob(pat)
		sort.Strings(files)
		for _, f := range files {
			// A file of a package directory counts with its package.
			isPkg := strings.Contains(pat, "*/") || strings.HasPrefix(pat, "compiler/")
			if finc0Can(f) && (!isPkg || finc0Can(filepath.Dir(f))) {
				srcs = append(srcs, f)
			}
		}
	}
	snipDirs, _ := filepath.Glob(filepath.Join(tmp, "s*", "t.fc"))
	sort.Strings(snipDirs)
	srcs = append(srcs, snipDirs...)
	if tot {
		tdirs, _ := filepath.Glob(filepath.Join(tmp, "t*", "t.fc"))
		sort.Strings(tdirs)
		srcs = append(srcs, tdirs...)
	}
	var out []tcase
	k := 0
	for _, f := range srcs {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		reps := n
		if strings.HasPrefix(f, "compiler/") {
			reps = max(1, n/4)
		}
		for i := 0; i < reps; i++ {
			m := ""
			if tot && i%2 == 0 {
				m = mutateTotality(string(src), rng)
			} else {
				m = mutate(string(src), rng)
			}
			k++
			root := filepath.Join(tmp, fmt.Sprintf("x%05d", k))
			_ = os.MkdirAll(root, 0o755)
			_ = os.WriteFile(filepath.Join(root, "t.fc"), []byte(m), 0o644)
			out = append(out, tcase{name: fmt.Sprintf("mutant %d of %s (%s)", i, f, root), dir: root, path: "t.fc", flags: []string{"--comptime-fuel=5000"}})
		}
	}
	return out
}

var mutTypes = []string{"u8", "u16", "u32", "u64", "i8", "i32", "i64", "int", "usize", "bool", "string", "error",
	"Conn", "Context", "Duration", "Code 0", "Code 1", "*u8", "[]u8", "[4]u8", "Scope", "Listener"}
var mutOps = []string{"+", "-", "*", "/", "%", "<<", ">>", "&", "|", "^", "==", "!=", "<", "<=", ">", ">=", "&&", "||"}
var mutLits = []string{"0", "1", "-1", "255", "256", "300", "65536", "1 << 63", "1 << 64", "18446744073709551615",
	"'a'", "\"s\"", "true", "false", "nil", "1 / 0", "-128", "(1 << 100)", "1 << 120 - 1", "-(1 << 64)"}

func mutate(src string, rng *rand.Rand) string {
	lines := strings.Split(src, "\n")
	steps := 1 + rng.Intn(3)
	for s := 0; s < steps; s++ {
		if len(lines) < 3 {
			break
		}
		li := rng.Intn(len(lines))
		line := lines[li]
		switch rng.Intn(9) {
		case 0: // delete a line
			lines = append(lines[:li], lines[li+1:]...)
		case 1: // duplicate a line
			lines = append(lines[:li+1], lines[li:]...)
		case 2, 3: // swap an identifier with another identifier of the file
			ids := identsOf(src)
			w := wordsOf(line)
			if len(ids) == 0 || len(w) == 0 {
				continue
			}
			lines[li] = replaceNth(line, w[rng.Intn(len(w))], ids[rng.Intn(len(ids))])
		case 4: // replace a type-ish word
			w := wordsOf(line)
			if len(w) == 0 {
				continue
			}
			lines[li] = replaceNth(line, w[rng.Intn(len(w))], mutTypes[rng.Intn(len(mutTypes))])
		case 5: // replace a number
			for _, w := range wordsOf(line) {
				if w[0] >= '0' && w[0] <= '9' {
					lines[li] = replaceNth(line, w, mutLits[rng.Intn(len(mutLits))])
					break
				}
			}
		case 6: // swap an operator
			for _, op := range mutOps {
				if strings.Contains(line, " "+op+" ") {
					lines[li] = strings.Replace(line, " "+op+" ", " "+mutOps[rng.Intn(len(mutOps))]+" ", 1)
					break
				}
			}
		case 7: // replace an identifier by a literal
			w := wordsOf(line)
			if len(w) == 0 {
				continue
			}
			lines[li] = replaceNth(line, w[rng.Intn(len(w))], mutLits[rng.Intn(len(mutLits))])
		case 8: // move a line elsewhere
			lj := rng.Intn(len(lines))
			lines[li], lines[lj] = lines[lj], lines[li]
		}
	}
	return strings.Join(lines, "\n")
}

func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func wordsOf(s string) []string {
	if i := strings.Index(s, "//"); i >= 0 {
		s = s[:i]
	}
	var out []string
	for i := 0; i < len(s); {
		if !isWordByte(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isWordByte(s[j]) {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

func identsOf(src string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range wordsOf(src) {
		if (w[0] < '0' || w[0] > '9') && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// replaceNth replaces the first whole-word occurrence of old in line.
func replaceNth(line, old, repl string) string {
	for i := 0; i+len(old) <= len(line); i++ {
		if line[i:i+len(old)] != old {
			continue
		}
		if i > 0 && isWordByte(line[i-1]) || i+len(old) < len(line) && isWordByte(line[i+len(old)]) {
			continue
		}
		return line[:i] + repl + line[i+len(old):]
	}
	return line
}

const totalityMain = "\ndriver func main(io Stdio) error {\n\treturn nil\n}\n"

// totalitySnippetCases extracts the sources of the totality unit tests.
func totalitySnippetCases(tmp string) ([]tcase, error) {
	f := "bootstrap/internal/totality/totality_test.go"
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, f, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []tcase
	n := 0
	add := func(lit ast.Expr) {
		src, ok := strLit(lit)
		if !ok {
			return
		}
		n++
		root := filepath.Join(tmp, fmt.Sprintf("t%03d", n))
		_ = os.MkdirAll(root, 0o755)
		_ = os.WriteFile(filepath.Join(root, "t.fc"), []byte("package main\n"+src+totalityMain), 0o644)
		label := fmt.Sprintf("%s:%d", f, fset.Position(lit.Pos()).Line)
		out = append(out, tcase{name: label, dir: root, path: "t.fc"})
	}
	ast.Inspect(af, func(nd ast.Node) bool {
		switch x := nd.(type) {
		case *ast.CompositeLit:
			if len(x.Elts) == 2 {
				if _, ok := strLit(x.Elts[0]); ok {
					add(x.Elts[1])
				}
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "load" && len(x.Args) == 2 {
				add(x.Args[1])
			}
		}
		return true
	})
	return out, nil
}

// compilerMutations are textual edits of compiler/ that break one
// recursion measure, loop or decreases clause each.
var compilerMutations = []struct{ file, old, new string }{
	{"compiler/types.fc", "depth-1", "depth"},
	{"compiler/parser.fc", "p.constSpec(depth - 1)", "p.constSpec(depth)"},
	{"compiler/parser.fc", "depth-1", "depth"},
	{"compiler/checker.fc", "writeExpr(o, l1at(n, i), dp-1)", "writeExpr(o, l1at(n, i), dp+1)"},
	{"compiler/check_expr.fc", "dp-1", "dp / 2"},
	{"compiler/check_stmt.fc", "dp-1", "dp>>1"},
	{"compiler/totality_flow.fc", "twStmt(l1at(s, i), st, dp-1)", "twStmt(l1at(s, i), st, dp)"},
	{"compiler/totality_flow.fc", "func eb(e0 u32, st u32, dp usize) Bnds decreases(dp) {", "func eb(e0 u32, st u32, dp usize) Bnds {"},
	{"compiler/totality_rec.fc", "func measureParam(fi u32)", "func measureParamX(fi u32, d usize) decreases(d) {\n\tif d == 0 {\n\t\treturn\n\t}\n\tmeasureParamX(fi, d)\n}\n\nfunc measureParam(fi u32)"},
	{"compiler/lexer.fc", "i < n; i++", "i <= n; i++"},
	{"compiler/sema.fc", "for i := usize(0); i < n; i++ {", "for i := usize(0); i < n; i += 2 {"},
	{"compiler/sema.fc", "for i := usize(0); i < n; i++ {", "for i := usize(0); i < n; i-- {"},
	{"compiler/build.fc", "i < n; i++ {", "i < n; i++ {\n\t\tn++"},
	{"compiler/totality.fc", "for p := s; p < e; p++ {", "for p := s; p < e; p++ {\n\t\te = e + 0"},
	{"compiler/values.fc", "decreases(depth)", ""},
	{"compiler/checker.fc", "func tooDeep() {", "func tooDeep() {\n\tfor {\n\t\tbreak\n\t}"},
	{"compiler/comptime.fc", "dp-1", "dp-0"},
	{"compiler/resolve.fc", "bindType(nb(s), depth-1)", "bindType(nb(s), depth/1)"},
	{"compiler/check_stmt.fc", "if dp == 0 {\n\t\ttooDeep()\n\t\treturn\n\t}", ""},
	{"compiler/types.fc", "if depth == 0 {", "if depth == 1 {"},
	{"compiler/main.fc", "driver func checkUsage(io Stdio) error {", "func spin(n usize) usize {\n\tfor n > 0 {\n\t\tn--\n\t}\n\treturn n\n}\n\ndriver func checkUsage(io Stdio) error {"},
	{"compiler/main.fc", "func strEq(a string, b string) bool {", "func shrink(b []byte) {\n\tfor len(b) > 0 {\n\t\tb = b[1:]\n\t}\n\trem := b\n\tfor len(rem) > 0 {\n\t\tn := len(rem) - 1\n\t\trem = rem[n:]\n\t}\n}\n\nfunc strEq(a string, b string) bool {"},
}

// stdMutations are textual edits of std/ that break one loop, measure or
// type each (totality and type errors in generic library code).
var stdMutations = []struct{ file, old, new string }{
	{"std/vec/vec.fc", "for i := n; i < top; i++ {", "for i := n; i < top; i += 0 {"},
	{"std/maps/maps.fc", "for g := usize(0); g < NB; g++ {", "for g := usize(0); g <= NB; g++ {"},
	{"std/maps/maps.fc", "for i := usize(0); i < NB; i++ {", "for i := usize(0); i < NB+t.n; i++ {\n\t\tt.n = 0"},
	{"std/sort/sort.fc", "for lo < hi decreases(hi - lo) {", "for lo < hi {"},
	{"std/sort/sort.fc", "hi = mid", "hi = mid + 1"},
	{"std/strconv/strconv.fc", "for g := i; g < n; g++ {", "for g := i; g < n; g-- {"},
	{"std/path/path.fc", "if m == 0 || p[m-1] != '/' {", "if m == 0 || p[m-1] != '/' || g > m {\n\t\t\tn++"},
	{"std/bytes/bytes.fc", "if a[i] < b[i] {", "if a[i] < b {"},
	{"std/vec/vec.fc", "var z T", "var z N"},
}

// mutantStdCases writes copies of one std package with one mutation each.
func mutantStdCases(tmp string) []tcase {
	var out []tcase
	for i, m := range stdMutations {
		pkg := filepath.Dir(m.file)
		if !finc0Can(pkg) {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(pkg, "*.fc"))
		root := filepath.Join(tmp, fmt.Sprintf("m%02d", i))
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
			fmt.Printf("warning: std mutation %d (%s) does not apply\n", i, m.file)
			continue
		}
		out = append(out, tcase{name: fmt.Sprintf("std mutant %d: %s %q -> %q", i, m.file, m.old, m.new), dir: root, path: pkg})
	}
	return out
}

// mutantCompilerCases writes copies of compiler/ with one mutation each.
func mutantCompilerCases(tmp string) []tcase {
	var out []tcase
	if !finc0Can("compiler") {
		return nil
	}
	files, _ := filepath.Glob("compiler/*.fc")
	for i, m := range compilerMutations {
		root := filepath.Join(tmp, fmt.Sprintf("c%02d", i))
		dir := filepath.Join(root, "compiler")
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
			fmt.Printf("warning: compiler mutation %d (%s) does not apply\n", i, m.file)
			continue
		}
		out = append(out, tcase{name: fmt.Sprintf("compiler mutant %d: %s %q -> %q", i, m.file, m.old, m.new), dir: root, path: "compiler"})
	}
	return out
}

// mutateTotality applies 1-3 edits aimed at the totality checker: loop
// conditions and steps, measure arithmetic, guards, decreases clauses and
// extra writes inside loop bodies.
func mutateTotality(src string, rng *rand.Rand) string {
	lines := strings.Split(src, "\n")
	edits := [][2]string{
		{" < ", " <= "}, {" <= ", " < "}, {" > ", " >= "}, {" >= ", " > "}, {" == ", " != "}, {" != ", " == "},
		{" < ", " > "}, {" > ", " < "}, {"++", "--"}, {"--", "++"}, {"++", " += 2"}, {"++", " += 1"},
		{" += 1", " += 3"}, {" -= 1", " -= 2"}, {"-1", "-0"}, {" - 1", " - 2"}, {" - 1", " / 2"}, {" - 1", " >> 1"},
		{"-1", "+1"}, {"- 1", "+ 1"}, {"[1:]", "[0:]"}, {"[n:]", "[1:]"}, {"[k:]", "[k-1:]"}, {"len(", "cap("},
		{"decreases(", "decreases(1 + "}, {" + 1", " + 2"}, {" / 2", " / 1"}, {") / 2", ") >> 1"},
		{"hi - lo", "lo - hi"}, {"usize(0)", "usize(1)"}, {"u32(0)", "u32(1)"}, {"if ", "if !"},
		{"return", "_ = 0 //"}, {"break", "_ = 0 //"}, {"continue", "_ = 0 //"}, {"&&", "||"}, {"||", "&&"},
	}
	steps := 1 + rng.Intn(3)
	for s := 0; s < steps; s++ {
		if len(lines) < 3 {
			break
		}
		li := rng.Intn(len(lines))
		line := lines[li]
		switch rng.Intn(6) {
		case 0, 1, 2: // textual edit
			e := edits[rng.Intn(len(edits))]
			if strings.Contains(line, e[0]) {
				lines[li] = strings.Replace(line, e[0], e[1], 1)
			}
		case 3: // remove a decreases clause
			if i := strings.Index(line, "decreases("); i >= 0 {
				if j := strings.Index(line[i:], ")"); j >= 0 {
					lines[li] = line[:i] + line[i+j+1:]
				}
			}
		case 4: // write a variable of the line inside the following block
			w := wordsOf(line)
			if strings.HasSuffix(strings.TrimSpace(line), "{") && len(w) > 0 {
				v := w[rng.Intn(len(w))]
				ops := []string{v + "++", v + "--", v + " = " + v, v + " += 1", v + " = " + v + "[1:]", "_ = &" + v}
				lines = append(lines[:li+1], append([]string{"\t" + ops[rng.Intn(len(ops))]}, lines[li+1:]...)...)
			}
		case 5: // swap an identifier with another identifier of the file
			ids := identsOf(src)
			w := wordsOf(line)
			if len(ids) == 0 || len(w) == 0 {
				continue
			}
			lines[li] = replaceNth(line, w[rng.Intn(len(w))], ids[rng.Intn(len(ids))])
		}
	}
	return strings.Join(lines, "\n")
}
