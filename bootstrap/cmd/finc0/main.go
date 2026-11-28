// Command finc0 is the Stage 0 (Go) bootstrap compiler for FinCore v0.1 Native.
//
// CLI:
//
//	finc0 parse [--dump-ast] <file.fc | dir>
//	finc0 check [--comptime-fuel=N] <file.fc | dir>
//	finc0 build [-o out_bin] [--emit-asm=out.s] [--comptime-fuel=N] <file.fc | dir>
//	finc0 run   [--comptime-fuel=N] <file.fc | dir> [-- args...]
//	finc0 test  [-v] <dir>
//	finc0 version
//
// Exit codes: 0 success, 1 compile errors (diagnostics on stderr) or I/O
// failure, 2 usage errors / unimplemented commands.
//
// Adding a command: implement a `func(env *Env, args []string) int` in its
// own file (see parse.go) and register it in the commands table below.
package main

import (
	"fmt"
	"io"
	"os"
)

// LangVersion is the FinCore language version finc0 implements. finc0 is
// the Stage 0 Go bootstrap compiler frozen at v0.1 Native; v0.2+ compilers
// are written in FinCore itself. `finc0 test` skips tests whose
// `// SINCE: vX.Y` header names a newer version.
const LangVersion = "0.1"

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Env carries the process environment for a command (testable).
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
}

// Command is a subcommand implementation.
type Command struct {
	Name    string
	Usage   string
	Summary string
	Run     func(env *Env, args []string) int
}

// commands is the subcommand table. Keep in spec 9.6 order.
var commands = []*Command{
	{Name: "parse", Usage: "parse [--dump-ast] <file.fc | dir>", Summary: "parse sources and optionally dump the AST", Run: runParse},
	{Name: "check", Usage: "check [--comptime-fuel=N] <file.fc | dir>", Summary: "type, scope and totality check", Run: runCheck},
	{Name: "build", Usage: "build [-o out_bin] [--emit-asm=out.s] [--comptime-fuel=N] <file.fc | dir>", Summary: "compile to a static Linux x86_64 ELF64 binary (no libc, no GC)", Run: runBuild},
	{Name: "run", Usage: "run [--comptime-fuel=N] <file.fc | dir> [-- args...]", Summary: "build and run", Run: runRun},
	{Name: "test", Usage: "test <dir>", Summary: "run conformance tests in a directory", Run: runTest},
	{Name: "version", Usage: "version", Summary: "print the FinCore language version", Run: runVersion},
}

func main() {
	env := &Env{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(Main(env, os.Args[1:]))
}

// Main dispatches to a subcommand and returns the exit code.
func Main(env *Env, args []string) int {
	if len(args) == 0 {
		usage(env.Stderr)
		return ExitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		usage(env.Stdout)
		return ExitOK
	case "--version":
		return runVersion(env, args[1:])
	}
	for _, c := range commands {
		if c.Name == args[0] {
			return c.Run(env, args[1:])
		}
	}
	fmt.Fprintf(env.Stderr, "finc0: unknown command %q\n", args[0])
	usage(env.Stderr)
	return ExitUsage
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: finc0 <command> [arguments]")
	fmt.Fprintln(w)
	for _, c := range commands {
		fmt.Fprintf(w, "  finc0 %s\n      %s\n", c.Usage, c.Summary)
	}
}

func notImplemented(name string) func(env *Env, args []string) int {
	return func(env *Env, args []string) int {
		fmt.Fprintf(env.Stderr, "finc0 %s: not implemented\n", name)
		return ExitUsage
	}
}

// runVersion prints "finc0 language vX.Y".
func runVersion(env *Env, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(env.Stderr, "usage: finc0 version")
		return ExitUsage
	}
	fmt.Fprintf(env.Stdout, "finc0 language v%s\n", LangVersion)
	return ExitOK
}
