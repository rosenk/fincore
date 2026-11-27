// Command speccheck tests the code examples of the language specification.
// It extracts every complete FinCore program from the Markdown files given
// (a ```fincore or ```go fenced block whose first line that is neither
// blank nor a `//` comment starts with `package`) and runs `finc check` on
// each one, from the working directory (the module root, so `std/...`
// imports resolve). Fragments are not checked.
//
// A block follows the conventions of testdata/README.md: `// FLAGS: ...` in
// its leading comment block adds flags to `finc check`, and `// ERROR
// E####` at the end of a line makes it a negative example: check must then
// fail with exactly the marked diagnostics (line and code; the column is
// not compared). Used by `make spec-check`.
//
//	go run ./scripts/speccheck [-finc bin/finc1] [-v] spec.md...
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Block is a complete FinCore program found in a Markdown file.
type Block struct {
	File string // Markdown file
	Line int    // line of the opening fence
	Src  string // program text
}

var (
	errMarker = regexp.MustCompile(`//\s*ERROR\s+(E\d{4})\b`)
	diagLine  = regexp.MustCompile(`^(.*?):(\d+):(\d+): error\[(E\d{4})\]`)
)

// Extract returns the complete FinCore programs of a Markdown text.
func Extract(file, text string) []Block {
	var out []Block
	var cur []string
	in, keep, start := false, false, 0
	for i, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if !in {
			if strings.HasPrefix(t, "```") {
				lang := strings.TrimSpace(strings.TrimPrefix(t, "```"))
				in, keep, start, cur = true, lang == "fincore" || lang == "go", i+1, nil
			}
			continue
		}
		if t == "```" {
			in = false
			if keep && isProgram(cur) {
				out = append(out, Block{File: file, Line: start, Src: strings.Join(cur, "\n") + "\n"})
			}
			continue
		}
		cur = append(cur, l)
	}
	return out
}

// isProgram reports whether the first line that is not blank and not a
// line comment starts with the package clause.
func isProgram(lines []string) bool {
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		return strings.HasPrefix(t, "package ")
	}
	return false
}

// flags returns the `// FLAGS:` arguments of the leading comment block.
func flags(src string) []string {
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "//") {
			break
		}
		if r, ok := strings.CutPrefix(t, "// FLAGS:"); ok {
			return strings.Fields(r)
		}
	}
	return nil
}

// markers returns the expected diagnostics as "line:code" keys.
func markers(src string) []string {
	var ms []string
	for i, l := range strings.Split(src, "\n") {
		if m := errMarker.FindStringSubmatch(l); m != nil {
			ms = append(ms, fmt.Sprintf("%d:%s", i+1, m[1]))
		}
	}
	return ms
}

// Check runs finc on one block and returns a description of the mismatch,
// or "" if the block behaves as expected.
func Check(finc, dir string, n int, b Block) string {
	path := filepath.Join(dir, fmt.Sprintf("spec_%03d_line%d.fc", n, b.Line))
	if err := os.WriteFile(path, []byte(b.Src), 0o644); err != nil {
		return err.Error()
	}
	args := append([]string{"check"}, flags(b.Src)...)
	out, err := exec.Command(finc, append(args, path)...).CombinedOutput()
	want := markers(b.Src)
	if len(want) == 0 {
		if err != nil {
			return fmt.Sprintf("finc check failed (%v):\n%s", err, out)
		}
		return ""
	}
	if err == nil {
		return fmt.Sprintf("finc check accepted the program, want %s", strings.Join(want, ", "))
	}
	var got []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if m := diagLine.FindStringSubmatch(sc.Text()); m != nil && filepath.Base(m[1]) == filepath.Base(path) {
			got = append(got, m[2]+":"+m[4])
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Sprintf("diagnostics %v, want %v (block lines):\n%s", got, want, out)
	}
	return ""
}

func main() {
	finc := flag.String("finc", "bin/finc1", "FinCore compiler to run")
	verbose := flag.Bool("v", false, "list every checked block")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: speccheck [-finc bin/finc1] [-v] spec.md...")
		os.Exit(2)
	}
	dir, err := os.MkdirTemp("", "speccheck")
	if err != nil {
		fmt.Fprintln(os.Stderr, "speccheck:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	total, failed, negative := 0, 0, 0
	for _, f := range flag.Args() {
		text, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "speccheck:", err)
			os.Exit(1)
		}
		for _, b := range Extract(f, string(text)) {
			total++
			if len(markers(b.Src)) > 0 {
				negative++
			}
			if msg := Check(*finc, dir, total, b); msg != "" {
				failed++
				fmt.Printf("FAIL %s:%d: %s\n", b.File, b.Line, msg)
			} else if *verbose {
				fmt.Printf("ok   %s:%d\n", b.File, b.Line)
			}
		}
	}
	fmt.Printf("spec-check: %d programs (%d negative), %d failed\n", total, negative, failed)
	if failed > 0 || total == 0 {
		os.Exit(1)
	}
}
