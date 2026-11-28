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
	"testing"
	"time"
)

// Corpus driver for testdata/fail and testdata/pass, implementing the
// conventions documented in testdata/README.md.

var (
	errorMarkerRE = regexp.MustCompile(`//\s*ERROR\s+(E\d{4})\b`)
	diagRE        = regexp.MustCompile(`(?m)^(\S+\.fc):(\d+):(\d+): error\[(E\d{4})\]`)
)

type header struct {
	flags     []string
	buildOnly bool
	sinceMaj  int
	sinceMin  int
}

var versionRE = regexp.MustCompile(`v(\d+)\.(\d+)`)

func compilerVersion(t testing.TB) (int, int) {
	t.Helper()
	out, _ := runFinc0(t, "version")
	m := versionRE.FindStringSubmatch(string(out))
	if m == nil {
		return 0, 2
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return maj, min
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
		if rest, ok := strings.CutPrefix(line, "// SINCE:"); ok {
			if m := versionRE.FindStringSubmatch(rest); m != nil {
				h.sinceMaj, _ = strconv.Atoi(m[1])
				h.sinceMin, _ = strconv.Atoi(m[2])
			}
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
	cMaj, cMin := compilerVersion(t)
	if cMaj > 0 || cMin >= 3 {
		v03Files, _ := filepath.Glob(filepath.Join(root, "testdata", "v03", "fail", "*.fc"))
		sort.Strings(v03Files)
		files = append(files, v03Files...)
	}
	if cMaj > 0 || cMin >= 4 {
		v04Files, _ := filepath.Glob(filepath.Join(root, "testdata", "v04", "fail", "*.fc"))
		sort.Strings(v04Files)
		files = append(files, v04Files...)
	}
	if cMaj > 0 || cMin >= 5 {
		v05Files, _ := filepath.Glob(filepath.Join(root, "testdata", "v05", "fail", "*.fc"))
		sort.Strings(v05Files)
		files = append(files, v05Files...)
	}
	if cMaj > 0 || cMin >= 6 {
		v06Files, _ := filepath.Glob(filepath.Join(root, "testdata", "v06", "fail", "*.fc"))
		sort.Strings(v06Files)
		files = append(files, v06Files...)
	}
	for _, file := range files {
		rel, _ := filepath.Rel(root, file)
		t.Run(strings.TrimSuffix(filepath.Base(file), ".fc"), func(t *testing.T) {
			h := parseHeader(t, file)
			if h.sinceMaj > cMaj || (h.sinceMaj == cMaj && h.sinceMin > cMin) {
				t.Skipf("needs v%d.%d (compiler is v%d.%d)", h.sinceMaj, h.sinceMin, cMaj, cMin)
			}
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
	requireFinc0(t)
	cMaj, cMin := compilerVersion(t)
	passDirs := []string{passDir}
	if cMaj > 0 || cMin >= 3 {
		v03Pass := filepath.Join(root, "testdata", "v03", "pass")
		if _, err := os.Stat(v03Pass); err == nil {
			passDirs = append(passDirs, v03Pass)
		}
	}
	if cMaj > 0 || cMin >= 4 {
		v04Pass := filepath.Join(root, "testdata", "v04", "pass")
		if _, err := os.Stat(v04Pass); err == nil {
			passDirs = append(passDirs, v04Pass)
		}
	}
	if cMaj > 0 || cMin >= 5 {
		v05Pass := filepath.Join(root, "testdata", "v05", "pass")
		if _, err := os.Stat(v05Pass); err == nil {
			passDirs = append(passDirs, v05Pass)
		}
	}
	if cMaj > 0 || cMin >= 6 {
		v06Pass := filepath.Join(root, "testdata", "v06", "pass")
		if _, err := os.Stat(v06Pass); err == nil {
			passDirs = append(passDirs, v06Pass)
		}
	}
	for _, curPassDir := range passDirs {
		entries, err := os.ReadDir(curPassDir)
		if err != nil {
			t.Fatal(err)
		}
		relDir, _ := filepath.Rel(root, curPassDir)
		for _, e := range entries {
			name := e.Name()
			var src, headerFile string
			switch {
			case e.IsDir():
				fcs, _ := filepath.Glob(filepath.Join(curPassDir, name, "*.fc"))
				if len(fcs) == 0 {
					continue
				}
				sort.Strings(fcs)
				src, headerFile = filepath.Join(relDir, name), fcs[0]
			case strings.HasSuffix(name, ".fc"):
				src, headerFile = filepath.Join(relDir, name), filepath.Join(curPassDir, name)
				name = strings.TrimSuffix(name, ".fc")
			default:
				continue
			}
			t.Run(name, func(t *testing.T) {
				h := parseHeader(t, headerFile)
				if h.sinceMaj > cMaj || (h.sinceMaj == cMaj && h.sinceMin > cMin) {
					t.Skipf("needs v%d.%d (compiler is v%d.%d)", h.sinceMaj, h.sinceMin, cMaj, cMin)
				}
				bin := buildFC(t, src, buildMode{"native", false}, h.flags...)
				if h.buildOnly {
					return
				}
				want, err := os.ReadFile(filepath.Join(curPassDir, name+".out"))
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
}

// TestSinceSkip checks `finc0 test`'s `// SINCE: vX.Y` import-closure skip logic.
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
	write("pass/future.fc", "// A test of a future language version.\n// SINCE: v9.9\npackage main\n\nnot v0.1 syntax\n")
	write("pass/hello.fc", "// SINCE: v0.1\npackage main\n\ndriver func main(io Stdio) error {\n\treturn io.WriteStdout(strBytes(\"hi\\n\"))\n}\n")
	write("pass/hello.out", "hi\n")
	write("fail/future.fc", "// SINCE: v9.9\npackage main\n\nvar g u32 // ERROR E9999\n")
	write("pass/second/a.fc", "package main\n\nnot v0.1 syntax\n")
	write("pass/second/b.fc", "// SINCE: v9.9\npackage main\n")
	write("pass/deep/a.fc", "package main\n\nnot v0.1 syntax\n")
	write("pass/deep/b.fc", "package main\n\nimport (\n\t\"std/none\"\n\tl \"pass/deep/lib\"\n)\n")
	write("pass/deep/lib/lib.fc", "// SINCE: v9.9\npackage lib\n\nnot v0.1 syntax\n")

	cmd := exec.Command(requireFinc0(t), "test", "-v", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PWD="+dir, "FINCORE_ROOT="+moduleRoot(t))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "5 tests, 1 passed, 0 failed, 4 skipped (needs v9.9)") {
		t.Fatalf("finc0 test: %v\n%s", err, out)
	}
	for _, n := range []string{"future.fc", "second", "deep"} {
		if !strings.Contains(string(out), n+" (needs v9.9)") {
			t.Errorf("%s not skipped:\n%s", n, out)
		}
	}
}
