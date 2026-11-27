package golang

import (
	"fmt"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/ir"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// goType renders a FinCore type as a Go type (spec 9.5.1). n is used for
// error positions only.
func (g *gen) goType(n ast.Node, t types.Type, e *env) string {
	switch t := t.(type) {
	case nil:
		g.fail(n, "missing type")
	case *types.Basic:
		return basicGo(n, g, t)
	case *types.Named:
		return g.namedType(n, t, e)
	case *types.Array, *types.Slice, *types.Pointer, *types.Struct, *types.Enum, *types.Signature:
		return g.typeLit(n, t, e)
	case *types.TypeParam:
		if t.IsConst() {
			g.fail(n, "const parameter %s used as a type", t.Name)
		}
		return g.tpName(t)
	case *types.Code:
		g.fail(n, "values of type %s cannot exist at run time", t)
	}
	g.fail(n, "unsupported type %v", t)
	return ""
}

func basicGo(n ast.Node, g *gen, t *types.Basic) string {
	switch t.Kind {
	case types.Bool, types.UntypedBool:
		return "bool"
	case types.U8:
		return "uint8"
	case types.U16:
		return "uint16"
	case types.U32:
		return "uint32"
	case types.U64:
		return "uint64"
	case types.I8:
		return "int8"
	case types.I16:
		return "int16"
	case types.I32:
		return "int32"
	case types.I64:
		return "int64"
	case types.Int, types.UntypedInt:
		return "int"
	case types.Usize:
		return "uint"
	case types.String, types.UntypedString:
		return "string"
	case types.Error:
		return "error"
	case types.UntypedChar:
		return "uint8"
	}
	g.fail(n, "unsupported basic type %s", t)
	return ""
}

// typeLit renders the type literal for a non-named type (or the
// underlying type of a type declaration).
func (g *gen) typeLit(n ast.Node, t types.Type, e *env) string {
	switch t := t.(type) {
	case *types.Array:
		l := t.Len
		if t.LenParam != nil {
			v, ok := e.lookup(t.LenParam)
			if !ok {
				g.fail(n, "unbound array length parameter %s", t.LenParam.Name)
			}
			l = v
		}
		return fmt.Sprintf("[%d]%s", l, g.goType(n, t.Elem, e))
	case *types.Slice:
		return "[]" + g.goType(n, t.Elem, e)
	case *types.Pointer:
		return "*" + g.goType(n, t.Elem, e)
	case *types.Struct:
		if len(t.Fields) == 0 {
			return "struct{}"
		}
		var b strings.Builder
		b.WriteString("struct {\n")
		for _, f := range t.Fields {
			fmt.Fprintf(&b, "%s %s\n", ir.FieldIdent(f.Name), g.goType(n, f.Type, e))
		}
		b.WriteString("}")
		return b.String()
	case *types.Enum:
		return g.enumStruct(n, t, e)
	case *types.Signature:
		return "func" + g.sigTypes(n, t, e)
	}
	return g.goType(n, t, e)
}

// sigTypes renders `(T1, T2) (R1, R2)` for a signature type.
func (g *gen) sigTypes(n ast.Node, s *types.Signature, e *env) string {
	var b strings.Builder
	b.WriteString("(")
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if s.Variadic && i == len(s.Params)-1 {
			b.WriteString("...")
		}
		b.WriteString(g.goType(n, p, e))
	}
	b.WriteString(")")
	b.WriteString(g.results(n, s.Results, e))
	return b.String()
}

func (g *gen) results(n ast.Node, rs []types.Type, e *env) string {
	switch len(rs) {
	case 0:
		return ""
	case 1:
		return " " + g.goType(n, rs[0], e)
	}
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = g.goType(n, r, e)
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// namedType renders a named type or instance.
func (g *gen) namedType(n ast.Node, t *types.Named, e *env) string {
	if t.Builtin != resolve.BNone || (t.Origin != nil && t.Origin.Builtin != resolve.BNone) {
		return g.builtinNamed(n, t, e)
	}
	if t.Origin == nil {
		if len(t.TypeParams) > 0 {
			g.fail(n, "uninstantiated generic type %s", t.Name)
		}
		return g.p.Name(t.Sym)
	}
	td := g.typeDeclOf(t.Origin)
	if td == nil {
		g.fail(n, "unknown generic type %s", t.Origin.Name)
	}
	var vals []uint64
	var targs []string
	for i, tp := range t.Origin.TypeParams {
		arg := t.TypeArgs[i]
		if tp.IsConst() {
			vals = append(vals, g.constArg(n, arg, e))
		} else {
			targs = append(targs, g.goType(n, arg, e))
		}
	}
	name := g.p.Name(td.Sym)
	if td.Mono() {
		name = g.monoType(td, vals)
	}
	if len(targs) > 0 {
		name += "[" + strings.Join(targs, ", ") + "]"
	}
	return name
}

func (g *gen) builtinNamed(n ast.Node, t *types.Named, e *env) string {
	b := types.BuiltinOf(t)
	switch b {
	case resolve.TScope:
		return "*" + g.rt("Scope")
	case resolve.TSlot:
		return g.rt("Slot") + "[" + g.goType(n, t.TypeArgs[0], e) + "]"
	case resolve.TDuration, resolve.TSignal, resolve.TExitStatus, resolve.TNet, resolve.TSignals,
		resolve.TClock, resolve.TListener, resolve.TConn, resolve.TContext, resolve.TCancelFunc,
		resolve.TFS, resolve.TEnv, resolve.TStdio, resolve.TExec:
		return g.rt(b.String())
	}
	g.fail(n, "type %s cannot be used at run time", t)
	return ""
}
