package types

import (
	"fmt"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
)

// Type is a resolved FinCore type. Named types (and their instances) are
// canonical pointers; all other types are compared structurally with
// Identical.
type Type interface {
	// Underlying returns the underlying type (itself for non-Named types).
	Underlying() Type
	String() string
}

// BasicKind enumerates scalar and untyped types.
type BasicKind uint8

const (
	Invalid BasicKind = iota // result of an erroneous expression; suppresses follow-up errors
	Bool
	U8
	U16
	U32
	U64
	I8
	I16
	I32
	I64
	Int
	Usize
	String
	Error
	UntypedInt
	UntypedChar // char literal; default type byte (u8)
	UntypedBool
	UntypedString
	UntypedNil
)

// Basic is a scalar type. Each kind has exactly one *Basic (see Typ).
type Basic struct {
	Kind BasicKind
	name string
}

func (b *Basic) Underlying() Type { return b }
func (b *Basic) String() string   { return b.name }

// Typ holds the canonical basic types, indexed by kind. byte is Typ[U8].
var Typ = [...]*Basic{
	Invalid:       {Invalid, "invalid type"},
	Bool:          {Bool, "bool"},
	U8:            {U8, "u8"},
	U16:           {U16, "u16"},
	U32:           {U32, "u32"},
	U64:           {U64, "u64"},
	I8:            {I8, "i8"},
	I16:           {I16, "i16"},
	I32:           {I32, "i32"},
	I64:           {I64, "i64"},
	Int:           {Int, "int"},
	Usize:         {Usize, "usize"},
	String:        {String, "string"},
	Error:         {Error, "error"},
	UntypedInt:    {UntypedInt, "untyped int"},
	UntypedChar:   {UntypedChar, "untyped char"},
	UntypedBool:   {UntypedBool, "untyped bool"},
	UntypedString: {UntypedString, "untyped string"},
	UntypedNil:    {UntypedNil, "untyped nil"},
}

// Named is a declared type (`type T ...`), a predeclared named type
// (Duration, Conn, ...), or an instance of a generic one (Vec[4, u32],
// Slot[[4096]u8]). Instances are canonicalised by the checker, so pointer
// equality is type identity.
type Named struct {
	Name string
	Sym  *resolve.Symbol  // declaring symbol (universe symbol for predeclared types)
	Pkg  *resolve.Package // nil for predeclared types
	// Builtin is non-zero for predeclared named types.
	Builtin resolve.Builtin
	// TypeParams of a generic declaration (nil for instances).
	TypeParams []*TypeParam
	// Origin is the generic type this is an instance of (nil otherwise).
	Origin *Named
	// TypeArgs of an instance, aligned with Origin.TypeParams. A const
	// argument is a *ConstArg (or a const *TypeParam inside generic code).
	TypeArgs []Type

	underlying Type
	class      Class
	classSet   bool
}

func (n *Named) Underlying() Type {
	if n.underlying == nil {
		return Typ[Invalid]
	}
	return n.underlying
}

func (n *Named) String() string {
	name := n.Name
	if n.Pkg != nil && n.Pkg.Path != "" {
		name = n.Pkg.Name + "." + name
	}
	if len(n.TypeArgs) == 0 {
		return name
	}
	return name + "[" + typeList(n.TypeArgs) + "]"
}

// IsGeneric reports whether n is an uninstantiated generic type.
func (n *Named) IsGeneric() bool { return len(n.TypeParams) > 0 }

// Array is [Len]Elem. Inside generic code the length may be a const type
// parameter (LenParam != nil, Len unused).
type Array struct {
	Len      uint64
	LenParam *TypeParam
	Elem     Type
}

func (a *Array) Underlying() Type { return a }
func (a *Array) String() string {
	if a.LenParam != nil {
		return "[" + a.LenParam.Name + "]" + a.Elem.String()
	}
	return fmt.Sprintf("[%d]%s", a.Len, a.Elem)
}

// Slice is []Elem.
type Slice struct{ Elem Type }

func (s *Slice) Underlying() Type { return s }
func (s *Slice) String() string   { return "[]" + s.Elem.String() }

// Pointer is *Elem.
type Pointer struct{ Elem Type }

func (p *Pointer) Underlying() Type { return p }
func (p *Pointer) String() string   { return "*" + p.Elem.String() }

// Field is a struct field.
type Field struct {
	Name  string
	Type  Type
	Index int
	Ident *ast.Ident // nil for synthesized fields
}

// Struct is struct { fields }.
type Struct struct{ Fields []*Field }

func (s *Struct) Underlying() Type { return s }
func (s *Struct) String() string {
	var b strings.Builder
	b.WriteString("struct{")
	for i, f := range s.Fields {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(f.Name + " " + f.Type.String())
	}
	b.WriteString("}")
	return b.String()
}

// FieldByName returns the field or nil.
func (s *Struct) FieldByName(name string) *Field {
	for _, f := range s.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Variant is an enum variant.
type Variant struct {
	Name    string
	Payload []Type
	Index   int
}

// Enum is enum { variants }.
type Enum struct{ Variants []*Variant }

func (e *Enum) Underlying() Type { return e }
func (e *Enum) String() string {
	var b strings.Builder
	b.WriteString("enum{")
	for i, v := range e.Variants {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(v.Name)
		if len(v.Payload) > 0 {
			b.WriteString("(" + typeList(v.Payload) + ")")
		}
	}
	b.WriteString("}")
	return b.String()
}

// VariantByName returns the variant or nil.
func (e *Enum) VariantByName(name string) *Variant {
	for _, v := range e.Variants {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// Effect is the effect mode of a function (spec 4.2, 6.1):
// Comptime < Total < Driver.
type Effect uint8

const (
	Comptime Effect = iota
	Total
	Driver
)

func (e Effect) String() string { return [...]string{"comptime", "total", "driver"}[e] }

// Signature is a function type. Func types written `driver func(...)` have
// Effect Driver; plain `func(...)` types are Total.
type Signature struct {
	// TypeParams of a generic function (nil for non-generic functions and
	// for instantiated signatures).
	TypeParams []*TypeParam
	Params     []Type
	Results    []Type
	// Variadic: the last parameter is `...T` (Params holds T itself).
	Variadic bool
	Effect   Effect
}

func (s *Signature) Underlying() Type { return s }
func (s *Signature) String() string {
	var b strings.Builder
	if s.Effect == Driver {
		b.WriteString("driver ")
	} else if s.Effect == Comptime {
		b.WriteString("comptime ")
	}
	b.WriteString("func(")
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if s.Variadic && i == len(s.Params)-1 {
			b.WriteString("...")
		}
		b.WriteString(p.String())
	}
	b.WriteString(")")
	switch len(s.Results) {
	case 0:
	case 1:
		b.WriteString(" " + s.Results[0].String())
	default:
		b.WriteString(" (" + typeList(s.Results) + ")")
	}
	return b.String()
}

// Result returns the type of a call: nil for no results, the single result,
// or a *Tuple.
func (s *Signature) Result() Type {
	switch len(s.Results) {
	case 0:
		return nil
	case 1:
		return s.Results[0]
	}
	return &Tuple{Types: s.Results}
}

// Code is the Layer-1 code type `Code n`. Level < 0 denotes a
// level-polymorphic code (the result of CodeU8() etc.), which adapts to the
// level expected by its context like an untyped constant.
type Code struct{ Level int }

func (c *Code) Underlying() Type { return c }
func (c *Code) String() string {
	if c.Level < 0 {
		return "Code ?"
	}
	return fmt.Sprintf("Code %d", c.Level)
}

// TypeParam is a type parameter ([T any]) or a const parameter ([N usize]).
type TypeParam struct {
	Name  string
	Sym   *resolve.Symbol
	Index int
	// Const is the value type of a const parameter (usize), nil for type
	// parameters.
	Const Type
}

func (t *TypeParam) Underlying() Type { return t }
func (t *TypeParam) String() string   { return t.Name }

// IsConst reports whether t is a const parameter.
func (t *TypeParam) IsConst() bool { return t.Const != nil }

// ConstArg is a constant type argument (the 4 in Vec[4, u32]).
type ConstArg struct{ Value uint64 }

func (c *ConstArg) Underlying() Type { return c }
func (c *ConstArg) String() string   { return fmt.Sprint(c.Value) }

// Tuple is the type of a call with several results.
type Tuple struct{ Types []Type }

func (t *Tuple) Underlying() Type { return t }
func (t *Tuple) String() string   { return "(" + typeList(t.Types) + ")" }

// Opaque is the underlying type of predeclared resources (Conn, Scope,
// Slot, ...): they have no FinCore-visible structure.
type Opaque struct{ Name string }

func (o *Opaque) Underlying() Type { return o }
func (o *Opaque) String() string   { return o.Name }

func typeList(ts []Type) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		if t == nil {
			parts[i] = "<nil>"
		} else {
			parts[i] = t.String()
		}
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Predicates

// Identical reports whether two types are identical.
func Identical(a, b Type) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch a := a.(type) {
	case *Basic:
		b, ok := b.(*Basic)
		return ok && a.Kind == b.Kind
	case *Named:
		b, ok := b.(*Named)
		if !ok || a.Origin == nil || a.Origin != b.Origin || len(a.TypeArgs) != len(b.TypeArgs) {
			return false
		}
		for i := range a.TypeArgs {
			if !Identical(a.TypeArgs[i], b.TypeArgs[i]) {
				return false
			}
		}
		return true
	case *Array:
		b, ok := b.(*Array)
		return ok && a.Len == b.Len && a.LenParam == b.LenParam && Identical(a.Elem, b.Elem)
	case *Slice:
		b, ok := b.(*Slice)
		return ok && Identical(a.Elem, b.Elem)
	case *Pointer:
		b, ok := b.(*Pointer)
		return ok && Identical(a.Elem, b.Elem)
	case *Struct:
		b, ok := b.(*Struct)
		if !ok || len(a.Fields) != len(b.Fields) {
			return false
		}
		for i := range a.Fields {
			if a.Fields[i].Name != b.Fields[i].Name || !Identical(a.Fields[i].Type, b.Fields[i].Type) {
				return false
			}
		}
		return true
	case *Enum:
		b, ok := b.(*Enum)
		if !ok || len(a.Variants) != len(b.Variants) {
			return false
		}
		for i := range a.Variants {
			va, vb := a.Variants[i], b.Variants[i]
			if va.Name != vb.Name || !identicalList(va.Payload, vb.Payload) {
				return false
			}
		}
		return true
	case *Signature:
		b, ok := b.(*Signature)
		return ok && a.Effect == b.Effect && a.Variadic == b.Variadic &&
			len(a.TypeParams) == 0 && len(b.TypeParams) == 0 &&
			identicalList(a.Params, b.Params) && identicalList(a.Results, b.Results)
	case *Code:
		b, ok := b.(*Code)
		return ok && a.Level == b.Level
	case *ConstArg:
		b, ok := b.(*ConstArg)
		return ok && a.Value == b.Value
	case *Tuple:
		b, ok := b.(*Tuple)
		return ok && identicalList(a.Types, b.Types)
	}
	return false
}

func identicalList(a, b []Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !Identical(a[i], b[i]) {
			return false
		}
	}
	return true
}

// IsInvalid reports whether t is nil or the invalid type.
func IsInvalid(t Type) bool {
	if t == nil {
		return true
	}
	b, ok := t.(*Basic)
	return ok && b.Kind == Invalid
}

func basicKind(t Type) BasicKind {
	if t == nil {
		return Invalid
	}
	if b, ok := t.Underlying().(*Basic); ok {
		return b.Kind
	}
	return Invalid
}

// IsInteger reports whether t's underlying type is an integer type
// (including untyped int/char constants).
func IsInteger(t Type) bool {
	k := basicKind(t)
	return (k >= U8 && k <= Usize) || k == UntypedInt || k == UntypedChar
}

// IsUnsigned reports whether t is an unsigned integer type.
func IsUnsigned(t Type) bool {
	k := basicKind(t)
	return k >= U8 && k <= U64 || k == Usize
}

// IsUntyped reports whether t is an untyped constant type.
func IsUntyped(t Type) bool {
	k := basicKind(t)
	return k >= UntypedInt && k <= UntypedNil
}

// IsBoolean reports whether t's underlying type is bool (typed or untyped).
func IsBoolean(t Type) bool { k := basicKind(t); return k == Bool || k == UntypedBool }

// IsString reports whether t's underlying type is string (typed or untyped).
func IsString(t Type) bool { k := basicKind(t); return k == String || k == UntypedString }

// IsByteSlice reports whether t's underlying type is []u8 (the targets of
// implicit constant-string conversions, v0.2).
func IsByteSlice(t Type) bool {
	s, ok := t.Underlying().(*Slice)
	return ok && Identical(s.Elem, Typ[U8])
}

// IntInfo returns the bit width and signedness of an integer type
// (usize and int are 64-bit). ok is false for non-integer and untyped types.
func IntInfo(t Type) (bits int, signed bool, ok bool) {
	switch basicKind(t) {
	case U8:
		return 8, false, true
	case U16:
		return 16, false, true
	case U32:
		return 32, false, true
	case U64, Usize:
		return 64, false, true
	case I8:
		return 8, true, true
	case I16:
		return 16, true, true
	case I32:
		return 32, true, true
	case I64, Int:
		return 64, true, true
	}
	return 0, false, false
}

// Default returns the default type of an untyped type (int, u8 for chars,
// bool, string); other types are returned unchanged. Polymorphic Code
// defaults to Code 0.
func Default(t Type) Type {
	switch basicKind(t) {
	case UntypedInt:
		return Typ[Int]
	case UntypedChar:
		return Typ[U8]
	case UntypedBool:
		return Typ[Bool]
	case UntypedString:
		return Typ[String]
	}
	if c, ok := t.(*Code); ok && c.Level < 0 {
		return &Code{Level: 0}
	}
	return t
}

// Deref returns the element type if t is a pointer, else t.
func Deref(t Type) (Type, bool) {
	if p, ok := t.(*Pointer); ok {
		return p.Elem, true
	}
	return t, false
}

// ---------------------------------------------------------------------------
// Value classes (spec 4.1)

// Class is the value class of a type: Value (first-class data), Scoped
// (second-class: capabilities, resources, pointers, function values, and
// aggregates containing them) or Linear (the Scoped subclass of resources
// that must be finalized exactly once). Linear implies Scoped.
type Class uint8

const (
	ClassValue Class = iota
	ClassScoped
	ClassLinear
)

func (c Class) String() string { return [...]string{"Value", "Scoped", "Linear"}[c] }

// ClassOf computes the class of t:
//   - scalars, error, Code, Duration, Signal, ExitStatus, type parameters: Value
//   - pointers and function values (Signature): Scoped
//   - Net, Signals, Clock, Context, Scope, FS, Env, Stdio, Exec: Scoped
//   - Listener, Conn, Slot[T], CancelFunc: Linear
//   - arrays/slices: the class of the element
//   - structs/enums: the maximum over their fields/payloads (a struct with a
//     pointer field is Scoped; with a Linear field, Linear).
func ClassOf(t Type) Class {
	switch t := t.(type) {
	case nil:
		return ClassValue
	case *Named:
		if t.classSet {
			return t.class
		}
		if t.underlying == nil {
			return ClassValue // not yet resolved; do not cache
		}
		t.classSet = true // breaks cycles through invalid recursive types
		t.class = ClassValue
		t.class = ClassOf(t.underlying)
		return t.class
	case *Pointer, *Signature:
		return ClassScoped
	case *Array:
		return ClassOf(t.Elem)
	case *Slice:
		return ClassOf(t.Elem)
	case *Struct:
		c := ClassValue
		for _, f := range t.Fields {
			c = max(c, ClassOf(f.Type))
		}
		return c
	case *Enum:
		c := ClassValue
		for _, v := range t.Variants {
			for _, p := range v.Payload {
				c = max(c, ClassOf(p))
			}
		}
		return c
	case *Tuple:
		c := ClassValue
		for _, e := range t.Types {
			c = max(c, ClassOf(e))
		}
		return c
	}
	return ClassValue
}

// IsScoped reports whether t is Scoped (including Linear).
func IsScoped(t Type) bool { return ClassOf(t) >= ClassScoped }

// IsLinear reports whether t is Linear.
func IsLinear(t Type) bool { return ClassOf(t) == ClassLinear }

// IsResource reports whether t is one of the predeclared capability or
// resource types (Net, Conn, Scope, Slot[T], CancelFunc, ...).
func IsResource(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	if n.Origin != nil {
		n = n.Origin
	}
	return n.Builtin >= resolve.TNet && n.Builtin <= resolve.TExec
}

// BuiltinOf returns the predeclared identity of a named type (BNone if
// none). For instances (Slot[T]) it reports the origin.
func BuiltinOf(t Type) resolve.Builtin {
	n, ok := t.(*Named)
	if !ok {
		return resolve.BNone
	}
	if n.Origin != nil {
		n = n.Origin
	}
	return n.Builtin
}

// Comparable reports whether values of type t may be compared with ==.
func Comparable(t Type) bool {
	switch u := t.Underlying().(type) {
	case *Basic:
		return u.Kind != UntypedNil && u.Kind != Invalid
	case *Pointer, *Code:
		return true
	case *Array:
		return Comparable(u.Elem)
	case *Struct:
		for _, f := range u.Fields {
			if !Comparable(f.Type) {
				return false
			}
		}
		return true
	case *Enum:
		for _, v := range u.Variants {
			for _, p := range v.Payload {
				if !Comparable(p) {
					return false
				}
			}
		}
		return true
	}
	return false
}

// HasTypeParam reports whether t mentions a type or const parameter.
func HasTypeParam(t Type) bool {
	switch t := t.(type) {
	case *TypeParam:
		return true
	case *Named:
		for _, a := range t.TypeArgs {
			if HasTypeParam(a) {
				return true
			}
		}
	case *Array:
		return t.LenParam != nil || HasTypeParam(t.Elem)
	case *Slice:
		return HasTypeParam(t.Elem)
	case *Pointer:
		return HasTypeParam(t.Elem)
	case *Struct:
		for _, f := range t.Fields {
			if HasTypeParam(f.Type) {
				return true
			}
		}
	case *Enum:
		for _, v := range t.Variants {
			for _, p := range v.Payload {
				if HasTypeParam(p) {
					return true
				}
			}
		}
	case *Signature:
		for _, p := range t.Params {
			if HasTypeParam(p) {
				return true
			}
		}
		for _, p := range t.Results {
			if HasTypeParam(p) {
				return true
			}
		}
	case *Tuple:
		for _, p := range t.Types {
			if HasTypeParam(p) {
				return true
			}
		}
	}
	return false
}
