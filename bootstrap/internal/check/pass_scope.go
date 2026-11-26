package check

import "fincore/bootstrap/internal/scope"

// The Layer-3 scope/linearity checker (S2-S5: E3002-E3005).
func init() {
	Passes = append(Passes, Pass{Name: "scope", Run: scope.Check})
}
