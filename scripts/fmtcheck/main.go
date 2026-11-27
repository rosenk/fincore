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
// The directories of -formatted (default compiler, std and examples) must
// also be in canonical form already: a file there that `finc fmt` would
// change fails the check (reformat with `finc fmt -w <dir>`). testdata/
// and scripts/*/cases are exempt, since their `// ERROR` markers and the
// diagnostics the differential tests compare depend on positions.
//
//	go run ./scripts/fmtcheck [-finc bin/finc1] [-list] [-formatted compiler,std,examples] [path...]
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
	formattedDirs := flag.String("formatted", "compiler,std,examples", "comma-separated directories whose files must already be formatted (empty: none)")
	flag.Parse()
	var mustBeFormatted []string
	for _, d := range strings.Split(*formattedDirs, ",") {
		if d = strings.TrimSpace(d); d != "" {
			mustBeFormatted = append(mustBeFormatted, filepath.Clean(d))
		}
	}
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
	var bad, unformatted []string
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
			if under(f, mustBeFormatted) {
				unformatted = append(unformatted, f)
			}
		}
	}
	checked := len(files) - skipped
	fmt.Printf("fmt-fc-check: %d files: %d checked (%d already formatted, %d would change), %d skipped (do not parse); idempotent and AST-equivalent: %d/%d\n",
		len(files), checked, same, changed, skipped, checked-len(bad), checked)
	for _, b := range bad {
		fmt.Println("FAIL", b)
	}
	for _, f := range unformatted {
		fmt.Printf("FAIL %s: not formatted (finc fmt -w %s)\n", f, f)
	}
	if len(unformatted) > 0 {
		fmt.Printf("fmt-fc-check: %d files of %s are not formatted\n", len(unformatted), strings.Join(mustBeFormatted, ", "))
	} else if len(mustBeFormatted) > 0 {
		fmt.Printf("fmt-fc-check: %s: all formatted\n", strings.Join(mustBeFormatted, ", "))
	}
	if len(bad) > 0 || len(unformatted) > 0 {
		os.Exit(1)
	}
}

// under reports whether the file path f lies in one of the directories.
func under(f string, dirs []string) bool {
	f = filepath.Clean(f)
	for _, d := range dirs {
		if f == d || strings.HasPrefix(f, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
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
