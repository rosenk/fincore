// Package langver reads the `// SINCE: vX.Y` directive of FinCore sources.
//
// finc0 is frozen at language v0.2 (bootstrap/README.md). A corpus input
// that needs a newer language version declares it in its leading comment
// block (the `//` comment and blank lines before the first other line):
//
//	// SINCE: v0.3
//	// pass_foo: ...
//	package main
//
// The finc0-vs-finc1 differential tools (scripts/diffcheck,
// scripts/diffbuild, and `make diff-parse` via scripts/sincefilter) skip
// every input that needs more than Finc0. A directory (package) needs the
// highest version of any .fc file below it.
package langver

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Finc0 is the language version finc0 implements (frozen).
const Finc0 = "v0.2"

// Version is a language version vMAJOR.MINOR.
type Version struct{ Major, Minor int }

// Parse parses "vX.Y".
func Parse(s string) (Version, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "v")
	maj, mnr, ok2 := strings.Cut(rest, ".")
	a, err1 := strconv.Atoi(maj)
	b, err2 := strconv.Atoi(mnr)
	if !ok || !ok2 || err1 != nil || err2 != nil || a < 0 || b < 0 {
		return Version{}, fmt.Errorf("invalid language version %q (want vX.Y)", strings.TrimSpace(s))
	}
	return Version{a, b}, nil
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

// Since returns the version declared by a `// SINCE: vX.Y` line in the
// leading comment block of src; ok is false when there is none.
func Since(src []byte) (v Version, ok bool, err error) {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
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

// Required returns the highest SINCE version of path (a .fc file, or every
// .fc file below a directory); ok is false when none declares one.
func Required(path string) (v Version, ok bool, err error) {
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !strings.HasSuffix(p, ".fc") {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		fv, fok, ferr := Since(src)
		if ferr != nil {
			return fmt.Errorf("%s: %v", p, ferr)
		}
		if fok && (!ok || v.Less(fv)) {
			v, ok = fv, true
		}
		return nil
	})
	return v, ok, err
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

// Finc0CanSource is Finc0Can for an in-memory source.
func Finc0CanSource(src []byte) (bool, error) {
	v, ok, err := Since(src)
	if err != nil || !ok {
		return err == nil, err
	}
	return !MustParse(Finc0).Less(v), nil
}
