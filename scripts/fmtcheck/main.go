// Command fmtcheck checks `finc fmt` on the repository corpus (make
// fmt-fc-check):
//
//   - idempotence: fmt(fmt(x)) == fmt(x);
//   - semantics: `finc parse --dump-ast` of the formatted file equals that of
//     the original, ignoring node IDs, positions and the file path.
//
// The formatter verifies the second property itself before it prints
// anything (fmt.fc); this is the independent check, through the parse
// command and a fresh process per file. Files that do not parse (the
// syntax-error fixtures) are skipped and counted. With -list the files
// whose formatting differs from the checked-in source are printed.
//
//	go run ./scripts/fmtcheck [-finc bin/finc1] [-list] [path...]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The default corpus: every .fc file of the repository except the
// bootstrap seed copies under .stage/.
var corpus = []string{
	"compiler", "std", "examples", "testdata", "scripts",
	"bootstrap/internal/parser/testdata",
}

var posRE = regexp.MustCompile(` #\d+ @\d+:\d+-\d+:\d+| Path="[^"]*"`)

func main() {
	finc := flag.String("finc", "bin/finc1", "compiler under test")
	list := flag.Bool("list", false, "print the files whose formatting differs")
	flag.Parse()
	roots := flag.Args()
	if len(roots) == 0 {
		roots = corpus
	}
	var files []string
	for _, r := range roots {
		err := filepath.WalkDir(r, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != r {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(p, ".fc") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "fmtcheck:", err)
			os.Exit(2)
		}
	}
	sort.Strings(files)
	tmp, err := os.MkdirTemp("", "fmtcheck-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fmtcheck:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(tmp)

	var same, changed, skipped int
	var bad []string
	for i, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		dump0, err := run(*finc, "parse", "--dump-ast", f)
		if err != nil {
			skipped++ // not a valid source file (syntax-error fixture)
			continue
		}
		out1, err := run(*finc, "fmt", f)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: fmt failed: %v", f, err))
			continue
		}
		t1 := filepath.Join(tmp, fmt.Sprintf("f%d.fc", i))
		if err := os.WriteFile(t1, out1, 0o644); err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		out2, err := run(*finc, "fmt", t1)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: fmt of the formatted file failed: %v", f, err))
			continue
		}
		if !bytes.Equal(out1, out2) {
			bad = append(bad, fmt.Sprintf("%s: not idempotent: fmt(fmt(x)) != fmt(x)", f))
		}
		dump1, err := run(*finc, "parse", "--dump-ast", t1)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: formatted file does not parse: %v", f, err))
			continue
		}
		if !bytes.Equal(posRE.ReplaceAll(dump0, nil), posRE.ReplaceAll(dump1, nil)) {
			bad = append(bad, fmt.Sprintf("%s: AST changed by formatting", f))
		}
		if bytes.Equal(src, out1) {
			same++
		} else {
			changed++
			if *list {
				fmt.Println(f)
			}
		}
	}
	checked := len(files) - skipped
	fmt.Printf("fmt-fc-check: %d files: %d checked (%d already formatted, %d would change), %d skipped (do not parse); idempotent and AST-equivalent: %d/%d\n",
		len(files), checked, same, changed, skipped, checked-len(bad), checked)
	if len(bad) > 0 {
		for _, b := range bad {
			fmt.Println("FAIL", b)
		}
		os.Exit(1)
	}
}

func run(finc string, args ...string) ([]byte, error) {
	cmd := exec.Command(finc, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
