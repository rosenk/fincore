package main

import (
	"flag"
	"fmt"
	"io"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/parser"
)

// newFlagSet creates a flag set whose errors/usage go to env.Stderr.
func newFlagSet(env *Env, name, usageLine string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "usage: finc0 %s\n", usageLine)
		fs.PrintDefaults()
	}
	return fs
}

// printDiags writes diagnostics (already sorted) to w, one per line.
func printDiags(w io.Writer, diags diag.List) {
	for _, d := range diags {
		fmt.Fprintln(w, d.String())
	}
}

// loadPackage parses a file or directory argument. It reports I/O errors and
// diagnostics on env.Stderr and returns ok=false if any occurred.
func loadPackage(env *Env, path string, ids *ast.IDAlloc) (*ast.Package, bool) {
	pkg, diags, err := parser.ParsePath(path, ids)
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return nil, false
	}
	if len(diags) > 0 {
		printDiags(env.Stderr, diags)
		return nil, false
	}
	return pkg, true
}

func runParse(env *Env, args []string) int {
	const usageLine = "parse [--dump-ast] <file.fc | dir>"
	fs := newFlagSet(env, "parse", usageLine)
	dump := fs.Bool("dump-ast", false, "print the AST")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return ExitUsage
	}
	pkg, ok := loadPackage(env, fs.Arg(0), &ast.IDAlloc{})
	if !ok {
		return ExitError
	}
	if *dump {
		fmt.Fprintf(env.Stdout, "Package %s (%d files)\n", pkg.Name, len(pkg.Files))
		for _, f := range pkg.Files {
			if err := ast.Dump(env.Stdout, f); err != nil {
				fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
				return ExitError
			}
		}
	}
	return ExitOK
}
