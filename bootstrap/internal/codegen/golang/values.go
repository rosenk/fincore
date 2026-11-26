package golang

import (
	"fmt"
	"strconv"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/ir"
	"fincore/bootstrap/internal/types"
)

// value renders a compile-time value of type t as a Go expression. Integer
// constants of types other than int are wrapped in a conversion so that
// their Go type never depends on the context.
func (g *gen) value(n ast.Node, v types.Value, t types.Type, e *env) string {
	switch v := v.(type) {
	case types.IntVal:
		s := v.V.String()
		if t == nil || isUntypedOrInt(t) {
			if v.V.Sign() < 0 {
				return "(" + s + ")"
			}
			return s
		}
		return g.goType(n, t, e) + "(" + s + ")"
	case types.BoolVal:
		s := strconv.FormatBool(bool(v))
		if _, named := t.(*types.Named); named {
			return g.goType(n, t, e) + "(" + s + ")"
		}
		return s
	case types.StringVal:
		s := strconv.Quote(string(v))
		if _, named := t.(*types.Named); named {
			return g.goType(n, t, e) + "(" + s + ")"
		}
		return s
	case types.ErrorVal:
		if v.Name == "" {
			return "nil"
		}
		return g.rt(v.Name)
	case types.NilVal:
		return "nil"
	case *types.StructVal:
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			g.fail(n, "struct value of non-struct type %v", t)
		}
		var parts []string
		for i, f := range st.Fields {
			if i < len(v.Fields) && !isZeroValue(v.Fields[i]) {
				parts = append(parts, ir.FieldIdent(f.Name)+": "+g.value(n, v.Fields[i], f.Type, e))
			}
		}
		return g.goType(n, t, e) + "{" + strings.Join(parts, ", ") + "}"
	case *types.EnumVal:
		en, ok := t.Underlying().(*types.Enum)
		if !ok || v.Variant >= len(en.Variants) {
			g.fail(n, "enum value of non-enum type %v", t)
		}
		vr := en.Variants[v.Variant]
		payload := make([]string, len(v.Payload))
		for i, p := range v.Payload {
			payload[i] = g.value(n, p, vr.Payload[i], e)
		}
		return g.variantLit(n, t, vr, payload, e)
	case *types.ArrayVal:
		at, ok := t.Underlying().(*types.Array)
		if !ok {
			g.fail(n, "array value of non-array type %v", t)
		}
		var parts []string
		for i, el := range v.Elems {
			if !isZeroValue(el) {
				parts = append(parts, fmt.Sprintf("%d: %s", i, g.value(n, el, at.Elem, e)))
			}
		}
		return g.goType(n, t, e) + "{" + strings.Join(parts, ", ") + "}"
	case types.SliceVal:
		if v.Arr == nil {
			return "nil"
		}
		st, ok := t.Underlying().(*types.Slice)
		if !ok {
			g.fail(n, "slice value of non-slice type %v", t)
		}
		parts := make([]string, 0, v.Hi-v.Lo)
		for _, el := range v.Arr.Elems[v.Lo:v.Hi] {
			parts = append(parts, g.value(n, el, st.Elem, e))
		}
		return g.goType(n, t, e) + "{" + strings.Join(parts, ", ") + "}"
	case *types.CodeVal:
		g.fail(n, "Code values cannot exist at run time")
	}
	g.fail(n, "unsupported constant %v", v)
	return ""
}

// variantLit renders an enum variant construction.
func (g *gen) variantLit(n ast.Node, t types.Type, v *types.Variant, payload []string, e *env) string {
	var parts []string
	if v.Index != 0 {
		parts = append(parts, fmt.Sprintf("_tag: %d", v.Index))
	}
	for i, p := range payload {
		parts = append(parts, variantField(v, i)+": "+p)
	}
	return g.goType(n, t, e) + "{" + strings.Join(parts, ", ") + "}"
}

func isUntypedOrInt(t types.Type) bool {
	b, ok := t.(*types.Basic)
	if !ok {
		return false
	}
	return b.Kind == types.Int || b.Kind == types.UntypedInt
}

// isZeroValue reports whether v is the zero value of its type.
func isZeroValue(v types.Value) bool {
	switch v := v.(type) {
	case nil:
		return true
	case types.IntVal:
		return v.V.Sign() == 0
	case types.BoolVal:
		return !bool(v)
	case types.StringVal:
		return v == ""
	case types.ErrorVal:
		return v.Name == ""
	case types.NilVal:
		return true
	case *types.StructVal:
		for _, f := range v.Fields {
			if !isZeroValue(f) {
				return false
			}
		}
		return true
	case *types.EnumVal:
		if v.Variant != 0 {
			return false
		}
		for _, p := range v.Payload {
			if !isZeroValue(p) {
				return false
			}
		}
		return true
	case *types.ArrayVal:
		for _, el := range v.Elems {
			if !isZeroValue(el) {
				return false
			}
		}
		return true
	case types.SliceVal:
		return v.Arr == nil
	}
	return false
}
