package check

import "fincore/bootstrap/internal/totality"

// The effect & totality checker (Layer 2: E5001, T1-T4) runs after the
// scope checker.
func init() {
	Passes = append(Passes, Pass{Name: "totality", Run: totality.Check})
}
