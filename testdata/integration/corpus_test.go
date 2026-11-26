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
	for _, file := range files {
		rel, _ := filepath.Rel(root, file)
		t.Run(strings.TrimSuffix(filepath.Base(file), ".fc"), func(t *testing.T) {
			want := expectedErrors(t, file)
			if len(want) == 0 {
				t.Fatalf("%s has no // ERROR marker", rel)
			}
			h := parseHeader(t, file)
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
