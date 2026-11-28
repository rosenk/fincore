package types

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
)

// Value is a compile-time value: the value of a constant expression or of a
// comptime evaluation. Values recorded in Info are immutable; the comptime
// interpreter copies aggregates (CopyValue) before mutating them.
type Value interface {
	String() string
}

// IntVal is an integer of arbitrary precision. Typed integer values are
// always within the range of their type.
type IntVal struct{ V *big.Int }

// BoolVal is a boolean.
type BoolVal bool

// StringVal is a string.
type StringVal string

// ErrorVal is an error value: a predeclared sentinel (Name "ErrEOF", ...) or
// nil (Name "").
type ErrorVal struct{ Name string }

// NilVal is the untyped nil constant.
type NilVal struct{}

// ArrayVal is an array value (Elems has the array's length).
type ArrayVal struct{ Elems []Value }

// SliceVal is a slice of an array (Arr nil for the nil/empty slice).
type SliceVal struct {
	Arr    *ArrayVal
	Lo, Hi int
}

// StructVal is a struct value (Fields aligned with Struct.Fields).
type StructVal struct{ Fields []Value }

// EnumVal is an enum value.
type EnumVal struct {
	Variant int
	Payload []Value
}

// TupleVal is the value of a multi-result comptime call.
type TupleVal struct{ Elems []Value }

// CodeKind enumerates the intensional code constructors of spec 5.1.
type CodeKind uint8

const (
	CodeEmpty CodeKind = iota
	CodeUnit
	CodeBool
	CodeU8
	CodeU16
	CodeU32
	CodeU64
	CodeI8
	CodeI16
	CodeI32
	CodeI64
	CodeArray
	CodeSlice
	CodeStruct
	CodeUniv
)

var codeKindNames = [...]string{"CodeEmpty", "CodeUnit", "CodeBool", "CodeU8", "CodeU16", "CodeU32", "CodeU64",
	"CodeI8", "CodeI16", "CodeI32", "CodeI64", "CodeArray", "CodeSlice", "CodeStruct", "CodeUniv"}

// CodeVal is a value of type `Code n`: a finite syntactic description of a
// type. El(c) folds it into a Type (FoldCode).
type CodeVal struct {
	Kind   CodeKind
	Len    uint64          // CodeArray
	Elem   *CodeVal        // CodeArray, CodeSlice
	Fields []*FieldCodeVal // CodeStruct
	Univ   int             // CodeUniv(n)
}

// FieldCodeVal is a struct-field code (extension: CodeField(name, c)).
type FieldCodeVal struct {
	Name string
	Code *CodeVal
}

// Level returns the universe level of the code: n+1 for CodeUniv(n), the
// maximum level of its components otherwise, -1 if level-polymorphic.
func (c *CodeVal) Level() int {
	switch c.Kind {
	case CodeUniv:
		return c.Univ + 1
	case CodeArray, CodeSlice:
		return c.Elem.Level()
	case CodeStruct:
		l := -1
		for _, f := range c.Fields {
			l = max(l, f.Code.Level())
		}
		return l
	}
	return -1
}

// MakeInt returns an IntVal.
func MakeInt(v int64) IntVal { return IntVal{big.NewInt(v)} }

// MakeUint returns an IntVal.
func MakeUint(v uint64) IntVal { return IntVal{new(big.Int).SetUint64(v)} }

func (v IntVal) String() string    { return v.V.String() }
func (v BoolVal) String() string   { return strconv.FormatBool(bool(v)) }
func (v StringVal) String() string { return strconv.Quote(string(v)) }
func (v ErrorVal) String() string {
	if v.Name == "" {
		return "nil"
	}
	return v.Name
}
func (NilVal) String() string { return "nil" }
func (v *ArrayVal) String() string {
	parts := make([]string, 0, min(len(v.Elems), 8))
	for i, e := range v.Elems {
		if i == 8 {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, e.String())
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
func (v SliceVal) String() string {
	if v.Arr == nil {
		return "[]"
	}
	return (&ArrayVal{v.Arr.Elems[v.Lo:v.Hi]}).String()
}
func (v *StructVal) String() string {
	parts := make([]string, len(v.Fields))
	for i, f := range v.Fields {
		parts[i] = f.String()
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
func (v *EnumVal) String() string {
	if len(v.Payload) == 0 {
		return fmt.Sprintf("variant#%d", v.Variant)
	}
	parts := make([]string, len(v.Payload))
	for i, f := range v.Payload {
		parts[i] = f.String()
	}
	return fmt.Sprintf("variant#%d(%s)", v.Variant, strings.Join(parts, ", "))
}
func (v TupleVal) String() string {
	parts := make([]string, len(v.Elems))
	for i, f := range v.Elems {
		parts[i] = f.String()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
func (c *CodeVal) String() string {
	switch c.Kind {
	case CodeArray:
		return fmt.Sprintf("CodeArray(%d, %s)", c.Len, c.Elem)
	case CodeSlice:
		return fmt.Sprintf("CodeSlice(%s)", c.Elem)
	case CodeStruct:
		parts := make([]string, len(c.Fields))
		for i, f := range c.Fields {
			parts[i] = f.String()
		}
		return "CodeStruct(" + strings.Join(parts, ", ") + ")"
	case CodeUniv:
		return fmt.Sprintf("CodeUniv(%d)", c.Univ)
	}
	return codeKindNames[c.Kind] + "()"
}
func (f *FieldCodeVal) String() string { return fmt.Sprintf("CodeField(%q, %s)", f.Name, f.Code) }

// CopyValue returns a deep copy of arrays, structs and enums (value
// semantics); other values are returned as is.
func CopyValue(v Value) Value {
	switch v := v.(type) {
	case *ArrayVal:
		out := &ArrayVal{Elems: make([]Value, len(v.Elems))}
		for i, e := range v.Elems {
			out.Elems[i] = CopyValue(e)
		}
		return out
	case *StructVal:
		out := &StructVal{Fields: make([]Value, len(v.Fields))}
		for i, e := range v.Fields {
			out.Fields[i] = CopyValue(e)
		}
		return out
	case *EnumVal:
		out := &EnumVal{Variant: v.Variant, Payload: make([]Value, len(v.Payload))}
		for i, e := range v.Payload {
			out.Payload[i] = CopyValue(e)
		}
		return out
	}
	return v
}

// ErrTooLarge is returned by Zero for arrays whose total element count
// exceeds MaxComptimeElems.
var ErrTooLarge = errors.New("array too large for compile-time evaluation")

// MaxComptimeElems bounds the size of arrays materialized at compile time.
const MaxComptimeElems = 1 << 20

// Zero returns the zero value of t. Scoped types other than pointers have
// no zero value; Zero returns nil for them.
func Zero(t Type) (Value, error) {
	budget := MaxComptimeElems
	return zero(t, &budget)
}

func zero(t Type, budget *int) (Value, error) {
	switch u := t.Underlying().(type) {
	case *Basic:
		switch {
		case IsInteger(u):
			return MakeInt(0), nil
		case IsBoolean(u):
			return BoolVal(false), nil
		case IsString(u):
			return StringVal(""), nil
		case u.Kind == Error:
			return ErrorVal{}, nil
		}
	case *Code:
		return &CodeVal{Kind: CodeEmpty}, nil
	case *Array:
		if u.LenParam != nil {
			return nil, errors.New("array with generic length")
		}
		if u.Len > uint64(*budget) {
			return nil, ErrTooLarge
		}
		*budget -= int(u.Len)
		out := &ArrayVal{Elems: make([]Value, u.Len)}
		for i := range out.Elems {
			z, err := zero(u.Elem, budget)
			if err != nil {
				return nil, err
			}
			out.Elems[i] = z
		}
		return out, nil
	case *Slice:
		return SliceVal{}, nil
	case *Struct:
		out := &StructVal{Fields: make([]Value, len(u.Fields))}
		for i, f := range u.Fields {
			z, err := zero(f.Type, budget)
			if err != nil {
				return nil, err
			}
			out.Fields[i] = z
		}
		return out, nil
	case *Enum:
		if len(u.Variants) == 0 {
			return &EnumVal{}, nil
		}
		v := u.Variants[0]
		out := &EnumVal{Payload: make([]Value, len(v.Payload))}
		for i, p := range v.Payload {
			z, err := zero(p, budget)
			if err != nil {
				return nil, err
			}
			out.Payload[i] = z
		}
		return out, nil
	}
	if BuiltinOf(t) == resolve.TFieldCode {
		return &FieldCodeVal{Code: &CodeVal{Kind: CodeEmpty}}, nil
	}
	if n, ok := t.(*Named); ok && n.Builtin != 0 {
		return nil, fmt.Errorf("%s has no compile-time value", t)
	}
	return nil, fmt.Errorf("%s has no zero value", t)
}

// ---------------------------------------------------------------------------
// Constant arithmetic

// Representable reports whether the constant v fits in type t (integers:
// range check; others: kind check).
func Representable(v Value, t Type) bool {
	switch v := v.(type) {
	case IntVal:
		if bits, signed, ok := IntInfo(t); ok {
			return inRange(v.V, bits, signed)
		}
		return IsUntyped(t) && IsInteger(t)
	case BoolVal:
		return IsBoolean(t)
	case StringVal:
		return IsString(t)
	}
	return true
}

func inRange(x *big.Int, bits int, signed bool) bool {
	if signed {
		lim := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
		return x.Cmp(new(big.Int).Neg(lim)) >= 0 && x.Cmp(lim) < 0
	}
	return x.Sign() >= 0 && x.BitLen() <= bits
}

// Wrap truncates an integer value to the range of type t (two's
// complement), as runtime arithmetic does. Non-integer values and untyped
// targets are returned unchanged.
func Wrap(v Value, t Type) Value {
	iv, ok := v.(IntVal)
	if !ok {
		return v
	}
	bits, signed, ok := IntInfo(t)
	if !ok || inRange(iv.V, bits, signed) {
		return v
	}
	mod := new(big.Int).Lsh(big.NewInt(1), uint(bits))
	r := new(big.Int).Mod(iv.V, mod) // 0 <= r < 2^bits
	if signed && r.Bit(bits-1) == 1 {
		r.Sub(r, mod)
	}
	return IntVal{r}
}

// ErrDivByZero is returned for constant division by zero.
var ErrDivByZero = errors.New("division by zero")

// MaxShift bounds constant shift counts.
const MaxShift = 1024

// BinaryOp evaluates x op y exactly (no wrapping; callers wrap or range
// check). Comparisons return BoolVal. Logical && and || are evaluated
// strictly.
func BinaryOp(op ast.BinaryOp, x, y Value) (Value, error) {
	switch op {
	case ast.OpEq, ast.OpNe, ast.OpLt, ast.OpLe, ast.OpGt, ast.OpGe:
		return compare(op, x, y)
	case ast.OpLAnd, ast.OpLOr:
		a, ok1 := x.(BoolVal)
		b, ok2 := y.(BoolVal)
		if !ok1 || !ok2 {
			return nil, errors.New("non-boolean operands")
		}
		if op == ast.OpLAnd {
			return a && b, nil
		}
		return a || b, nil
	}
	if op == ast.OpAdd {
		if a, ok := x.(StringVal); ok {
			if b, ok := y.(StringVal); ok {
				return a + b, nil // constant string concatenation (v0.2)
			}
		}
	}
	a, ok1 := x.(IntVal)
	b, ok2 := y.(IntVal)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("operator %s not defined on %s, %s", op, x, y)
	}
	r := new(big.Int)
	switch op {
	case ast.OpAdd:
		r.Add(a.V, b.V)
	case ast.OpSub:
		r.Sub(a.V, b.V)
	case ast.OpMul:
		r.Mul(a.V, b.V)
	case ast.OpDiv:
		if b.V.Sign() == 0 {
			return nil, ErrDivByZero
		}
		r.Quo(a.V, b.V)
	case ast.OpRem:
		if b.V.Sign() == 0 {
			return nil, ErrDivByZero
		}
		r.Rem(a.V, b.V)
	case ast.OpAnd:
		r.And(a.V, b.V)
	case ast.OpOr:
		r.Or(a.V, b.V)
	case ast.OpXor:
		r.Xor(a.V, b.V)
	case ast.OpShl, ast.OpShr:
		if b.V.Sign() < 0 {
			return nil, errors.New("negative shift count")
		}
		if !b.V.IsInt64() || b.V.Int64() > MaxShift {
			if op == ast.OpShr {
				if a.V.Sign() < 0 {
					return MakeInt(-1), nil
				}
				return MakeInt(0), nil
			}
			return nil, errors.New("shift count too large")
		}
		if op == ast.OpShl {
			r.Lsh(a.V, uint(b.V.Int64()))
		} else {
			r.Rsh(a.V, uint(b.V.Int64()))
		}
	default:
		return nil, fmt.Errorf("unsupported operator %s", op)
	}
	return IntVal{r}, nil
}

func compare(op ast.BinaryOp, x, y Value) (Value, error) {
	if op == ast.OpEq || op == ast.OpNe {
		eq, err := Equal(x, y)
		if err != nil {
			return nil, err
		}
		return BoolVal(eq == (op == ast.OpEq)), nil
	}
	var c int
	switch a := x.(type) {
	case IntVal:
		b, ok := y.(IntVal)
		if !ok {
			return nil, errors.New("mismatched operands")
		}
		c = a.V.Cmp(b.V)
	case StringVal:
		b, ok := y.(StringVal)
		if !ok {
			return nil, errors.New("mismatched operands")
		}
		c = strings.Compare(string(a), string(b))
	default:
		return nil, fmt.Errorf("operator %s not defined on %s", op, x)
	}
	switch op {
	case ast.OpLt:
		return BoolVal(c < 0), nil
	case ast.OpLe:
		return BoolVal(c <= 0), nil
	case ast.OpGt:
		return BoolVal(c > 0), nil
	}
	return BoolVal(c >= 0), nil
}

// Equal compares two values for ==.
func Equal(x, y Value) (bool, error) {
	switch a := x.(type) {
	case IntVal:
		b, ok := y.(IntVal)
		return ok && a.V.Cmp(b.V) == 0, nil
	case BoolVal:
		b, ok := y.(BoolVal)
		return ok && a == b, nil
	case StringVal:
		b, ok := y.(StringVal)
		return ok && a == b, nil
	case ErrorVal:
		switch b := y.(type) {
		case ErrorVal:
			return a == b, nil
		case NilVal:
			return a.Name == "", nil
		}
		return false, nil
	case NilVal:
		switch b := y.(type) {
		case ErrorVal:
			return b.Name == "", nil
		case SliceVal:
			return b.Arr == nil || b.Hi == b.Lo, nil
		case NilVal:
			return true, nil
		}
	case SliceVal:
		if _, ok := y.(NilVal); ok {
			return a.Arr == nil || a.Hi == a.Lo, nil
		}
	case *CodeVal:
		b, ok := y.(*CodeVal)
		return ok && a.String() == b.String(), nil
	case *ArrayVal:
		b, ok := y.(*ArrayVal)
		if !ok || len(a.Elems) != len(b.Elems) {
			return false, nil
		}
		for i := range a.Elems {
			if eq, err := Equal(a.Elems[i], b.Elems[i]); err != nil || !eq {
				return eq, err
			}
		}
		return true, nil
	case *StructVal:
		b, ok := y.(*StructVal)
		if !ok || len(a.Fields) != len(b.Fields) {
			return false, nil
		}
		for i := range a.Fields {
			if eq, err := Equal(a.Fields[i], b.Fields[i]); err != nil || !eq {
				return eq, err
			}
		}
		return true, nil
	case *EnumVal:
		b, ok := y.(*EnumVal)
		if !ok || a.Variant != b.Variant || len(a.Payload) != len(b.Payload) {
			return false, nil
		}
		for i := range a.Payload {
			if eq, err := Equal(a.Payload[i], b.Payload[i]); err != nil || !eq {
				return eq, err
			}
		}
		return true, nil
	}
	return false, fmt.Errorf("cannot compare %s and %s", x, y)
}

// UnaryOp evaluates !x, -x or ^x exactly.
func UnaryOp(op ast.UnaryOp, x Value) (Value, error) {
	switch op {
	case ast.OpNot:
		if b, ok := x.(BoolVal); ok {
			return !b, nil
		}
	case ast.OpNeg:
		if i, ok := x.(IntVal); ok {
			return IntVal{new(big.Int).Neg(i.V)}, nil
		}
	case ast.OpBitNot:
		if i, ok := x.(IntVal); ok {
			return IntVal{new(big.Int).Not(i.V)}, nil
		}
	}
	return nil, fmt.Errorf("operator %s not defined on %s", op, x)
}

// Uint64 returns v as a uint64 if it is a non-negative integer that fits.
func Uint64(v Value) (uint64, bool) {
	i, ok := v.(IntVal)
	if !ok || i.V.Sign() < 0 || !i.V.IsUint64() {
		return 0, false
	}
	return i.V.Uint64(), true
}
