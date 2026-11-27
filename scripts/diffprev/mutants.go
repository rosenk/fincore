package main

// The mutators of scripts/diffcheck (diff-check-fuzz, diff-check-totality),
// copied so that diffprev does not depend on finc0's differential scripts.

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mutation is a textual edit of one file of a package directory.
type mutation struct{ file, old, new string }

// mutantCases writes n token-level mutants of every single-file corpus
// source, package file and snippet; every other mutant uses
// mutateTotality. Mutants are checked with --passes=types and all.
func mutantCases(tmp string, n int, seed int64) []tcase {
	rng := rand.New(rand.NewSource(seed))
	var srcs []string
	for _, pat := range []string{"examples/*.fc", "testdata/pass/*.fc", "testdata/fail/*.fc",
		"testdata/pass/*/*.fc", "testdata/pass/*/*/*.fc", "scripts/diffcheck/cases/*.fc",
		"scripts/diffbuild/cases/*.fc", "std/*/*.fc", "compiler/*.fc"} {
		files, _ := filepath.Glob(pat)
		sort.Strings(files)
		srcs = append(srcs, files...)
	}
	for _, pat := range []string{"s*", "t*"} {
		snips, _ := filepath.Glob(filepath.Join(tmp, pat, "t.fc"))
		sort.Strings(snips)
		srcs = append(srcs, snips...)
	}
	var out []tcase
	k := 0
	for _, f := range srcs {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		name := f
		if rel, err := filepath.Rel(tmp, f); err == nil && !strings.HasPrefix(rel, "..") {
			name = "snippet " + filepath.Dir(rel)
		}
		// A mutant needs the language version of its source: mutants of
		// `// SINCE: v0.3` sources are rejected by the previous release
		// for that reason, not because of the mutation.
		since := required(f)
		reps := n
		if strings.HasPrefix(f, "compiler/") {
			reps = max(1, n/4)
		}
		for i := 0; i < reps; i++ {
			m := ""
			if i%2 == 0 {
				m = mutateTotality(string(src), rng)
			} else {
				m = mutate(string(src), rng)
			}
			k++
			root := filepath.Join(tmp, fmt.Sprintf("x%05d", k))
			_ = os.MkdirAll(root, 0o755)
			_ = os.WriteFile(filepath.Join(root, "t.fc"), []byte(m), 0o644)
			out = append(out, tcase{name: fmt.Sprintf("mutant %d of %s (seed %d)", i, name, seed), dir: root, path: "t.fc",
				flags: []string{"--comptime-fuel=5000"}, passes: []string{"types", "all"}, mutant: true, since: since})
		}
	}
	return out
}

var mutTypes = []string{"u8", "u16", "u32", "u64", "i8", "i32", "i64", "int", "usize", "bool", "string", "error",
	"Conn", "Context", "Duration", "Code 0", "Code 1", "*u8", "[]u8", "[4]u8", "Scope", "Listener"}
var mutOps = []string{"+", "-", "*", "/", "%", "<<", ">>", "&", "|", "^", "==", "!=", "<", "<=", ">", ">=", "&&", "||"}
var mutLits = []string{"0", "1", "-1", "255", "256", "300", "65536", "1 << 63", "1 << 64", "18446744073709551615",
	"'a'", "\"s\"", "true", "false", "nil", "1 / 0", "-128", "(1 << 100)", "1 << 120 - 1", "-(1 << 64)"}

func mutate(src string, rng *rand.Rand) string {
	lines := strings.Split(src, "\n")
	steps := 1 + rng.Intn(3)
	for s := 0; s < steps; s++ {
		if len(lines) < 3 {
			break
		}
		li := rng.Intn(len(lines))
		line := lines[li]
		switch rng.Intn(9) {
		case 0: // delete a line
			lines = append(lines[:li], lines[li+1:]...)
		case 1: // duplicate a line
			lines = append(lines[:li+1], lines[li:]...)
		case 2, 3: // swap an identifier with another identifier of the file
			ids := identsOf(src)
			w := wordsOf(line)
			if len(ids) == 0 || len(w) == 0 {
				continue
			}
			lines[li] = replaceNth(line, w[rng.Intn(len(w))], ids[rng.Intn(len(ids))])
		case 4: // replace a type-ish word
			w := wordsOf(line)
			if len(w) == 0 {
				continue
			}
			lines[li] = replaceNth(line, w[rng.Intn(len(w))], mutTypes[rng.Intn(len(mutTypes))])
		case 5: // replace a number
			for _, w := range wordsOf(line) {
				if w[0] >= '0' && w[0] <= '9' {
					lines[li] = replaceNth(line, w, mutLits[rng.Intn(len(mutLits))])
					break
				}
			}
		case 6: // swap an operator
			for _, op := range mutOps {
				if strings.Contains(line, " "+op+" ") {
					lines[li] = strings.Replace(line, " "+op+" ", " "+mutOps[rng.Intn(len(mutOps))]+" ", 1)
					break
				}
			}
		case 7: // replace an identifier by a literal
			w := wordsOf(line)
			if len(w) == 0 {
				continue
			}
			lines[li] = replaceNth(line, w[rng.Intn(len(w))], mutLits[rng.Intn(len(mutLits))])
		case 8: // move a line elsewhere
			lj := rng.Intn(len(lines))
			lines[li], lines[lj] = lines[lj], lines[li]
		}
	}
	return strings.Join(lines, "\n")
}

func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func wordsOf(s string) []string {
	if i := strings.Index(s, "//"); i >= 0 {
		s = s[:i]
	}
	var out []string
	for i := 0; i < len(s); {
		if !isWordByte(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isWordByte(s[j]) {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

func identsOf(src string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range wordsOf(src) {
		if (w[0] < '0' || w[0] > '9') && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// replaceNth replaces the first whole-word occurrence of old in line.
func replaceNth(line, old, repl string) string {
	for i := 0; i+len(old) <= len(line); i++ {
		if line[i:i+len(old)] != old {
			continue
		}
		if i > 0 && isWordByte(line[i-1]) || i+len(old) < len(line) && isWordByte(line[i+len(old)]) {
			continue
		}
		return line[:i] + repl + line[i+len(old):]
	}
	return line
}

// compilerMutations are textual edits of compiler/ that break one
// recursion measure, loop or decreases clause each.
var compilerMutations = []mutation{
	{"compiler/types.fc", "depth-1", "depth"},
	{"compiler/parser.fc", "p.constSpec(depth - 1)", "p.constSpec(depth)"},
	{"compiler/parser.fc", "depth-1", "depth"},
	{"compiler/checker.fc", "writeExpr(o, l1at(n, i), dp-1)", "writeExpr(o, l1at(n, i), dp+1)"},
	{"compiler/check_expr.fc", "dp-1", "dp / 2"},
	{"compiler/check_stmt.fc", "dp-1", "dp>>1"},
	{"compiler/totality_flow.fc", "twStmt(l1at(s, i), st, dp-1)", "twStmt(l1at(s, i), st, dp)"},
	{"compiler/totality_flow.fc", "func eb(e0 u32, st u32, dp usize) Bnds decreases(dp) {", "func eb(e0 u32, st u32, dp usize) Bnds {"},
	{"compiler/totality_rec.fc", "func measureParam(fi u32)", "func measureParamX(fi u32, d usize) decreases(d) {\n\tif d == 0 {\n\t\treturn\n\t}\n\tmeasureParamX(fi, d)\n}\n\nfunc measureParam(fi u32)"},
	{"compiler/lexer.fc", "i < n; i++", "i <= n; i++"},
	{"compiler/sema.fc", "for i := usize(0); i < n; i++ {", "for i := usize(0); i < n; i += 2 {"},
	{"compiler/sema.fc", "for i := usize(0); i < n; i++ {", "for i := usize(0); i < n; i-- {"},
	{"compiler/build.fc", "i < n; i++ {", "i < n; i++ {\n\t\tn++"},
	{"compiler/totality.fc", "for p := s; p < e; p++ {", "for p := s; p < e; p++ {\n\t\te = e + 0"},
	{"compiler/values.fc", "decreases(depth)", ""},
	{"compiler/checker.fc", "func tooDeep() {", "func tooDeep() {\n\tfor {\n\t\tbreak\n\t}"},
	{"compiler/comptime.fc", "dp-1", "dp-0"},
	{"compiler/resolve.fc", "bindType(nb(s), depth-1)", "bindType(nb(s), depth/1)"},
	{"compiler/check_stmt.fc", "if dp == 0 {\n\t\ttooDeep()\n\t\treturn\n\t}", ""},
	{"compiler/types.fc", "if depth == 0 {", "if depth == 1 {"},
	{"compiler/main.fc", "driver func checkUsage(io Stdio) error {", "func spin(n usize) usize {\n\tfor n > 0 {\n\t\tn--\n\t}\n\treturn n\n}\n\ndriver func checkUsage(io Stdio) error {"},
	{"compiler/main.fc", "func strEq(a string, b string) bool {", "func shrink(b []byte) {\n\tfor len(b) > 0 {\n\t\tb = b[1:]\n\t}\n\trem := b\n\tfor len(rem) > 0 {\n\t\tn := len(rem) - 1\n\t\trem = rem[n:]\n\t}\n}\n\nfunc strEq(a string, b string) bool {"},
}

// stdMutations are textual edits of std/ that break one loop, measure or
// type each (totality and type errors in generic library code).
var stdMutations = []mutation{
	{"std/vec/vec.fc", "for i := n; i < top; i++ {", "for i := n; i < top; i += 0 {"},
	{"std/maps/maps.fc", "for g := usize(0); g < NB; g++ {", "for g := usize(0); g <= NB; g++ {"},
	{"std/maps/maps.fc", "for i := usize(0); i < NB; i++ {", "for i := usize(0); i < NB+t.n; i++ {\n\t\tt.n = 0"},
	{"std/sort/sort.fc", "for lo < hi decreases(hi - lo) {", "for lo < hi {"},
	{"std/sort/sort.fc", "hi = mid", "hi = mid + 1"},
	{"std/strconv/strconv.fc", "for g := i; g < n; g++ {", "for g := i; g < n; g-- {"},
	{"std/path/path.fc", "if m == 0 || p[m-1] != '/' {", "if m == 0 || p[m-1] != '/' || g > m {\n\t\t\tn++"},
	{"std/bytes/bytes.fc", "if a[i] < b[i] {", "if a[i] < b {"},
	{"std/vec/vec.fc", "var z T", "var z N"},
}

// mutateTotality applies 1-3 edits aimed at the totality checker: loop
// conditions and steps, measure arithmetic, guards, decreases clauses and
// extra writes inside loop bodies.
func mutateTotality(src string, rng *rand.Rand) string {
	lines := strings.Split(src, "\n")
	edits := [][2]string{
		{" < ", " <= "}, {" <= ", " < "}, {" > ", " >= "}, {" >= ", " > "}, {" == ", " != "}, {" != ", " == "},
		{" < ", " > "}, {" > ", " < "}, {"++", "--"}, {"--", "++"}, {"++", " += 2"}, {"++", " += 1"},
		{" += 1", " += 3"}, {" -= 1", " -= 2"}, {"-1", "-0"}, {" - 1", " - 2"}, {" - 1", " / 2"}, {" - 1", " >> 1"},
		{"-1", "+1"}, {"- 1", "+ 1"}, {"[1:]", "[0:]"}, {"[n:]", "[1:]"}, {"[k:]", "[k-1:]"}, {"len(", "cap("},
		{"decreases(", "decreases(1 + "}, {" + 1", " + 2"}, {" / 2", " / 1"}, {") / 2", ") >> 1"},
		{"hi - lo", "lo - hi"}, {"usize(0)", "usize(1)"}, {"u32(0)", "u32(1)"}, {"if ", "if !"},
		{"return", "_ = 0 //"}, {"break", "_ = 0 //"}, {"continue", "_ = 0 //"}, {"&&", "||"}, {"||", "&&"},
	}
	steps := 1 + rng.Intn(3)
	for s := 0; s < steps; s++ {
		if len(lines) < 3 {
			break
		}
		li := rng.Intn(len(lines))
		line := lines[li]
		switch rng.Intn(6) {
		case 0, 1, 2: // textual edit
			e := edits[rng.Intn(len(edits))]
			if strings.Contains(line, e[0]) {
				lines[li] = strings.Replace(line, e[0], e[1], 1)
			}
		case 3: // remove a decreases clause
			if i := strings.Index(line, "decreases("); i >= 0 {
				if j := strings.Index(line[i:], ")"); j >= 0 {
					lines[li] = line[:i] + line[i+j+1:]
				}
			}
		case 4: // write a variable of the line inside the following block
			w := wordsOf(line)
			if strings.HasSuffix(strings.TrimSpace(line), "{") && len(w) > 0 {
				v := w[rng.Intn(len(w))]
				ops := []string{v + "++", v + "--", v + " = " + v, v + " += 1", v + " = " + v + "[1:]", "_ = &" + v}
				lines = append(lines[:li+1], append([]string{"\t" + ops[rng.Intn(len(ops))]}, lines[li+1:]...)...)
			}
		case 5: // swap an identifier with another identifier of the file
			ids := identsOf(src)
			w := wordsOf(line)
			if len(ids) == 0 || len(w) == 0 {
				continue
			}
			lines[li] = replaceNth(line, w[rng.Intn(len(w))], ids[rng.Intn(len(ids))])
		}
	}
	return strings.Join(lines, "\n")
}
