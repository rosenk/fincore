package types

import (
	"strings"

	"fincore/bootstrap/internal/ast"
)

// Pattern usefulness (v0.2), after L. Maranget, "Warnings for pattern
// matching" (JFP 2007). A pattern matrix is a list of rows; a row is a
// vector of patterns, one per column, nil standing for a wildcard. A row q
// is useful with respect to a matrix P if some value vector matches q and
// no row of P. An arm alternative is unreachable iff it is not useful with
// respect to the rows of the earlier unguarded arms (and earlier
// alternatives of its own arm); a match is exhaustive iff the wildcard row
// is not useful with respect to all unguarded rows.
//
// Column types: enums (constructors = variants, in declaration order,
// arity = number of payloads), bool (constructors true, false, arity 0)
// and every other type, whose literal domain is treated as infinite (a
// column of literals is never complete). A tuple scrutinee contributes one
// column per element. Or-patterns are expanded when they reach the head
// column; `..` expands to wildcards; bindings (also top-level and tuple
// element bindings) are wildcards; constant patterns are literals of their
// value (a constant of enum type is its variant).
//
// Witnesses of non-exhaustiveness are computed by the same recursion,
// constructor by constructor in declaration order: a constructor present
// in the column is specialized (its witnesses are prefixed with it); a
// missing one, with wildcard payloads, is combined with the witnesses of
// the default matrix. A column of an infinite domain, or one without any
// constructor (except at the top of a non-tuple scrutinee, where the
// missing variants are always listed), contributes a wildcard.
// finc1's check_match.fc implements the same algorithm.

const (
	// maxPatternDepth bounds the recursion (fuel). When it runs out an
	// alternative counts as useful and the match as exhaustive.
	maxPatternDepth = 1000
	// maxWitnesses is the number of missing patterns listed in a
	// non-exhaustive match message (more are elided with ", ...").
	maxWitnesses = 32
)

// head kinds
const (
	hWild = iota
	hCtor
	hLit
	hOr
)

type usefulness struct{ info *Info }

// wtok is one node of a witness in preorder: a wildcard or a constructor
// (variant or bool value) of type t followed by its arguments.
type wtok struct {
	wild    bool
	missing bool // constructor absent from its column
	t       Type
	c       int
}

// row returns the matrix row of an arm alternative.
func (u *usefulness) row(alt ast.Pattern, n int, isTuple bool) []ast.Pattern {
	alt = stripAt(alt)
	if !isTuple {
		return []ast.Pattern{alt}
	}
	if tp, ok := alt.(*ast.TuplePattern); ok {
		out := make([]ast.Pattern, len(tp.Elems))
		for i, el := range tp.Elems {
			out[i] = stripAt(el)
		}
		return out
	}
	return make([]ast.Pattern, n)
}

// ctorCount returns the number of constructors of a finite column type
// (0 for infinite domains).
func ctorCount(t Type) int {
	if en, ok := t.Underlying().(*Enum); ok {
		return len(en.Variants)
	}
	if IsBoolean(t) {
		return 2
	}
	return 0
}

// argTypes returns the payload types of constructor c of t.
func argTypes(t Type, c int) []Type {
	if en, ok := t.Underlying().(*Enum); ok {
		return en.Variants[c].Payload
	}
	return nil
}

// head classifies the pattern p of a column of type t.
func (u *usefulness) head(p ast.Pattern, t Type) (kind, ctor int, val Value) {
	switch p := p.(type) {
	case *ast.Ident, *ast.CtorPattern:
		// A variant (or a constant of enum type), a constant, or a
		// binding (a wildcard).
		if v := u.info.VariantAt(p); v != nil {
			return hCtor, v.Index, nil
		}
		if cv := u.info.PatConst(p); cv != nil {
			return litHead(cv, t)
		}
	case *ast.LiteralPattern:
		return litHead(u.info.ValueOf(p.Lit), t)
	case *ast.OrPattern:
		return hOr, 0, nil
	}
	return hWild, 0, nil
}

func litHead(v Value, t Type) (int, int, Value) {
	if b, ok := v.(BoolVal); ok && IsBoolean(t) {
		if b {
			return hCtor, 0, nil
		}
		return hCtor, 1, nil
	}
	return hLit, 0, v
}

// args returns the a sub-patterns of the head p (constructor c) for
// specialization; wildcards are nil.
func (u *usefulness) args(p ast.Pattern, a int) []ast.Pattern {
	out := make([]ast.Pattern, a)
	if cp, ok := p.(*ast.CtorPattern); ok && u.info.VariantAt(cp) != nil {
		for i, x := range cp.Args {
			if _, ok := x.(*ast.RestPattern); ok {
				continue
			}
			out[PayloadIndex(cp, i, a)] = stripAt(x)
		}
	}
	return out
}

// expand appends row to out, expanding an or-pattern at its head.
func (u *usefulness) expand(row []ast.Pattern, out *[][]ast.Pattern) {
	if op, ok := row[0].(*ast.OrPattern); ok {
		for _, alt := range op.Alts {
			r := append([]ast.Pattern{stripAt(alt)}, row[1:]...)
			u.expand(r, out)
		}
		return
	}
	*out = append(*out, row)
}

// specialize returns S(c, P) for a constructor (kind hCtor, ctor c, arity
// a) or a literal (kind hLit, value v) of the first column of type t.
func (u *usefulness) specialize(rows [][]ast.Pattern, t Type, kind, c int, v Value) [][]ast.Pattern {
	a := len(argTypes(t, c))
	if kind != hCtor {
		a = 0
	}
	var out [][]ast.Pattern
	for _, row0 := range rows {
		var ex [][]ast.Pattern
		u.expand(row0, &ex)
		for _, row := range ex {
			k, rc, rv := u.head(row[0], t)
			switch {
			case k == hWild:
				out = append(out, append(make([]ast.Pattern, a), row[1:]...))
			case k == hCtor && kind == hCtor && rc == c:
				out = append(out, append(u.args(row[0], a), row[1:]...))
			case k == hLit && kind == hLit && valueEq(rv, v):
				out = append(out, append([]ast.Pattern(nil), row[1:]...))
			}
		}
	}
	return out
}

// defaults returns the default matrix D(P): the rows with a wildcard in
// the first column, without it.
func (u *usefulness) defaults(rows [][]ast.Pattern, t Type) [][]ast.Pattern {
	var out [][]ast.Pattern
	for _, row0 := range rows {
		var ex [][]ast.Pattern
		u.expand(row0, &ex)
		for _, row := range ex {
			if k, _, _ := u.head(row[0], t); k == hWild {
				out = append(out, append([]ast.Pattern(nil), row[1:]...))
			}
		}
	}
	return out
}

// present returns which of the n constructors head a row of the first
// column, and how many distinct ones there are.
func (u *usefulness) present(rows [][]ast.Pattern, t Type, n int) ([]bool, int) {
	seen := make([]bool, n)
	cnt := 0
	for _, row0 := range rows {
		var ex [][]ast.Pattern
		u.expand(row0, &ex)
		for _, row := range ex {
			if k, c, _ := u.head(row[0], t); k == hCtor && !seen[c] {
				seen[c] = true
				cnt++
			}
		}
	}
	return seen, cnt
}

// useful reports whether row q is useful with respect to rows (columns of
// types cols).
func (u *usefulness) useful(rows [][]ast.Pattern, q []ast.Pattern, cols []Type, depth int) bool {
	if depth == 0 {
		return true
	}
	if len(cols) == 0 {
		return len(rows) == 0
	}
	t := cols[0]
	k, c, v := u.head(q[0], t)
	switch k {
	case hOr:
		for _, alt := range q[0].(*ast.OrPattern).Alts {
			if u.useful(rows, append([]ast.Pattern{stripAt(alt)}, q[1:]...), cols, depth-1) {
				return true
			}
		}
		return false
	case hCtor:
		at := argTypes(t, c)
		return u.useful(u.specialize(rows, t, hCtor, c, nil), append(u.args(q[0], len(at)), q[1:]...), append(append([]Type(nil), at...), cols[1:]...), depth-1)
	case hLit:
		return u.useful(u.specialize(rows, t, hLit, 0, v), q[1:], cols[1:], depth-1)
	}
	n := ctorCount(t)
	if n > 0 {
		if _, cnt := u.present(rows, t, n); cnt == n {
			for c := 0; c < n; c++ {
				at := argTypes(t, c)
				if u.useful(u.specialize(rows, t, hCtor, c, nil), append(make([]ast.Pattern, len(at)), q[1:]...), append(append([]Type(nil), at...), cols[1:]...), depth-1) {
					return true
				}
			}
			return false
		}
	}
	return u.useful(u.defaults(rows, t), q[1:], cols[1:], depth-1)
}

// witnesses returns up to limit value vectors (in preorder) matched by no
// row. top is set for the single column of a non-tuple scrutinee.
func (u *usefulness) witnesses(rows [][]ast.Pattern, cols []Type, top bool, limit, depth int) [][]wtok {
	if depth == 0 || limit == 0 {
		return nil
	}
	if len(cols) == 0 {
		if len(rows) == 0 {
			return [][]wtok{nil}
		}
		return nil
	}
	t := cols[0]
	n := ctorCount(t)
	var out [][]wtok
	seen, cnt := u.present(rows, t, n)
	if n == 0 || (cnt == 0 && !top) {
		// Infinite domain, or no constructor at all: a wildcard.
		for _, w := range u.witnesses(u.defaults(rows, t), cols[1:], false, limit, depth-1) {
			out = append(out, append([]wtok{{wild: true}}, w...))
		}
		return out
	}
	var dws [][]wtok
	if cnt < n {
		dws = u.witnesses(u.defaults(rows, t), cols[1:], false, limit, depth-1)
	}
	for c := 0; c < n && len(out) < limit; c++ {
		at := argTypes(t, c)
		if seen[c] {
			ws := u.witnesses(u.specialize(rows, t, hCtor, c, nil), append(append([]Type(nil), at...), cols[1:]...), false, limit-len(out), depth-1)
			for _, w := range ws {
				out = append(out, append([]wtok{{t: t, c: c}}, w...))
			}
			continue
		}
		head := []wtok{{missing: true, t: t, c: c}}
		for range at {
			head = append(head, wtok{wild: true})
		}
		for _, w := range dws {
			if len(out) == limit {
				break
			}
			out = append(out, append(append([]wtok(nil), head...), w...))
		}
	}
	return out
}

// render prints a witness: `(p1, p2)` for a tuple scrutinee, otherwise one
// pattern. At the top of a non-tuple scrutinee a variant whose payloads
// are all wildcards is printed by its name alone.
func (u *usefulness) render(w []wtok, cols []Type, isTuple bool) string {
	if !isTuple {
		s, _ := renderTok(w, 0, true)
		return s
	}
	parts := make([]string, len(cols))
	pos := 0
	for i := range cols {
		parts[i], pos = renderTok(w, pos, false)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func renderTok(w []wtok, pos int, top bool) (string, int) {
	tk := w[pos]
	pos++
	if tk.wild {
		return "_", pos
	}
	en, ok := tk.t.Underlying().(*Enum)
	if !ok {
		if tk.c == 0 {
			return "true", pos
		}
		return "false", pos
	}
	v := en.Variants[tk.c]
	if len(v.Payload) == 0 {
		return v.Name, pos
	}
	args := make([]string, len(v.Payload))
	allWild := true
	for i := range args {
		args[i], pos = renderTok(w, pos, false)
		if args[i] != "_" {
			allWild = false
		}
	}
	if top && allWild && tk.missing {
		return v.Name, pos
	}
	return v.Name + "(" + strings.Join(args, ", ") + ")", pos
}
