package main

import (
	"fmt"
	"strings"

	"fincore/bootstrap/internal/check"
)

func runCheck(env *Env, args []string) int {
	const usageLine = "check [--comptime-fuel=N] <file.fc | dir>"
	fs := newFlagSet(env, "check", usageLine)
	fuel := fs.Uint64("comptime-fuel", 1_000_000, "comptime fuel budget per evaluation")
	args, typesOnly, only, ok := passesFlag(env, args)
	if !ok {
		return ExitUsage
	}
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
	res, err := check.Run(fs.Arg(0), check.Options{Fuel: *fuel, TypesOnly: typesOnly, Only: only})
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

// passesFlag extracts the hidden flag --passes=types|all|<pass>[,<pass>] (also -passes=...)
// from the flags before the operand. --passes=types stops after name
// resolution, type checking and comptime evaluation; it is the oracle for
// the self-hosted compiler's differential tests (make diff-check). The
// flag is not part of the documented CLI and does not appear in the usage
// text.
func passesFlag(env *Env, args []string) ([]string, bool, []string, bool) {
	out := make([]string, 0, len(args))
	typesOnly := false
	var only []string
	for i, a := range args {
		if a == "--" || !strings.HasPrefix(a, "-") {
			out = append(out, args[i:]...)
			break
		}
		v, isPasses := strings.CutPrefix(strings.TrimPrefix(a, "-"), "-passes=")
		if !isPasses {
			v, isPasses = strings.CutPrefix(a, "-passes=")
		}
		if !isPasses {
			out = append(out, a)
			continue
		}
		switch v {
		case "types":
			typesOnly, only = true, nil
		case "all":
			typesOnly, only = false, nil
		default:
			typesOnly, only = false, nil
			for _, name := range strings.Split(v, ",") {
				if !knownPass(name) {
					fmt.Fprintf(env.Stderr, "finc0 check: invalid --passes value %q (want types, all or a list of passes)\n", v)
					return nil, false, nil, false
				}
				only = append(only, name)
			}
		}
	}
	return out, typesOnly, only, true
}

func knownPass(name string) bool {
	for _, p := range check.Passes {
		if p.Name == name {
			return true
		}
	}
	return false
}
