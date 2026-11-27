//go:build unix

package integration

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fincore/scripts/langver"
)

// Corpus driver for testdata/fail and testdata/pass, implementing the
// conventions documented in testdata/README.md. A future `finc test` must
// implement the same conventions.

var (
	errorMarkerRE = regexp.MustCompile(`//\s*ERROR\s+(E\d{4})\b`)
	diagRE        = regexp.MustCompile(`(?m)^(\S+\.fc):(\d+):(\d+): error\[(E\d{4})\]`)
)

type header struct {
	flags     []string
	buildOnly bool
}

// baseLangVersion is assumed for compilers without a `version` command
// (the v0.2.0 seed predates it).
var baseLangVersion = langver.MustParse(langver.Finc0)

var (
	compilerLangOnce sync.Once
	compilerLang     langver.Version
)

// compilerLangVersion asks the compiler under test (`finc version`, which
// prints "<name> language vX.Y") for the language version it implements.
func compilerLangVersion(t testing.TB) langver.Version {
	t.Helper()
	bin := requireFinc0(t)
	compilerLangOnce.Do(func() {
		compilerLang = baseLangVersion
		out, err := exec.Command(bin, "version").Output()
		if err != nil {
			return
		}
		f := strings.Fields(string(out))
		if len(f) > 0 {
			if v, err := langver.Parse(f[len(f)-1]); err == nil {
				compilerLang = v
			}
		}
	})
	return compilerLang
}

// sinceSkip reports whether the test src (a file, or a package directory:
// the highest version of its files) needs a language version newer than
// have (scripts/langver). A malformed `// SINCE:` value is an error.
func sinceSkip(src string, have langver.Version) (langver.Version, bool, error) {
	need, ok, err := langver.Required(src)
	if err != nil || !ok {
		return need, false, err
	}
	return need, have.Less(need), nil
}

// skipCounter counts the subtests skipped for a newer language version and
// logs "N skipped (needs vX.Y)" (the newest version needed).
type skipCounter struct {
	n      int
	newest langver.Version
}

// check skips t when the test src needs a newer language version than the
// compiler under test implements.
func (c *skipCounter) check(t *testing.T, src string) {
	t.Helper()
	have := compilerLangVersion(t)
	need, skip, err := sinceSkip(src, have)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		c.n++
		if c.newest.Less(need) {
			c.newest = need
		}
		t.Skipf("needs %s (compiler implements %s)", need, have)
	}
}

func (c *skipCounter) report(t *testing.T) {
	if c.n > 0 {
		t.Logf("%d skipped (needs %s)", c.n, c.newest)
	}
}

// parseHeader reads the leading comment block (before `package`).
func parseHeader(t testing.TB, path string) header {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var h header
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
	return h
}

type diagKey struct {
	file string // base name
	line int
	code string
}

func expectedErrors(t testing.TB, path string) []diagKey {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []diagKey
	for i, line := range strings.Split(string(data), "\n") {
		for _, m := range errorMarkerRE.FindAllStringSubmatch(line, -1) {
			out = append(out, diagKey{filepath.Base(path), i + 1, m[1]})
		}
	}
	return out
}

func TestCorpusFail(t *testing.T) {
	root := moduleRoot(t)
	files, _ := filepath.Glob(filepath.Join(root, "testdata", "fail", "*.fc"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no testdata/fail/*.fc files")
	}
	requireFinc0(t)
	var skips skipCounter
	defer skips.report(t)
	for _, file := range files {
		rel, _ := filepath.Rel(root, file)
		t.Run(strings.TrimSuffix(filepath.Base(file), ".fc"), func(t *testing.T) {
			skips.check(t, file)
			h := parseHeader(t, file)
			want := expectedErrors(t, file)
			if len(want) == 0 {
				t.Fatalf("%s has no // ERROR marker", rel)
			}
			args := append([]string{"check"}, h.flags...)
			args = append(args, rel)
			out, code := runFinc0(t, args...)
			if code == 0 {
				t.Fatalf("finc0 %s succeeded; want %v\n%s", strings.Join(args, " "), want, out)
			}
			got := map[diagKey]bool{}
			for _, m := range diagRE.FindAllStringSubmatch(string(out), -1) {
				line, _ := strconv.Atoi(m[2])
				got[diagKey{filepath.Base(m[1]), line, m[4]}] = true
			}
			for _, w := range want {
				if !got[w] {
					t.Errorf("missing diagnostic %s:%d: error[%s]", w.file, w.line, w.code)
				}
				delete(got, w)
			}
			for g := range got {
				t.Errorf("unexpected diagnostic %s:%d: error[%s]", g.file, g.line, g.code)
			}
			if t.Failed() {
				t.Logf("finc0 output:\n%s", out)
			}
		})
	}
}

func TestCorpusPass(t *testing.T) {
	root := moduleRoot(t)
	passDir := filepath.Join(root, "testdata", "pass")
	entries, err := os.ReadDir(passDir)
	if err != nil {
		t.Fatal(err)
	}
	requireFinc0(t)
	var skips skipCounter
	defer skips.report(t)
	for _, e := range entries {
		name := e.Name()
		var src, headerFile string
		switch {
		case e.IsDir():
			fcs, _ := filepath.Glob(filepath.Join(passDir, name, "*.fc"))
			if len(fcs) == 0 {
				continue
			}
			sort.Strings(fcs)
			src, headerFile = filepath.Join("testdata", "pass", name), fcs[0]
		case strings.HasSuffix(name, ".fc"):
			src, headerFile = filepath.Join("testdata", "pass", name), filepath.Join(passDir, name)
			name = strings.TrimSuffix(name, ".fc")
		default:
			continue
		}
		t.Run(name, func(t *testing.T) {
			skips.check(t, filepath.Join(root, src))
			h := parseHeader(t, headerFile)
			bin := buildFC(t, src, buildMode{"static", false}, h.flags...)
			if h.buildOnly {
				return
			}
			want, err := os.ReadFile(filepath.Join(passDir, name+".out"))
			if err != nil {
				t.Fatalf("missing expected output %s.out: %v", name, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin)
			cmd.Dir = t.TempDir()
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("run: %v\nstderr:\n%s", err, stderr.String())
			}
			if !bytes.Equal(stdout.Bytes(), want) {
				t.Fatalf("stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", stdout.String(), want)
			}
		})
	}
}

// TestSinceSkip checks the `// SINCE: vX.Y` handling of this runner and of
// the compiler's own `test` command (finc0, or finc2 under
// scripts/bootstrap_verify.go) on dummy tests outside the corpus.
func TestSinceSkip(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	future := write("pass/future.fc", "// A test of a future language version.\n// SINCE: v9.9\npackage main\n\nnot v0.2 syntax\n")
	cur := write("pass/hello.fc", "// SINCE: v0.2\npackage main\n\ndriver func main(io Stdio) error {\n\treturn io.WriteStdout(strBytes(\"hi\\n\"))\n}\n")
	write("pass/hello.out", "hi\n")
	write("fail/future.fc", "// SINCE: v0.3\npackage main\n\nvar g u32 // ERROR E9999\n")

	// The runner's own logic.
	have := langver.MustParse("v0.2")
	if need, skip, err := sinceSkip(future, have); err != nil || !skip || need != langver.MustParse("v9.9") {
		t.Fatalf("future: %v %v %v", need, skip, err)
	}
	if _, skip, err := sinceSkip(cur, have); err != nil || skip {
		t.Fatalf("current: %v %v", skip, err)
	}
	if _, skip, _ := sinceSkip(future, langver.MustParse("v9.10")); skip {
		t.Fatal("v9.9 skipped by a v9.10 compiler")
	}
	if need, skip, err := sinceSkip(filepath.Join(dir, "pass"), have); err != nil || !skip || need != langver.MustParse("v9.9") {
		t.Fatalf("directory: %v %v %v", need, skip, err)
	}
	bad := write("bad/bad.fc", "// SINCE: 9.9\npackage main\n")
	if _, _, err := sinceSkip(bad, have); err == nil {
		t.Error("SINCE: 9.9 accepted")
	}
	if err := os.RemoveAll(filepath.Dir(bad)); err != nil {
		t.Fatal(err)
	}

	// The compiler's own `test` command (when it is older than v9.9 and
	// has the version command, i.e. is not the v0.2.0 seed).
	if out, err := exec.Command(requireFinc0(t), "version").Output(); err != nil {
		t.Skipf("compiler has no version command: %v %s", err, out)
	}
	if v := compilerLangVersion(t); !v.Less(langver.MustParse("v9.9")) {
		t.Skipf("compiler implements %s", v)
	}
	out, code := runFinc0(t, "test", "-v", dir)
	if code != 0 || !strings.Contains(string(out), "3 tests, 1 passed, 0 failed, 2 skipped (needs v9.9)") ||
		!strings.Contains(string(out), "future.fc (needs v9.9)") {
		t.Fatalf("finc test: exit %d\n%s", code, out)
	}
}
