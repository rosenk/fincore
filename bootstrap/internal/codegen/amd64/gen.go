// Package amd64 is the direct Linux x86_64 assembly code generator for FinCore.
// It lowers a checked, fully monomorphized FinCore program directly into
// deterministic GNU GAS x86_64 assembly linked statically against
// runtime/linux_amd64/*.s with zero libc and zero GC.
package amd64

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/ir"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// Error is a code generation failure.
type Error struct {
	Node ast.Node
	Msg  string
}

func (e *Error) Error() string {
	if e.Node != nil {
		return fmt.Sprintf("%s: amd64 codegen: %s", e.Node.Span(), e.Msg)
	}
	return "amd64 codegen: " + e.Msg
}

// Generate emits deterministic Linux x86_64 assembly for p.
func Generate(p *ir.Program) (out []byte, err error) {
	g := &gen{
		p:          p,
		info:       p.Info,
		strLabels:  map[string]string{},
		globalSyms: map[*resolve.Symbol]string{},
		monoLabels: map[string]string{},
		litLabels:  map[string]string{},
		funcLabels: map[*types.FuncInfo]string{},
	}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*Error); ok {
				out, err = nil, e
				return
			}
			panic(r)
		}
	}()
	if p.Main == nil {
		return nil, &Error{Msg: "the root package has no main function"}
	}
	g.program()
	return g.assemble(), nil
}

type monoItem struct {
	label string
	fi    *types.FuncInfo
	bind  map[*types.TypeParam]types.Type
}

type litItem struct {
	label string
	fi    *types.FuncInfo
	bind  map[*types.TypeParam]types.Type
}

type gen struct {
	p    *ir.Program
	info *types.Info

	textBuf   bytes.Buffer
	rodataBuf bytes.Buffer
	strBuf    bytes.Buffer
	dataBuf   bytes.Buffer
	bssBuf    bytes.Buffer

	labelSeq  int
	strSeq    int
	strLabels map[string]string // exact bytes -> .L_str_N

	globalSyms map[*resolve.Symbol]string
	funcLabels map[*types.FuncInfo]string

	monoLabels map[string]string
	monoQueue  []monoItem

	litLabels map[string]string
	litQueue  []litItem
}

func (g *gen) fail(n ast.Node, format string, args ...any) {
	panic(&Error{Node: n, Msg: fmt.Sprintf(format, args...)})
}

func (g *gen) freshLabel(prefix string) string {
	g.labelSeq++
	return fmt.Sprintf(".L_%s_%d", prefix, g.labelSeq)
}

// internStr emits s into .rodata (via strBuf so it never interleaves with rodataBuf) once and returns its label.
func (g *gen) internStr(s string) string {
	if lbl, ok := g.strLabels[s]; ok {
		return lbl
	}
	g.strSeq++
	lbl := fmt.Sprintf(".L_str_%d", g.strSeq)
	g.strLabels[s] = lbl
	fmt.Fprintf(&g.strBuf, "%s:\n", lbl)
	if len(s) == 0 {
		fmt.Fprintf(&g.strBuf, "    .byte 0\n")
		return lbl
	}
	// Emit in 16-byte chunks as .byte for 100% escaping safety.
	for i := 0; i < len(s); i += 16 {
		end := i + 16
		if end > len(s) {
			end = len(s)
		}
		fmt.Fprintf(&g.strBuf, "    .byte ")
		for j := i; j < end; j++ {
			if j > i {
				g.strBuf.WriteString(", ")
			}
			fmt.Fprintf(&g.strBuf, "%d", s[j])
		}
		g.strBuf.WriteByte('\n')
	}
	return lbl
}

func (g *gen) program() {
	// 1. Assign deterministic labels to all globals and non-generic functions.
	for _, pk := range g.p.Packages {
		for _, gl := range pk.Globals {
			lbl := "_fc_g_" + g.p.Name(gl.Sym)
			g.globalSyms[gl.Sym] = lbl
		}
		for _, f := range pk.Funcs {
			if len(f.Info.TypeParams) > 0 {
				continue // emitted on demand via monomorphization
			}
			g.funcLabels[f.Info] = g.baseFuncLabel(f.Info)
		}
	}

	// 2. Emit globals (.bss for zero-init, .data for non-zero constant init).
	for _, pk := range g.p.Packages {
		for _, gl := range pk.Globals {
			g.emitGlobal(gl)
		}
	}

	// 3. Emit _fc_entry calling _fc_main with zero-size capabilities.
	g.emitEntry()

	// 4. Emit all non-generic functions.
	for _, pk := range g.p.Packages {
		for _, f := range pk.Funcs {
			if len(f.Info.TypeParams) > 0 {
				continue
			}
			g.emitFunc(g.funcLabels[f.Info], f.Info, nil)
		}
	}

	// 5. Drain monomorphized functions and closure literals until fixpoint.
	for mi, li := 0, 0; mi < len(g.monoQueue) || li < len(g.litQueue); {
		for mi < len(g.monoQueue) {
			it := g.monoQueue[mi]
			mi++
			g.emitFunc(it.label, it.fi, it.bind)
		}
		for li < len(g.litQueue) {
			it := g.litQueue[li]
			li++
			g.emitFunc(it.label, it.fi, it.bind)
		}
	}
}

func (g *gen) baseFuncLabel(fi *types.FuncInfo) string {
	if fi == g.p.Main {
		return "_fc_main"
	}
	if fi.Method != nil {
		pre := ""
		if fi.Method.Recv != nil && fi.Method.Recv.Sym != nil {
			pre = g.p.Name(fi.Method.Recv.Sym) + "_"
		}
		return "_fc_fn_" + pre + fi.Method.Name
	}
	return "_fc_fn_" + g.p.Name(fi.Sym)
}

func (g *gen) requestFunc(fi *types.FuncInfo, typeArgs []types.Type) string {
	if len(fi.TypeParams) == 0 {
		if lbl, ok := g.funcLabels[fi]; ok {
			return lbl
		}
		lbl := g.baseFuncLabel(fi)
		g.funcLabels[fi] = lbl
		return lbl
	}
	key := g.monoKey(fi, typeArgs)
	if lbl, ok := g.monoLabels[key]; ok {
		return lbl
	}
	lbl := fmt.Sprintf("%s__m%d", g.baseFuncLabel(fi), len(g.monoLabels)+1)
	g.monoLabels[key] = lbl
	bind := map[*types.TypeParam]types.Type{}
	for i, tp := range fi.TypeParams {
		if i < len(typeArgs) {
			bind[tp] = typeArgs[i]
		}
	}
	g.monoQueue = append(g.monoQueue, monoItem{label: lbl, fi: fi, bind: bind})
	return lbl
}

func (g *gen) requestLit(fi *types.FuncInfo, bind map[*types.TypeParam]types.Type) string {
	var b strings.Builder
	fmt.Fprintf(&b, "lit#%d", fi.Lit.ID())
	if len(bind) > 0 {
		for _, tp := range fi.Outer.TypeParams {
			if t, ok := bind[tp]; ok {
				b.WriteByte('|')
				b.WriteString(typeKey(t))
			}
		}
	}
	key := b.String()
	if lbl, ok := g.litLabels[key]; ok {
		return lbl
	}
	lbl := fmt.Sprintf("_fc_lit_%d", len(g.litLabels)+1)
	g.litLabels[key] = lbl
	var bcopy map[*types.TypeParam]types.Type
	if len(bind) > 0 {
		bcopy = map[*types.TypeParam]types.Type{}
		for k, v := range bind {
			bcopy[k] = v
		}
	}
	g.litQueue = append(g.litQueue, litItem{label: lbl, fi: fi, bind: bcopy})
	return lbl
}

func (g *gen) monoKey(fi *types.FuncInfo, args []types.Type) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%p", fi)
	for _, a := range args {
		b.WriteByte('|')
		b.WriteString(typeKey(a))
	}
	return b.String()
}

func typeKey(t types.Type) string {
	if t == nil {
		return "<nil>"
	}
	switch u := t.(type) {
	case *types.ConstArg:
		return fmt.Sprintf("C%d", u.Value)
	case *types.Basic:
		return u.String()
	case *types.Named:
		if u.Origin != nil {
			var b strings.Builder
			fmt.Fprintf(&b, "N%p[", u.Origin)
			for i, a := range u.TypeArgs {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(typeKey(a))
			}
			b.WriteByte(']')
			return b.String()
		}
		return fmt.Sprintf("N%p", u)
	case *substNamed:
		var b strings.Builder
		orig := u.orig
		if orig.Origin != nil {
			orig = orig.Origin
		}
		fmt.Fprintf(&b, "N%p[", orig)
		for i, a := range u.args {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(typeKey(a))
		}
		b.WriteByte(']')
		return b.String()
	case *types.Array:
		return fmt.Sprintf("[%d]%s", u.Len, typeKey(u.Elem))
	case *types.Slice:
		return "[]" + typeKey(u.Elem)
	case *types.Pointer:
		return "*" + typeKey(u.Elem)
	default:
		return t.String()
	}
}

// emitEntry emits `_fc_entry` called by `_start` in sys.s.
// It invokes `_fc_main(...)` (all capabilities are 0-byte tokens) and returns
// its error sentinel in %rax.
func (g *gen) emitEntry() {
	abi := computeCallABI(nil, g.p.Main.Sig.Params, g.p.Main.Sig.Results)
	fmt.Fprintf(&g.textBuf, ".global _fc_entry\n_fc_entry:\n")
	fmt.Fprintf(&g.textBuf, "    pushq %%rbp\n")
	fmt.Fprintf(&g.textBuf, "    movq %%rsp, %%rbp\n")
	if abi.totalSize > 0 {
		fmt.Fprintf(&g.textBuf, "    subq $%d, %%rsp\n", abi.totalSize)
	}
	fmt.Fprintf(&g.textBuf, "    xorq %%r12, %%r12\n")
	fmt.Fprintf(&g.textBuf, "    call _fc_main\n")
	if len(abi.retOffs) > 0 {
		fmt.Fprintf(&g.textBuf, "    movq %d(%%rsp), %%rax\n", abi.retOffs[0])
	} else {
		fmt.Fprintf(&g.textBuf, "    xorq %%rax, %%rax\n")
	}
	fmt.Fprintf(&g.textBuf, "    movq %%rbp, %%rsp\n")
	fmt.Fprintf(&g.textBuf, "    popq %%rbp\n")
	fmt.Fprintf(&g.textBuf, "    ret\n\n")
}

// emitGlobal emits a package-level variable in .bss (zero) or .data (constant init).
func (g *gen) emitGlobal(gl *ir.Global) {
	lbl := g.globalSyms[gl.Sym]
	l := layoutOf(gl.Type)
	if l.size == 0 {
		l.size = 1
	}
	var val types.Value
	if gl.Decl != nil && gl.Decl.Value != nil {
		val = g.info.ValueOf(gl.Decl.Value)
	}
	if val == nil || isZeroVal(val) {
		fmt.Fprintf(&g.bssBuf, ".balign %d\n%s:\n    .skip %d\n", l.align, lbl, l.size)
		return
	}
	fmt.Fprintf(&g.dataBuf, ".balign %d\n%s:\n", l.align, lbl)
	g.emitStaticVal(&g.dataBuf, gl.Type, val)
}

func isZeroVal(v types.Value) bool {
	switch v := v.(type) {
	case nil:
		return true
	case types.BoolVal:
		return !bool(v)
	case types.IntVal:
		return v.V == nil || v.V.Sign() == 0
	case types.StringVal:
		return len(v) == 0
	case types.ErrorVal:
		return v.Name == ""
	case *types.ArrayVal:
		for _, e := range v.Elems {
			if !isZeroVal(e) {
				return false
			}
		}
		return true
	case *types.StructVal:
		for _, f := range v.Fields {
			if !isZeroVal(f) {
				return false
			}
		}
		return true
	case *types.EnumVal:
		if v.Variant != 0 {
			return false
		}
		for _, p := range v.Payload {
			if !isZeroVal(p) {
				return false
			}
		}
		return true
	case types.SliceVal:
		return v.Arr == nil && v.Lo == 0 && v.Hi == 0
	}
	return false
}

// emitStaticVal emits exact bytes/relocations of constant value v of type t.
func (g *gen) emitStaticVal(w *bytes.Buffer, t types.Type, v types.Value) {
	l := layoutOf(t)
	if l.size == 0 {
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch u.Kind {
		case types.Bool:
			b := byte(0)
			if bv, ok := v.(types.BoolVal); ok && bool(bv) {
				b = 1
			}
			fmt.Fprintf(w, "    .byte %d\n", b)
		case types.U8, types.I8:
			fmt.Fprintf(w, "    .byte %d\n", intValBits(v)&0xff)
		case types.U16, types.I16:
			fmt.Fprintf(w, "    .short %d\n", intValBits(v)&0xffff)
		case types.U32, types.I32:
			fmt.Fprintf(w, "    .long %d\n", intValBits(v)&0xffffffff)
		case types.U64, types.I64, types.Int, types.Usize:
			fmt.Fprintf(w, "    .quad %d\n", int64(intValBits(v)))
		case types.Error:
			ev, _ := v.(types.ErrorVal)
			fmt.Fprintf(w, "    .quad %d\n", errSentinelID(ev.Name))
		case types.String:
			sv, _ := v.(types.StringVal)
			if len(sv) == 0 {
				fmt.Fprintf(w, "    .quad 0, 0\n")
			} else {
				slbl := g.internStr(string(sv))
				fmt.Fprintf(w, "    .quad %s, %d\n", slbl, len(sv))
			}
		}
	case *types.Array:
		av, ok := v.(*types.ArrayVal)
		if !ok {
			fmt.Fprintf(w, "    .skip %d\n", l.size)
			return
		}
		el := layoutOf(u.Elem)
		stride := alignUp(el.size, el.align)
		// Fast path for byte/u32/u64 arrays: pack cleanly!
		if b, isBasic := u.Elem.Underlying().(*types.Basic); isBasic && (b.Kind == types.U8 || b.Kind == types.U32 || b.Kind == types.U64 || b.Kind == types.I64 || b.Kind == types.Int) {
			raw := make([]byte, l.size)
			for i := uint64(0); i < u.Len && int(i) < len(av.Elems); i++ {
				bits := intValBits(av.Elems[i])
				off := int(i) * int(stride)
				switch el.size {
				case 1:
					raw[off] = byte(bits)
				case 4:
					binary.LittleEndian.PutUint32(raw[off:], uint32(bits))
				case 8:
					binary.LittleEndian.PutUint64(raw[off:], bits)
				}
			}
			emitRawBytes(w, raw)
			return
		}
		for i := uint64(0); i < u.Len; i++ {
			if int(i) < len(av.Elems) {
				g.emitStaticVal(w, u.Elem, av.Elems[i])
			} else {
				fmt.Fprintf(w, "    .skip %d\n", el.size)
			}
			if stride > el.size {
				fmt.Fprintf(w, "    .skip %d\n", stride-el.size)
			}
		}
	case *types.Struct:
		sv, ok := v.(*types.StructVal)
		if !ok {
			fmt.Fprintf(w, "    .skip %d\n", l.size)
			return
		}
		offs, sl := structFieldOffsets(u)
		var pos int64
		for i, f := range u.Fields {
			if offs[i] > pos {
				fmt.Fprintf(w, "    .skip %d\n", offs[i]-pos)
				pos = offs[i]
			}
			fl := layoutOf(f.Type)
			if i < len(sv.Fields) {
				g.emitStaticVal(w, f.Type, sv.Fields[i])
			} else {
				fmt.Fprintf(w, "    .skip %d\n", fl.size)
			}
			pos += fl.size
		}
		if sl.size > pos {
			fmt.Fprintf(w, "    .skip %d\n", sl.size-pos)
		}
	case *types.Enum:
		ev, ok := v.(*types.EnumVal)
		if !ok {
			fmt.Fprintf(w, "    .skip %d\n", l.size)
			return
		}
		el := enumLayoutOf(u)
		switch el.tagSize {
		case 1:
			fmt.Fprintf(w, "    .byte %d\n", ev.Variant)
		case 2:
			fmt.Fprintf(w, "    .short %d\n", ev.Variant)
		case 4:
			fmt.Fprintf(w, "    .long %d\n", ev.Variant)
		}
		pos := el.tagSize
		vr := u.Variants[ev.Variant]
		for pi, pt := range vr.Payload {
			target := el.varOffsets[ev.Variant][pi]
			if target > pos {
				fmt.Fprintf(w, "    .skip %d\n", target-pos)
				pos = target
			}
			pl := layoutOf(pt)
			if pi < len(ev.Payload) {
				g.emitStaticVal(w, pt, ev.Payload[pi])
			} else {
				fmt.Fprintf(w, "    .skip %d\n", pl.size)
			}
			pos += pl.size
		}
		if el.size > pos {
			fmt.Fprintf(w, "    .skip %d\n", el.size-pos)
		}
	default:
		fmt.Fprintf(w, "    .skip %d\n", l.size)
	}
}

func emitRawBytes(w *bytes.Buffer, raw []byte) {
	for i := 0; i < len(raw); i += 16 {
		end := i + 16
		if end > len(raw) {
			end = len(raw)
		}
		w.WriteString("    .byte ")
		for j := i; j < end; j++ {
			if j > i {
				w.WriteString(", ")
			}
			fmt.Fprintf(w, "%d", raw[j])
		}
		w.WriteByte('\n')
	}
}

func intValBits(v types.Value) uint64 {
	iv, ok := v.(types.IntVal)
	if !ok || iv.V == nil {
		return 0
	}
	if iv.V.Sign() < 0 {
		return uint64(iv.V.Int64())
	}
	return iv.V.Uint64()
}

func (g *gen) assemble() []byte {
	var out bytes.Buffer
	out.WriteString("# Generated by finc0 (FinCore v0.1 Native Linux x86_64 — zero libc, zero GC)\n")
	out.WriteString(".text\n\n")
	out.Write(g.textBuf.Bytes())
	if g.rodataBuf.Len() > 0 || g.strBuf.Len() > 0 {
		out.WriteString("\n.section .rodata\n")
		out.Write(g.rodataBuf.Bytes())
		out.Write(g.strBuf.Bytes())
	}
	if g.dataBuf.Len() > 0 {
		out.WriteString("\n.section .data\n")
		out.Write(g.dataBuf.Bytes())
	}
	if g.bssBuf.Len() > 0 {
		out.WriteString("\n.section .bss\n")
		out.Write(g.bssBuf.Bytes())
	}
	out.WriteString("\n.section .note.GNU-stack,\"\",@progbits\n")
	return out.Bytes()
}
