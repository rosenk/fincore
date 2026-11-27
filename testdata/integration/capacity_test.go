//go:build unix

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCapacityExceeded: inputs that overflow one fixed-capacity arena of
// bin/finc1 (compiler/README.md, "Capacities and memory") make `finc check`
// fail with exit status 1 and `finc: <path>: compiler capacity exceeded
// (<arena>)` on stderr, never a crash or a wrong result. The generated
// inputs exceed the arena by a small margin, so lowering a capacity keeps
// them failing and raising one past them is noticed here.
func TestCapacityExceeded(t *testing.T) {
	root := moduleRoot(t)
	// The compiler under test: $FINC0 (the bootstrap passes finc2 there)
	// when it is the self-hosted compiler, else bin/finc1. finc0 has its
	// own (Go) capacities and is not tested here.
	bin := os.Getenv("FINC0")
	if bin != "" {
		out, err := exec.Command(bin, "version").Output()
		if err != nil || !strings.HasPrefix(string(out), "finc language") {
			t.Skipf("$FINC0 (%s) is not the self-hosted compiler", bin)
		}
	} else {
		bin = filepath.Join(root, "bin", "finc1")
		if !fileExists(bin) {
			t.Skip("bin/finc1 not built (make finc1)")
		}
	}
	const mainFn = "\ndriver func main(io Stdio) error {\n\treturn nil\n}\n"
	repeat := func(n int, f func(i int) string) string {
		var b strings.Builder
		for i := range n {
			b.WriteString(f(i))
		}
		return b.String()
	}
	cases := []struct {
		name  string
		arena string
		files map[string]string // a single file "x.fc" is checked as a file, more as a package directory
	}{
		{
			// MaxTokens = 262_144 tokens per file.
			name: "tokens", arena: "tokens",
			files: map[string]string{"x.fc": "package main\n\nvar t = [...]u8{" +
				repeat(140_000, func(int) string { return "1," }) + "}\n" + mainFn},
		},
		{
			// MaxOperands = 16_384 operands of nested calls.
			name: "operands", arena: "operand stack",
			files: map[string]string{"x.fc": "package main\n\nfunc f(x u32) u32 {\n\treturn x\n}\n\nfunc g() u32 {\n\treturn f(" +
				repeat(17_000, func(int) string { return "0, " }) + "0)\n}\n" + mainFn},
		},
		{
			// MaxScopes = 131_072 scopes in the program.
			name: "scopes", arena: "scopes",
			files: map[string]string{
				"a.fc": "package main\n\nfunc a() {\n" + repeat(45_000, func(int) string { return "\t{\n\t}\n" }) + "}\n" + mainFn,
				"b.fc": "package main\n\nfunc b() {\n" + repeat(45_000, func(int) string { return "\t{\n\t}\n" }) + "}\n",
				"c.fc": "package main\n\nfunc c() {\n" + repeat(45_000, func(int) string { return "\t{\n\t}\n" }) + "}\n",
			},
		},
		{
			// MaxTypes = 32_768 distinct types.
			name: "types", arena: "types",
			files: map[string]string{
				"a.fc": "package main\n\n" + repeat(17_000, func(i int) string { return fmt.Sprintf("var a%d [%d]u8\n", i, i+1) }) + mainFn,
				"b.fc": "package main\n\n" + repeat(17_000, func(i int) string { return fmt.Sprintf("var b%d [%d]u16\n", i, i+1) }),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, src := range c.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			target := dir
			if len(c.files) == 1 {
				target = filepath.Join(dir, "x.fc")
			}
			stdout, stderr, code := runIn(t, root, bin, "check", target)
			want := fmt.Sprintf("finc: %s: compiler capacity exceeded (%s)\n", target, c.arena)
			if code != 1 || stdout != "" || !strings.HasPrefix(stderr, want) {
				t.Errorf("%s check: exit %d, stdout %q, stderr:\n%.500s\nwant exit 1 and stderr starting with %q", filepath.Base(bin), code, stdout, stderr, want)
			}
		})
	}
}
