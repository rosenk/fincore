// Package gomain normalizes the Go main of programs emitted by compilers
// older than v0.3 (finc0, the seed v0.2.0) to the v0.3 form, so that the
// differential tools (scripts/diffbuild, scripts/diffprev) can compare
// their emitted Go with the current compiler's byte for byte.
//
// Up to v0.2 the generated main prints and exits inline and imports "os":
//
//	import (
//		"fincore/runtime/fincrt"
//		"os"
//	)
//	...
//	func main() {
//		if err := _fc_main(fincrt.DefaultStdio()); err != nil {
//			_, _ = os.Stderr.WriteString("error: " + err.Error() + "\n")
//			os.Exit(1)
//		}
//	}
//
// Since v0.3 (docs/semantics_decisions.md 156) it is
// `fincrt.MainExit(_fc_main(...))` and the only import is fincrt.
package gomain

import (
	"bytes"
	"regexp"
)

var oldMain = regexp.MustCompile(`(?m)^func main\(\) \{\n\tif err := ([^\n]*); err != nil \{\n\t\t_, _ = os\.Stderr\.WriteString\("error: " \+ err\.Error\(\) \+ "\\n"\)\n\t\tos\.Exit\(1\)\n\t\}\n\}\n`)

var (
	oldImportsRT = []byte("import (\n\t\"fincore/runtime/fincrt\"\n\t\"os\"\n)\n")
	oldImportsOS = []byte("import (\n\t\"os\"\n)\n")
	newImports   = []byte("import (\n\t\"fincore/runtime/fincrt\"\n)\n")
)

// ToV03 rewrites an old-style generated main (and its import block) to the
// v0.3 form. Sources without an old-style main are returned unchanged.
func ToV03(src []byte) []byte {
	if !oldMain.Match(src) {
		return src
	}
	out := oldMain.ReplaceAll(src, []byte("func main() {\n\tfincrt.MainExit($1)\n}\n"))
	if i := bytes.Index(out, oldImportsRT); i >= 0 {
		return append(append(append([]byte{}, out[:i]...), newImports...), out[i+len(oldImportsRT):]...)
	}
	if i := bytes.Index(out, oldImportsOS); i >= 0 {
		return append(append(append([]byte{}, out[:i]...), newImports...), out[i+len(oldImportsOS):]...)
	}
	return out
}
