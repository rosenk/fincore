//go:build ignore

// bootstrap_verify runs the FinCore fixed-point bootstrap gate (spec §13.4).
//
// finc0 is frozen at language v0.2 (bootstrap/README.md). It builds the
// self-hosted compiler only at the seed release named in bootstrap/SEED;
// that seed compiler then builds the current compiler/:
//
//  1. go build finc0 (Stage 0, Go, frozen)                → .stage/finc0
//  2. finc0 build compiler/ at the seed tag → finc-seed   (seed.go; seed tree
//     from `git archive <seed>` in .stage/seed/<seed>, built against the
//     seed's own runtime/; if bootstrap/SEED lists several tags, oldest
//     first, each built compiler builds the next tag and the last is the
//     seed)
//  3. finc-seed build ./compiler → finc1  (stage1.go, must pass go vet)
//  4. finc1 build ./compiler → finc2      (stage2.go)
//  5. finc2 build ./compiler → finc3      (stage3.go)
//  6. sha256(stage2.go) == sha256(stage3.go)
//  7. finc1 and finc2 emit identical Go for examples/tcp_echo.fc
//  8. finc2 passes testdata (pass + fail) and the integration suite
//
// Steps 3-8 run in the current tree against the current runtime/.
//
// Usage (from the module root):
//
//	go run ./scripts/bootstrap_verify.go [-skip-integration] [-seed-only] [-out .stage]
//
// -seed-only stops after step 2 (`make seed`: .stage/finc-seed) and keeps an
// existing finc-seed built from the same seed commit by the same finc0.
package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var goBin = "go"

var (
	skipIntegration = flag.Bool("skip-integration", false, "skip step 8's TCP echo integration tests")
	seedOnly        = flag.Bool("seed-only", false, "stop after building <out>/finc-seed")
	outDir          = flag.String("out", ".stage", "directory for stage*.go and binaries")
)

// seedPaths are the parts of the seed tree finc0 needs to build compiler/.
var seedPaths = []string{"go.mod", "compiler", "std", "runtime"}

func main() {
	flag.Parse()
	if _, err := os.Stat("go.mod"); err != nil {
		fatal("run from the module root (go.mod not found)")
	}
	must(os.MkdirAll(*outDir, 0o755))
	// Resolve the real go binary: version-manager shims (e.g. mise) may not
	// work outside the project directory, where vetGo runs.
	if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
		goBin = filepath.Join(string(bytes.TrimSpace(out)), "bin", "go")
	}
	p := func(name string) string { return filepath.Join(*outDir, name) }
	abs := func(name string) string { a, _ := filepath.Abs(p(name)); return a }

	tags := readSeeds()
	seed := tags[len(tags)-1]
	step("1. build finc0 (frozen at v0.2)", func() error {
		return run(nil, goBin, "build", "-trimpath", "-o", p("finc0"), "./bootstrap/cmd/finc0")
	})
	step(fmt.Sprintf("2. finc0 build compiler/@%s → finc-seed", strings.Join(tags, " → @")), func() error {
		return buildSeed(tags, abs("finc0"), abs("seed"), abs(""), abs("finc-seed"), *seedOnly)
	})
	if *seedOnly {
		fmt.Printf("\nSEED OK: %s (compiler/ at %s, bootstrapped from finc0)\n", p("finc-seed"), seed)
		return
	}
	stage := func(n int, compiler string) {
		step(fmt.Sprintf("%d. %s build ./compiler → finc%d", n+2, compiler, n), func() error {
			return run(nil, p(compiler), "build", "--trimpath",
				"--emit-go="+p(fmt.Sprintf("stage%d.go", n)), "-o", p(fmt.Sprintf("finc%d", n)), "./compiler")
		})
	}
	stage(1, "finc-seed")
	step("   go vet stage1.go", func() error { return vetGo(p("stage1.go")) })
	stage(2, "finc1")
	stage(3, "finc2")

	step("6. fixed point: sha256(stage2.go) == sha256(stage3.go)", func() error {
		h2, h3 := sha(p("stage2.go")), sha(p("stage3.go"))
		fmt.Printf("   stage2.go %s\n   stage3.go %s\n", h2, h3)
		if h2 != h3 {
			return fmt.Errorf("stage2.go and stage3.go differ")
		}
		return nil
	})
	step("7. finc1 and finc2 emit identical Go for examples/tcp_echo.fc", func() error {
		for _, c := range []string{"finc1", "finc2"} {
			if err := run(nil, p(c), "build", "--emit-go="+p("echo_"+c+".go"), "-o", p("echo_"+c), "examples/tcp_echo.fc"); err != nil {
				return err
			}
		}
		if a, b := sha(p("echo_finc1.go")), sha(p("echo_finc2.go")); a != b {
			return fmt.Errorf("echo_finc1.go (%s) != echo_finc2.go (%s)", a, b)
		}
		return nil
	})
	step("8. finc2 test testdata", func() error { return run(nil, p("finc2"), "test", "testdata") })
	if !*skipIntegration {
		step("8. integration suite with finc2", func() error {
			return run([]string{"FINC0=" + abs("finc2")}, goBin, "test", "-count=1", "./testdata/integration/")
		})
	}
	fmt.Printf("\nBOOTSTRAP OK: finc0 → finc-seed (%s) → finc1 → finc2 → finc3; fixed point reached (stage2.go == stage3.go)\n", seed)
}

// readSeeds returns the tags listed in bootstrap/SEED, one per line. The
// last is the seed (finc-seed); earlier ones, if any, are hops of a
// multi-step chain finc0 → first → ... → seed, needed once a seed uses a
// feature that finc0 lacks (see bootstrap/README.md). Today it is one tag.
// `#` starts a comment.
func readSeeds() []string {
	b, err := os.ReadFile(filepath.Join("bootstrap", "SEED"))
	if err != nil {
		fatal("cannot read the seed tag: " + err.Error())
	}
	var tags []string
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		if t := strings.TrimSpace(line); t != "" {
			if strings.ContainsAny(t, " \t/\\") || strings.HasPrefix(t, "-") {
				fatal(fmt.Sprintf("bootstrap/SEED: invalid tag name %q", t))
			}
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		fatal("bootstrap/SEED is empty: it must name the seed tag (e.g. v0.2.0)")
	}
	return tags
}

// buildSeed builds the seed compiler out (finc-seed) from the tags of
// bootstrap/SEED without touching the working tree: for each tag, `git
// archive` extracts go.mod, compiler/, std/ and runtime/ into
// <seedDir>/<tag>, and the previous compiler (finc0 for the first tag)
// builds compiler/ there. Each tree is its own fincore module root, so its
// generated Go uses that tag's runtime. With reuse, an existing out built
// from the same commits by the same finc0 binary (out + ".stamp") is kept;
// the full bootstrap always rebuilds.
func buildSeed(tags []string, finc0, seedDir, outDir, out string, reuse bool) error {
	seed := tags[len(tags)-1]
	if _, err := exec.LookPath("git"); err != nil {
		return seedUnavailable(seed, "git is not installed")
	}
	if o, err := exec.Command("git", "rev-parse", "--git-dir").CombinedOutput(); err != nil {
		return seedUnavailable(seed, "not a git checkout: "+strings.TrimSpace(string(o)))
	}
	stamp := fmt.Sprintf("finc0 %s\n", sha(finc0))
	commits := make([]string, len(tags))
	for i, tag := range tags {
		o, err := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/tags/"+tag+"^{commit}").Output()
		if err != nil {
			return seedUnavailable(tag, "tag "+tag+" not found")
		}
		commits[i] = strings.TrimSpace(string(o))
		fmt.Printf("   seed %s = %s\n", tag, commits[i])
		stamp += fmt.Sprintf("seed %s %s\n", tag, commits[i])
	}
	if err := exec.Command("git", "diff", "--quiet", commits[len(tags)-1], "--", "compiler").Run(); err == nil {
		fmt.Println("   (compiler/ is identical to the seed's: the chain is equivalent to building it with finc0)")
	}
	if old, err := os.ReadFile(out + ".stamp"); reuse && err == nil && string(old) == stamp {
		if _, err := os.Stat(out); err == nil {
			fmt.Println("   up to date (same seed commits and finc0)")
			return nil
		}
	}
	_ = os.Remove(out + ".stamp")
	if err := os.RemoveAll(seedDir); err != nil {
		return err
	}
	compiler := finc0
	for i, tag := range tags {
		dir := filepath.Join(seedDir, tag)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := extract(commits[i], dir); err != nil {
			return fmt.Errorf("git archive %s: %v", tag, err)
		}
		bin, emit := out, filepath.Join(outDir, "seed.go")
		if i < len(tags)-1 {
			bin, emit = filepath.Join(outDir, "finc-"+tag), filepath.Join(outDir, "seed-"+tag+".go")
		}
		if len(tags) > 1 {
			fmt.Printf("   %s build compiler/@%s → %s\n", filepath.Base(compiler), tag, filepath.Base(bin))
		}
		// Run in the seed tree: std/ imports resolve against the working
		// directory and the runtime against $FINCORE_ROOT ($PWD for finc1).
		cmd := exec.Command(compiler, "build", "--trimpath", "--emit-go="+emit, "-o", bin, "./compiler")
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
		for _, kv := range os.Environ() {
			if k, _, _ := strings.Cut(kv, "="); k != "FINCROOT" && k != "FINCORE_ROOT" && k != "PWD" {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "FINCORE_ROOT="+dir, "PWD="+dir)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("building compiler/@%s: %v", tag, err)
		}
		compiler = bin
	}
	return os.WriteFile(out+".stamp", []byte(stamp), 0o644)
}

func seedUnavailable(seed, why string) error {
	return fmt.Errorf("%s.\nThe bootstrap builds compiler/ at the seed tag %s (bootstrap/SEED) with the frozen finc0,\n"+
		"so it needs a git checkout that has the tag. Run `git fetch --tags` (CI: actions/checkout with fetch-depth: 0)", why, seed)
}

// extract writes `git archive <commit> seedPaths...` into dir.
func extract(commit, dir string) error {
	cmd := exec.Command("git", append([]string{"archive", "--format=tar", commit}, seedPaths...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	tr := tar.NewReader(pipe)
	var xerr error
	for xerr == nil {
		var h *tar.Header
		h, xerr = tr.Next()
		if xerr != nil {
			break
		}
		name := filepath.FromSlash(h.Name)
		if !filepath.IsLocal(name) {
			xerr = fmt.Errorf("unsafe path %q in archive", h.Name)
			break
		}
		target := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			xerr = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			xerr = writeFile(target, tr, os.FileMode(h.Mode).Perm())
		}
	}
	if errors.Is(xerr, io.EOF) {
		xerr = nil
	}
	_, _ = io.Copy(io.Discard, pipe)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return xerr
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o200)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func step(name string, f func() error) {
	start := time.Now()
	fmt.Printf("==> %s\n", name)
	if err := f(); err != nil {
		fatal(fmt.Sprintf("%s: %v", name, err))
	}
	fmt.Printf("    ok (%s)\n", time.Since(start).Round(time.Millisecond))
}

func run(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	return cmd.Run()
}

// vetGo vets a generated single-file program in a throwaway module that
// points at this module's runtime.
func vetGo(file string) error {
	root, _ := os.Getwd()
	dir, err := os.MkdirTemp("", "fincore-vet-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	src, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	gomod := fmt.Sprintf("module fincvet\n\ngo 1.27\n\nrequire fincore v0.0.0\n\nreplace fincore => %s\n", root)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), src, 0o644); err != nil {
		return err
	}
	var out bytes.Buffer
	cmd := exec.Command(goBin, "vet", ".")
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &out
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v\n%s", err, out.String())
	}
	return nil
}

func sha(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal(err.Error())
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func must(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "BOOTSTRAP FAILED:", msg)
	os.Exit(1)
}
