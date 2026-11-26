package ast

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// Dump writes a deterministic, indented textual dump of the tree rooted at n.
// Each node is printed as `TypeName #ID @line:col-line:col` followed by its
// non-zero fields in declaration order. Nil and empty fields are omitted.
// The format is stable and used by `finc0 parse --dump-ast`.
func Dump(w io.Writer, n Node) error {
	d := &dumper{w: w}
	d.node(reflect.ValueOf(n), 0)
	return d.err
}

// DumpString returns Dump's output as a string.
func DumpString(n Node) string {
	var b strings.Builder
	_ = Dump(&b, n)
	return b.String()
}

type dumper struct {
	w   io.Writer
	err error
}

func (d *dumper) printf(indent int, format string, args ...any) {
	if d.err != nil {
		return
	}
	_, d.err = fmt.Fprintf(d.w, "%s%s", strings.Repeat("  ", indent), fmt.Sprintf(format, args...))
}

var nodeType = reflect.TypeOf((*Node)(nil)).Elem()

// node prints a node header line plus its fields; the caller has already
// written any "Field: " prefix on the current line.
func (d *dumper) node(v reflect.Value, indent int) {
	for v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	n := v.Interface().(Node)
	sp := n.Span()
	st := v.Elem()
	header := fmt.Sprintf("%s #%d @%d:%d-%d:%d", st.Type().Name(), n.ID(), sp.Start.Line, sp.Start.Col, sp.End.Line, sp.End.Col)
	// Inline scalar fields into the header.
	var scalars []string
	t := st.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name == "Base" || f.Name == "OpPos" {
			continue
		}
		fv := st.Field(i)
		if s, ok := scalarString(fv); ok {
			scalars = append(scalars, f.Name+"="+s)
		}
	}
	if len(scalars) > 0 {
		header += " " + strings.Join(scalars, " ")
	}
	d.printf(0, "%s\n", header)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name == "Base" {
			continue
		}
		fv := st.Field(i)
		switch {
		case fv.Kind() == reflect.Slice && fv.Type().Elem().Implements(nodeType) || fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.Interface:
			if fv.Len() == 0 {
				continue
			}
			d.printf(indent+1, "%s:\n", f.Name)
			for j := 0; j < fv.Len(); j++ {
				e := fv.Index(j)
				if isNilValue(e) {
					d.printf(indent+2, "- nil\n")
					continue
				}
				d.printf(indent+2, "- ")
				d.node(e, indent+2)
			}
		case fv.Type().Implements(nodeType):
			if isNilValue(fv) {
				continue
			}
			d.printf(indent+1, "%s: ", f.Name)
			d.node(fv, indent+1)
		}
	}
}

func isNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

func scalarString(v reflect.Value) (string, bool) {
	if v.Type().Implements(nodeType) {
		return "", false
	}
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Struct || v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		return "", false
	}
	if v.IsZero() {
		// zero enums with a meaningful name are still informative for ops
		if s, ok := v.Interface().(fmt.Stringer); ok && v.Kind() == reflect.Uint8 {
			switch v.Interface().(type) {
			case BinaryOp, UnaryOp, AssignOp, LitKind:
				return s.String(), true
			}
		}
		return "", false
	}
	if s, ok := v.Interface().(fmt.Stringer); ok {
		return s.String(), true
	}
	switch v.Kind() {
	case reflect.String:
		return strconv.Quote(v.String()), true
	case reflect.Bool:
		return "true", true
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint:
		return strconv.FormatUint(v.Uint(), 10), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	}
	return fmt.Sprint(v.Interface()), true
}
