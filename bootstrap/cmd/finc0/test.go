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
//   - a test that requires a language version newer than LangVersion
//     is skipped and counted in the summary ("N skipped (needs vX.Y)",
//     the newest version needed). The required version is the highest
//     `// SINCE: vX.Y` of the test's import closure (requiredVersion;
//     the rule of scripts/langver, documented in testdata/README.md).

var (
	testErrorMarkerRE = regexp.MustCompile(`//\s*ERROR\s+(E\d{4})\b`)
	testDiagRE        = regexp.MustCompile(`(?m)^(\S+\.fc):(\d+):(\d+): error\[(E\d{4})\]`)
)

type testCase struct {
	name   string // display name (path relative to the given dir)
	src    string // file or directory passed to check/build
	header string // file carrying the FLAGS/BUILD-ONLY directives
	files  []string
	out    string // expected stdout file (pass tests)
}

type testHeader struct {
	flags     []string
	buildOnly bool
}

// langVersion is a FinCore language version X.Y.
type langVersion struct{ major, minor int }

func (v langVersion) less(w langVersion) bool {
	return v.major < w.major || v.major == w.major && v.minor < w.minor
}

func (v langVersion) String() string { return fmt.Sprintf("v%d.%d", v.major, v.minor) }

// parseLangVersion parses "X.Y" (with an optional leading "v"); X and Y
// are 1 to 4 decimal digits.
func parseLangVersion(s string) (langVersion, bool) {
	s = strings.TrimPrefix(s, "v")
	a, b, ok := strings.Cut(s, ".")
	x, ok1 := versionDigits(a)
	y, ok2 := versionDigits(b)
	if !ok || !ok1 || !ok2 {
		return langVersion{}, false
	}
	return langVersion{x, y}, true
}

func versionDigits(s string) (int, bool) {
	if len(s) == 0 || len(s) > 4 {
		return 0, false
	}
	v := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		v = v*10 + int(s[i]-'0')
	}
	return v, true
}

// ---------------------------------------------------------------------------
// Required language version. Keep identical to scripts/langver and
// compiler/build.fc; the rule is documented in testdata/README.md.

// space is the white space of the SINCE and import scans (ASCII).
const space = " \t\n\v\f\r"

// fileSince returns the value of the first `// SINCE:` line of the leading
// comment block of src (the trimmed lines that are empty or start with
// `//`, up to the first other line).
func fileSince(src string) (val string, ok bool) {
	for _, line := range strings.Split(src, "\n") {
		line = strings.Trim(line, space)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			break
		}
		if rest, found := strings.CutPrefix(line, "// SINCE:"); found {
			return strings.Trim(rest, space), true
		}
	}
	return "", false
}

// stripComments replaces each `//` and `/* */` comment by a space (a
// block comment keeps its newlines); string literals are not recognized.
func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
			if i < len(s) {
				b.WriteByte('\n')
			}
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			b.WriteByte(' ')
			i += 2
			for i < len(s) && !(s[i] == '*' && i+1 < len(s) && s[i+1] == '/') {
				if s[i] == '\n' {
					b.WriteByte('\n')
				}
				i++
			}
			i++ // now at the '/' of "*/" (or past the end)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// startsWord reports whether t starts with kw followed by the end, white
// space, `(` or `"`.
func startsWord(t, kw string) bool {
	if !strings.HasPrefix(t, kw) {
		return false
	}
	if len(t) == len(kw) {
		return true
	}
	c := t[len(kw)]
	return strings.IndexByte(space, c) >= 0 || c == '(' || c == '"'
}

// importSpecPath parses `[name] "path"` (t is trimmed).
func importSpecPath(t string) (string, bool) {
	q := strings.IndexByte(t, '"')
	if q < 0 || strings.ContainsAny(strings.TrimRight(t[:q], space), space) {
		return "", false
	}
	end := strings.IndexByte(t[q+1:], '"')
	if end < 0 {
		return "", false
	}
	return t[q+1 : q+1+end], true
}

// scanImports returns the import paths of src (the textual scan of
// testdata/README.md): comments removed, lines trimmed, empty lines
// skipped; `package ...` lines skipped; a line `import (` opens a block
// of one spec per line up to a line starting with `)`; `import [name]
// "p"`; any other line is the first declaration and ends the scan.
func scanImports(src string) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(stripComments(src), "\n") {
		t := strings.Trim(line, space)
		switch {
		case t == "":
		case inBlock:
			if strings.HasPrefix(t, ")") {
				inBlock = false
			} else if p, ok := importSpecPath(t); ok {
				out = append(out, p)
			}
		case startsWord(t, "package"):
		case startsWord(t, "import"):
			r := strings.Trim(t[len("import"):], space)
			if r == "(" {
				inBlock = true
			} else if p, ok := importSpecPath(r); ok {
				out = append(out, p)
			}
		default:
			return out
		}
	}
	return out
}

// validImportPath is the compiler's import path check (resolve/load.go).
func validImportPath(ip string) bool {
	return ip != "" && !strings.HasPrefix(ip, "/") && !strings.Contains(ip, "\\") &&
		!strings.HasPrefix(ip, "..") && ip == filepath.ToSlash(filepath.Clean(ip))
}

// requiredVersion returns the language version the test input src (a
// file or a package directory) requires: the highest SINCE of its own
// package (the file, or every .fc file directly in the directory) and,
// transitively, of every package it imports. Valid import paths resolve
// like the compiler's: against the working directory (the module root),
// std/... against $FINCROOT when it is set. Imports that are not
// directories are ignored; a visited package (keyed by its directory) is
// not read again. A malformed directive is an error naming its file.
func requiredVersion(src string) (need langVersion, ok bool, err error) {
	fincRoot := os.Getenv("FINCROOT")
	visited := map[string]bool{}
	var queue []string
	read := func(file string) error {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if val, has := fileSince(string(data)); has {
			v, good := parseLangVersion(val)
			if !good || !strings.HasPrefix(val, "v") {
				return fmt.Errorf("%s: bad // SINCE: %q (want vX.Y)", file, val)
			}
			if !ok || need.less(v) {
				need, ok = v, true
			}
		}
		for _, ip := range scanImports(string(data)) {
			if !validImportPath(ip) {
				continue
			}
			d := filepath.FromSlash(ip)
			if fincRoot != "" && (ip == "std" || strings.HasPrefix(ip, "std/")) {
				d = filepath.Join(fincRoot, d)
			}
			if !visited[d] {
				visited[d] = true
				queue = append(queue, d)
			}
		}
		return nil
	}
	readPkg := func(dir string) error {
		ents, err := os.ReadDir(dir) // sorted by name
		if err != nil {
			return err
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".fc") {
				continue
			}
			if err := read(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if isDir(src) {
		err = readPkg(src)
	} else {
		err = read(src)
	}
	for i := 0; err == nil && i < len(queue); i++ {
		if isDir(queue[i]) {
			err = readPkg(queue[i])
		}
	}
	if err != nil {
		return langVersion{}, false, err
	}
	return need, ok, nil
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
	need, ok, err := requiredVersion(tc.src)
	if err != nil {
		return "?", err.Error(), need
	}
	if cur, _ := parseLangVersion(LangVersion); ok && cur.less(need) {
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
