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
	if cMaj > 0 || cMin >= 7 {
		v07Files, _ := filepath.Glob(filepath.Join(root, "testdata", "v07", "fail", "*.fc"))
		sort.Strings(v07Files)
		files = append(files, v07Files...)
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
	if cMaj > 0 || cMin >= 7 {
		v07Pass := filepath.Join(root, "testdata", "v07", "pass")
		if _, err := os.Stat(v07Pass); err == nil {
			passDirs = append(passDirs, v07Pass)
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

// TestRemoteModGetAndTransitiveLink tests `finc get`, module cache resolution,
// and transitive `link` inheritance from a remote git repository (hermetically via FINC_GIT_BASE).
func TestRemoteModGetAndTransitiveLink(t *testing.T) {
	fincBin := requireFinc0(t)
	cMaj, cMin := compilerVersion(t)
	if cMaj == 0 && cMin < 7 {
		t.Skip("needs v0.7")
	}

	tmp := t.TempDir()
	remotesDir := filepath.Join(tmp, "remotes")
	workLib := filepath.Join(tmp, "worklib")
	bareLib := filepath.Join(remotesDir, "example.test", "org", "cmath.git")
	cacheDir := filepath.Join(tmp, "cache")
	appDir := filepath.Join(tmp, "app")

	mustRun := func(dir string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.test",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
		return string(out)
	}

	if err := os.MkdirAll(workLib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bareLib), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create remote library repo with `link m` in its own finc.mod and tag v0.1.0
	if err := os.WriteFile(filepath.Join(workLib, "finc.mod"), []byte("module example.test/org/cmath\n\nfinc 0.7\n\nlink m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workLib, "cmath.fc"), []byte("package cmath\n\nextern \"floor\" driver func Floor(x f64) f64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(workLib, "git", "init", "-b", "main")
	mustRun(workLib, "git", "add", ".")
	mustRun(workLib, "git", "commit", "-m", "initial")
	mustRun(workLib, "git", "tag", "v0.1.0")
	mustRun(tmp, "git", "clone", "--bare", workLib, bareLib)

	// Create consumer app WITHOUT any local path or `link m` in its finc.mod
	if err := os.WriteFile(filepath.Join(appDir, "finc.mod"), []byte("module example.test/app\n\nfinc 0.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appSrc := "package main\n\nimport \"example.test/org/cmath\"\n\ndriver func main(io Stdio) error {\n\tv := cmath.Floor(3.9)\n\tvar buf [32]byte\n\tn := formatTo(buf[:], \"floor=%d\\n\", i64(v))\n\treturn io.WriteStdout(buf[:n])\n}\n"
	if err := os.WriteFile(filepath.Join(appDir, "main.fc"), []byte(appSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	baseEnv := append(os.Environ(),
		"FINCORE_ROOT="+moduleRoot(t),
		"FINC_MOD_CACHE="+cacheDir,
		"FINC_GIT_BASE=file://"+remotesDir,
	)

	// 1. `finc get example.test/org/cmath@v0.1.0`
	cmdGet := exec.Command(fincBin, "get", "example.test/org/cmath@v0.1.0")
	cmdGet.Dir = appDir
	cmdGet.Env = baseEnv
	outGet, err := cmdGet.CombinedOutput()
	if err != nil {
		t.Fatalf("finc get failed: %v\n%s", err, outGet)
	}
	if !strings.Contains(string(outGet), "downloaded example.test/org/cmath@v0.1.0") {
		t.Fatalf("unexpected finc get output:\n%s", outGet)
	}
	modBytes, err := os.ReadFile(filepath.Join(appDir, "finc.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(modBytes), "require example.test/org/cmath v0.1.0") {
		t.Fatalf("finc.mod not updated:\n%s", modBytes)
	}
	lockBytes, err := os.ReadFile(filepath.Join(appDir, "finc.lock"))
	if err != nil {
		t.Fatalf("finc.lock not created: %v", err)
	}
	if !strings.Contains(string(lockBytes), "example.test/org/cmath v0.1.0 sha256:") {
		t.Fatalf("unexpected finc.lock:\n%s", lockBytes)
	}

	// 2. `finc build` offline (from cache only, testing transitive `link m`)
	binPath := filepath.Join(appDir, "appbin")
	cmdBuild := exec.Command(fincBin, "build", "-o", binPath, ".")
	cmdBuild.Dir = appDir
	cmdBuild.Env = append(os.Environ(),
		"FINCORE_ROOT="+moduleRoot(t),
		"FINC_MOD_CACHE="+cacheDir,
	)
	outBuild, err := cmdBuild.CombinedOutput()
	if err != nil {
		t.Fatalf("finc build failed: %v\n%s", err, outBuild)
	}

	outRun, err := exec.Command(binPath).CombinedOutput()
	if err != nil || string(outRun) != "floor=3\n" {
		t.Fatalf("appbin run: err=%v out=%q", err, outRun)
	}

	// 2b. Verify tamper detection via finc.lock
	cachedFile := filepath.Join(cacheDir, "example.test", "org", "cmath@v0.1.0", "cmath.fc")
	origSrc, err := os.ReadFile(cachedFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedFile, append(origSrc, []byte("\n// tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdTamper := exec.Command(fincBin, "check", ".")
	cmdTamper.Dir = appDir
	cmdTamper.Env = append(os.Environ(),
		"FINCORE_ROOT="+moduleRoot(t),
		"FINC_MOD_CACHE="+cacheDir,
	)
	outTamper, err := cmdTamper.CombinedOutput()
	if err == nil || !strings.Contains(string(outTamper), "finc.lock checksum mismatch") {
		t.Fatalf("expected checksum mismatch on tampered cache, got err=%v out=%s", err, outTamper)
	}

	// 3. Wipe cache, run bare `finc get` (sync from finc.mod), and verify cache restored + verified against finc.lock
	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatal(err)
	}
	cmdSync := exec.Command(fincBin, "get")
	cmdSync.Dir = appDir
	cmdSync.Env = baseEnv
	outSync, err := cmdSync.CombinedOutput()
	if err != nil || !strings.Contains(string(outSync), "downloaded example.test/org/cmath@v0.1.0") {
		t.Fatalf("finc get sync failed: %v\n%s", err, outSync)
	}
}

// TestDwarfAndLSP verifies DWARF -g debug line tables and `finc lsp` JSON-RPC
// (initialize, live didOpen/didChange diagnostics, hover types, go-to-definition).
func TestDwarfAndLSP(t *testing.T) {
	fincBin := requireFinc0(t)
	cMaj, cMin := compilerVersion(t)
	if cMaj == 0 && cMin < 7 {
		t.Skip("needs v0.7")
	}

	tmp := t.TempDir()
	srcPath := filepath.Join(tmp, "sample.fc")
	goodSrc := "package main\n\nfunc addTwo(a i64, b i64) i64 {\n\treturn a + b\n}\n\ndriver func main(io Stdio) error {\n\t_ = io\n\tres := addTwo(20, 22)\n\t_ = res\n\treturn nil\n}\n"
	if err := os.WriteFile(srcPath, []byte(goodSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. DWARF -g build verification
	dbgBin := filepath.Join(tmp, "sample_dbg")
	cmdDbg := exec.Command(fincBin, "build", "-g", "-o", dbgBin, srcPath)
	cmdDbg.Env = append(os.Environ(), "FINCORE_ROOT="+moduleRoot(t))
	if out, err := cmdDbg.CombinedOutput(); err != nil {
		t.Fatalf("finc build -g failed: %v\n%s", err, out)
	}
	reOut, err := exec.Command("readelf", "-S", dbgBin).CombinedOutput()
	if err != nil || !strings.Contains(string(reOut), ".debug_line") {
		t.Fatalf("missing .debug_line in -g binary: err=%v\n%s", err, reOut)
	}

	// 2. End-to-end `finc lsp` JSON-RPC session
	frame := func(body string) string {
		return "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	}
	uri := "file://" + srcPath
	badSrcEsc := "package main\\n\\nfunc addTwo(a i64, b i64) i64 {\\n\\treturn \\\"oops\\\"\\n}\\n"
	goodSrcEsc := strings.ReplaceAll(goodSrc, "\n", "\\n")
	goodSrcEsc = strings.ReplaceAll(goodSrcEsc, "\t", "\\t")

	var in bytes.Buffer
	in.WriteString(frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"processId":null,"rootUri":null,"capabilities":{}}}`))
	in.WriteString(frame(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"` + uri + `","languageId":"finc","version":1,"text":"` + badSrcEsc + `"}}}`))
	in.WriteString(frame(`{"jsonrpc":"2.0","method":"textDocument/didChange","params":{"textDocument":{"uri":"` + uri + `","version":2},"contentChanges":[{"text":"` + goodSrcEsc + `"}]}}`))
	in.WriteString(frame(`{"jsonrpc":"2.0","id":2,"method":"textDocument/hover","params":{"textDocument":{"uri":"` + uri + `"},"position":{"line":8,"character":2}}}`))
	in.WriteString(frame(`{"jsonrpc":"2.0","id":3,"method":"textDocument/definition","params":{"textDocument":{"uri":"` + uri + `"},"position":{"line":8,"character":10}}}`))
	in.WriteString(frame(`{"jsonrpc":"2.0","id":4,"method":"shutdown","params":null}`))
	in.WriteString(frame(`{"jsonrpc":"2.0","method":"exit","params":null}`))

	cmdLsp := exec.Command(fincBin, "lsp")
	cmdLsp.Env = append(os.Environ(), "FINCORE_ROOT="+moduleRoot(t))
	cmdLsp.Stdin = &in
	var lspOut, lspErr bytes.Buffer
	cmdLsp.Stdout = &lspOut
	cmdLsp.Stderr = &lspErr
	if err := cmdLsp.Run(); err != nil {
		t.Fatalf("finc lsp failed: %v\nstderr:\n%s\nstdout:\n%s", err, lspErr.String(), lspOut.String())
	}
	got := lspOut.String()
	for _, want := range []string{
		`"name":"finc-lsp"`,
		`"code":"E2002"`,
		`"diagnostics":[]`,
		`var res i64`,
		`"line":2`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("finc lsp output missing %q\nfull output:\n%s", want, got)
		}
	}
}
