// Command tutorialcheck checks the FinCore snippets of a Markdown file
// (docs/tutorial.md) against a compiler. Used by `make tutorial-check`.
//
//	go run ./scripts/tutorialcheck [-finc bin/finc1] [-v] docs/tutorial.md
//
// Conventions:
//
//   - A ```go block whose first line is `// file: NAME.fc` is a complete
//     program. Other ```go blocks are fragments and are not checked.
//   - If the next fenced block (only blank lines in between) is a ```text
//     block whose first line is `$ finc1 check NAME.fc`, `$ finc1 run
//     NAME.fc` or `$ finc1 build NAME.fc`, the command is run and the rest
//     of the block must equal its output (stdout, then stderr) exactly.
//     Otherwise `finc1 check NAME.fc` must succeed without output.
//
// Every program is written to its own temporary directory, which is the
// working directory of the command, so diagnostics name `NAME.fc`.
// FINCROOT points at the module root, so `import "std/..."` works.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type block struct {
	lang string
	line int // line of the opening fence (1-based)
	body []string
}

func main() {
	finc := flag.String("finc", "bin/finc1", "compiler to run")
	verbose := flag.Bool("v", false, "print every checked snippet")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: tutorialcheck [-finc path] [-v] file.md")
		os.Exit(2)
	}
	md := flag.Arg(0)
	src, err := os.ReadFile(md)
	if err != nil {
		fatal(err)
	}
	finPath, err := filepath.Abs(*finc)
	if err != nil {
		fatal(err)
	}
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	blocks, gaps := parse(string(src))
	failed, checked := 0, 0
	for i, b := range blocks {
		if b.lang != "go" || len(b.body) == 0 || !strings.HasPrefix(b.body[0], "// file: ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(b.body[0], "// file: "))
		cmd, want := []string{"check", name}, []string(nil)
		if i+1 < len(blocks) && gaps[i] && blocks[i+1].lang == "text" && len(blocks[i+1].body) > 0 {
			f := strings.Fields(blocks[i+1].body[0])
			if len(f) >= 3 && f[0] == "$" && f[1] == "finc1" && f[len(f)-1] == name {
				cmd, want = f[2:], blocks[i+1].body[1:]
			}
		}
		checked++
		got, err := run(finPath, root, name, strings.Join(b.body, "\n")+"\n", cmd)
		if err != nil {
			fatal(err)
		}
		gotLines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if got == "" {
			gotLines = nil
		}
		if strings.Join(gotLines, "\n") != strings.Join(want, "\n") {
			failed++
			fmt.Printf("%s:%d: %s: `finc1 %s` output differs\n--- want\n%s\n--- got\n%s\n", md, b.line, name,
				strings.Join(cmd, " "), strings.Join(want, "\n"), strings.Join(gotLines, "\n"))
		} else if *verbose {
			fmt.Printf("%s:%d: %s: ok (finc1 %s)\n", md, b.line, name, strings.Join(cmd, " "))
		}
	}
	if failed > 0 {
		fmt.Printf("tutorial-check: %d of %d snippets failed\n", failed, checked)
		os.Exit(1)
	}
	fmt.Printf("tutorial-check: %d snippets ok\n", checked)
}

// parse returns the fenced blocks of a Markdown text and, for each block,
// whether only blank lines separate it from the next block.
func parse(src string) ([]block, []bool) {
	var blocks []block
	var gaps []bool
	lines := strings.Split(src, "\n")
	adjacent := false
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, "```") {
			if strings.TrimSpace(l) != "" {
				adjacent = false
			}
			continue
		}
		if len(blocks) > 0 {
			gaps[len(gaps)-1] = adjacent
		}
		b := block{lang: strings.TrimSpace(strings.TrimPrefix(l, "```")), line: i + 1}
		for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
			b.body = append(b.body, lines[i])
		}
		blocks = append(blocks, b)
		gaps = append(gaps, false)
		adjacent = true
	}
	return blocks, gaps
}

func run(finc, root, name, src string, args []string) (string, error) {
	dir, err := os.MkdirTemp("", "tutorialcheck-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
		return "", err
	}
	c := exec.Command(finc, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "FINCROOT="+root, "FINCORE_ROOT="+root)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	_ = c.Run() // the exit status shows in the output (diagnostics)
	return stdout.String() + stderr.String(), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "tutorialcheck:", err)
	os.Exit(1)
}
