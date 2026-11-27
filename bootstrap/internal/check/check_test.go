package check

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/types"
	"fincore/scripts/langver"
)

// moduleRoot changes into the module root (local imports resolve against
// the working directory, decision 15).
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return root
}

func passInputs(t *testing.T) []string {
	ins := []string{"examples/tcp_echo.fc"}
	ents, err := os.ReadDir("testdata/pass")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if (e.IsDir() || strings.HasSuffix(e.Name(), ".fc")) && finc0Can(t, ".", "testdata/pass/"+e.Name()) {
			ins = append(ins, "testdata/pass/"+e.Name())
		}
	}
	if _, err := os.Stat("compiler"); err == nil {
		ins = append(ins, "compiler")
	}
	return ins
}

// The fail tests owned by the scope/totality checkers are otherwise
// valid programs: the type checker must accept them.
var laterPassFail = map[string]bool{
	"fail_conn_leak": true, "fail_driver_in_total": true, "fail_mutual_recursion_unbounded": true,
	"fail_scope_escape_return": true, "fail_scope_escape_var": true, "fail_slot_leak": true,
	"fail_total_infinite_for": true, "fail_total_missing_zero_check": true, "fail_use_after_move": true,
	"fail_total_format_recursion": true, "fail_total_format_mutual": true,
}

func TestPassCorpusTypechecks(t *testing.T) {
	moduleRoot(t)
	ins := passInputs(t)
	fails, _ := filepath.Glob("testdata/fail/*.fc")
	for _, f := range fails {
		if laterPassFail[strings.TrimSuffix(filepath.Base(f), ".fc")] {
			ins = append(ins, f)
		}
	}
	for _, in := range ins {
		t.Run(in, func(t *testing.T) {
			res, err := Run(in, Options{})
			if err != nil {
				t.Fatal(err)
			}
			// Fail tests of the later passes may only report their
			// marked diagnostics (from those passes, never from types).
			diags := res.Diags
			if strings.HasPrefix(in, "testdata/fail/") {
				diags = unmarked(t, in, diags)
			}
			if len(diags) > 0 || res.Info == nil {
				t.Fatalf("unexpected diagnostics:\n%v", diags)
			}
			assertComplete(t, res.Info)
		})
	}
}

// assertComplete checks that every expression of every package has a
// recorded type (or is a call without results / a name position), and
// every call a Call record.
func assertComplete(t *testing.T, info *types.Info) {
	t.Helper()
	skip := map[ast.Node]bool{}
	for _, pkg := range info.Prog.Packages {
		for _, f := range pkg.AST.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					skip[n.Sel] = true
					if info.Prog.Uses[n.Sel.ID()] != nil {
						skip[n.X] = true // package name
					}
				case *ast.CallExpr:
					if info.CallOf(n) == nil || info.CallOf(n).Kind == types.CallInvalid {
						t.Errorf("%s: call without Call record", n.Range)
					}
					if info.CallOf(n) != nil && info.KindOf(n) == types.KindNoValue {
						skip[n] = true
					}
				case *ast.FieldInit:
					skip[n.Name] = true
				case *ast.NamedType:
					if n.Pkg != nil {
						skip[n.Pkg] = true // package name of pkg.T
					}
				case *ast.CtorPattern:
					skip[n.Name] = true
					if n.Qual != nil {
						skip[n.Qual] = true
					}
				case *ast.File:
					skip[n.Package] = true
				case *ast.Receiver:
					skip[n.Name] = true
					skip[n.TypeName] = true
					for _, id := range n.TypeParams {
						skip[id] = true
					}
				case *ast.TypeParam:
					for _, id := range n.Names {
						skip[id] = true
					}
				case *ast.ScopeStmt:
					skip[n.Name] = true
				case *ast.FuncType:
					// parameter names of a function type declare nothing
					for _, p := range n.Sig.Params {
						for _, id := range p.Names {
							skip[id] = true
						}
					}
				case *ast.TypeExpr:
					// a type argument such as `[]byte` in `Sort[[]byte]`:
					// the type is recorded on the wrapped type node
					skip[n] = true
				case *ast.ImportSpec:
					skip[n.Path] = true
					if n.Name != nil {
						skip[n.Name] = true
					}
				case *ast.ConstSpec:
					skip[n.Name] = true
				case *ast.TypeDecl:
					skip[n.Name] = true
				case *ast.FuncDecl:
					skip[n.Name] = true
				case *ast.Variant:
					skip[n.Name] = true
				}
				if e, ok := n.(ast.Expr); ok && !skip[n] && info.KindOf(e) != types.KindBuiltin {
					if id, ok := e.(*ast.Ident); ok && id.Name == "_" {
						return true
					}
					if typ := info.TypeOf(e); typ == nil || types.IsInvalid(typ) || types.IsUntyped(typ) && typ.String() != "untyped nil" {
						t.Errorf("%s: expression %T has type %v", e.Span(), e, typ)
					}
				}
				return true
			})
		}
	}
}

var markerRE = regexp.MustCompile(`//\s*ERROR\s+(E\d{4})`)

func TestM1FailCorpus(t *testing.T) {
	moduleRoot(t)
	cases := map[string]Options{
		"fail_type_in_type":  {},
		"fail_comptime_loop": {Fuel: 1000},
		"fail_global_conn":   {},
		"fail_nil_conn":      {},
		// v0.2 formatTo
		"fail_format_arity":      {},
		"fail_format_verb":       {},
		"fail_format_argtype":    {},
		"fail_format_nonvalue":   {},
		"fail_format_nonconst":   {},
		"fail_format_method_sig": {},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := "testdata/fail/" + name + ".fc"
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for i, line := range strings.Split(string(src), "\n") {
				if m := markerRE.FindStringSubmatch(line); m != nil {
					want = append(want, m[1]+"@"+itoa(i+1))
				}
			}
			res, err := Run(path, cases[name])
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range res.Diags {
				got = append(got, d.Code.String()+"@"+itoa(d.Span.Start.Line))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("got %v, want %v\n%v", got, want, res.Diags)
			}
		})
	}
}

func itoa(n int) string {
	s := ""
	for {
		s = string(rune('0'+n%10)) + s
		n /= 10
		if n == 0 {
			return s
		}
	}
}

// unmarked returns the diagnostics without a matching `// ERROR E####`
// marker (same line and code) in the file at path.
func unmarked(t *testing.T, path string, diags diag.List) diag.List {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for i, line := range strings.Split(string(src), "\n") {
		if m := markerRE.FindStringSubmatch(line); m != nil {
			want[m[1]+"@"+itoa(i+1)] = true
		}
	}
	var out diag.List
	for _, d := range diags {
		if !want[d.Code.String()+"@"+itoa(d.Span.Start.Line)] {
			out = append(out, d)
		}
	}
	return out
}

// finc0Can reports whether the input in (relative to the module root
// root) needs at most the language version finc0 implements (v0.2, frozen):
// tests of newer versions carry `// SINCE: vX.Y` and are skipped here
// (testdata/README.md).
func finc0Can(t *testing.T, root, in string) bool {
	t.Helper()
	v, ok, err := langver.RequiredIn(root, os.Getenv("FINCROOT"), filepath.Join(root, in))
	if err != nil {
		t.Fatal(err)
	}
	return !ok || !langver.MustParse(langver.Finc0).Less(v)
}
