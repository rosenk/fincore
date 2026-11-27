// Package langver reads the `// SINCE: vX.Y` directive of FinCore sources
// and computes the language version an input (a file or a package
// directory) requires.
//
// finc0 is frozen at language v0.2 (bootstrap/README.md). A source file
// that needs a newer language version declares it in its leading comment
// block (the `//` comment and blank lines before the first other line):
//
//	// SINCE: v0.3
//	// pass_foo: ...
//	package main
//
// The required version of an input is the highest SINCE of the files of
// its import closure (RequiredIn; testdata/README.md, "SINCE"). The same
// rule is implemented by `finc0 test` (bootstrap/cmd/finc0/test.go) and
// `finc test` of compiler/ (compiler/build.fc); keep the three identical.
//
// The finc0-vs-finc1 differential tools (scripts/diffcheck,
// scripts/diffbuild, and `make diff-parse` via scripts/sincefilter) skip
// every input that needs more than Finc0; scripts/diffprev and the Go
// corpus runner (testdata/integration) use Required too.
package langver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// space is the white space of the SINCE and import scans (ASCII, like
// std/bytes.IsSpace).
const space = " \t\n\v\f\r"

// Finc0 is the language version finc0 implements (frozen).
const Finc0 = "v0.2"

// Version is a language version vMAJOR.MINOR.
type Version struct{ Major, Minor int }

// Parse parses "vX.Y": surrounding white space is ignored; X and Y are 1
// to 4 decimal digits.
func Parse(s string) (Version, error) {
	t := strings.Trim(s, space)
	rest, ok := strings.CutPrefix(t, "v")
	maj, mnr, ok2 := strings.Cut(rest, ".")
	a, ok3 := digits(maj)
	b, ok4 := digits(mnr)
	if !ok || !ok2 || !ok3 || !ok4 {
		return Version{}, fmt.Errorf("invalid language version %q (want vX.Y)", t)
	}
	return Version{a, b}, nil
}

// digits parses 1 to 4 decimal digits.
func digits(s string) (int, bool) {
	if len(s) == 0 || len(s) > 4 {
		return 0, false
	}
	v := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		v = v*10 + int(s[i]-'0')
	}
	return v, true
}

// MustParse is Parse that panics on error.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

func (v Version) String() string { return fmt.Sprintf("v%d.%d", v.Major, v.Minor) }

// Less reports v < w.
func (v Version) Less(w Version) bool {
	return v.Major < w.Major || (v.Major == w.Major && v.Minor < w.Minor)
}

// Since returns the version declared by the first `// SINCE: vX.Y` line
// of the leading comment block of src (the lines, trimmed of white space,
// that are empty or start with `//`, up to the first other line); ok is
// false when there is none.
func Since(src []byte) (v Version, ok bool, err error) {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.Trim(line, space)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			break
		}
		if rest, found := strings.CutPrefix(line, "// SINCE:"); found {
			v, err := Parse(rest)
			return v, err == nil, err
		}
	}
	return Version{}, false, nil
}

// Imports returns the import paths of src, found by a textual scan
// (testdata/README.md, "SINCE"):
//
//   - comments (`//` to the end of the line, `/* ... */`, not nested) are
//     removed first, without regard to string literals; every line is then
//     trimmed of white space and empty lines are skipped;
//   - a line starting with the word `package` is skipped;
//   - a line that is exactly `import (` opens a block: every following
//     line up to one starting with `)` holds at most one spec;
//   - any other line starting with the word `import` holds one spec after
//     the keyword;
//   - any other line is the first top-level declaration and ends the scan;
//   - a spec is an optional name (one word: no white space, no quote)
//     followed by a quoted path; the path is the text up to the next quote
//     (no escapes). A line that is not a spec is ignored.
//
// A word ends at the end of the line, white space, `(` or `"`.
func Imports(src []byte) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(stripComments(string(src)), "\n") {
		t := strings.Trim(line, space)
		switch {
		case t == "":
		case inBlock:
			if strings.HasPrefix(t, ")") {
				inBlock = false
			} else if p, ok := importSpec(t); ok {
				out = append(out, p)
			}
		case isWord(t, "package"):
		case isWord(t, "import"):
			r := strings.Trim(t[len("import"):], space)
			if r == "(" {
				inBlock = true
			} else if p, ok := importSpec(r); ok {
				out = append(out, p)
			}
		default:
			return out
		}
	}
	return out
}

// isWord reports whether t starts with the keyword kw followed by the end,
// white space, `(` or `"`.
func isWord(t, kw string) bool {
	if !strings.HasPrefix(t, kw) {
		return false
	}
	if len(t) == len(kw) {
		return true
	}
	c := t[len(kw)]
	return strings.IndexByte(space, c) >= 0 || c == '(' || c == '"'
}

// importSpec parses `[name] "path"` (t is trimmed).
func importSpec(t string) (string, bool) {
	q := strings.IndexByte(t, '"')
	if q < 0 || strings.ContainsAny(strings.TrimRight(t[:q], space), space) {
		return "", false
	}
	end := strings.IndexByte(t[q+1:], '"')
	if end < 0 {
		return "", false
	}
	return t[q+1 : q+1+end], true
}

// stripComments replaces each comment by a space (a `/* */` comment keeps
// its newlines).
func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
			if i < len(s) {
				b.WriteByte('\n')
			}
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			b.WriteByte(' ')
			i += 2
			for i < len(s) && !(s[i] == '*' && i+1 < len(s) && s[i+1] == '/') {
				if s[i] == '\n' {
					b.WriteByte('\n')
				}
				i++
			}
			i++ // now at the '/' of "*/" (or past the end)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// ValidImportPath reports whether the compilers accept ip as an import
// path: non-empty, not starting with `/` or `..`, without `\`, and clean.
// Required ignores other paths (the compiler reports them).
func ValidImportPath(ip string) bool {
	return ip != "" && !strings.HasPrefix(ip, "/") && !strings.Contains(ip, "\\") &&
		!strings.HasPrefix(ip, "..") && ip == filepath.ToSlash(filepath.Clean(ip))
}

// ImportDir resolves a valid import path like the compilers (decisions
// 15, 41, 58): `std` and `std/...` against fincRoot when it is non-empty,
// everything else against moduleRoot.
func ImportDir(moduleRoot, fincRoot, ip string) string {
	if fincRoot != "" && (ip == "std" || strings.HasPrefix(ip, "std/")) {
		return filepath.Join(fincRoot, filepath.FromSlash(ip))
	}
	return filepath.Join(moduleRoot, filepath.FromSlash(ip))
}

// Required returns the language version path (a .fc file or a package
// directory) needs when compiled from the working directory (the module
// root) with the current $FINCROOT; ok is false when no file of its
// import closure declares one. See RequiredIn.
func Required(path string) (v Version, ok bool, err error) {
	return RequiredIn(".", os.Getenv("FINCROOT"), path)
}

// RequiredIn is Required for the module root moduleRoot (the working
// directory of the compiler) and the $FINCROOT value fincRoot; a relative
// fincRoot is relative to moduleRoot. path itself is used as given.
//
// The required version is the highest SINCE (Since) over
//   - the input's own package: the file, or every .fc file directly in the
//     directory;
//   - transitively, every package it imports: each valid import path
//     (ValidImportPath) of each file read (Imports), resolved with
//     ImportDir. Paths that do not name a directory are ignored; a package
//     already visited (keyed by its resolved directory) is not read again,
//     so import cycles terminate.
//
// A malformed directive in any file read is an error naming the file.
func RequiredIn(moduleRoot, fincRoot, path string) (v Version, ok bool, err error) {
	if fincRoot != "" && !filepath.IsAbs(fincRoot) {
		fincRoot = filepath.Join(moduleRoot, fincRoot)
	}
	visited := map[string]bool{}
	var queue []string
	read := func(file string) error {
		src, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		fv, fok, ferr := Since(src)
		if ferr != nil {
			return fmt.Errorf("%s: %v", file, ferr)
		}
		if fok && (!ok || v.Less(fv)) {
			v, ok = fv, true
		}
		for _, ip := range Imports(src) {
			if !ValidImportPath(ip) {
				continue
			}
			if d := ImportDir(moduleRoot, fincRoot, ip); !visited[d] {
				visited[d] = true
				queue = append(queue, d)
			}
		}
		return nil
	}
	readPkg := func(dir string) error {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		var files []string
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".fc") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
		sort.Strings(files)
		for _, f := range files {
			if err := read(f); err != nil {
				return err
			}
		}
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return Version{}, false, err
	}
	if st.IsDir() {
		err = readPkg(path)
	} else {
		err = read(path)
	}
	for i := 0; err == nil && i < len(queue); i++ {
		if st, serr := os.Stat(queue[i]); serr != nil || !st.IsDir() {
			continue
		}
		err = readPkg(queue[i])
	}
	if err != nil {
		return Version{}, false, err
	}
	return v, ok, nil
}

// Finc0Can reports whether path needs at most the language version finc0
// implements, i.e. whether it belongs in the finc0-vs-finc1 differential.
// A malformed SINCE directive is an error.
func Finc0Can(path string) (bool, error) {
	v, ok, err := Required(path)
	if err != nil || !ok {
		return err == nil, err
	}
	return !MustParse(Finc0).Less(v), nil
}

// Finc0CanSource is Finc0Can for an in-memory source (its own directive
// only: imports are not followed).
func Finc0CanSource(src []byte) (bool, error) {
	v, ok, err := Since(src)
	if err != nil || !ok {
		return err == nil, err
	}
	return !MustParse(Finc0).Less(v), nil
}
