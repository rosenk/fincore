package amd64

import (
	"fmt"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

// ---------------------------------------------------------------------------
// Low-level frame/memory helpers

func (f *fnGen) emitZero(off, size int64) {
	if size == 0 {
		return
	}
	if size <= 64 {
		var p int64
		for p+8 <= size {
			fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", off+p)
			p += 8
		}
		for p < size {
			fmt.Fprintf(&f.body, "    movb $0, %d(%%rbp)\n", off+p)
			p++
		}
		return
	}
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", off)
	fmt.Fprintf(&f.body, "    xorl %%eax, %%eax\n")
	fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", size)
	fmt.Fprintf(&f.body, "    cld\n")
	fmt.Fprintf(&f.body, "    rep stosb\n")
}

func (f *fnGen) emitCopyFrame(dstOff, srcOff, size int64) {
	if size == 0 || dstOff == srcOff {
		return
	}
	if size <= 64 {
		var p int64
		for p+8 <= size {
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", srcOff+p)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff+p)
			p += 8
		}
		for p < size {
			fmt.Fprintf(&f.body, "    movzbl %d(%%rbp), %%eax\n", srcOff+p)
			fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dstOff+p)
			p++
		}
		return
	}
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", dstOff)
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rsi\n", srcOff)
	fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", size)
	fmt.Fprintf(&f.body, "    cld\n")
	fmt.Fprintf(&f.body, "    rep movsb\n")
}

// emitCopyFromFrameToReg copies size bytes from srcOff(%rbp) to address in %rdi.
func (f *fnGen) emitCopyFromFrameToReg(srcOff, size int64) {
	if size == 0 {
		return
	}
	if size <= 32 {
		var p int64
		for p+8 <= size {
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", srcOff+p)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rdi)\n", p)
			p += 8
		}
		for p < size {
			fmt.Fprintf(&f.body, "    movzbl %d(%%rbp), %%eax\n", srcOff+p)
			fmt.Fprintf(&f.body, "    movb %%al, %d(%%rdi)\n", p)
			p++
		}
		return
	}
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rsi\n", srcOff)
	fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", size)
	fmt.Fprintf(&f.body, "    cld\n")
	fmt.Fprintf(&f.body, "    rep movsb\n")
}

// emitCopyFromRegToFrame copies size bytes from address in %rax to dstOff(%rbp).
func (f *fnGen) emitCopyFromRegToFrame(dstOff, size int64) {
	if size == 0 {
		return
	}
	if size <= 32 {
		var p int64
		for p+8 <= size {
			fmt.Fprintf(&f.body, "    movq %d(%%rax), %%rcx\n", p)
			fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dstOff+p)
			p += 8
		}
		for p < size {
			fmt.Fprintf(&f.body, "    movzbl %d(%%rax), %%ecx\n", p)
			fmt.Fprintf(&f.body, "    movb %%cl, %d(%%rbp)\n", dstOff+p)
			p++
		}
		return
	}
	fmt.Fprintf(&f.body, "    movq %%rax, %%rsi\n")
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", dstOff)
	fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", size)
	fmt.Fprintf(&f.body, "    cld\n")
	fmt.Fprintf(&f.body, "    rep movsb\n")
}

func (f *fnGen) emitLoadScalarReg(reg string, t types.Type) {
	l := layoutOf(t)
	signed := isSignedInt(t)
	switch l.size {
	case 1:
		if signed {
			fmt.Fprintf(&f.body, "    movsbq (%%%s), %%rax\n", reg)
		} else {
			fmt.Fprintf(&f.body, "    movzbq (%%%s), %%rax\n", reg)
		}
	case 2:
		if signed {
			fmt.Fprintf(&f.body, "    movswq (%%%s), %%rax\n", reg)
		} else {
			fmt.Fprintf(&f.body, "    movzwq (%%%s), %%rax\n", reg)
		}
	case 4:
		if signed {
			fmt.Fprintf(&f.body, "    movslq (%%%s), %%rax\n", reg)
		} else {
			fmt.Fprintf(&f.body, "    movl (%%%s), %%eax\n", reg)
		}
	case 8:
		fmt.Fprintf(&f.body, "    movq (%%%s), %%rax\n", reg)
	}
}

func (f *fnGen) emitStoreScalarReg(reg string, t types.Type) {
	l := layoutOf(t)
	switch l.size {
	case 1:
		fmt.Fprintf(&f.body, "    movb %%al, (%%%s)\n", reg)
	case 2:
		fmt.Fprintf(&f.body, "    movw %%ax, (%%%s)\n", reg)
	case 4:
		fmt.Fprintf(&f.body, "    movl %%eax, (%%%s)\n", reg)
	case 8:
		fmt.Fprintf(&f.body, "    movq %%rax, (%%%s)\n", reg)
	}
}

func (f *fnGen) emitLoadScalarFromFrame(off int64, t types.Type) {
	l := layoutOf(t)
	signed := isSignedInt(t)
	switch l.size {
	case 1:
		if signed {
			fmt.Fprintf(&f.body, "    movsbq %d(%%rbp), %%rax\n", off)
		} else {
			fmt.Fprintf(&f.body, "    movzbq %d(%%rbp), %%rax\n", off)
		}
	case 2:
		if signed {
			fmt.Fprintf(&f.body, "    movswq %d(%%rbp), %%rax\n", off)
		} else {
			fmt.Fprintf(&f.body, "    movzwq %d(%%rbp), %%rax\n", off)
		}
	case 4:
		if signed {
			fmt.Fprintf(&f.body, "    movslq %d(%%rbp), %%rax\n", off)
		} else {
			fmt.Fprintf(&f.body, "    movl %d(%%rbp), %%eax\n", off)
		}
	case 8:
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", off)
	}
}

func (f *fnGen) emitStoreScalarToFrame(off int64, t types.Type) {
	l := layoutOf(t)
	switch l.size {
	case 1:
		fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", off)
	case 2:
		fmt.Fprintf(&f.body, "    movw %%ax, %d(%%rbp)\n", off)
	case 4:
		fmt.Fprintf(&f.body, "    movl %%eax, %d(%%rbp)\n", off)
	case 8:
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", off)
	}
}

func (f *fnGen) emitMaskScalar(t types.Type) {
	l := layoutOf(t)
	signed := isSignedInt(t)
	switch l.size {
	case 1:
		if signed {
			fmt.Fprintf(&f.body, "    movsbq %%al, %%rax\n")
		} else {
			fmt.Fprintf(&f.body, "    movzbq %%al, %%rax\n")
		}
	case 2:
		if signed {
			fmt.Fprintf(&f.body, "    movswq %%ax, %%rax\n")
		} else {
			fmt.Fprintf(&f.body, "    movzwq %%ax, %%rax\n")
		}
	case 4:
		if signed {
			fmt.Fprintf(&f.body, "    movslq %%eax, %%rax\n")
		} else {
			fmt.Fprintf(&f.body, "    movl %%eax, %%eax\n")
		}
	}
}

// ---------------------------------------------------------------------------
// Address computation: puts address of lvalue e into %rax

func (f *fnGen) emitAddr(e ast.Expr) {
	e = unparen(e)
	switch e := e.(type) {
	case *ast.Ident:
		sym := f.g.info.SymbolOf(e)
		if sym == nil {
			f.g.fail(e, "address of unresolved identifier %s", e.Name)
		}
		if off, ok := f.varOff[sym]; ok {
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rax\n", off)
			return
		}
		if coff, ok := f.capOff[sym]; ok {
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", f.envSaveOff)
			if f.capByValue {
				fmt.Fprintf(&f.body, "    leaq %d(%%rax), %%rax\n", coff)
			} else {
				fmt.Fprintf(&f.body, "    movq %d(%%rax), %%rax\n", coff)
			}
			return
		}
		if glbl, ok := f.g.globalSyms[sym]; ok {
			fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", glbl)
			return
		}
		f.g.fail(e, "cannot take address of symbol %s", sym.Name)
	case *ast.SelectorExpr:
		// Package-qualified global `pkg.Var`
		if sym := f.g.info.Prog.Uses[e.Sel.ID()]; sym != nil && sym.Kind == resolve.SymVar {
			if glbl, ok := f.g.globalSyms[sym]; ok {
				fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", glbl)
				return
			}
		}
		sel := f.g.info.SelectionOf(e)
		if sel == nil || sel.Kind != types.SelField {
			f.g.fail(e, "cannot take address of selector %s", e.Sel.Name)
		}
		xt := f.typeOf(e.X)
		var st *types.Struct
		if sel.Indirect {
			f.emitScalar(e.X, xt)
			st = xt.Underlying().(*types.Pointer).Elem.Underlying().(*types.Struct)
		} else {
			f.emitAddr(e.X)
			st = xt.Underlying().(*types.Struct)
		}
		offs, _ := structFieldOffsets(st)
		foff := offs[sel.Field.Index]
		if foff != 0 {
			fmt.Fprintf(&f.body, "    addq $%d, %%rax\n", foff)
		}
	case *ast.IndexExpr:
		xt := f.typeOf(e.X)
		idxSlot := f.allocLocal(8, 8)
		f.emitScalar(e.Indices[0], f.typeOf(e.Indices[0]))
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", idxSlot)
		switch u := xt.Underlying().(type) {
		case *types.Array:
			f.emitAddr(e.X)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", idxSlot)
			fmt.Fprintf(&f.body, "    cmpq $%d, %%rcx\n", u.Len)
			fmt.Fprintf(&f.body, "    jae _fc_rt_panic_bounds\n")
			el := layoutOf(u.Elem)
			stride := alignUp(el.size, el.align)
			fmt.Fprintf(&f.body, "    imulq $%d, %%rcx, %%rcx\n", stride)
			fmt.Fprintf(&f.body, "    addq %%rcx, %%rax\n")
		case *types.Pointer:
			arr := u.Elem.Underlying().(*types.Array)
			f.emitScalar(e.X, xt)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", idxSlot)
			fmt.Fprintf(&f.body, "    cmpq $%d, %%rcx\n", arr.Len)
			fmt.Fprintf(&f.body, "    jae _fc_rt_panic_bounds\n")
			el := layoutOf(arr.Elem)
			stride := alignUp(el.size, el.align)
			fmt.Fprintf(&f.body, "    imulq $%d, %%rcx, %%rcx\n", stride)
			fmt.Fprintf(&f.body, "    addq %%rcx, %%rax\n")
		case *types.Slice:
			slSlot := f.allocLocal(24, 8)
			f.emitExprTo(e.X, slSlot, xt)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", idxSlot)
			fmt.Fprintf(&f.body, "    cmpq %d(%%rbp), %%rcx\n", slSlot+8) // len
			fmt.Fprintf(&f.body, "    jae _fc_rt_panic_bounds\n")
			el := layoutOf(u.Elem)
			stride := alignUp(el.size, el.align)
			fmt.Fprintf(&f.body, "    imulq $%d, %%rcx, %%rcx\n", stride)
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", slSlot) // ptr
			fmt.Fprintf(&f.body, "    addq %%rcx, %%rax\n")
		default:
			f.g.fail(e, "cannot index address of type %s", xt)
		}
	case *ast.UnaryExpr:
		if e.Op == ast.OpDeref {
			f.emitScalar(e.X, f.typeOf(e.X))
			return
		}
		f.g.fail(e, "cannot take address of unary %s", e.Op)
	default:
		f.g.fail(e, "cannot take address of %T", e)
	}
}

// ---------------------------------------------------------------------------
// Expression evaluation

// emitScalar evaluates scalar expression e of type t into %rax.
func (f *fnGen) emitScalar(e ast.Expr, t types.Type) {
	l := layoutOf(t)
	tmp := f.allocLocal(l.size, l.align)
	f.emitExprTo(e, tmp, t)
	f.emitLoadScalarFromFrame(tmp, t)
}

// emitExprTo evaluates expression e into dstOff(%rbp) as type wantType.
func (f *fnGen) emitExprTo(e ast.Expr, dstOff int64, wantType types.Type) {
	e = unparen(e)
	if wantType == nil {
		wantType = f.typeOf(e)
	}
	if f.g.info.IsImplicitBytes(e) {
		sv, _ := f.g.info.ValueOf(e).(types.StringVal)
		f.emitStrBytesConst(string(sv), dstOff)
		return
	}
	// Constant / folded comptime values (except calls whose side effects/folds are constants).
	if v := f.g.info.ValueOf(e); v != nil {
		// Inside generic code, skip if e references a const type param? No, const params have value via bind!
		if id, ok := e.(*ast.Ident); ok {
			if sym := f.g.info.SymbolOf(id); sym != nil && sym.Kind == resolve.SymTypeParam {
				if tp, ok := f.g.info.SymType(sym).(*types.TypeParam); ok {
					if r, ok := f.bind[tp]; ok {
						if ca, ok := r.(*types.ConstArg); ok {
							f.emitConstVal(types.MakeUint(ca.Value), dstOff, wantType)
							return
						}
					}
				}
			}
		}
		f.emitConstVal(v, dstOff, wantType)
		return
	}

	switch e := e.(type) {
	case *ast.Ident:
		sym := f.g.info.SymbolOf(e)
		if sym == nil {
			f.g.fail(e, "unresolved identifier %s", e.Name)
		}
		if sym.Kind == resolve.SymTypeParam {
			if tp, ok := f.g.info.SymType(sym).(*types.TypeParam); ok {
				if r, ok := f.bind[tp]; ok {
					if ca, ok := r.(*types.ConstArg); ok {
						f.emitConstVal(types.MakeUint(ca.Value), dstOff, wantType)
						return
					}
				}
			}
		}
		if sym.Kind == resolve.SymFunc {
			fi := f.g.info.FuncOfSym(sym)
			lbl := f.g.requestFunc(fi, nil)
			fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", lbl)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff)
			fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dstOff+8)
			return
		}
		if sym.Kind == resolve.SymConst {
			if cv := f.g.info.SymValue(sym); cv != nil {
				f.emitConstVal(cv, dstOff, wantType)
				return
			}
		}
		f.emitAddr(e)
		f.emitCopyFromRegToFrame(dstOff, layoutOf(wantType).size)

	case *ast.NilLit:
		f.emitZero(dstOff, layoutOf(wantType).size)

	case *ast.BoolLit:
		v := 0
		if e.Value {
			v = 1
		}
		fmt.Fprintf(&f.body, "    movb $%d, %d(%%rbp)\n", v, dstOff)

	case *ast.BasicLit:
		switch e.Kind {
		case ast.IntLit, ast.CharLit:
			fmt.Fprintf(&f.body, "    movabsq $%d, %%rax\n", int64(e.Int))
			f.emitStoreScalarToFrame(dstOff, wantType)
		case ast.StringLit:
			f.emitConstVal(types.StringVal(e.Str), dstOff, wantType)
		}

	case *ast.UnaryExpr:
		f.emitUnary(e, dstOff, wantType)

	case *ast.BinaryExpr:
		f.emitBinary(e, dstOff, wantType)

	case *ast.SelectorExpr:
		// Package-qualified function value `pkg.Fn`
		if sym := f.g.info.Prog.Uses[e.Sel.ID()]; sym != nil {
			if sym.Kind == resolve.SymFunc {
				fi := f.g.info.FuncOfSym(sym)
				lbl := f.g.requestFunc(fi, nil)
				fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", lbl)
				fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff)
				fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dstOff+8)
				return
			}
			if sym.Kind == resolve.SymConst {
				f.emitConstVal(f.g.info.SymValue(sym), dstOff, wantType)
				return
			}
		}
		sel := f.g.info.SelectionOf(e)
		if sel != nil && sel.Kind == types.SelVariant {
			// Payload-less enum variant: zero enum, store tag!
			en := wantType.Underlying().(*types.Enum)
			el := enumLayoutOf(en)
			f.emitZero(dstOff, el.size)
			switch el.tagSize {
			case 1:
				fmt.Fprintf(&f.body, "    movb $%d, %d(%%rbp)\n", sel.Variant.Index, dstOff)
			case 2:
				fmt.Fprintf(&f.body, "    movw $%d, %d(%%rbp)\n", sel.Variant.Index, dstOff)
			case 4:
				fmt.Fprintf(&f.body, "    movl $%d, %d(%%rbp)\n", sel.Variant.Index, dstOff)
			}
			return
		}
		// Field selection (works on both addressable and non-addressable struct expressions!)
		if sel != nil && sel.Kind == types.SelField {
			xt := f.typeOf(e.X)
			if sel.Indirect {
				f.emitScalar(e.X, xt)
				st := xt.Underlying().(*types.Pointer).Elem.Underlying().(*types.Struct)
				offs, _ := structFieldOffsets(st)
				if fo := offs[sel.Field.Index]; fo != 0 {
					fmt.Fprintf(&f.body, "    addq $%d, %%rax\n", fo)
				}
				f.emitCopyFromRegToFrame(dstOff, layoutOf(wantType).size)
				return
			}
			st := xt.Underlying().(*types.Struct)
			offs, _ := structFieldOffsets(st)
			if f.g.info.KindOf(e.X) == types.KindVar {
				f.emitAddr(e.X)
				if fo := offs[sel.Field.Index]; fo != 0 {
					fmt.Fprintf(&f.body, "    addq $%d, %%rax\n", fo)
				}
				f.emitCopyFromRegToFrame(dstOff, layoutOf(wantType).size)
			} else {
				xl := layoutOf(xt)
				tmp := f.allocLocal(xl.size, xl.align)
				f.emitExprTo(e.X, tmp, xt)
				f.emitCopyFrame(dstOff, tmp+offs[sel.Field.Index], layoutOf(wantType).size)
			}
			return
		}
		f.g.fail(e, "unsupported selector %s", e.Sel.Name)

	case *ast.IndexExpr:
		// Generic function value instantiation `fn[T]`
		if inst := f.g.info.InstanceOf(e); inst != nil {
			fi := f.g.info.FuncOfSym(inst.Sym)
			args := make([]types.Type, len(inst.TypeArgs))
			for i, a := range inst.TypeArgs {
				args[i] = f.subst(a)
			}
			lbl := f.g.requestFunc(fi, args)
			fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", lbl)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff)
			fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dstOff+8)
			return
		}
		xt := f.typeOf(e.X)
		if types.IsString(xt) {
			strSlot := f.allocLocal(16, 8)
			f.emitExprTo(e.X, strSlot, xt)
			f.emitScalar(e.Indices[0], f.typeOf(e.Indices[0]))
			fmt.Fprintf(&f.body, "    cmpq %d(%%rbp), %%rax\n", strSlot+8)
			fmt.Fprintf(&f.body, "    jae _fc_rt_panic_bounds\n")
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", strSlot)
			fmt.Fprintf(&f.body, "    movzbl (%%rcx, %%rax), %%eax\n")
			fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dstOff)
			return
		}
		if _, isArr := xt.Underlying().(*types.Array); isArr && f.g.info.KindOf(e.X) != types.KindVar {
			arr := xt.Underlying().(*types.Array)
			al := layoutOf(xt)
			arrSlot := f.allocLocal(al.size, al.align)
			f.emitExprTo(e.X, arrSlot, xt)
			f.emitScalar(e.Indices[0], f.typeOf(e.Indices[0]))
			fmt.Fprintf(&f.body, "    cmpq $%d, %%rax\n", arr.Len)
			fmt.Fprintf(&f.body, "    jae _fc_rt_panic_bounds\n")
			el := layoutOf(arr.Elem)
			stride := alignUp(el.size, el.align)
			fmt.Fprintf(&f.body, "    imulq $%d, %%rax, %%rax\n", stride)
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rcx\n", arrSlot)
			fmt.Fprintf(&f.body, "    addq %%rcx, %%rax\n")
			f.emitCopyFromRegToFrame(dstOff, el.size)
			return
		}
		f.emitAddr(e)
		f.emitCopyFromRegToFrame(dstOff, layoutOf(wantType).size)

	case *ast.SliceExpr:
		f.emitSliceExpr(e, dstOff)

	case *ast.StructLit:
		st := wantType.Underlying().(*types.Struct)
		offs, sl := structFieldOffsets(st)
		f.emitZero(dstOff, sl.size)
		for _, fi := range e.Fields {
			fld := st.FieldByName(fi.Name.Name)
			if fld != nil {
				f.emitExprTo(fi.Value, dstOff+offs[fld.Index], fld.Type)
			}
		}

	case *ast.ArrayLit:
		arr := wantType.Underlying().(*types.Array)
		al := layoutOf(wantType)
		el := layoutOf(arr.Elem)
		stride := alignUp(el.size, el.align)
		f.emitZero(dstOff, al.size)
		for i, elExpr := range e.Elems {
			f.emitExprTo(elExpr, dstOff+int64(i)*stride, arr.Elem)
		}

	case *ast.MatchExpr:
		f.emitMatchExpr(e, dstOff, wantType)

	case *ast.FuncLit:
		f.emitFuncLit(e, dstOff)

	case *ast.CallExpr:
		f.emitSingleCallTo(e, dstOff, wantType)

	default:
		f.g.fail(e, "unsupported expression %T", e)
	}
}

func (f *fnGen) emitStrBytesConst(s string, dstOff int64) {
	if len(s) == 0 {
		f.emitZero(dstOff, 24)
		return
	}
	slbl := f.g.internStr(s)
	fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rdi\n", slbl)
	fmt.Fprintf(&f.body, "    movq $%d, %%rsi\n", len(s))
	fmt.Fprintf(&f.body, "    call _fc_rt_str_bytes\n")
	fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff)
	fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dstOff+8)
	fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dstOff+16)
}

func (f *fnGen) emitConstVal(v types.Value, dstOff int64, t types.Type) {
	l := layoutOf(t)
	if l.size == 0 {
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch u.Kind {
		case types.Bool, types.UntypedBool:
			b := 0
			if bv, ok := v.(types.BoolVal); ok && bool(bv) {
				b = 1
			}
			fmt.Fprintf(&f.body, "    movb $%d, %d(%%rbp)\n", b, dstOff)
		case types.String, types.UntypedString:
			sv, _ := v.(types.StringVal)
			if len(sv) == 0 {
				fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dstOff)
				fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dstOff+8)
			} else {
				slbl := f.g.internStr(string(sv))
				fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", slbl)
				fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff)
				fmt.Fprintf(&f.body, "    movq $%d, %d(%%rbp)\n", len(sv), dstOff+8)
			}
		case types.Error:
			ev, _ := v.(types.ErrorVal)
			fmt.Fprintf(&f.body, "    movq $%d, %d(%%rbp)\n", errSentinelID(ev.Name), dstOff)
		default:
			bits := intValBits(v)
			fmt.Fprintf(&f.body, "    movabsq $%d, %%rax\n", int64(bits))
			f.emitStoreScalarToFrame(dstOff, t)
		}
	case *types.Slice:
		if sv, ok := v.(types.StringVal); ok {
			f.emitStrBytesConst(string(sv), dstOff)
			return
		}
		f.emitZero(dstOff, 24)
	case *types.Array, *types.Struct, *types.Enum:
		// Emit folded composite constant into .rodata and copy!
		lbl := f.g.freshLabel("cval")
		fmt.Fprintf(&f.g.rodataBuf, ".balign %d\n%s:\n", l.align, lbl)
		f.g.emitStaticVal(&f.g.rodataBuf, t, v)
		fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", lbl)
		f.emitCopyFromRegToFrame(dstOff, l.size)
	default:
		f.emitZero(dstOff, l.size)
	}
}

func (f *fnGen) emitUnary(e *ast.UnaryExpr, dstOff int64, wantType types.Type) {
	switch e.Op {
	case ast.OpAddr:
		f.emitAddr(e.X)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff)
	case ast.OpDeref:
		f.emitScalar(e.X, f.typeOf(e.X))
		f.emitCopyFromRegToFrame(dstOff, layoutOf(wantType).size)
	case ast.OpNot:
		f.emitScalar(e.X, types.Typ[types.Bool])
		fmt.Fprintf(&f.body, "    xorb $1, %%al\n")
		fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dstOff)
	case ast.OpNeg:
		f.emitScalar(e.X, wantType)
		fmt.Fprintf(&f.body, "    negq %%rax\n")
		f.emitStoreScalarToFrame(dstOff, wantType)
	case ast.OpBitNot:
		f.emitScalar(e.X, wantType)
		fmt.Fprintf(&f.body, "    notq %%rax\n")
		f.emitStoreScalarToFrame(dstOff, wantType)
	}
}

func (f *fnGen) emitBinary(e *ast.BinaryExpr, dstOff int64, wantType types.Type) {
	// Short-circuit && and ||
	if e.Op == ast.OpLAnd || e.Op == ast.OpLOr {
		doneLbl := f.g.freshLabel("sc_done")
		f.emitScalar(e.X, types.Typ[types.Bool])
		fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dstOff)
		fmt.Fprintf(&f.body, "    testb %%al, %%al\n")
		if e.Op == ast.OpLAnd {
			fmt.Fprintf(&f.body, "    jz %s\n", doneLbl)
		} else {
			fmt.Fprintf(&f.body, "    jnz %s\n", doneLbl)
		}
		f.emitScalar(e.Y, types.Typ[types.Bool])
		fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dstOff)
		fmt.Fprintf(&f.body, "%s:\n", doneLbl)
		return
	}

	xt := f.typeOf(e.X)
	yt := f.typeOf(e.Y)
	// Handle untyped nil comparisons (e.g. err != nil, slice == nil)
	if _, isNilX := unparen(e.X).(*ast.NilLit); isNilX {
		xt = yt
	}
	if _, isNilY := unparen(e.Y).(*ast.NilLit); isNilY {
		yt = xt
	}

	switch e.Op {
	case ast.OpEq, ast.OpNe, ast.OpLt, ast.OpLe, ast.OpGt, ast.OpGe:
		f.emitCompare(e.Op, e.X, e.Y, xt, dstOff)
		return
	}

	// Integer arithmetic / bitwise / shift
	lhsSlot := f.allocLocal(8, 8)
	f.emitScalar(e.X, xt)
	fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", lhsSlot)
	if e.Op == ast.OpShl || e.Op == ast.OpShr {
		f.emitScalar(e.Y, yt)
	} else {
		f.emitScalar(e.Y, xt)
	}
	fmt.Fprintf(&f.body, "    movq %%rax, %%rcx\n")
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", lhsSlot)
	f.emitIntBinOp(e.Op, wantType)
	f.emitStoreScalarToFrame(dstOff, wantType)
}

// emitIntBinOp computes %rax = %rax op %rcx for integer type t.
func (f *fnGen) emitIntBinOp(op ast.BinaryOp, t types.Type) {
	l := layoutOf(t)
	bits := l.size * 8
	signed := isSignedInt(t)
	switch op {
	case ast.OpAdd:
		fmt.Fprintf(&f.body, "    addq %%rcx, %%rax\n")
	case ast.OpSub:
		fmt.Fprintf(&f.body, "    subq %%rcx, %%rax\n")
	case ast.OpMul:
		fmt.Fprintf(&f.body, "    imulq %%rcx, %%rax\n")
	case ast.OpAnd:
		fmt.Fprintf(&f.body, "    andq %%rcx, %%rax\n")
	case ast.OpOr:
		fmt.Fprintf(&f.body, "    orq %%rcx, %%rax\n")
	case ast.OpXor:
		fmt.Fprintf(&f.body, "    xorq %%rcx, %%rax\n")
	case ast.OpShl:
		zeroLbl := f.g.freshLabel("shl0")
		doneLbl := f.g.freshLabel("shld")
		fmt.Fprintf(&f.body, "    cmpq $%d, %%rcx\n", bits)
		fmt.Fprintf(&f.body, "    jae %s\n", zeroLbl)
		fmt.Fprintf(&f.body, "    shlq %%cl, %%rax\n")
		fmt.Fprintf(&f.body, "    jmp %s\n", doneLbl)
		fmt.Fprintf(&f.body, "%s:\n    xorq %%rax, %%rax\n%s:\n", zeroLbl, doneLbl)
	case ast.OpShr:
		f.emitMaskScalar(t)
		ovfLbl := f.g.freshLabel("shr_ovf")
		doneLbl := f.g.freshLabel("shr_done")
		fmt.Fprintf(&f.body, "    cmpq $%d, %%rcx\n", bits)
		fmt.Fprintf(&f.body, "    jae %s\n", ovfLbl)
		if signed {
			fmt.Fprintf(&f.body, "    sarq %%cl, %%rax\n")
		} else {
			fmt.Fprintf(&f.body, "    shrq %%cl, %%rax\n")
		}
		fmt.Fprintf(&f.body, "    jmp %s\n", doneLbl)
		if signed {
			fmt.Fprintf(&f.body, "%s:\n    sarq $63, %%rax\n%s:\n", ovfLbl, doneLbl)
		} else {
			fmt.Fprintf(&f.body, "%s:\n    xorq %%rax, %%rax\n%s:\n", ovfLbl, doneLbl)
		}
	case ast.OpDiv, ast.OpRem:
		f.emitMaskScalar(t)
		fmt.Fprintf(&f.body, "    testq %%rcx, %%rcx\n")
		fmt.Fprintf(&f.body, "    jz _fc_rt_panic_div0\n")
		if signed {
			// Guard MinI64 / -1 so idivq never faults with SIGFPE
			doneLbl := f.g.freshLabel("div_done")
			okLbl := f.g.freshLabel("div_ok")
			fmt.Fprintf(&f.body, "    cmpq $-1, %%rcx\n")
			fmt.Fprintf(&f.body, "    jne %s\n", okLbl)
			fmt.Fprintf(&f.body, "    movabsq $-9223372036854775808, %%rdx\n")
			fmt.Fprintf(&f.body, "    cmpq %%rdx, %%rax\n")
			fmt.Fprintf(&f.body, "    jne %s\n", okLbl)
			if op == ast.OpRem {
				fmt.Fprintf(&f.body, "    xorq %%rax, %%rax\n")
			}
			fmt.Fprintf(&f.body, "    jmp %s\n", doneLbl)
			fmt.Fprintf(&f.body, "%s:\n", okLbl)
			fmt.Fprintf(&f.body, "    cqto\n")
			fmt.Fprintf(&f.body, "    idivq %%rcx\n")
			if op == ast.OpRem {
				fmt.Fprintf(&f.body, "    movq %%rdx, %%rax\n")
			}
			fmt.Fprintf(&f.body, "%s:\n", doneLbl)
		} else {
			fmt.Fprintf(&f.body, "    xorq %%rdx, %%rdx\n")
			fmt.Fprintf(&f.body, "    divq %%rcx\n")
			if op == ast.OpRem {
				fmt.Fprintf(&f.body, "    movq %%rdx, %%rax\n")
			}
		}
	}
	f.emitMaskScalar(t)
}

func (f *fnGen) emitCompare(op ast.BinaryOp, x, y ast.Expr, t types.Type, dstOff int64) {
	if types.IsString(t) {
		sx := f.allocLocal(16, 8)
		sy := f.allocLocal(16, 8)
		f.emitExprTo(x, sx, t)
		f.emitExprTo(y, sy, t)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", sx)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", sx+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", sy)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", sy+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_str_cmp\n")
		fmt.Fprintf(&f.body, "    cmpq $0, %%rax\n")
		f.emitSetCC(op, true, dstOff)
		return
	}
	// Slice == nil / != nil
	if _, isSl := t.Underlying().(*types.Slice); isSl {
		sl := f.allocLocal(24, 8)
		if _, isNilX := unparen(x).(*ast.NilLit); isNilX {
			f.emitExprTo(y, sl, t)
		} else {
			f.emitExprTo(x, sl, t)
		}
		fmt.Fprintf(&f.body, "    cmpq $0, %d(%%rbp)\n", sl)
		f.emitSetCC(op, false, dstOff)
		return
	}
	// Scalar comparison
	if isScalar(t) {
		lhs := f.allocLocal(8, 8)
		f.emitScalar(x, t)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", lhs)
		f.emitScalar(y, t)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", lhs)
		fmt.Fprintf(&f.body, "    cmpq %%rax, %%rcx\n")
		f.emitSetCC(op, isSignedInt(t), dstOff)
		return
	}
	// Structural equality for struct / array / enum (== and !=)
	l := layoutOf(t)
	ax := f.allocLocal(l.size, l.align)
	ay := f.allocLocal(l.size, l.align)
	f.emitExprTo(x, ax, t)
	f.emitExprTo(y, ay, t)
	f.emitValueEqual(ax, ay, t, dstOff)
	if op == ast.OpNe {
		fmt.Fprintf(&f.body, "    xorb $1, %d(%%rbp)\n", dstOff)
	}
}

func (f *fnGen) emitSetCC(op ast.BinaryOp, signed bool, dstOff int64) {
	var cc string
	switch op {
	case ast.OpEq:
		cc = "sete"
	case ast.OpNe:
		cc = "setne"
	case ast.OpLt:
		if signed {
			cc = "setl"
		} else {
			cc = "setb"
		}
	case ast.OpLe:
		if signed {
			cc = "setle"
		} else {
			cc = "setbe"
		}
	case ast.OpGt:
		if signed {
			cc = "setg"
		} else {
			cc = "seta"
		}
	case ast.OpGe:
		if signed {
			cc = "setge"
		} else {
			cc = "setae"
		}
	}
	fmt.Fprintf(&f.body, "    %s %d(%%rbp)\n", cc, dstOff)
}

// emitValueEqual sets byte at dstOff(%rbp) to 1 iff values at ax and ay of type t are equal.
func (f *fnGen) emitValueEqual(ax, ay int64, t types.Type, dstOff int64) {
	if types.IsString(t) {
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", ax)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", ax+8)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", ay)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", ay+8)
		fmt.Fprintf(&f.body, "    call _fc_rt_bytes_eq\n")
		fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dstOff)
		return
	}
	if isScalar(t) {
		f.emitLoadScalarFromFrame(ax, t)
		fmt.Fprintf(&f.body, "    movq %%rax, %%rcx\n")
		f.emitLoadScalarFromFrame(ay, t)
		fmt.Fprintf(&f.body, "    cmpq %%rax, %%rcx\n")
		fmt.Fprintf(&f.body, "    sete %d(%%rbp)\n", dstOff)
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		neqLbl := f.g.freshLabel("seq_no")
		endLbl := f.g.freshLabel("seq_end")
		offs, _ := structFieldOffsets(u)
		tmp := f.allocLocal(1, 1)
		for i, fld := range u.Fields {
			f.emitValueEqual(ax+offs[i], ay+offs[i], fld.Type, tmp)
			fmt.Fprintf(&f.body, "    cmpb $0, %d(%%rbp)\n", tmp)
			fmt.Fprintf(&f.body, "    je %s\n", neqLbl)
		}
		fmt.Fprintf(&f.body, "    movb $1, %d(%%rbp)\n", dstOff)
		fmt.Fprintf(&f.body, "    jmp %s\n", endLbl)
		fmt.Fprintf(&f.body, "%s:\n    movb $0, %d(%%rbp)\n%s:\n", neqLbl, dstOff, endLbl)
	case *types.Array:
		neqLbl := f.g.freshLabel("aeq_no")
		endLbl := f.g.freshLabel("aeq_end")
		el := layoutOf(u.Elem)
		stride := alignUp(el.size, el.align)
		tmp := f.allocLocal(1, 1)
		for i := uint64(0); i < u.Len; i++ {
			f.emitValueEqual(ax+int64(i)*stride, ay+int64(i)*stride, u.Elem, tmp)
			fmt.Fprintf(&f.body, "    cmpb $0, %d(%%rbp)\n", tmp)
			fmt.Fprintf(&f.body, "    je %s\n", neqLbl)
		}
		fmt.Fprintf(&f.body, "    movb $1, %d(%%rbp)\n", dstOff)
		fmt.Fprintf(&f.body, "    jmp %s\n", endLbl)
		fmt.Fprintf(&f.body, "%s:\n    movb $0, %d(%%rbp)\n%s:\n", neqLbl, dstOff, endLbl)
	case *types.Enum:
		neqLbl := f.g.freshLabel("eeq_no")
		endLbl := f.g.freshLabel("eeq_end")
		el := enumLayoutOf(u)
		// Compare tags first
		switch el.tagSize {
		case 1:
			fmt.Fprintf(&f.body, "    movzbl %d(%%rbp), %%eax\n", ax)
			fmt.Fprintf(&f.body, "    movzbl %d(%%rbp), %%ecx\n", ay)
		case 2:
			fmt.Fprintf(&f.body, "    movzwl %d(%%rbp), %%eax\n", ax)
			fmt.Fprintf(&f.body, "    movzwl %d(%%rbp), %%ecx\n", ay)
		case 4:
			fmt.Fprintf(&f.body, "    movl %d(%%rbp), %%eax\n", ax)
			fmt.Fprintf(&f.body, "    movl %d(%%rbp), %%ecx\n", ay)
		}
		fmt.Fprintf(&f.body, "    cmpl %%ecx, %%eax\n")
		fmt.Fprintf(&f.body, "    jne %s\n", neqLbl)
		tmp := f.allocLocal(1, 1)
		tagSlot := f.allocLocal(4, 4)
		fmt.Fprintf(&f.body, "    movl %%eax, %d(%%rbp)\n", tagSlot)
		for vi, vr := range u.Variants {
			if len(vr.Payload) == 0 {
				continue
			}
			skip := f.g.freshLabel("ev_sk")
			fmt.Fprintf(&f.body, "    cmpl $%d, %d(%%rbp)\n", vi, tagSlot)
			fmt.Fprintf(&f.body, "    jne %s\n", skip)
			for pi, pt := range vr.Payload {
				po := el.varOffsets[vi][pi]
				f.emitValueEqual(ax+po, ay+po, pt, tmp)
				fmt.Fprintf(&f.body, "    cmpb $0, %d(%%rbp)\n", tmp)
				fmt.Fprintf(&f.body, "    je %s\n", neqLbl)
			}
			fmt.Fprintf(&f.body, "%s:\n", skip)
		}
		fmt.Fprintf(&f.body, "    movb $1, %d(%%rbp)\n", dstOff)
		fmt.Fprintf(&f.body, "    jmp %s\n", endLbl)
		fmt.Fprintf(&f.body, "%s:\n    movb $0, %d(%%rbp)\n%s:\n", neqLbl, dstOff, endLbl)
	default:
		// Zero-size or bytewise compare
		l := layoutOf(t)
		fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdi\n", ax)
		fmt.Fprintf(&f.body, "    movq $%d, %%rsi\n", l.size)
		fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rdx\n", ay)
		fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", l.size)
		fmt.Fprintf(&f.body, "    call _fc_rt_bytes_eq\n")
		fmt.Fprintf(&f.body, "    movb %%al, %d(%%rbp)\n", dstOff)
	}
}

func (f *fnGen) emitSliceExpr(e *ast.SliceExpr, dstOff int64) {
	xt := f.typeOf(e.X)
	baseSlot := f.allocLocal(8, 8)
	lenSlot := f.allocLocal(8, 8)
	capSlot := f.allocLocal(8, 8)
	var elemStride int64

	switch u := xt.Underlying().(type) {
	case *types.Array:
		f.emitAddr(e.X)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", baseSlot)
		fmt.Fprintf(&f.body, "    movq $%d, %d(%%rbp)\n", u.Len, lenSlot)
		fmt.Fprintf(&f.body, "    movq $%d, %d(%%rbp)\n", u.Len, capSlot)
		el := layoutOf(u.Elem)
		elemStride = alignUp(el.size, el.align)
	case *types.Pointer:
		arr := u.Elem.Underlying().(*types.Array)
		f.emitScalar(e.X, xt)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", baseSlot)
		fmt.Fprintf(&f.body, "    movq $%d, %d(%%rbp)\n", arr.Len, lenSlot)
		fmt.Fprintf(&f.body, "    movq $%d, %d(%%rbp)\n", arr.Len, capSlot)
		el := layoutOf(arr.Elem)
		elemStride = alignUp(el.size, el.align)
	case *types.Slice:
		slSlot := f.allocLocal(24, 8)
		f.emitExprTo(e.X, slSlot, xt)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", slSlot)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", baseSlot)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", slSlot+8)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", lenSlot)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", slSlot+16)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", capSlot)
		el := layoutOf(u.Elem)
		elemStride = alignUp(el.size, el.align)
	case *types.Basic:
		// string[lo:hi]
		ss := f.allocLocal(16, 8)
		f.emitExprTo(e.X, ss, xt)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", ss)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", baseSlot)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", ss+8)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", lenSlot)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", capSlot)
		elemStride = 1
	}

	loSlot := f.allocLocal(8, 8)
	hiSlot := f.allocLocal(8, 8)
	if e.Lo != nil {
		f.emitScalar(e.Lo, f.typeOf(e.Lo))
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", loSlot)
	} else {
		fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", loSlot)
	}
	if e.Hi != nil {
		f.emitScalar(e.Hi, f.typeOf(e.Hi))
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", hiSlot)
	} else {
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", lenSlot)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", hiSlot)
	}
	// Bounds check: lo <= hi && hi <= cap (unsigned comparison!)
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", loSlot)
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", hiSlot)
	fmt.Fprintf(&f.body, "    cmpq %%rcx, %%rax\n")
	fmt.Fprintf(&f.body, "    ja _fc_rt_panic_bounds\n")
	fmt.Fprintf(&f.body, "    cmpq %d(%%rbp), %%rcx\n", capSlot)
	fmt.Fprintf(&f.body, "    ja _fc_rt_panic_bounds\n")
	// new_ptr = base + lo * elemStride
	fmt.Fprintf(&f.body, "    imulq $%d, %%rax, %%rdx\n", elemStride)
	fmt.Fprintf(&f.body, "    addq %d(%%rbp), %%rdx\n", baseSlot)
	fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dstOff)
	// new_len = hi - lo
	fmt.Fprintf(&f.body, "    subq %%rax, %%rcx\n")
	fmt.Fprintf(&f.body, "    movq %%rcx, %d(%%rbp)\n", dstOff+8)
	if !types.IsString(xt) {
		// new_cap = cap - lo
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", capSlot)
		fmt.Fprintf(&f.body, "    subq %%rax, %%rdx\n")
		fmt.Fprintf(&f.body, "    movq %%rdx, %d(%%rbp)\n", dstOff+16)
	}
}

func (f *fnGen) emitFuncLit(lit *ast.FuncLit, dstOff int64) {
	fi := f.g.info.FuncOf(lit)
	lbl := f.g.requestLit(fi, f.bind)
	fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", lbl)
	fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff)
	if len(fi.Captures) == 0 {
		fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", dstOff+8)
		f.litEnvSize[lit] = 0
		return
	}
	byVal := f.goLits[lit]
	var envSize int64
	offs := make([]int64, len(fi.Captures))
	for i, cs := range fi.Captures {
		var sz, al int64 = 8, 8
		if byVal {
			l := layoutOf(f.symType(cs))
			sz, al = l.size, l.align
		}
		envSize = alignUp(envSize, al)
		offs[i] = envSize
		envSize += sz
	}
	envSize = alignUp(envSize, 8)
	f.litEnvSize[lit] = envSize
	envOff := f.allocLocal(envSize, 8)
	for i, cs := range fi.Captures {
		// Get address of captured symbol in current function
		if voff, ok := f.varOff[cs]; ok {
			fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rax\n", voff)
		} else if coff, ok := f.capOff[cs]; ok {
			fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rax\n", f.envSaveOff)
			if f.capByValue {
				fmt.Fprintf(&f.body, "    leaq %d(%%rax), %%rax\n", coff)
			} else {
				fmt.Fprintf(&f.body, "    movq %d(%%rax), %%rax\n", coff)
			}
		} else if glbl, ok := f.g.globalSyms[cs]; ok {
			fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rax\n", glbl)
		}
		if byVal {
			f.emitCopyFromRegToFrame(envOff+offs[i], layoutOf(f.symType(cs)).size)
		} else {
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", envOff+offs[i])
		}
	}
	fmt.Fprintf(&f.body, "    leaq %d(%%rbp), %%rax\n", envOff)
	fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", dstOff+8)
}
