// dist packs and smoke-tests a FinCore release archive (make dist,
// make dist-test; layout and reproducibility in docs/release.md).
//
//	go run ./scripts/dist -version v0.3.0 -finc .stage/finc2 [-src .] [-out dist]
//	go run ./scripts/dist -test -version v0.3.0 [-src .] [-out dist]
//
// The archive dist/fincore-<version>-<goos>-<goarch>.tar.gz (version without
// the leading "v") holds one top-level directory fincore/:
//
//	fincore/bin/finc            the self-hosted compiler (finc2 of the bootstrap)
//	fincore/go.mod              `module fincore`: the Go module of the runtime
//	fincore/runtime/fincrt/     the runtime sources (no tests)
//	fincore/std/                the FinCore standard library sources
//	fincore/examples/, docs/    reference program, spec, stdlib and runtime docs
//	fincore/VERSION             the version; marks an installed release
//	fincore/README.md, CHANGELOG.md, LICENSE* (when present)
//
// finc finds go.mod (the runtime) and std/ by walking up from its own
// executable, so the directory works wherever it is unpacked (std/ only
// falls back to a module root that has a VERSION file). A compiler
// that cannot locate itself (the sources at -src predate it: compiler/
// build.fc does not mention FINCRT_EXECUTABLE, i.e. v0.2.0) is installed as
// fincore/libexec/finc behind a POSIX sh launcher bin/finc that sets
// FINCROOT (if unset) to the install directory; the compiler finds the
// runtime from its own path (Arg(0)) then.
//
// The archive is reproducible: entries are sorted, every mtime is
// $SOURCE_DATE_EPOCH or else the commit time of HEAD in -src, owner and
// group are 0 with empty names, modes are 0755 (directories, executables)
// or 0644, and gzip writes no name or time. Identical inputs and Go
// toolchain give byte-identical archives.
//
// -test unpacks the archive with tar into a temporary directory and, from
// another temporary project directory, with FINCROOT, FINCORE_ROOT,
// FINC_GO and FINC_GOFMT removed and the archive's bin/ first on PATH:
// builds and runs a hello program that imports std/strconv and uses
// formatTo, runs it again through a symlink to bin/finc, builds
// examples/tcp_echo.fc, and runs `finc test` on copies of a few testdata
// tests.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	version = flag.String("version", "", "release version (e.g. v0.3.0); the archive name drops the leading v")
	finc    = flag.String("finc", "", "compiler binary to package (the bootstrap's finc2)")
	src     = flag.String("src", ".", "source tree (module root) whose std/, runtime/ and docs are packaged")
	out     = flag.String("out", "dist", "output directory")
	test    = flag.Bool("test", false, "smoke-test the archive instead of building it")
)

func main() {
	flag.Parse()
	if *version == "" {
		fatal("-version is required")
	}
	var err error
	if *test {
		err = smoke(archivePath())
	} else {
		err = pack(archivePath())
	}
	if err != nil {
		fatal(err.Error())
	}
}

func archivePath() string {
	name := fmt.Sprintf("fincore-%s-%s-%s.tar.gz", strings.TrimPrefix(*version, "v"), runtime.GOOS, runtime.GOARCH)
	return filepath.Join(*out, name)
}

// entry is one regular file of the archive.
type entry struct {
	name string // path below fincore/
	data []byte
	exec bool
}

func pack(dst string) error {
	if *finc == "" {
		return fmt.Errorf("-finc is required")
	}
	bin, err := os.ReadFile(*finc)
	if err != nil {
		return err
	}
	buildFC, err := os.ReadFile(filepath.Join(*src, "compiler", "build.fc"))
	if err != nil {
		return err
	}
	legacy := !bytes.Contains(buildFC, []byte("FINCRT_EXECUTABLE"))

	var es []entry
	add := func(name string, data []byte, exe bool) { es = append(es, entry{name, data, exe}) }
	if legacy {
		add("libexec/finc", bin, true)
		add("bin/finc", []byte(launcher), true)
	} else {
		add("bin/finc", bin, true)
	}
	addFile := func(rel, as string) error {
		b, err := os.ReadFile(filepath.Join(*src, rel))
		if err != nil {
			return err
		}
		add(as, b, false)
		return nil
	}
	addTree := func(rel string, keep func(string) bool) error {
		root := filepath.Join(*src, rel)
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s: not a regular file", p)
			}
			r, _ := filepath.Rel(*src, p)
			r = filepath.ToSlash(r)
			if !keep(r) {
				return nil
			}
			return addFile(r, r)
		})
	}
	if err := addFile("go.mod", "go.mod"); err != nil {
		return err
	}
	if err := addTree("runtime", func(r string) bool {
		return strings.HasSuffix(r, ".go") && !strings.HasSuffix(r, "_test.go")
	}); err != nil {
		return err
	}
	if err := addTree("std", func(r string) bool { return strings.HasSuffix(r, ".fc") || strings.HasSuffix(r, ".md") }); err != nil {
		return err
	}
	for _, f := range []string{"README.md", "examples/tcp_echo.fc"} {
		if err := addFile(f, f); err != nil {
			return err
		}
	}
	// Optional files: present in newer trees only.
	optional := map[string]string{
		"CHANGELOG.md":                           "CHANGELOG.md",
		"LICENSE":                                "LICENSE",
		"LICENSE.md":                             "LICENSE.md",
		"NOTICE":                                 "NOTICE",
		"fincore_language_specification_v0_1.md": "docs/fincore_language_specification_v0_1.md",
		"docs/stdlib.md":                         "docs/stdlib.md",
		"docs/runtime_api.md":                    "docs/runtime_api.md",
		"docs/semantics_decisions.md":            "docs/semantics_decisions.md",
		"docs/v0_2.md":                           "docs/v0_2.md",
		"docs/release.md":                        "docs/release.md",
	}
	for f, as := range optional {
		if _, err := os.Stat(filepath.Join(*src, f)); err == nil {
			if err := addFile(f, as); err != nil {
				return err
			}
		}
	}
	add("VERSION", []byte(*version+"\n"), false)

	mtime, err := sourceDate()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := writeArchive(es, mtime)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	mode := "bin/finc"
	if legacy {
		mode = "libexec/finc behind the bin/finc launcher (compiler predates self-location)"
	}
	fmt.Printf("dist: wrote %s (%d files, %d bytes; %s; mtime %s)\n", dst, len(es), len(data), mode, mtime.UTC().Format(time.RFC3339))
	return nil
}

// launcher is bin/finc for compilers that cannot locate their install
// directory (v0.2.0): resolve symlinks to this script, default FINCROOT
// to the install directory and run the real compiler by absolute path, so
// that it finds go.mod (the runtime) above its Arg(0).
const launcher = `#!/bin/sh
# FinCore launcher: this compiler predates self-location, so point it at
# the install directory (std/ through FINCROOT; the runtime is found from
# the absolute path of libexec/finc). FINCROOT, if set, wins.
self=$0
while [ -h "$self" ]; do
	link=$(readlink "$self")
	case $link in
	/*) self=$link ;;
	*) self=$(dirname "$self")/$link ;;
	esac
done
root=$(cd "$(dirname "$self")/.." && pwd -P) || exit 1
FINCROOT=${FINCROOT:-$root}
export FINCROOT
exec "$root/libexec/finc" "$@"
`

// sourceDate is $SOURCE_DATE_EPOCH, else the commit time of HEAD in -src.
func sourceDate() (time.Time, error) {
	s := os.Getenv("SOURCE_DATE_EPOCH")
	if s == "" {
		cmd := exec.Command("git", "log", "-1", "--format=%ct", "HEAD")
		cmd.Dir = *src
		o, err := cmd.Output()
		if err != nil {
			return time.Time{}, fmt.Errorf("commit time of HEAD (set SOURCE_DATE_EPOCH outside a git checkout): %v", err)
		}
		s = strings.TrimSpace(string(o))
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad SOURCE_DATE_EPOCH %q", s)
	}
	return time.Unix(n, 0), nil
}

// writeArchive builds the deterministic .tar.gz of es under fincore/.
func writeArchive(es []entry, mtime time.Time) ([]byte, error) {
	files := map[string]entry{}
	dirs := map[string]bool{"fincore": true}
	for _, e := range es {
		name := "fincore/" + e.name
		if _, dup := files[name]; dup {
			return nil, fmt.Errorf("duplicate archive entry %s", name)
		}
		files[name] = e
		for d := filepath.ToSlash(filepath.Dir(name)); d != "." && d != "fincore"; d = filepath.ToSlash(filepath.Dir(d)) {
			dirs[d] = true
		}
	}
	var names []string
	for n := range files {
		names = append(names, n)
	}
	for d := range dirs {
		names = append(names, d+"/")
	}
	sort.Strings(names)

	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	hdr := func(name string, mode int64, size int64, typ byte) error {
		return tw.WriteHeader(&tar.Header{
			Typeflag: typ, Name: name, Mode: mode, Size: size,
			ModTime: mtime, Format: tar.FormatPAX,
		})
	}
	for _, n := range names {
		if strings.HasSuffix(n, "/") {
			if err := hdr(n, 0o755, 0, tar.TypeDir); err != nil {
				return nil, err
			}
			continue
		}
		e := files[n]
		mode := int64(0o644)
		if e.exec {
			mode = 0o755
		}
		if err := hdr(n, mode, int64(len(e.data)), tar.TypeReg); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------
// smoke test

const helloSrc = `// hello: the dist smoke test (std/strconv + formatTo).
package main

import "std/strconv"

driver func main(io Stdio) error {
	var num [strconv.MaxIntLen]byte
	k := strconv.FormatInt(num[:], -255, 16)
	var b [128]byte
	n := formatTo(b[:], "hello from %s: %d = %s (hex)\n", "fincore", -255, num[:k])
	return io.WriteStdout(b[:n])
}
`

const helloWant = "hello from fincore: -255 = -ff (hex)\n"

// smokeTests are testdata tests copied into the project (all exist since
// v0.2.0).
var smokeTests = []string{
	"pass/pass_std_strconv.fc", "pass/pass_std_strconv.out",
	"pass/pass_extra_format.fc", "pass/pass_extra_format.out",
	"fail/fail_conn_leak.fc",
}

func smoke(archive string) error {
	archive, err := filepath.Abs(archive)
	if err != nil {
		return err
	}
	if _, err := os.Stat(archive); err != nil {
		return fmt.Errorf("%v (run make dist first)", err)
	}
	srcAbs, err := filepath.Abs(*src)
	if err != nil {
		return err
	}
	// Resolve the real go binary here: version-manager shims (mise) may not
	// work outside the project directory. Users need go on PATH; so does
	// the test, after the archive's bin/.
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return fmt.Errorf("go env GOROOT: %v", err)
	}
	gobin := filepath.Join(strings.TrimSpace(string(goroot)), "bin")

	tmp, err := os.MkdirTemp("", "fincore-dist-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	inst := filepath.Join(tmp, "install")
	proj := filepath.Join(tmp, "project")
	for _, d := range []string{inst, proj, filepath.Join(proj, "tests", "pass"), filepath.Join(proj, "tests", "fail"), filepath.Join(tmp, "links")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	step := func(what string, f func() error) error {
		fmt.Printf("==> %s\n", what)
		if err := f(); err != nil {
			return fmt.Errorf("%s: %v", what, err)
		}
		return nil
	}
	if err := step("unpack "+filepath.Base(archive), func() error {
		return run(tmp, nil, "tar", "-xzf", archive, "-C", inst)
	}); err != nil {
		return err
	}
	bin := filepath.Join(inst, "fincore", "bin")
	env := cleanEnv(bin + string(os.PathListSeparator) + gobin + string(os.PathListSeparator) + os.Getenv("PATH"))

	if err := os.WriteFile(filepath.Join(proj, "hello.fc"), []byte(helloSrc), 0o644); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(srcAbs, "examples", "tcp_echo.fc"), filepath.Join(proj, "tcp_echo.fc")); err != nil {
		return err
	}
	for _, t := range smokeTests {
		if err := copyFile(filepath.Join(srcAbs, "testdata", t), filepath.Join(proj, "tests", t)); err != nil {
			return err
		}
	}
	steps := []struct {
		what string
		f    func() error
	}{
		{"finc help (bin/ on PATH)", func() error { return run(proj, env, "finc", "help") }},
		{"finc build hello.fc (std/strconv, formatTo) and run it", func() error {
			if err := run(proj, env, "finc", "build", "-o", "hello", "hello.fc"); err != nil {
				return err
			}
			return expect(proj, env, helloWant, filepath.Join(proj, "hello"))
		}},
		{"finc run hello.fc", func() error { return expect(proj, env, helloWant, "finc", "run", "hello.fc") }},
		{"finc run hello.fc through a symlink to bin/finc", func() error {
			link := filepath.Join(tmp, "links", "finc")
			if err := os.Symlink(filepath.Join(bin, "finc"), link); err != nil {
				return err
			}
			return expect(proj, env, helloWant, link, "run", "hello.fc")
		}},
		{"finc build tcp_echo.fc", func() error { return run(proj, env, "finc", "build", "-o", "tcp_echo", "tcp_echo.fc") }},
		{"finc test tests (copies of testdata)", func() error { return run(proj, env, "finc", "test", "tests") }},
	}
	for _, s := range steps {
		if err := step(s.what, s.f); err != nil {
			return err
		}
	}
	fmt.Printf("\nDIST TEST OK: %s\n", archive)
	return nil
}

// cleanEnv is the environment without the FinCore variables, with PATH set.
func cleanEnv(path string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "FINCROOT", "FINCORE_ROOT", "FINC_GO", "FINC_GOFMT", "PATH", "PWD", "GOFLAGS":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PATH="+path)
}

// command runs name in dir. With env (the test environment), a bare name
// is looked up in env's PATH and keeps argv[0] as typed, like a shell.
func command(dir string, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	if env != nil {
		if !strings.ContainsRune(name, filepath.Separator) {
			if p := lookPath(env, name); p != "" {
				cmd = exec.Command(p, args...)
				cmd.Args[0] = name
			}
		}
		cmd.Env = append(append([]string{}, env...), "PWD="+dir)
	}
	cmd.Dir = dir
	return cmd
}

func lookPath(env []string, name string) string {
	for _, kv := range env {
		if path, ok := strings.CutPrefix(kv, "PATH="); ok {
			for _, d := range filepath.SplitList(path) {
				p := filepath.Join(d, name)
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
					return p
				}
			}
		}
	}
	return ""
}

func run(dir string, env []string, name string, args ...string) error {
	cmd := command(dir, env, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func expect(dir string, env []string, want string, name string, args ...string) error {
	cmd := command(dir, env, name, args...)
	var o bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	fmt.Print(o.String())
	if o.String() != want {
		return fmt.Errorf("output %q, want %q", o.String(), want)
	}
	return nil
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "DIST FAILED:", msg)
	os.Exit(1)
}
