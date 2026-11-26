package main

import (
	"fmt"

	"fincore/bootstrap/internal/check"
)

func runCheck(env *Env, args []string) int {
	const usageLine = "check [--comptime-fuel=N] <file.fc | dir>"
	fs := newFlagSet(env, "check", usageLine)
	fuel := fs.Uint64("comptime-fuel", 1_000_000, "comptime fuel budget per evaluation")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return ExitUsage
	}
	if *fuel == 0 {
		fmt.Fprintln(env.Stderr, "finc0 check: --comptime-fuel must be positive")
		return ExitUsage
	}
	res, err := check.Run(fs.Arg(0), check.Options{Fuel: *fuel})
	if err != nil {
		fmt.Fprintf(env.Stderr, "finc0: %v\n", err)
		return ExitError
	}
	if len(res.Diags) > 0 {
		printDiags(env.Stderr, res.Diags)
		return ExitError
	}
	return ExitOK
}
