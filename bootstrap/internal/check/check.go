// Package check runs the finc0 front-end pipeline: load + resolve
// (package resolve), type checking with comptime evaluation (packages
// types and comptime), followed by an ordered list of semantic passes
// (scope checker, totality checker, ...).
//
// Cascades are avoided by stopping at the first stage that reports
// errors: parse/import/resolve errors stop before type checking, and type
// errors stop before the semantic passes. Passes after the type checker
// may therefore assume a well-typed program (every expression has a valid
// type, every call has a Call record).
//
// Adding a pass: append a Pass to Passes (e.g. in an init function of the
// pass's package wiring, or directly here):
//
//	check.Passes = append(check.Passes, check.Pass{Name: "scope", Run: scope.Check})
//
// Passes run in order; all diagnostics of one pass are reported together
// and the pipeline stops after the first pass with errors.
package check

import (
	"fincore/bootstrap/internal/comptime"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// Options configure Run.
type Options struct {
	// Fuel is the comptime fuel budget per evaluation root (0 = default
	// comptime.DefaultFuel).
	Fuel uint64
	// ModuleRoot for local imports ("" = current directory).
	ModuleRoot string
	// TypesOnly stops after load/resolve and type checking (with comptime
	// evaluation); the later Passes do not run. Used by the hidden
	// `finc0 check --passes=types` oracle for finc1's differential tests.
	TypesOnly bool
	// Only, if non-empty, restricts the semantic passes to those named
	// (e.g. ["scope"] or ["totality"]). Used by the hidden
	// `finc0 check --passes=<name>` oracle.
	Only []string
}

// Result is the output of the front end.
type Result struct {
	Prog  *resolve.Program
	Info  *types.Info // nil if type checking did not run
	Diags diag.List   // sorted
}

// Pass is a semantic pass over a well-typed program.
type Pass struct {
	Name string
	Run  func(prog *resolve.Program, info *types.Info) diag.List
}

// Passes run after type checking, in order. The scope (S1-S5) and
// totality (T1-T4, E5001) checkers register here.
var Passes []Pass

// Run checks the file or directory at path. err is non-nil only for I/O
// failures on path itself.
func Run(path string, opts Options) (*Result, error) {
	prog, diags, err := resolve.Load(path, resolve.Options{ModuleRoot: opts.ModuleRoot})
	if err != nil {
		return nil, err
	}
	res := &Result{Prog: prog}
	if len(diags) > 0 {
		res.Diags = diags
		return res, nil
	}
	info, diags := types.Check(prog, types.Config{Evaluator: comptime.New(opts.Fuel)})
	res.Info = info
	if len(diags) > 0 || opts.TypesOnly {
		res.Diags = diags
		return res, nil
	}
	for _, p := range Passes {
		if len(opts.Only) > 0 && !contains(opts.Only, p.Name) {
			continue
		}
		d := p.Run(prog, info)
		if len(d) > 0 {
			d.Sort()
			res.Diags = d
			return res, nil
		}
	}
	return res, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
