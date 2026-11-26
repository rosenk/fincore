//go:build ignore

// bootstrap_verify runs the FinCore fixed-point bootstrap gate (spec §13.4):
//
//  1. go build finc0 (Stage 0, Go)
//  2. finc0 build ./compiler → finc1  (stage1.go, must pass go vet)
//  3. finc1 build ./compiler → finc2  (stage2.go)
//  4. finc2 build ./compiler → finc3  (stage3.go)
//  5. sha256(stage2.go) == sha256(stage3.go)
//  6. finc1 and finc2 emit identical Go for examples/tcp_echo.fc
//  7. finc2 passes testdata (pass + fail) and the integration suite
//
// Usage (from the module root): go run ./scripts/bootstrap_verify.go [-skip-integration]
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var goBin = "go"

var (
	skipIntegration = flag.Bool("skip-integration", false, "skip step 7's TCP echo integration tests")
	outDir          = flag.String("out", ".stage", "directory for stage*.go and binaries")
)

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

	step("1. build finc0", func() error {
		return run(nil, goBin, "build", "-trimpath", "-o", p("finc0"), "./bootstrap/cmd/finc0")
	})
	stage := func(n int, compiler string) {
		step(fmt.Sprintf("%d. %s build ./compiler → finc%d", n+1, compiler, n), func() error {
			return run(nil, p(compiler), "build", "--trimpath",
				"--emit-go="+p(fmt.Sprintf("stage%d.go", n)), "-o", p(fmt.Sprintf("finc%d", n)), "./compiler")
		})
	}
	stage(1, "finc0")
	step("   go vet stage1.go", func() error { return vetGo(p("stage1.go")) })
	stage(2, "finc1")
	stage(3, "finc2")

	step("5. fixed point: sha256(stage2.go) == sha256(stage3.go)", func() error {
		h2, h3 := sha(p("stage2.go")), sha(p("stage3.go"))
		fmt.Printf("   stage2.go %s\n   stage3.go %s\n", h2, h3)
		if h2 != h3 {
			return fmt.Errorf("stage2.go and stage3.go differ")
		}
		return nil
	})
	step("6. finc1 and finc2 emit identical Go for examples/tcp_echo.fc", func() error {
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
	step("7. finc2 test testdata", func() error { return run(nil, p("finc2"), "test", "testdata") })
	if !*skipIntegration {
		step("7. integration suite with finc2", func() error {
			return run([]string{"FINC0=" + abs("finc2")}, goBin, "test", "-count=1", "./testdata/integration/")
		})
	}
	fmt.Println("\nBOOTSTRAP OK: fixed point reached (stage2.go == stage3.go)")
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
