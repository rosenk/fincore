package amd64

import (
	"fmt"

	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// typeLayout describes the concrete Linux x86_64 memory layout of a FinCore type.
type typeLayout struct {
	size  int64
	align int64
}

func alignUp(v, a int64) int64 {
	if a <= 1 {
		return v
	}
	return (v + a - 1) &^ (a - 1)
}

// substType replaces any TypeParam in t according to bind (nil = identity).
func substType(t types.Type, bind map[*types.TypeParam]types.Type) types.Type {
	if t == nil || len(bind) == 0 {
		return t
	}
	switch u := t.(type) {
	case *types.TypeParam:
		if r, ok := bind[u]; ok {
			return r
		}
		return u
	case *types.Named:
		if len(u.TypeArgs) == 0 {
			return u
		}
		changed := false
		args := make([]types.Type, len(u.TypeArgs))
		for i, a := range u.TypeArgs {
			na := substType(a, bind)
			if na != a {
				changed = true
			}
			args[i] = na
		}
		if !changed {
			return u
		}
		// Build a substituted synthetic Named whose underlying is substituted.
		inst := *u
		inst.TypeArgs = args
		// Also substitute the underlying type.
		// Note: we can't set unexported Named.underlying directly; instead
		// substUnderlying handles it via struct/enum/array/slice/etc.
		return &substNamed{orig: u, args: args, bind: bind}
	case *types.Array:
		elem := substType(u.Elem, bind)
		ln := u.Len
		var lp *types.TypeParam
		if u.LenParam != nil {
			if r, ok := bind[u.LenParam]; ok {
				if ca, ok := r.(*types.ConstArg); ok {
					ln = ca.Value
				} else if tp, ok := r.(*types.TypeParam); ok {
					lp = tp
				}
			} else {
				lp = u.LenParam
			}
		}
		if elem == u.Elem && ln == u.Len && lp == u.LenParam {
			return u
		}
		return &types.Array{Len: ln, LenParam: lp, Elem: elem}
	case *types.Slice:
		elem := substType(u.Elem, bind)
		if elem == u.Elem {
			return u
		}
		return &types.Slice{Elem: elem}
	case *types.Pointer:
		elem := substType(u.Elem, bind)
		if elem == u.Elem {
			return u
		}
		return &types.Pointer{Elem: elem}
	case *types.Struct:
		fs := make([]*types.Field, len(u.Fields))
		changed := false
		for i, f := range u.Fields {
			nt := substType(f.Type, bind)
			if nt != f.Type {
				changed = true
			}
			nf := *f
			nf.Type = nt
			fs[i] = &nf
		}
		if !changed {
			return u
		}
		return &types.Struct{Fields: fs}
	case *types.Enum:
		vs := make([]*types.Variant, len(u.Variants))
		changed := false
		for i, v := range u.Variants {
			nv := *v
			nv.Payload = make([]types.Type, len(v.Payload))
			for j, pt := range v.Payload {
				nt := substType(pt, bind)
				if nt != pt {
					changed = true
				}
				nv.Payload[j] = nt
			}
			vs[i] = &nv
		}
		if !changed {
			return u
		}
		return &types.Enum{Variants: vs}
	case *types.Signature:
		ps := make([]types.Type, len(u.Params))
		rs := make([]types.Type, len(u.Results))
		changed := false
		for i, p := range u.Params {
			ps[i] = substType(p, bind)
			if ps[i] != p {
				changed = true
			}
		}
		for i, r := range u.Results {
			rs[i] = substType(r, bind)
			if rs[i] != r {
				changed = true
			}
		}
		if !changed {
			return u
		}
		ns := *u
		ns.Params = ps
		ns.Results = rs
		return &ns
	case *types.Tuple:
		ts := make([]types.Type, len(u.Types))
		changed := false
		for i, x := range u.Types {
			ts[i] = substType(x, bind)
			if ts[i] != x {
				changed = true
			}
		}
		if !changed {
			return u
		}
		return &types.Tuple{Types: ts}
	}
	return t
}

// substNamed is a monomorphized view of a generic *types.Named instantiated
// inside another generic function/method.
type substNamed struct {
	orig *types.Named
	args []types.Type
	bind map[*types.TypeParam]types.Type
}

func (s *substNamed) Underlying() types.Type {
	return substType(s.orig.Underlying(), s.bind)
}

func (s *substNamed) String() string {
	return fmt.Sprintf("%s%v", s.orig.Name, s.args)
}

func originAndArgs(t types.Type) (*types.Named, []types.Type) {
	switch u := t.(type) {
	case *types.Named:
		if u.Origin != nil {
			return u.Origin, u.TypeArgs
		}
		return u, u.TypeArgs
	case *substNamed:
		if u.orig.Origin != nil {
			return u.orig.Origin, u.args
		}
		return u.orig, u.args
	}
	return nil, nil
}

func builtinOfType(t types.Type) resolve.Builtin {
	if n, ok := t.(*types.Named); ok {
		if n.Builtin != 0 {
			return n.Builtin
		}
		if n.Origin != nil {
			return n.Origin.Builtin
		}
	}
	if s, ok := t.(*substNamed); ok {
		return builtinOfType(s.orig)
	}
	return resolve.BNone
}

// layoutOf returns (size, align) for a concrete FinCore type on Linux x86_64.
func layoutOf(t types.Type) typeLayout {
	if t == nil {
		return typeLayout{0, 1}
	}
	switch builtinOfType(t) {
	case resolve.TNet, resolve.TSignals, resolve.TClock, resolve.TFS, resolve.TEnv, resolve.TStdio, resolve.TExec:
		return typeLayout{0, 1}
	case resolve.TListener:
		return typeLayout{16, 8} // {fd int64, packed_ip_port uint64}
	case resolve.TConn:
		return typeLayout{8, 8} // {fd int64}
	case resolve.TContext:
		return typeLayout{24, 8} // {state_ptr, deadline_ns, reserved}
	case resolve.TCancelFunc:
		return typeLayout{16, 8} // closure {fn_ptr, env_ptr}
	case resolve.TScope:
		return typeLayout{8, 8} // *fc_scope
	case resolve.TSlot:
		return typeLayout{16, 8} // {idx int64, buf_ptr *T}
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch u.Kind {
		case types.Bool, types.U8, types.I8, types.UntypedBool, types.UntypedChar:
			return typeLayout{1, 1}
		case types.U16, types.I16:
			return typeLayout{2, 2}
		case types.U32, types.I32:
			return typeLayout{4, 4}
		case types.U64, types.I64, types.Int, types.Usize, types.Error, types.UntypedInt:
			return typeLayout{8, 8}
		case types.String, types.UntypedString:
			return typeLayout{16, 8}
		case types.UntypedNil:
			return typeLayout{8, 8}
		}
	case *types.Pointer:
		return typeLayout{8, 8}
	case *types.Slice:
		return typeLayout{24, 8} // {ptr, len, cap}
	case *types.Signature:
		return typeLayout{16, 8} // {fn_ptr, env_ptr}
	case *types.Array:
		el := layoutOf(u.Elem)
		stride := alignUp(el.size, el.align)
		return typeLayout{int64(u.Len) * stride, el.align}
	case *types.Struct:
		_, l := structFieldOffsets(u)
		return l
	case *types.Enum:
		el := enumLayoutOf(u)
		return typeLayout{el.size, el.align}
	}
	panic(fmt.Sprintf("amd64: cannot lay out type %T (%s)", t, t))
}

func structFieldOffsets(st *types.Struct) ([]int64, typeLayout) {
	offs := make([]int64, len(st.Fields))
	var off int64
	maxAlign := int64(1)
	for i, f := range st.Fields {
		fl := layoutOf(f.Type)
		if fl.align > maxAlign {
			maxAlign = fl.align
		}
		off = alignUp(off, fl.align)
		offs[i] = off
		off += fl.size
	}
	return offs, typeLayout{alignUp(off, maxAlign), maxAlign}
}

type enumLayout struct {
	tagSize    int64 // 1, 2, or 4
	payloadOff int64
	size       int64
	align      int64
	// varOffsets[v][i] is the byte offset of payload i of variant v from enum start.
	varOffsets [][]int64
}

func enumLayoutOf(en *types.Enum) enumLayout {
	tagSize := int64(1)
	if len(en.Variants) > 65536 {
		tagSize = 4
	} else if len(en.Variants) > 256 {
		tagSize = 2
	}
	maxAlign := tagSize
	var maxPaySize int64
	varOffsets := make([][]int64, len(en.Variants))
	// First pass: find max align across all payloads
	for _, v := range en.Variants {
		for _, pt := range v.Payload {
			pl := layoutOf(pt)
			if pl.align > maxAlign {
				maxAlign = pl.align
			}
		}
	}
	payloadOff := alignUp(tagSize, maxAlign)
	// Second pass: lay out each variant's payload fields starting at payloadOff
	for vi, v := range en.Variants {
		offs := make([]int64, len(v.Payload))
		off := payloadOff
		for pi, pt := range v.Payload {
			pl := layoutOf(pt)
			off = alignUp(off, pl.align)
			offs[pi] = off
			off += pl.size
		}
		varOffsets[vi] = offs
		if ps := off - payloadOff; ps > maxPaySize {
			maxPaySize = ps
		}
	}
	total := alignUp(payloadOff+maxPaySize, maxAlign)
	if total == 0 {
		total = 1
	}
	return enumLayout{
		tagSize:    tagSize,
		payloadOff: payloadOff,
		size:       total,
		align:      maxAlign,
		varOffsets: varOffsets,
	}
}

// callABI describes the exact stack layout of a FinCore function call.
// Caller allocates `totalSize` (16-byte aligned) on stack before `call`.
// Inside callee, all offsets are shifted by +16 from %rbp (saved rbp + ret addr).
type callABI struct {
	hasRecv   bool
	recvOff   int64
	argOffs   []int64
	retOffs   []int64
	totalSize int64
}

func computeCallABI(recvType types.Type, params, results []types.Type) callABI {
	var abi callABI
	var off int64
	if recvType != nil {
		abi.hasRecv = true
		rl := layoutOf(recvType)
		off = alignUp(off, rl.align)
		abi.recvOff = off
		off += rl.size
	}
	abi.argOffs = make([]int64, len(params))
	for i, p := range params {
		pl := layoutOf(p)
		off = alignUp(off, pl.align)
		abi.argOffs[i] = off
		off += pl.size
	}
	abi.retOffs = make([]int64, len(results))
	for i, r := range results {
		rl := layoutOf(r)
		off = alignUp(off, rl.align)
		abi.retOffs[i] = off
		off += rl.size
	}
	abi.totalSize = alignUp(off, 16)
	return abi
}

// isScalar reports whether t fits in a single 64-bit GP register (<=8 bytes, scalar/ptr/err/conn/scope).
func isScalar(t types.Type) bool {
	if t == nil {
		return false
	}
	switch builtinOfType(t) {
	case resolve.TConn, resolve.TScope:
		return true
	case resolve.TNet, resolve.TSignals, resolve.TClock, resolve.TFS, resolve.TEnv, resolve.TStdio, resolve.TExec,
		resolve.TListener, resolve.TContext, resolve.TCancelFunc, resolve.TSlot:
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Kind != types.String && u.Kind != types.UntypedString
	case *types.Pointer:
		return true
	}
	return false
}

func isSignedInt(t types.Type) bool {
	if b, ok := t.Underlying().(*types.Basic); ok {
		switch b.Kind {
		case types.I8, types.I16, types.I32, types.I64, types.Int:
			return true
		}
	}
	return false
}

// errSentinelID maps predeclared error sentinel names to runtime uint64 IDs
// (must match runtime/linux_amd64/sys.s).
func errSentinelID(name string) uint64 {
	switch name {
	case "", "nil":
		return 0
	case "ErrEOF":
		return 1
	case "ErrTimeout":
		return 2
	case "ErrCancelled":
		return 3
	case "ErrZeroWrite":
		return 4
	case "ErrClosed":
		return 5
	case "ErrIO":
		return 6
	case "ErrNotFound":
		return 7
	case "ErrMaxDepth":
		return 8
	case "ErrArenaFull":
		return 9
	case "ErrExec":
		return 10
	case "ErrParse":
		return 11
	}
	return 6
}
