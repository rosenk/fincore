package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// `finc0 test <dir>` runs the conformance conventions of testdata/README.md:
//
//   - a test is a *.fc file of dir, or a subdirectory of dir with *.fc
//     files (a multi-file package; directives come from its first file);
//     if dir has pass/ or fail/ subdirectories (a corpus root such as
//     testdata/), only those are searched;
//   - tests with `// ERROR E####` markers are fail tests: `check [FLAGS]`
//     must fail and report exactly the marked (file, line, code) triples;
//   - other tests are pass tests: `build [FLAGS]` must succeed; unless
//     the header says `// BUILD-ONLY`, the program is run with no
//     arguments, must exit 0 and its stdout must equal <name>.out.

var (
	testErrorMarkerRE = regexp.MustCompile(`//\s*ERROR\s+(E\d{4})\b`)
	testDiagRE        = regexp.MustCompile(`(?m)^(\S+\.fc):(\d+):(\d+): error\[(E\d{4})\]`)
)

type testCase struct {
	name   string // display name (path relative to the given dir)
	src    string // file or directory passed to check/build
	header string // file carrying the directives
	files  []string
	out    string // expected stdout file (pass tests)
}

type testHeader struct {
	flags     []string
	buildOnly bool
}

func runTest(env *Env, args []string) int {
	const usageLine = "test <dir>"
	fs := newFlagSet(env, "test", usageLine)
	verbose := fs.Bool("v", false, "print every test result")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return ExitUsage
	}
	dir := fs.Arg(0)
	if !isDir(dir) {
		fmt.Fprintf(env.Stderr, "finc0 test: %s is not a directory\n", dir)
		return ExitUsage
	}
	cases, err := collectTests(dir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0 test: %v\n", err)
		return ExitError
	}
	if len(cases) == 0 {
		fmt.Fprintf(env.Stderr, "finc0 test: no tests in %s\n", dir)
		return ExitError
	}
	work, err := os.MkdirTemp("", "finc0-test-")
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0 test: %v\n", err)
		return ExitError
	}
	defer os.RemoveAll(work)
	failed := 0
	for i, tc := range cases {
		start := time.Now()
		kind, msg := runTestCase(tc, work, i)
		status := "ok  "
		if msg != "" {
			status = "FAIL"
			failed++
		}
		if msg != "" || *verbose {
			fmt.Fprintf(env.Stdout, "%s %s %s (%.2fs)\n", status, kind, tc.name, time.Since(start).Seconds())
		}
		if msg != "" {
			for _, l := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
				fmt.Fprintf(env.Stdout, "    %s\n", l)
			}
		}
	}
	fmt.Fprintf(env.Stdout, "%d tests, %d passed, %d failed\n", len(cases), len(cases)-failed, failed)
	if failed > 0 {
		return ExitError
	}
	return ExitOK
}

func collectTests(dir string) ([]*testCase, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	// A corpus root (with pass/ and/or fail/ subdirectories) contributes
	// only those.
	for _, e := range ents {
		if e.IsDir() && (e.Name() == "pass" || e.Name() == "fail") {
			var out []*testCase
			for _, e := range ents {
				if e.IsDir() && (e.Name() == "pass" || e.Name() == "fail") {
					sub, err := collectTests(filepath.Join(dir, e.Name()))
					if err != nil {
						return nil, err
					}
					out = append(out, sub...)
				}
			}
			return out, nil
		}
	}
	var out []*testCase
	for _, e := range ents { // ReadDir sorts by name
		p := filepath.Join(dir, e.Name())
		switch {
		case e.IsDir():
			fcs, _ := filepath.Glob(filepath.Join(p, "*.fc"))
			if len(fcs) == 0 {
				continue
			}
			sort.Strings(fcs)
			out = append(out, &testCase{name: p, src: p, header: fcs[0], files: fcs, out: p + ".out"})
		case strings.HasSuffix(e.Name(), ".fc"):
			out = append(out, &testCase{name: p, src: p, header: p, files: []string{p}, out: strings.TrimSuffix(p, ".fc") + ".out"})
		}
	}
	return out, nil
}

func readTestHeader(path string) (testHeader, error) {
	var h testHeader
	f, err := os.Open(path)
	if err != nil {
		return h, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
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
	return h, sc.Err()
}

type diagKey struct {
	file string
	line int
	code string
}

func expectedDiags(files []string) ([]diagKey, error) {
	var out []diagKey
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, m := range testErrorMarkerRE.FindAllStringSubmatch(line, -1) {
				out = append(out, diagKey{filepath.Base(path), i + 1, m[1]})
			}
		}
	}
	return out, nil
}

// runTestCase returns the test kind and a failure message ("" = pass).
func runTestCase(tc *testCase, work string, idx int) (kind, msg string) {
	h, err := readTestHeader(tc.header)
	if err != nil {
		return "?", err.Error()
	}
	want, err := expectedDiags(tc.files)
	if err != nil {
		return "?", err.Error()
	}
	if len(want) > 0 {
		return "fail", runFailTest(tc, h, want)
	}
	return "pass", runPassTest(tc, h, work, idx)
}

func runFailTest(tc *testCase, h testHeader, want []diagKey) string {
	var stderr bytes.Buffer
	env := &Env{Stdout: &stderr, Stderr: &stderr}
	args := append(append([]string{}, h.flags...), tc.src)
	if code := runCheck(env, args); code == ExitOK {
		return fmt.Sprintf("check succeeded; want %v", want)
	}
	got := map[diagKey]bool{}
	var gotList []diagKey
	for _, m := range testDiagRE.FindAllStringSubmatch(stderr.String(), -1) {
		line, _ := strconv.Atoi(m[2])
		k := diagKey{filepath.Base(m[1]), line, m[4]}
		if !got[k] {
			gotList = append(gotList, k)
		}
		got[k] = true
	}
	var b strings.Builder
	for _, w := range want {
		if !got[w] {
			fmt.Fprintf(&b, "missing diagnostic %s:%d: error[%s]\n", w.file, w.line, w.code)
		}
		delete(got, w)
	}
	for _, g := range gotList {
		if got[g] {
			fmt.Fprintf(&b, "unexpected diagnostic %s:%d: error[%s]\n", g.file, g.line, g.code)
		}
	}
	if b.Len() > 0 {
		b.WriteString("check output:\n" + stderr.String())
	}
	return b.String()
}

func runPassTest(tc *testCase, h testHeader, work string, idx int) string {
	var stderr bytes.Buffer
	env := &Env{Stdout: &stderr, Stderr: &stderr}
	o, rest, ok := parseBuildFlags(env, "build", "build", h.flags, true)
	if !ok || len(rest) != 0 {
		return "bad // FLAGS: " + strings.Join(h.flags, " ") + "\n" + stderr.String()
	}
	o.Out = filepath.Join(work, fmt.Sprintf("t%d", idx))
	if !buildBinary(env, tc.src, o) {
		return "build failed:\n" + stderr.String()
	}
	if h.buildOnly {
		return ""
	}
	want, err := os.ReadFile(tc.out)
	if err != nil {
		return fmt.Sprintf("missing expected output: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.Out)
	cmd.Dir = work
	var stdout, perr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &perr
	if err := cmd.Run(); err != nil {
		return fmt.Sprintf("run: %v\nstderr:\n%s", err, perr.String())
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		return fmt.Sprintf("stdout mismatch\n--- got ---\n%s--- want ---\n%s", stdout.String(), want)
	}
	return ""
}
