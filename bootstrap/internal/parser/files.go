package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
)

// SourceExt is the FinCore source file extension.
const SourceExt = ".fc"

// ListSourceFiles returns the paths of all regular `.fc` files directly in
// dir (non-recursive), sorted lexicographically (spec 9.2).
func ListSourceFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), SourceExt) {
			continue
		}
		if !e.Type().IsRegular() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	slices.Sort(out)
	return out, nil
}

// ParseSourceFile reads and parses one file from disk. I/O failures are
// returned as err; syntax errors as diagnostics.
func ParseSourceFile(path string, ids *ast.IDAlloc) (*ast.File, diag.List, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	f, d := ParseFile(path, src, ids)
	return f, d, nil
}

// ParseFiles parses the given files (in the given order) as one package. All
// files must have the same package clause; a mismatch is reported as E1001
// at the offending package name. Files with syntax errors are omitted from
// the result; their diagnostics are included. The returned diagnostics are
// sorted canonically.
func ParseFiles(dir string, paths []string, ids *ast.IDAlloc) (*ast.Package, diag.List, error) {
	if ids == nil {
		ids = &ast.IDAlloc{}
	}
	pkg := &ast.Package{Dir: dir}
	var diags diag.List
	for _, path := range paths {
		f, d, err := ParseSourceFile(path, ids)
		if err != nil {
			return nil, nil, err
		}
		diags = append(diags, d...)
		if f == nil {
			continue
		}
		if pkg.Name == "" {
			pkg.Name = f.Package.Name
		} else if f.Package.Name != pkg.Name {
			diags.Addf(diag.E1001, f.Package.Range, "package %s; expected package %s (from %s)", f.Package.Name, pkg.Name, pkg.Files[0].Path)
			continue
		}
		pkg.Files = append(pkg.Files, f)
	}
	diags.Sort()
	return pkg, diags, nil
}

// ParseDir parses all `.fc` files of dir as one package.
func ParseDir(dir string, ids *ast.IDAlloc) (*ast.Package, diag.List, error) {
	paths, err := ListSourceFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("%s: no %s files in directory", dir, SourceExt)
	}
	return ParseFiles(dir, paths, ids)
}

// ParsePath parses a single file (as a one-file package) or a directory.
func ParsePath(path string, ids *ast.IDAlloc) (*ast.Package, diag.List, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if st.IsDir() {
		return ParseDir(path, ids)
	}
	return ParseFiles(path, []string{path}, ids)
}
