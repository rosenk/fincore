package amd64

import (
	"bytes"
	"fmt"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// ---------------------------------------------------------------------------
// Call dispatch

func (f *fnGen) emitCallDiscard(e *ast.CallExpr) {
	ci := f.g.info.CallOf(e)
	if ci == nil {
		return
	}
	if ci.Sig != nil && len(ci.Sig.Results) > 0 {
		if len(ci.Sig.Results) == 1 {
			rt := f.subst(ci.Sig.Results[0])
			tmp := f.allocLocal(layoutOf(rt).size, layoutOf(rt).align)
			f.emitSingleCallTo(e, tmp, rt)
			return
		}
		dts := make([]types.Type, len(ci.Sig.Results))
		dsts := make([]int64, len(ci.Sig.Results))
		for i, r := range ci.Sig.Results {
			dts[i] = f.subst(r)
			l := layoutOf(dts[i])
			dsts[i] = f.allocLocal(l.size, l.align)
		}
		f.emitMultiCallTo(e, dsts, dts)
		return
	}
	f.emitMultiCallTo(e, nil, nil)
}

func (f *fnGen) emitSingleCallTo(e *ast.CallExpr, dstOff int64, wantType types.Type) {
	f.emitMultiCallTo(e, []int64{dstOff}, []types.Type{wantType})
}

// emitMultiCallTo evaluates call expression e and stores its N results into dsts[i](%rbp).
// dsts[i] == 0 means discard result i.
func (f *fnGen) emitMultiCallTo(e *ast.CallExpr, dsts []int64, dts []types.Type) {
	ci := f.g.info.CallOf(e)
	if ci == nil {
		f.g.fail(e, "missing call info")
	}
	switch ci.Kind {
	case types.CallConversion:
		f.emitConversion(e, dsts[0], f.subst(ci.Conv))
	case types.CallVariant:
		f.emitVariantCtor(e, ci, dsts[0], dts[0])
	case types.CallBuiltin:
		f.emitBuiltinCall(e, ci, dsts, dts)
	case types.CallBuiltinMeth:
		f.emitBuiltinMethodCall(e, ci, dsts, dts)
	case types.CallFunc, types.CallMethod, types.CallValue:
		f.emitUserCall(e, ci, dsts, dts)
	default:
		f.g.fail(e, "unsupported call kind %d", ci.Kind)
	}
}

func (f *fnGen) emitConversion(e *ast.CallExpr, dstOff int64, target types.Type) {
	src := e.Args[0]
	st := f.typeOf(src)
	if types.IsString(target) {
		f.emitExprTo(src, dstOff, target)
		return
	}
	if _, isPtr := target.Underlying().(*types.Pointer); isPtr {
		f.emitScalar(src, st)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff)
		return
	}
	if isScalar(target) {
		f.emitScalar(src, st)
		f.emitMaskScalar(target)
		f.emitStoreScalarToFrame(dstOff, target)
		return
	}
	// Underlying-identical composite conversion (named struct/array/slice/enum)
	f.emitExprTo(src, dstOff, target)
}

func (f *fnGen) emitVariantCtor(e *ast.CallExpr, ci *types.Call, dstOff int64, wantType types.Type) {
	et := f.subst(ci.VariantType)
	en := et.Underlying().(*types.Enum)
	el := enumLayoutOf(en)
	vr := en.Variants[ci.Variant.Index]
	f.emitZero(dstOff, el.size)
	switch el.tagSize {
	case 1:
		fmt.Fprintf(&f.body, "    movb $%d, %d(%%rbp)\n", vr.Index, dstOff)
	case 2:
		fmt.Fprintf(&f.body, "    movw $%d, %d(%%rbp)\n", vr.Index, dstOff)
	case 4:
		fmt.Fprintf(&f.body, "    movl $%d, %d(%%rbp)\n", vr.Index, dstOff)
	}
	for i, a := range e.Args {
		pt := f.subst(vr.Payload[i])
		f.emitExprTo(a, dstOff+el.varOffsets[vr.Index][i], pt)
	}
}

func (f *fnGen) emitUserCall(e *ast.CallExpr, ci *types.Call, dsts []int64, dts []types.Type) {
	sig := f.subst(ci.Sig).(*types.Signature)
	var recvType types.Type
	var recvSlot int64
	if ci.Kind == types.CallMethod {
		recvType = f.subst(ci.RecvType)
		rl := layoutOf(recvType)
		recvSlot = f.allocLocal(rl.size, rl.align)
		if ci.AutoAddr {
			f.emitAddr(ci.Recv)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", recvSlot)
		} else {
			f.emitExprTo(ci.Recv, recvSlot, recvType)
		}
	}
	var fnValSlot int64
	if ci.Kind == types.CallValue {
		fnValSlot = f.allocLocal(16, 8)
		f.emitExprTo(e.Fun, fnValSlot, sig)
	}

	// Evaluate arguments (handling multi-result sole argument f(g()) too!).
	nParams := len(sig.Params)
	argSlots := make([]int64, nParams)
	for i, pt := range sig.Params {
		pl := layoutOf(pt)
		argSlots[i] = f.allocLocal(pl.size, pl.align)
	}
	if len(e.Args) == 1 && nParams > 1 {
		if call, ok := unparen(e.Args[0]).(*ast.CallExpr); ok {
			f.emitMultiCallTo(call, argSlots, sig.Params)
		}
	} else {
		for i, a := range e.Args {
			f.emitExprTo(a, argSlots[i], sig.Params[i])
		}
	}

	abi := computeCallABI(recvType, sig.Params, sig.Results)
	if abi.totalSize > 0 {
		fmt.Fprintf(&f.body, "    subq $%d, %%rsp\n", abi.totalSize)
	}
	// Copy receiver + args into outgoing stack area.
	if recvType != nil {
		f.emitCopyFrameToRSP(abi.recvOff, recvSlot, layoutOf(recvType).size)
	}
	for i, pt := range sig.Params {
		f.emitCopyFrameToRSP(abi.argOffs[i], argSlots[i], layoutOf(pt).size)
	}

	switch ci.Kind {
	case types.CallFunc:
		fi := f.g.info.FuncOfSym(ci.Callee)
		targs := make([]types.Type, len(ci.TypeArgs))
		for i, a := range ci.TypeArgs {
			targs[i] = f.subst(a)
		}
		lbl := f.g.requestFunc(fi, targs)
		fmt.Fprintf(&f.body, "    xorq %%r12, %%r12\n")
		fmt.Fprintf(&f.body, "    call %s\n", lbl)
	case types.CallMethod:
		fi := f.g.info.FuncOfSym(ci.Callee)
		targs := make([]types.Type, len(ci.TypeArgs))
		for i, a := range ci.TypeArgs {
			targs[i] = f.subst(a)
		}
		if len(targs) == 0 && len(fi.TypeParams) > 0 {
			base, _ := types.Deref(recvType)
			_, ta := originAndArgs(base)
			targs = ta
		}
		lbl := f.g.requestFunc(fi, targs)
		fmt.Fprintf(&f.body, "    xorq %%r12, %%r12\n")
		fmt.Fprintf(&f.body, "    call %s\n", lbl)
	case types.CallValue:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", fnValSlot)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%r12\n", fnValSlot+8)
		fmt.Fprintf(&f.body, "    call *%%rax\n")
	}

	// Copy return values out before restoring %rsp.
	for i, rt := range sig.Results {
		if i < len(dsts) && dsts[i] != 0 {
			f.emitCopyRSPToFrame(dsts[i], abi.retOffs[i], layoutOf(rt).size)
		}
	}
	if abi.totalSize > 0 {
		fmt.Fprintf(&f.body, "    addq $%d, %%rsp\n", abi.totalSize)
	}
}

func (f *fnGen) emitCopyFrameToRSP(rspOff, srcOff, size int64) {
	if size == 0 {
		return
	}
	if size <= 32 {
		var p int64
		for p+8 <= size {
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", srcOff+p)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rsp)\n", rspOff+p)
			p += 8
		}
		for p < size {
			fmt.Fprintf(&f.body, "    movzbl %d(%%rbp), %%eax\n", srcOff+p)
			fmt.Fprintf(&f.body, "    movb %%al, %d(%%rsp)\n", rspOff+p)
			p++
		}
		return
	}
	fmt.Fprintf(&f.body, "    leaq %d(%%rsp), %%rdi\n", rspOff)
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rsi\n", srcOff)
	fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", size)
	fmt.Fprintf(&f.body, "    cld\n")
	fmt.Fprintf(&f.body, "    rep movsb\n")
}

func (f *fnGen) emitCopyRSPToFrame(dstOff, rspOff, size int64) {
	if size == 0 {
		return
	}
	if size <= 32 {
		var p int64
		for p+8 <= size {
			fmt.Fprintf(&f.body, "    movq %d(%%rsp), %%rax\n", rspOff+p)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff+p)
			p += 8
		}
		for p < size {
			fmt.Fprintf(&f.body, "    movzbl %d(%%rsp), %%eax\n", rspOff+p)
			fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dstOff+p)
			p++
		}
		return
	}
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", dstOff)
	fmt.Fprintf(&f.body, "    leaq %d(%%rsp), %%rsi\n", rspOff)
	fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", size)
	fmt.Fprintf(&f.body, "    cld\n")
	fmt.Fprintf(&f.body, "    rep movsb\n")
}

// ---------------------------------------------------------------------------
// Deferred call invocation (at function epilogue)

func (f *fnGen) emitDeferredCall(w *bytes.Buffer, ds *deferSlot) {
	// Temporarily swap f.body to emit into w!
	saved := f.body
	f.body = bytes.Buffer{}
	ci := ds.callInfo
	switch ci.Kind {
	case types.CallFunc, types.CallMethod, types.CallValue:
		sig := f.subst(ci.Sig).(*types.Signature)
		var recvType types.Type
		if ci.Kind == types.CallMethod {
			recvType = ds.recvType
		}
		abi := computeCallABI(recvType, sig.Params, sig.Results)
		if abi.totalSize > 0 {
			fmt.Fprintf(&f.body, "    subq $%d, %%rsp\n", abi.totalSize)
		}
		if recvType != nil {
			f.emitCopyFrameToRSP(abi.recvOff, ds.recvOff, layoutOf(recvType).size)
		}
		for i, pt := range sig.Params {
			f.emitCopyFrameToRSP(abi.argOffs[i], ds.argOffs[i], layoutOf(pt).size)
		}
		switch ci.Kind {
		case types.CallFunc:
			fi := f.g.info.FuncOfSym(ci.Callee)
			targs := make([]types.Type, len(ci.TypeArgs))
			for i, a := range ci.TypeArgs {
				targs[i] = f.subst(a)
			}
			lbl := f.g.requestFunc(fi, targs)
			fmt.Fprintf(&f.body, "    xorq %%r12, %%r12\n")
			fmt.Fprintf(&f.body, "    call %s\n", lbl)
		case types.CallMethod:
			fi := f.g.info.FuncOfSym(ci.Callee)
			targs := make([]types.Type, len(ci.TypeArgs))
			for i, a := range ci.TypeArgs {
				targs[i] = f.subst(a)
			}
			if len(targs) == 0 && len(fi.TypeParams) > 0 {
				base, _ := types.Deref(recvType)
				_, ta := originAndArgs(base)
				targs = ta
			}
			lbl := f.g.requestFunc(fi, targs)
			fmt.Fprintf(&f.body, "    xorq %%r12, %%r12\n")
			fmt.Fprintf(&f.body, "    call %s\n", lbl)
		case types.CallValue:
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", ds.recvOff)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%r12\n", ds.recvOff+8)
			fmt.Fprintf(&f.body, "    call *%%rax\n")
		}
		if abi.totalSize > 0 {
			fmt.Fprintf(&f.body, "    addq $%d, %%rsp\n", abi.totalSize)
		}
	case types.CallBuiltinMeth:
		f.emitBuiltinMethodFromSlots(ci.Method.Builtin, ds.recvOff, ds.argOffs, nil)
	}
	w.Write(f.body.Bytes())
	f.body = saved
}

// ---------------------------------------------------------------------------
// Predeclared Builtins

func (f *fnGen) emitBuiltinCall(e *ast.CallExpr, ci *types.Call, dsts []int64, dts []types.Type) {
	switch ci.Builtin {
	case resolve.FLen, resolve.FCap:
		at := f.typeOf(e.Args[0])
		switch u := at.Underlying().(type) {
		case *types.Array:
			fmt.Fprintf(&f.body, "    movq $%d, %d(%%rbp)\n", u.Len, dsts[0])
		case *types.Pointer:
			arr := u.Elem.Underlying().(*types.Array)
			fmt.Fprintf(&f.body, "    movq $%d, %d(%%rbp)\n", arr.Len, dsts[0])
		case *types.Slice:
			sl := f.allocLocal(24, 8)
			f.emitExprTo(e.Args[0], sl, at)
			if ci.Builtin == resolve.FCap {
				fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", sl+16)
			} else {
				fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", sl+8)
			}
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		case *types.Basic: // string
			ss := f.allocLocal(16, 8)
			f.emitExprTo(e.Args[0], ss, at)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", ss+8)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}

	case resolve.FCopy:
		dt := f.typeOf(e.Args[0])
		sl0 := f.allocLocal(24, 8)
		sl1 := f.allocLocal(24, 8)
		f.emitExprTo(e.Args[0], sl0, dt)
		st := f.typeOf(e.Args[1])
		if f.g.info.IsImplicitBytes(e.Args[1]) {
			st = &types.Slice{Elem: types.Typ[types.U8]}
		}
		f.emitExprTo(e.Args[1], sl1, st)
		el := layoutOf(dt.Underlying().(*types.Slice).Elem)
		stride := alignUp(el.size, el.align)
		// n = min(len(dst), len(src))
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", sl0+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", sl1+8)
		fmt.Fprintf(&f.body, "    cmpq %%rcx, %%rax\n")
		okLbl := f.g.freshLabel("cp_min")
		fmt.Fprintf(&f.body, "    jbe %s\n", okLbl)
		fmt.Fprintf(&f.body, "    movq %%rcx, %%rax\n")
		fmt.Fprintf(&f.body, "%s:\n", okLbl)
		nSlot := f.allocLocal(8, 8)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", nSlot)
		fmt.Fprintf(&f.body, "    imulq $%d, %%rax, %%rdx\n", stride)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", sl0)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", sl1)
		fmt.Fprintf(&f.body, "    call _fc_rt_memmove\n")
		if len(dsts) > 0 && dsts[0] != 0 {
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", nSlot)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}

	case resolve.FBytesEqual:
		bt := &types.Slice{Elem: types.Typ[types.U8]}
		s0 := f.allocLocal(24, 8)
		s1 := f.allocLocal(24, 8)
		f.emitExprTo(e.Args[0], s0, bt)
		f.emitExprTo(e.Args[1], s1, bt)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", s0)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", s0+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", s1)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", s1+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_bytes_eq\n")
		fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dsts[0])

	case resolve.FStrBytes:
		ss := f.allocLocal(16, 8)
		f.emitExprTo(e.Args[0], ss, types.Typ[types.String])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", ss)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", ss+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_str_bytes\n")
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+8)
		fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+16)

	case resolve.FBytesToStr:
		bt := &types.Slice{Elem: types.Typ[types.U8]}
		bs := f.allocLocal(24, 8)
		f.emitExprTo(e.Args[0], bs, bt)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", bs)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", bs+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_bytes_to_str\n")
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+8)

	case resolve.FFormatUint:
		vSlot := f.allocLocal(8, 8)
		f.emitScalar(e.Args[0], types.Typ[types.U64])
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", vSlot)
		bs := f.allocLocal(24, 8)
		f.emitExprTo(e.Args[1], bs, &types.Slice{Elem: types.Typ[types.U8]})
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", vSlot)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", bs)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", bs+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_format_uint\n")
		if len(dsts) > 0 && dsts[0] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if len(dsts) > 1 && dsts[1] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}

	case resolve.FParseUint:
		bs := f.allocLocal(24, 8)
		f.emitExprTo(e.Args[0], bs, &types.Slice{Elem: types.Typ[types.U8]})
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", bs)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", bs+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_parse_uint\n")
		if len(dsts) > 0 && dsts[0] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if len(dsts) > 1 && dsts[1] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}

	case resolve.FNewContext:
		fmt.Fprintf(&f.body, "    call _fc_rt_new_ctx\n")
		// dsts[0]: Context (24 bytes: {state_ptr, 0, 0})
		if len(dsts) > 0 && dsts[0] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
			fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dsts[0]+8)
			fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dsts[0]+16)
		}
		// dsts[1]: CancelFunc (16 bytes: {_fc_rt_cancel_fn, state_ptr})
		if len(dsts) > 1 && dsts[1] != 0 {
			fmt.Fprintf(&f.body, "    leaq _fc_rt_cancel_fn(%%rip), %%rcx\n")
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dsts[1])
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[1]+8)
		}

	case resolve.FNewScope:
		f.emitScalar(e.Args[0], types.Typ[types.Usize])
		fmt.Fprintf(&f.body, "    movq %%rax, %%rdi\n")
		fmt.Fprintf(&f.body, "    call _fc_rt_scope_new\n")
		if len(dsts) > 0 && dsts[0] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}

	case resolve.FPanic:
		ss := f.allocLocal(16, 8)
		f.emitExprTo(e.Args[0], ss, types.Typ[types.String])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", ss)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", ss+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_panic\n")

	case resolve.FFormatTo:
		f.emitFormatTo(e, ci, dsts)

	default:
		f.g.fail(e, "unsupported builtin %s", ci.Builtin)
	}
}

func (f *fnGen) emitFormatTo(e *ast.CallExpr, ci *types.Call, dsts []int64) {
	fi := ci.Format
	// Evaluate dst and all arguments left-to-right first!
	dstSl := f.allocLocal(24, 8)
	f.emitExprTo(e.Args[0], dstSl, &types.Slice{Elem: types.Typ[types.U8]})
	argSlots := make([]int64, len(fi.Args))
	argTypes := make([]types.Type, len(fi.Args))
	for i := range fi.Args {
		a := e.Args[2+i]
		at := f.typeOf(a)
		if f.g.info.IsImplicitBytes(a) {
			at = &types.Slice{Elem: types.Typ[types.U8]}
		}
		l := layoutOf(at)
		argSlots[i] = f.allocLocal(l.size, l.align)
		argTypes[i] = at
		f.emitExprTo(a, argSlots[i], at)
	}
	// Allocate fc_fmt (88 bytes) on stack
	fmtSlot := f.allocLocal(88, 8)
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", dstSl)
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", dstSl+8)
	fmt.Fprintf(&f.body, "    call _fc_rt_fmt_init\n")

	k := 0
	for _, p := range fi.Pieces {
		if p.Verb == 0 {
			if len(p.Lit) > 0 {
				slbl := f.g.internStr(p.Lit)
				fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
				fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rsi\n", slbl)
				fmt.Fprintf(&f.body, "    movq $%d, %%rdx\n", len(p.Lit))
				fmt.Fprintf(&f.body, "    call _fc_rt_fmt_lit\n")
			}
			continue
		}
		fa := fi.Args[k]
		aSlot := argSlots[k]
		aType := argTypes[k]
		k++
		w := int64(p.FormatWidth())
		z := int64(0)
		if p.Zero {
			z = 1
		}
		switch fa.Op {
		case types.FmtInt:
			f.emitLoadScalarFromFrame(aSlot, aType)
			fmt.Fprintf(&f.body, "    movq %%rax, %%rsi\n")
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq $%d, %%rdx\n", w)
			fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", z)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_int\n")
		case types.FmtHexInt:
			f.emitLoadScalarFromFrame(aSlot, aType)
			fmt.Fprintf(&f.body, "    movq %%rax, %%rsi\n")
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq $%d, %%rdx\n", w)
			fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", z)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_hex_int\n")
		case types.FmtUint:
			f.emitLoadScalarFromFrame(aSlot, aType)
			fmt.Fprintf(&f.body, "    movq %%rax, %%rsi\n")
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq $%d, %%rdx\n", w)
			fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", z)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_uint\n")
		case types.FmtHexUint:
			f.emitLoadScalarFromFrame(aSlot, aType)
			fmt.Fprintf(&f.body, "    movq %%rax, %%rsi\n")
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq $%d, %%rdx\n", w)
			fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", z)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_hex\n")
		case types.FmtStr, types.FmtBytes:
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", aSlot)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", aSlot+8)
			fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", w)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_str\n")
		case types.FmtQuoteStr, types.FmtQuoteBytes:
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", aSlot)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", aSlot+8)
			fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", w)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_quote\n")
		case types.FmtByte:
			f.emitLoadScalarFromFrame(aSlot, aType)
			fmt.Fprintf(&f.body, "    movq %%rax, %%rsi\n")
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq $%d, %%rdx\n", w)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_byte\n")
		case types.FmtBool:
			f.emitLoadScalarFromFrame(aSlot, aType)
			fmt.Fprintf(&f.body, "    movq %%rax, %%rsi\n")
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq $%d, %%rdx\n", w)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_bool\n")
		case types.FmtFormat:
			inScSlot := f.allocLocal(8, 8)
			subSlice := f.allocLocal(24, 8)
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq $%d, %%rsi\n", w)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_dst\n")
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", subSlice)
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", subSlice+8)
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", subSlice+16)
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", inScSlot)
			// Invoke T.Format(dst []byte) usize
			mfi := f.g.info.FuncOfSym(fa.Method.Sym)
			_, targs := originAndArgs(aType)
			mlbl := f.g.requestFunc(mfi, targs)
			bytesT := &types.Slice{Elem: types.Typ[types.U8]}
			abi := computeCallABI(aType, []types.Type{bytesT}, []types.Type{types.Typ[types.Usize]})
			if abi.totalSize > 0 {
				fmt.Fprintf(&f.body, "    subq $%d, %%rsp\n", abi.totalSize)
			}
			f.emitCopyFrameToRSP(abi.recvOff, aSlot, layoutOf(aType).size)
			f.emitCopyFrameToRSP(abi.argOffs[0], subSlice, 24)
			fmt.Fprintf(&f.body, "    xorq %%r12, %%r12\n")
			fmt.Fprintf(&f.body, "    call %s\n", mlbl)
			fmt.Fprintf(&f.body, "    movq %d(%%rsp), %%rsi\n", abi.retOffs[0])
			if abi.totalSize > 0 {
				fmt.Fprintf(&f.body, "    addq $%d, %%rsp\n", abi.totalSize)
			}
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
			fmt.Fprintf(&f.body, "    movq $%d, %%rdx\n", w)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", inScSlot)
			fmt.Fprintf(&f.body, "    call _fc_rt_fmt_advance\n")
		}
	}
	if len(dsts) > 0 && dsts[0] != 0 {
		fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", fmtSlot)
		fmt.Fprintf(&f.body, "    call _fc_rt_fmt_n\n")
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
	}
}

// ---------------------------------------------------------------------------
// Capability & Resource Builtin Methods

func (f *fnGen) emitBuiltinMethodCall(e *ast.CallExpr, ci *types.Call, dsts []int64, dts []types.Type) {
	bm := ci.Method.Builtin
	rt := f.subst(ci.RecvType)
	rl := layoutOf(rt)
	recvOff := f.allocLocal(rl.size, rl.align)
	if rl.size > 0 {
		f.emitExprTo(ci.Recv, recvOff, rt)
	}

	// Special cases with generic/variadic arguments
	switch bm {
	case types.SignalsNotifyContext:
		maskSlot := f.allocLocal(8, 8)
		fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", maskSlot)
		for _, a := range e.Args {
			f.emitScalar(a, f.typeOf(a))
			fmt.Fprintf(&f.body, "    movq %%rax, %%rcx\n")
			fmt.Fprintf(&f.body, "    movq $1, %%rax\n")
			fmt.Fprintf(&f.body, "    shlq %%cl, %%rax\n")
			fmt.Fprintf(&f.body, "    orq %%rax, %d(%%rbp)\n", maskSlot)
		}
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", maskSlot)
		fmt.Fprintf(&f.body, "    call _fc_rt_sig_notify\n")
		if len(dsts) > 0 && dsts[0] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
			fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dsts[0]+8)
			fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dsts[0]+16)
		}
		if len(dsts) > 1 && dsts[1] != 0 {
			fmt.Fprintf(&f.body, "    leaq _fc_rt_cancel_fn(%%rip), %%rcx\n")
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dsts[1])
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[1]+8)
		}
		return

	case types.ScopeAcquireSlot:
		ctxSlot := f.allocLocal(24, 8)
		f.emitExprTo(e.Args[0], ctxSlot, f.typeOf(e.Args[0]))
		poolPtrSlot := f.allocLocal(8, 8)
		pt := f.typeOf(e.Args[1])
		f.emitScalar(e.Args[1], pt)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", poolPtrSlot)
		arr := pt.Underlying().(*types.Pointer).Elem.Underlying().(*types.Array)
		el := layoutOf(arr.Elem)
		stride := alignUp(el.size, el.align)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", ctxSlot)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", ctxSlot+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", poolPtrSlot)
		fmt.Fprintf(&f.body, "    movq $%d, %%r8\n", arr.Len)
		fmt.Fprintf(&f.body, "    movq $%d, %%r9\n", stride)
		fmt.Fprintf(&f.body, "    call _fc_rt_scope_acquire\n")
		if len(dsts) > 0 && dsts[0] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])   // slot_idx
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+8) // buf_ptr
		}
		if len(dsts) > 1 && dsts[1] != 0 {
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dsts[1]) // err
		}
		return

	case types.ScopeGo:
		slotOff := f.allocLocal(16, 8)
		f.emitExprTo(e.Args[0], slotOff, f.typeOf(e.Args[0]))
		fnOff := f.allocLocal(16, 8)
		f.emitExprTo(e.Args[1], fnOff, f.typeOf(e.Args[1]))
		var envSize int64
		if lit, ok := unparen(e.Args[1]).(*ast.FuncLit); ok {
			envSize = f.litEnvSize[lit]
		}
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)   // scope
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", slotOff)   // slot_idx
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", slotOff+8) // buf_ptr
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", fnOff)     // fn_ptr
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%r8\n", fnOff+8)    // env_ptr
		fmt.Fprintf(&f.body, "    movq $%d, %%r9\n", envSize)          // env_size
		fmt.Fprintf(&f.body, "    call _fc_rt_scope_go\n")
		return
	}

	// Evaluate standard args into frame slots
	argOffs := make([]int64, len(e.Args))
	for i, a := range e.Args {
		at := f.typeOf(a)
		if f.g.info.IsImplicitBytes(a) {
			at = &types.Slice{Elem: types.Typ[types.U8]}
		}
		l := layoutOf(at)
		argOffs[i] = f.allocLocal(l.size, l.align)
		f.emitExprTo(a, argOffs[i], at)
	}
	f.emitBuiltinMethodFromSlots(bm, recvOff, argOffs, dsts)
}

func (f *fnGen) emitBuiltinMethodFromSlots(bm types.BuiltinMethod, recvOff int64, argOffs []int64, dsts []int64) {
	hasDst := func(i int) bool { return i < len(dsts) && dsts[i] != 0 }
	switch bm {
	case types.ContextDone:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", recvOff+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_ctx_done\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dsts[0])
		}
	case types.ContextErr:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", recvOff+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_ctx_err\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
	case types.ContextWithTimeout:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    call _fc_rt_ctx_with_timeout\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", recvOff)
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dsts[0])
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0]+8)
			fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dsts[0]+16)
		}

	case types.NetListenTCP:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_net_listen_tcp\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+8)
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dsts[1])
		}
	case types.NetDialTCP:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", argOffs[1])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", argOffs[1]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_net_dial_tcp\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}
	case types.ListenerAccept:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_listener_accept\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}
	case types.ListenerAddr:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_listener_addr\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+8)
		}
	case types.ListenerClose, types.ConnClose:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    call _fc_rt_close_fd\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
	case types.ConnRead:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", argOffs[1])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%r8\n", argOffs[1]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_conn_read\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}
	case types.ConnWrite:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", argOffs[1])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%r8\n", argOffs[1]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_conn_write\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}
	case types.ConnCloseWrite:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    call _fc_rt_conn_close_write\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}

	case types.ClockNow:
		fmt.Fprintf(&f.body, "    call _fc_rt_clock_now\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
	case types.ClockSleep:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", argOffs[1])
		fmt.Fprintf(&f.body, "    call _fc_rt_clock_sleep\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}

	case types.ScopeReleaseSlot:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    call _fc_rt_scope_release\n")
	case types.ScopeWaitTimeout:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    call _fc_rt_scope_wait_timeout\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dsts[0])
		}
	case types.ScopeWait:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", recvOff)
		fmt.Fprintf(&f.body, "    call _fc_rt_scope_wait\n")

	case types.FSReadFileInto:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", argOffs[1])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", argOffs[1]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_fs_read_file_into\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}
	case types.FSWriteFile:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", argOffs[1])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", argOffs[1]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_fs_write_file\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
	case types.FSIsDir:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_fs_is_dir\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dsts[0])
		}
	case types.FSListSourceFiles:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", argOffs[1])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", argOffs[1]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_fs_list_fc\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}
	case types.FSMkdirTemp:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_fs_mkdir_temp\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+8)
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dsts[1])
		}
	case types.FSRemoveAll:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_fs_remove_all\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}

	case types.EnvArgsCount:
		fmt.Fprintf(&f.body, "    call _fc_rt_args_count\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
	case types.EnvArg:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    call _fc_rt_arg\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+8)
		}
	case types.EnvGetEnv:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_getenv\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[0]+8)
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movb %%cl, %d(%%rbp)\n", dsts[1])
		}

	case types.StdioReadStdin:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_read_stdin\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}
	case types.StdioWriteStdout:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_write_stdout\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
	case types.StdioWriteStderr:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", argOffs[0])
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", argOffs[0]+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_write_stderr\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}

	case types.ExecRun:
		// Pack arg struct (96 bytes) on frame
		pkt := f.allocLocal(96, 8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[0], pkt+0)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[0]+8, pkt+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[1], pkt+16)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[1]+8, pkt+24)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[2], pkt+32)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[2]+8, pkt+40)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[3], pkt+48)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[3]+8, pkt+56)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[4], pkt+64)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[4]+8, pkt+72)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[5], pkt+80)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n    movq %%rax, %d(%%rbp)\n", argOffs[5]+8, pkt+88)
		fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", pkt)
		fmt.Fprintf(&f.body, "    call _fc_rt_exec_run\n")
		if hasDst(0) {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dsts[0])
		}
		if hasDst(1) {
			fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dsts[1])
		}
		if hasDst(2) {
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dsts[2])
		}
	}
}
