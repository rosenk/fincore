// Command sincefilter prints those of its arguments (.fc files or package
// directories) that finc0 can compile: inputs whose `// SINCE: vX.Y`
// directive (scripts/langver) names a version newer than finc0's frozen
// v0.2 are dropped with a note on stderr. Used by `make diff-parse`.
//
//	go run ./scripts/sincefilter path...
package main

import (
	"fmt"
	"os"

	"fincore/scripts/langver"
)

func main() {
	for _, p := range os.Args[1:] {
		ok, err := langver.Finc0Can(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sincefilter:", err)
			os.Exit(1)
		}
		if ok {
			fmt.Println(p)
		} else {
			fmt.Fprintf(os.Stderr, "sincefilter: skip %s (SINCE newer than finc0's %s)\n", p, langver.Finc0)
		}
	}
}
