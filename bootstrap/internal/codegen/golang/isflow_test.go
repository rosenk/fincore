package golang

import (
	"bytes"
	"testing"
)

// v0.2 flow-scoped `is` bindings: hoisted `var _fc_tN_x T` declarations
// before the statement, assigned by the test; no wrapping block, so a
// let-else's variable is a plain variable of the enclosing Go block.
func TestIsFlowLowering(t *testing.T) {
	root := moduleRoot(t)
	src := `package main

type Opt enum {
	Some(u32)
	None
}

func letElse(x Opt) u32 {
	{
		if !(x is Some(x)) {
			return 0
		}
		return x + 1
	}
}

func chain(o Opt, p Opt) bool {
	return o is Some(a) && p is Some(b) && a < b
}

func unused(o Opt) bool {
	return o is Some(z)
}

driver func main() error {
	if letElse(Opt.Some(1)) != 2 || !chain(Opt.Some(1), Opt.Some(2)) || unused(Opt.None) {
		return ErrNotFound
	}
	return nil
}
`
	out := generate(t, root, writeProg(t, src))
	expect(t, out,
		`func letElse\(x Opt\) uint32 \{ \{ var _fc_t0_x uint32 if !\(func\(\) bool \{ _fc_t1 := x if _fc_t1\._tag == 0 \{ _fc_t0_x = _fc_t1\._u32_0 return true \} return false \}\(\)\) \{ return 0 \} return _fc_t0_x \+ 1 \} \}`,
		`func chain\(o Opt, p Opt\) bool \{ var _fc_t0_a uint32 var _fc_t1_b uint32 return func\(\) bool \{`,
		`\}\(\) && _fc_t0_a < _fc_t1_b \}`,
		`func unused\(o Opt\) bool \{ return \(o\._tag == 0\) \}`,
	)
	if bytes.Contains(out, []byte("_z")) {
		t.Error("an unused binding was declared")
	}
	if testing.Short() {
		return
	}
	goVet(t, root, out)
}
