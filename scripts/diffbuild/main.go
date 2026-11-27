// Command diffbuild compares the Go code emitted by `finc1 build
// --emit-go` with the oracle `finc0 build --emit-go`, byte for byte.
//
//	go run ./scripts/diffbuild [-finc0 bin/finc0] [-finc1 bin/finc1] [-v] [-keep] [-j N]
//
// Cases (run from the module root): examples/*.fc, testdata/pass (files and
// package directories), testdata/integration/*.fc, compiler/,
// scripts/diffbuild/cases/*.fc (hand-written code generation edge cases;
// cg_std.fc imports every std/ package)
// and every FinCore snippet embedded in finc0's codegen, types, comptime,
// resolve, scope, totality, ir and check (formatTo) unit tests (string literals starting with "package ", each
// written to t.fc in its own temporary directory; snippets without a
// main get `driver func main() error { return nil }`). `// FLAGS:` header
// directives are honoured.
//
// A case matches when both compilers succeed and the emitted Go files are
// identical, or when both fail (most unit-test snippets are error cases;
// their diagnostics are compared by diff-check).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type tcase struct {
	name  string
	dir   string // working directory
	path  string // argument to build
	flags []string
}

type result struct {
	ok, both bool // ok: match; both: both compilers failed
	msg      string
}

func main() {
	finc0 := flag.String("finc0", "bin/finc0", "oracle compiler")
	finc1 := flag.String("finc1", "bin/finc1", "self-hosted compiler")
	verbose := flag.Bool("v", false, "print every case")
	keep := flag.Bool("keep", false, "keep temporary directories")
	jobs := flag.Int("j", 8, "parallel cases")
	flag.Parse()
	abs0, _ := filepath.Abs(*finc0)
	abs1, _ := filepath.Abs(*finc1)

	tmp, err := os.MkdirTemp("", "diffbuild-")
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

	results := make([]result, len(cases))
	var wg sync.WaitGroup
	sem := make(chan struct{}, *jobs)
	for i := range cases {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runCase(abs0, abs1, tmp, i, cases[i])
		}(i)
	}
	wg.Wait()

	match, both, mismatch := 0, 0, 0
	for i, c := range cases {
		r := results[i]
		switch {
		case r.ok && r.both:
			both++
			if *verbose {
				fmt.Printf("fail  %s (both fail)\n", c.name)
			}
		case r.ok:
			match++
			if *verbose {
				fmt.Printf("ok    %s\n", c.name)
			}
		default:
			mismatch++
			fmt.Printf("DIFF  %s\n%s", c.name, r.msg)
		}
	}
	fmt.Printf("diff-build: %d cases, %d identical, %d fail in both, %d mismatch\n", len(cases), match, both, mismatch)
	if mismatch > 0 {
		os.Exit(1)
	}
}

func runCase(finc0, finc1, tmp string, i int, c tcase) result {
	out := filepath.Join(tmp, fmt.Sprintf("out%04d", i))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return result{msg: err.Error() + "\n"}
	}
	g0, g1 := filepath.Join(out, "stage0.go"), filepath.Join(out, "stage1.go")
	e0, x0 := run(finc0, c.dir, append(append([]string{"build"}, c.flags...), "--emit-go="+g0, "-o", filepath.Join(out, "bin0"), c.path))
	e1, x1 := run(finc1, c.dir, append(append([]string{"build"}, c.flags...), "--emit-go="+g1, "-o", filepath.Join(out, "bin1"), c.path))
	if x0 != 0 || x1 != 0 {
		if x0 != 0 && x1 != 0 {
			return result{ok: true, both: true}
		}
		return result{msg: fmt.Sprintf("  exit %d vs %d\n%s%s", x0, x1, indent("  finc0: ", e0), indent("  finc1: ", e1))}
	}
	a, err0 := os.ReadFile(g0)
	b, err1 := os.ReadFile(g1)
	if err0 != nil || err1 != nil {
		return result{msg: fmt.Sprintf("  missing output: %v %v\n", err0, err1)}
	}
	if bytes.Equal(a, b) {
		return result{ok: true}
	}
	return result{msg: firstDiff(a, b)}
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
			return fmt.Sprintf("  line %d:\n  finc0: %s\n  finc1: %s\n", i+1, x, y)
		}
	}
	return "  (length differs)\n"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "diffbuild:", err)
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

func run(bin, dir string, args []string) (string, int) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var errb bytes.Buffer
	cmd.Stdout = &errb
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
	return errb.String(), code
}

// headerFlags returns the `// FLAGS:` directive of a source file.
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
		out = append(out, tcase{name: path, dir: ".", path: path, flags: flags})
	}
	for _, pat := range []string{"examples/*.fc", "testdata/pass/*.fc", "testdata/integration/*.fc", "scripts/diffbuild/cases/*.fc"} {
		files, _ := filepath.Glob(pat)
		sort.Strings(files)
		for _, f := range files {
			add(f, headerFlags(f))
		}
	}
	dirs, _ := filepath.Glob("testdata/pass/*")
	dirs2, _ := filepath.Glob("scripts/diffbuild/cases/*")
	dirs = append(dirs, dirs2...)
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
	add("compiler", nil)
	return out
}

// snippetCases extracts the FinCore sources of finc0's unit tests.
func snippetCases(tmp string) ([]tcase, error) {
	var out []tcase
	n := 0
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
			ast.Inspect(af, func(nd ast.Node) bool {
				bl, ok := nd.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(bl.Value)
				if err != nil || !strings.HasPrefix(s, "package main") {
					return true
				}
				if !strings.Contains(s, "func main(") {
					// Give library snippets an entry point so that all
					// their declarations are emitted.
					s += "\ndriver func main() error {\n\treturn nil\n}\n"
				}
				n++
				root := filepath.Join(tmp, fmt.Sprintf("s%04d", n))
				_ = os.MkdirAll(root, 0o755)
				_ = os.WriteFile(filepath.Join(root, "t.fc"), []byte(s), 0o644)
				pos := fset.Position(bl.Pos())
				out = append(out, tcase{name: fmt.Sprintf("%s:%d", f, pos.Line), dir: root, path: "t.fc"})
				return true
			})
		}
	}
	return out, nil
}
