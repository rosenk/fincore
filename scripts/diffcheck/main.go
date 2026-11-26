// Command diffcheck compares `finc1 check` with the oracle
// `finc0 check --passes=types` (load, resolve, type checking and comptime
// evaluation only) on the corpus and on the source snippets embedded in
// finc0's resolver, type checker and comptime unit tests.
//
//	go run ./scripts/diffcheck [-finc0 bin/finc0] [-finc1 bin/finc1] [-v] [-keep]
//
// Corpus cases (run from the module root): examples/*.fc, testdata/pass
// (files and package directories), testdata/fail/*.fc,
// testdata/integration/*.fc, the parser fixtures and compiler/. `// FLAGS:`
// header directives are honoured.
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
)

type tcase struct {
	name  string
	dir   string // working directory
	path  string // argument to check
	flags []string
}

func main() {
	finc0 := flag.String("finc0", "bin/finc0", "oracle compiler")
	finc1 := flag.String("finc1", "bin/finc1", "self-hosted compiler")
	verbose := flag.Bool("v", false, "print every case")
	keep := flag.Bool("keep", false, "keep temporary directories")
	mutants := flag.Int("mutate", 0, "also check N random token-level mutants of every corpus file")
	seed := flag.Int64("seed", 1, "mutation seed")
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
	if *mutants > 0 {
		cases = append(cases, mutantCases(tmp, *mutants, *seed)...)
	}

	match, mismatch := 0, 0
	var failed []string
	for _, c := range cases {
		o0, e0, x0 := run(abs0, c.dir, append(append([]string{"check", "--passes=types"}, c.flags...), c.path))
		o1, e1, x1 := run(abs1, c.dir, append(append([]string{"check"}, c.flags...), c.path))
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
	fmt.Printf("diff-check: %d cases, %d match, %d mismatch\n", len(cases), match, mismatch)
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

func run(bin, dir string, args []string) (string, string, int) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
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

func corpusCases() []tcase {
	var out []tcase
	add := func(path string, flags []string) {
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
	add("compiler/", nil)
	add("compiler", nil)
	return out
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

func strLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	return s, err == nil
}

// mutantCases writes n token-level mutants of every single-file corpus
// source and of the snippets (identifier swaps, literal changes, deleted
// or duplicated lines, swapped operators and types).
func mutantCases(tmp string, n int, seed int64) []tcase {
	rng := rand.New(rand.NewSource(seed))
	var srcs []string
	for _, pat := range []string{"examples/*.fc", "testdata/pass/*.fc", "testdata/fail/*.fc",
		"testdata/pass/*/*.fc", "testdata/pass/*/*/*.fc", "scripts/diffcheck/cases/*.fc", "compiler/*.fc"} {
		files, _ := filepath.Glob(pat)
		sort.Strings(files)
		srcs = append(srcs, files...)
	}
	snipDirs, _ := filepath.Glob(filepath.Join(tmp, "s*", "t.fc"))
	sort.Strings(snipDirs)
	srcs = append(srcs, snipDirs...)
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
			m := mutate(string(src), rng)
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
