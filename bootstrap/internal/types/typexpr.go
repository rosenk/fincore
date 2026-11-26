package types

import (
	"fmt"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
)

// typExpr resolves a syntactic type and records it.
func (c *Checker) typExpr(t ast.Type) Type {
	r := c.typExpr0(t)
	if r == nil {
		r = Typ[Invalid]
	}
	c.info.Types[t.ID()] = r
	c.info.Kinds[t.ID()] = KindType
	return r
}

func (c *Checker) typExpr0(t ast.Type) Type {
	switch t := t.(type) {
	case *ast.NamedType:
		return c.namedType(t)
	case *ast.ArrayType:
		elem := c.typExpr(t.Elem)
		n, lp, ok := c.arrayLen(t.Len)
		if !ok || IsInvalid(elem) {
			return Typ[Invalid]
		}
		return &Array{Len: n, LenParam: lp, Elem: elem}
	case *ast.SliceType:
		elem := c.typExpr(t.Elem)
		if IsInvalid(elem) {
			return Typ[Invalid]
		}
		return &Slice{Elem: elem}
	case *ast.PointerType:
		elem := c.typExpr(t.Elem)
		if IsInvalid(elem) {
			return Typ[Invalid]
		}
		return &Pointer{Elem: elem}
	case *ast.StructType:
		st := &Struct{}
		seen := map[string]bool{}
		for _, f := range t.Fields {
			ft := c.typExpr(f.Type)
			for _, id := range f.Names {
				if id.Name != "_" && seen[id.Name] {
					c.errorf(diag.E2002, id.Range, "duplicate field %s", id.Name)
				}
				seen[id.Name] = true
				st.Fields = append(st.Fields, &Field{Name: id.Name, Type: ft, Index: len(st.Fields), Ident: id})
				c.info.Types[id.ID()] = ft
			}
		}
		return st
	case *ast.EnumType:
		en := &Enum{}
		seen := map[string]bool{}
		for _, v := range t.Variants {
			if seen[v.Name.Name] {
				c.errorf(diag.E2002, v.Name.Range, "duplicate variant %s", v.Name.Name)
			}
			seen[v.Name.Name] = true
			vr := &Variant{Name: v.Name.Name, Index: len(en.Variants)}
			for _, p := range v.Payload {
				vr.Payload = append(vr.Payload, c.typExpr(p))
			}
			en.Variants = append(en.Variants, vr)
		}
		return en
	case *ast.FuncType:
		eff := Total
		if t.Driver {
			eff = Driver
		}
		return c.signature(t.Sig, eff)
	case *ast.CodeType:
		c.constCtx++
		defer func() { c.constCtx-- }()
		x := c.exprValue(t.Level)
		if x.invalid() {
			return Typ[Invalid]
		}
		if x.mode != KindConst || !IsInteger(x.typ) {
			c.errorf(diag.E2010, t.Level.Span(), "Code level must be a constant natural number, not %s", exprString(t.Level))
			return Typ[Invalid]
		}
		c.convertUntyped(&x, Typ[Usize])
		lvl, ok := Uint64(x.val)
		if !ok || lvl > 1<<20 {
			c.errorf(diag.E2010, t.Level.Span(), "invalid Code level %s", x.val)
			return Typ[Invalid]
		}
		return &Code{Level: int(lvl)}
	case *ast.ElType:
		return c.elType(t.Code)
	case *ast.AnyType:
		c.errorf(diag.E2002, t.Range, "any is only allowed as a type parameter constraint")
		return Typ[Invalid]
	}
	return Typ[Invalid]
}

// elType evaluates El(e): e must be a constant Code; the result is the
// folded type (spec 5.1, 5.3).
func (c *Checker) elType(e ast.Expr) Type {
	c.constCtx++
	defer func() { c.constCtx-- }()
	x := c.exprValue(e)
	if x.invalid() {
		return Typ[Invalid]
	}
	if _, ok := x.typ.(*Code); !ok {
		c.errorf(diag.E2002, e.Span(), "El expects a Code value, got %s (type %s)", exprString(e), x.typ)
		return Typ[Invalid]
	}
	cv, ok := x.val.(*CodeVal)
	if x.mode != KindConst || !ok {
		c.errorf(diag.E2002, e.Span(), "El argument %s is not a compile-time constant", exprString(e))
		return Typ[Invalid]
	}
	return FoldCode(cv)
}

// FoldCode converts a code value into the type it denotes:
// CodeU8() -> u8, CodeArray(n, c) -> [n]El(c), CodeSlice(c) -> []El(c),
// CodeStruct(fs) -> struct{...}, CodeUnit() -> struct{}, CodeEmpty() ->
// enum{} (the empty type), CodeUniv(n) -> Code n.
func FoldCode(cv *CodeVal) Type {
	switch cv.Kind {
	case CodeEmpty:
		return &Enum{}
	case CodeUnit:
		return &Struct{}
	case CodeBool:
		return Typ[Bool]
	case CodeU8:
		return Typ[U8]
	case CodeU16:
		return Typ[U16]
	case CodeU32:
		return Typ[U32]
	case CodeU64:
		return Typ[U64]
	case CodeI8:
		return Typ[I8]
	case CodeI16:
		return Typ[I16]
	case CodeI32:
		return Typ[I32]
	case CodeI64:
		return Typ[I64]
	case CodeArray:
		return &Array{Len: cv.Len, Elem: FoldCode(cv.Elem)}
	case CodeSlice:
		return &Slice{Elem: FoldCode(cv.Elem)}
	case CodeStruct:
		st := &Struct{}
		for i, f := range cv.Fields {
			st.Fields = append(st.Fields, &Field{Name: f.Name, Type: FoldCode(f.Code), Index: i})
		}
		return st
	case CodeUniv:
		return &Code{Level: cv.Univ}
	}
	return Typ[Invalid]
}

// arrayLen evaluates an array length: a non-negative constant (E4003
// otherwise) or a const type parameter.
func (c *Checker) arrayLen(e ast.Expr) (uint64, *TypeParam, bool) {
	if id, ok := e.(*ast.Ident); ok {
		if sym := c.prog.Uses[id.ID()]; sym != nil && sym.Kind == resolve.SymTypeParam {
			if tp, ok := c.info.SymTypes[sym.ID].(*TypeParam); ok && tp.IsConst() {
				c.info.Types[id.ID()] = tp.Const
				c.info.Kinds[id.ID()] = KindValue
				return 0, tp, true
			}
		}
	}
	c.constCtx++
	x := c.exprValue(e)
	c.constCtx--
	if x.invalid() {
		return 0, nil, false
	}
	if x.mode != KindConst {
		c.errorf(diag.E4003, e.Span(), "array length %s is not a compile-time constant", exprString(e))
		return 0, nil, false
	}
	if !IsInteger(x.typ) {
		c.errorf(diag.E4003, e.Span(), "array length %s must be an integer (have %s)", exprString(e), x.typ)
		return 0, nil, false
	}
	n, ok := Uint64(x.val)
	if !ok || n > 1<<48 {
		c.errorf(diag.E4003, e.Span(), "invalid array length %s", x.val)
		return 0, nil, false
	}
	if IsUntyped(x.typ) {
		c.convertUntyped(&x, Typ[Usize])
	}
	return n, nil, true
}

func (c *Checker) signature(s *ast.Signature, eff Effect) *Signature {
	sig := &Signature{Effect: eff}
	for _, p := range s.Params {
		t := c.typExpr(p.Type)
		for range p.Names {
			sig.Params = append(sig.Params, t)
		}
		if p.Variadic {
			sig.Variadic = true
		}
	}
	for _, r := range s.Results {
		sig.Results = append(sig.Results, c.typExpr(r))
	}
	return sig
}

// namedType resolves [pkg.]Name[Args].
func (c *Checker) namedType(t *ast.NamedType) Type {
	sym := c.prog.Uses[t.Name.ID()]
	if sym == nil {
		return Typ[Invalid]
	}
	if t.Pkg != nil {
		c.info.Kinds[t.Pkg.ID()] = KindPackage
	}
	r := c.typeSym(sym, t.Name)
	if IsInvalid(r) {
		return r
	}
	c.info.Types[t.Name.ID()] = r
	c.info.Kinds[t.Name.ID()] = KindType
	n, ok := r.(*Named)
	if ok && n.IsGeneric() {
		if len(t.Args) == 0 {
			c.errorf(diag.E2002, t.Range, "generic type %s used without type arguments", n.Name)
			return Typ[Invalid]
		}
		return c.instantiateArgs(n, t.Args, t, t.Range)
	}
	if len(t.Args) > 0 {
		c.errorf(diag.E2002, t.Range, "%s is not a generic type", r)
		return Typ[Invalid]
	}
	return r
}

// typeSym returns the type denoted by a symbol in type position.
func (c *Checker) typeSym(sym *resolve.Symbol, id *ast.Ident) Type {
	switch sym.Kind {
	case resolve.SymType:
		switch sym.Builtin {
		case resolve.TCode:
			c.errorf(diag.E2010, id.Range, "Code requires a level (Code n)")
			return Typ[Invalid]
		case resolve.TEl:
			c.errorf(diag.E2002, id.Range, "El requires an argument (El(code))")
			return Typ[Invalid]
		}
		t := c.symType(sym)
		if t == nil {
			return Typ[Invalid]
		}
		return t
	case resolve.SymTypeParam:
		tp, _ := c.info.SymTypes[sym.ID].(*TypeParam)
		if tp == nil {
			return Typ[Invalid]
		}
		if tp.IsConst() {
			c.errorf(diag.E2002, id.Range, "%s is a const parameter, not a type", sym.Name)
			return Typ[Invalid]
		}
		return tp
	}
	c.errorf(diag.E2002, id.Range, "%s is not a type", sym.Name)
	return Typ[Invalid]
}

// instantiateArgs instantiates a generic named type with syntactic
// arguments; node is recorded in Info.Instances.
func (c *Checker) instantiateArgs(n *Named, args []ast.Expr, node ast.Node, sp diag.SourceSpan) Type {
	if len(args) != len(n.TypeParams) {
		c.errorf(diag.E2003, sp, "wrong number of type arguments for %s: have %d, want %d", n.Name, len(args), len(n.TypeParams))
		return Typ[Invalid]
	}
	targs := make([]Type, len(args))
	for i, a := range args {
		targs[i] = c.typeArg(a, n.TypeParams[i])
		if IsInvalid(targs[i]) {
			return Typ[Invalid]
		}
	}
	return c.instantiate(n, targs, node)
}

// typeArg checks one type argument against its parameter.
func (c *Checker) typeArg(e ast.Expr, tp *TypeParam) Type {
	if tp.IsConst() {
		if id, ok := unparen(e).(*ast.Ident); ok {
			if sym := c.prog.Uses[id.ID()]; sym != nil && sym.Kind == resolve.SymTypeParam {
				if p, ok := c.info.SymTypes[sym.ID].(*TypeParam); ok && p.IsConst() {
					c.info.Types[id.ID()] = p.Const
					c.info.Kinds[id.ID()] = KindValue
					return p
				}
			}
		}
		c.constCtx++
		x := c.exprValue(e)
		c.constCtx--
		if x.invalid() {
			return Typ[Invalid]
		}
		if x.mode != KindConst || !IsInteger(x.typ) {
			c.errorf(diag.E2002, e.Span(), "type argument %s for const parameter %s must be an integer constant", exprString(e), tp.Name)
			return Typ[Invalid]
		}
		if !c.assignment(&x, tp.Const, "type argument") {
			return Typ[Invalid]
		}
		v, ok := Uint64(x.val)
		if !ok {
			c.errorf(diag.E2002, e.Span(), "invalid const argument %s", x.val)
			return Typ[Invalid]
		}
		return &ConstArg{Value: v}
	}
	at, ok := ast.ExprAsType(e)
	if !ok {
		c.errorf(diag.E2002, e.Span(), "%s is not a type", exprString(e))
		return Typ[Invalid]
	}
	t := c.typExpr(at)
	if IsInvalid(t) {
		return t
	}
	if ClassOf(t) != ClassValue {
		c.errorf(diag.E2002, e.Span(), "type argument %s must be a Value type (type parameters range over Value types)", t)
		return Typ[Invalid]
	}
	return t
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// instantiate returns the canonical instance origin[args]. If node is not
// nil the instantiation is recorded for it.
func (c *Checker) instantiate(origin *Named, args []Type, node ast.Node) *Named {
	key := typeKey(origin) + "[" + typeKeys(args) + "]"
	inst := c.instCache[key]
	if inst == nil {
		inst = &Named{Name: origin.Name, Sym: origin.Sym, Pkg: origin.Pkg, Builtin: origin.Builtin, Origin: origin, TypeArgs: args}
		if origin.classSet && origin.Builtin != 0 {
			inst.class, inst.classSet = origin.class, true
		}
		c.instCache[key] = inst
		if origin.underlying == nil {
			c.pending[origin] = append(c.pending[origin], inst)
		} else {
			c.completeInstance(inst)
		}
	}
	rec := c.instByKey[key]
	if rec == nil {
		rec = &Instance{Sym: origin.Sym, Named: inst, TypeArgs: args, Key: inst.String(), Generic: anyTypeParam(args)}
		c.instByKey[key] = rec
		c.info.InstanceList = append(c.info.InstanceList, rec)
	}
	if node != nil {
		c.info.Instances[node.ID()] = rec
	}
	return inst
}

func anyTypeParam(ts []Type) bool {
	for _, t := range ts {
		if HasTypeParam(t) {
			return true
		}
	}
	return false
}

func (c *Checker) completeInstance(inst *Named) {
	o := inst.Origin
	if _, ok := o.underlying.(*Opaque); ok {
		inst.underlying = o.underlying
		return
	}
	inst.underlying = c.subst(o.underlying, bindings(o.TypeParams, inst.TypeArgs))
}

func bindings(tps []*TypeParam, args []Type) map[*TypeParam]Type {
	m := make(map[*TypeParam]Type, len(tps))
	for i, tp := range tps {
		if i < len(args) {
			m[tp] = args[i]
		}
	}
	return m
}

// subst replaces type parameters in t.
func (c *Checker) subst(t Type, m map[*TypeParam]Type) Type {
	if len(m) == 0 || t == nil {
		return t
	}
	switch t := t.(type) {
	case *TypeParam:
		if r, ok := m[t]; ok {
			return r
		}
		return t
	case *Named:
		if t.Origin == nil {
			return t
		}
		args := make([]Type, len(t.TypeArgs))
		changed := false
		for i, a := range t.TypeArgs {
			args[i] = c.subst(a, m)
			changed = changed || args[i] != a
		}
		if !changed {
			return t
		}
		return c.instantiate(t.Origin, args, nil)
	case *Array:
		elem := c.subst(t.Elem, m)
		if t.LenParam != nil {
			switch r := m[t.LenParam].(type) {
			case *ConstArg:
				return &Array{Len: r.Value, Elem: elem}
			case *TypeParam:
				return &Array{LenParam: r, Elem: elem}
			}
		}
		return &Array{Len: t.Len, LenParam: t.LenParam, Elem: elem}
	case *Slice:
		return &Slice{Elem: c.subst(t.Elem, m)}
	case *Pointer:
		return &Pointer{Elem: c.subst(t.Elem, m)}
	case *Struct:
		st := &Struct{Fields: make([]*Field, len(t.Fields))}
		for i, f := range t.Fields {
			st.Fields[i] = &Field{Name: f.Name, Type: c.subst(f.Type, m), Index: f.Index, Ident: f.Ident}
		}
		return st
	case *Enum:
		en := &Enum{Variants: make([]*Variant, len(t.Variants))}
		for i, v := range t.Variants {
			nv := &Variant{Name: v.Name, Index: v.Index}
			for _, p := range v.Payload {
				nv.Payload = append(nv.Payload, c.subst(p, m))
			}
			en.Variants[i] = nv
		}
		return en
	case *Signature:
		s := &Signature{Variadic: t.Variadic, Effect: t.Effect}
		for _, p := range t.Params {
			s.Params = append(s.Params, c.subst(p, m))
		}
		for _, r := range t.Results {
			s.Results = append(s.Results, c.subst(r, m))
		}
		return s
	case *Tuple:
		tu := &Tuple{}
		for _, e := range t.Types {
			tu.Types = append(tu.Types, c.subst(e, m))
		}
		return tu
	}
	return t
}

// typeKey returns a string that identifies t up to identity (type
// parameters and named types are qualified by symbol ID).
func typeKey(t Type) string {
	var b strings.Builder
	writeKey(&b, t)
	return b.String()
}

func typeKeys(ts []Type) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = typeKey(t)
	}
	return strings.Join(parts, ",")
}

func writeKey(b *strings.Builder, t Type) {
	switch t := t.(type) {
	case nil:
		b.WriteString("nil")
	case *Named:
		id := resolve.SymID(0)
		if t.Sym != nil {
			id = t.Sym.ID
		}
		fmt.Fprintf(b, "N%d", id)
		if len(t.TypeArgs) > 0 {
			b.WriteString("[" + typeKeys(t.TypeArgs) + "]")
		}
	case *TypeParam:
		id := resolve.SymID(0)
		if t.Sym != nil {
			id = t.Sym.ID
		}
		fmt.Fprintf(b, "P%d", id)
	case *Array:
		if t.LenParam != nil {
			b.WriteString("[")
			writeKey(b, t.LenParam)
			b.WriteString("]")
		} else {
			fmt.Fprintf(b, "[%d]", t.Len)
		}
		writeKey(b, t.Elem)
	case *Slice:
		b.WriteString("[]")
		writeKey(b, t.Elem)
	case *Pointer:
		b.WriteString("*")
		writeKey(b, t.Elem)
	case *Struct:
		b.WriteString("struct{")
		for _, f := range t.Fields {
			b.WriteString(f.Name + " ")
			writeKey(b, f.Type)
			b.WriteString(";")
		}
		b.WriteString("}")
	case *Enum:
		b.WriteString("enum{")
		for _, v := range t.Variants {
			b.WriteString(v.Name + "(" + typeKeys(v.Payload) + ");")
		}
		b.WriteString("}")
	case *Signature:
		fmt.Fprintf(b, "func%d(%s)", t.Effect, typeKeys(t.Params))
		if t.Variadic {
			b.WriteString("...")
		}
		b.WriteString("(" + typeKeys(t.Results) + ")")
	case *Tuple:
		b.WriteString("(" + typeKeys(t.Types) + ")")
	default:
		b.WriteString(t.String())
	}
}
