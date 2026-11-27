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
//     arguments, must exit 0 and its stdout must equal <name>.out;
//   - a test whose header says `// SINCE: vX.Y` with X.Y newer than
//     LangVersion is skipped and counted in the summary
//     ("N skipped (needs vX.Y)", the newest version needed).

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
	since     string // `// SINCE:` value ("v0.3"), "" if absent
}

// langVersion is a FinCore language version X.Y.
type langVersion struct{ major, minor int }

func (v langVersion) less(w langVersion) bool {
	return v.major < w.major || v.major == w.major && v.minor < w.minor
}

func (v langVersion) String() string { return fmt.Sprintf("v%d.%d", v.major, v.minor) }

// parseLangVersion parses "X.Y" (with an optional leading "v").
func parseLangVersion(s string) (langVersion, bool) {
	s = strings.TrimPrefix(s, "v")
	a, b, ok := strings.Cut(s, ".")
	if !ok {
		return langVersion{}, false
	}
	x, err1 := strconv.ParseUint(a, 10, 16)
	y, err2 := strconv.ParseUint(b, 10, 16)
	if err1 != nil || err2 != nil {
		return langVersion{}, false
	}
	return langVersion{int(x), int(y)}, true
}

// sinceSkip reports whether a test with header h needs a newer language
// version than finc0's; bad is set for a malformed `// SINCE:` value.
func sinceSkip(h testHeader) (need langVersion, skip bool, bad bool) {
	if h.since == "" {
		return langVersion{}, false, false
	}
	need, ok := parseLangVersion(h.since)
	if !ok || !strings.HasPrefix(h.since, "v") {
		return langVersion{}, false, true
	}
	cur, _ := parseLangVersion(LangVersion)
	return need, cur.less(need), false
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
	failed, skipped := 0, 0
	var newest langVersion
	for i, tc := range cases {
		start := time.Now()
		kind, msg, need := runTestCase(tc, work, i)
		if kind == "skip" {
			skipped++
			if newest.less(need) {
				newest = need
			}
			if *verbose {
				fmt.Fprintf(env.Stdout, "skip %s (needs %s)\n", tc.name, need)
			}
			continue
		}
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
	fmt.Fprintf(env.Stdout, "%d tests, %d passed, %d failed", len(cases), len(cases)-failed-skipped, failed)
	if skipped > 0 {
		fmt.Fprintf(env.Stdout, ", %d skipped (needs %s)", skipped, newest)
	}
	fmt.Fprintln(env.Stdout)
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
		if rest, ok := strings.CutPrefix(line, "// SINCE:"); ok {
			h.since = strings.TrimSpace(rest)
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

// runTestCase returns the test kind ("pass", "fail", "skip" or "?") and
// a failure message ("" = pass); need is the version a skipped test needs.
func runTestCase(tc *testCase, work string, idx int) (kind, msg string, need langVersion) {
	h, err := readTestHeader(tc.header)
	if err != nil {
		return "?", err.Error(), need
	}
	need, skip, bad := sinceSkip(h)
	if bad {
		return "?", fmt.Sprintf("bad // SINCE: %q (want vX.Y)", h.since), need
	}
	if skip {
		return "skip", "", need
	}
	want, err := expectedDiags(tc.files)
	if err != nil {
		return "?", err.Error(), need
	}
	if len(want) > 0 {
		return "fail", runFailTest(tc, h, want), need
	}
	return "pass", runPassTest(tc, h, work, idx), need
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
