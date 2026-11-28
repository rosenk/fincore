package amd64

import (
	"bytes"
	"fmt"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/resolve"
	"fincore/bootstrap/internal/types"
)

type deferSlot struct {
	stmt      *ast.DeferStmt
	activeOff int64 // 1-byte flag in frame
	recvOff   int64 // saved receiver (or closure for CallValue)
	recvType  types.Type
	argOffs   []int64
	argTypes  []types.Type
	callInfo  *types.Call
	callExpr  *ast.CallExpr
}

type loopLabels struct {
	contLabel  string
	breakLabel string
	scopeDepth int // len(activeScopes) at loop entry
}

type fnGen struct {
	g     *gen
	label string
	fi    *types.FuncInfo
	bind  map[*types.TypeParam]types.Type

	body bytes.Buffer

	// Stack frame layout:
	// Saved regs at -8(%rbp) = %rbx, -16(%rbp) = %r12.
	// Local slots grow downward starting below -16(%rbp).
	frameUsed int64 // bytes below -16(%rbp)

	// Incoming ABI (caller args/rets start at +16(%rbp)).
	abi      callABI
	recvType types.Type
	params   []types.Type
	results  []types.Type

	// Variable locations:
	// varOff[sym] > 0 means incoming arg at +varOff(%rbp);
	// varOff[sym] < 0 means local slot at varOff(%rbp).
	varOff map[*resolve.Symbol]int64
	// capOff[sym] holds offset inside closure env (%r12), if sym is captured.
	capOff     map[*resolve.Symbol]int64
	capByValue bool // true for Scope.Go closures (env holds value copies), false = pointers
	envSaveOff int64

	// Closures passed directly to Scope.Go (value-captured).
	goLits map[*ast.FuncLit]bool
	// Closure env layouts for literals defined in this function.
	litEnvSize map[*ast.FuncLit]int64

	// Active scopes for barrier cleanup on break/continue/return.
	scopeSlots   []int64 // stack of active scope local offsets (innermost last)
	allScopeOffs []int64 // every scope slot in the function (for epilogue safety)

	// Defers in source order.
	defers   []*deferSlot
	deferMap map[*ast.DeferStmt]*deferSlot

	loops       []loopLabels
	epilogueLbl string
	panicNilLbl string
}

func (g *gen) emitFunc(label string, fi *types.FuncInfo, bind map[*types.TypeParam]types.Type) {
	fn := &fnGen{
		g:           g,
		label:       label,
		fi:          fi,
		bind:        bind,
		varOff:      map[*resolve.Symbol]int64{},
		capOff:      map[*resolve.Symbol]int64{},
		goLits:      map[*ast.FuncLit]bool{},
		litEnvSize:  map[*ast.FuncLit]int64{},
		deferMap:    map[*ast.DeferStmt]*deferSlot{},
		epilogueLbl: g.freshLabel("epi"),
	}
	fn.setup()
	if fi.Body != nil {
		fn.emitBlock(fi.Body)
	}
	fn.finish()
}

func (f *fnGen) subst(t types.Type) types.Type {
	return substType(t, f.bind)
}

func (f *fnGen) typeOf(n ast.Node) types.Type {
	return f.subst(f.g.info.TypeOf(n))
}

func (f *fnGen) symType(s *resolve.Symbol) types.Type {
	return f.subst(f.g.info.SymType(s))
}

// allocLocal reserves a local slot of (size, align) and returns its negative %rbp offset.
func (f *fnGen) allocLocal(size, align int64) int64 {
	if size == 0 {
		return -16
	}
	if align < 1 {
		align = 1
	}
	// Offset from %rbp is -(16 + frameUsed + size), aligned down to align.
	top := 16 + f.frameUsed + size
	top = alignUp(top, align)
	f.frameUsed = top - 16
	return -top
}

func (f *fnGen) setup() {
	// Resolve receiver, parameter, and result types under bind.
	if f.fi.Method != nil && f.fi.RecvSym != nil {
		f.recvType = f.symType(f.fi.RecvSym)
	}
	for _, p := range f.fi.Sig.Params {
		f.params = append(f.params, f.subst(p))
	}
	for _, r := range f.fi.Sig.Results {
		f.results = append(f.results, f.subst(r))
	}
	f.abi = computeCallABI(f.recvType, f.params, f.results)

	if f.fi.RecvSym != nil {
		f.varOff[f.fi.RecvSym] = 16 + f.abi.recvOff
	}
	for i, ps := range f.fi.Params {
		if ps != nil {
			f.varOff[ps] = 16 + f.abi.argOffs[i]
		}
	}

	// If this is a function literal with captures, lay out its env struct.
	if f.fi.Lit != nil && len(f.fi.Captures) > 0 {
		f.envSaveOff = f.allocLocal(8, 8)
		f.capByValue = f.isGoLit(f.fi.Lit)
		var off int64
		for _, capSym := range f.fi.Captures {
			var sz, al int64 = 8, 8
			if f.capByValue {
				l := layoutOf(f.symType(capSym))
				sz, al = l.size, l.align
			}
			off = alignUp(off, al)
			f.capOff[capSym] = off
			off += sz
		}
	}

	// Scan body (not descending into nested FuncLits) to:
	// 1. detect Scope.Go literals;
	// 2. allocate local variable slots;
	// 3. allocate defer slots.
	if f.fi.Body == nil {
		return
	}
	ast.Inspect(f.fi.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if ci := f.g.info.CallOf(n); ci != nil && ci.Kind == types.CallBuiltinMeth && ci.Method.Builtin == types.ScopeGo {
				if len(n.Args) == 2 {
					if lit, ok := unparen(n.Args[1]).(*ast.FuncLit); ok {
						f.goLits[lit] = true
					}
				}
			}
		case *ast.VarDecl:
			if sym := f.g.info.Prog.Defs[n.Name.ID()]; sym != nil {
				f.ensureLocal(sym)
			}
		case *ast.ShortVarDecl:
			for _, id := range n.Names {
				if sym := f.g.info.Prog.Defs[id.ID()]; sym != nil {
					f.ensureLocal(sym)
				}
			}
		case *ast.ScopeStmt:
			if sym := f.g.info.Prog.Defs[n.Name.ID()]; sym != nil {
				off := f.ensureLocal(sym)
				f.allScopeOffs = append(f.allScopeOffs, off)
			}
		case *ast.CtorPattern:
			for _, a := range n.Args {
				if id, ok := a.(*ast.Ident); ok && id.Name != "_" {
					if sym := f.g.info.Prog.Defs[id.ID()]; sym != nil {
						f.ensureLocal(sym)
					}
				}
			}
		case *ast.DeferStmt:
			f.allocDeferSlot(n)
		}
		return true
	})
}

func (f *fnGen) isGoLit(lit *ast.FuncLit) bool {
	if f.fi.Outer == nil || f.fi.Outer.Body == nil {
		return false
	}
	found := false
	ast.Inspect(f.fi.Outer.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if ci := f.g.info.CallOf(call); ci != nil && ci.Kind == types.CallBuiltinMeth && ci.Method.Builtin == types.ScopeGo {
				if len(call.Args) == 2 && unparen(call.Args[1]) == lit {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

func (f *fnGen) ensureLocal(sym *resolve.Symbol) int64 {
	if off, ok := f.varOff[sym]; ok {
		return off
	}
	l := layoutOf(f.symType(sym))
	off := f.allocLocal(l.size, l.align)
	f.varOff[sym] = off
	return off
}

func (f *fnGen) allocDeferSlot(ds *ast.DeferStmt) {
	call, ok := unparen(ds.X).(*ast.CallExpr)
	if !ok {
		return
	}
	ci := f.g.info.CallOf(call)
	if ci == nil {
		return
	}
	slot := &deferSlot{
		stmt:      ds,
		activeOff: f.allocLocal(1, 1),
		callInfo:  ci,
		callExpr:  call,
	}
	switch ci.Kind {
	case types.CallMethod, types.CallBuiltinMeth:
		rt := f.subst(ci.RecvType)
		slot.recvType = rt
		l := layoutOf(rt)
		slot.recvOff = f.allocLocal(l.size, l.align)
	case types.CallValue:
		ft := f.typeOf(call.Fun)
		slot.recvType = ft
		l := layoutOf(ft)
		slot.recvOff = f.allocLocal(l.size, l.align)
	}
	for _, a := range call.Args {
		at := f.typeOf(a)
		if f.g.info.IsImplicitBytes(a) {
			at = &types.Slice{Elem: types.Typ[types.U8]}
		}
		l := layoutOf(at)
		slot.argTypes = append(slot.argTypes, at)
		slot.argOffs = append(slot.argOffs, f.allocLocal(l.size, l.align))
	}
	f.defers = append(f.defers, slot)
	f.deferMap[ds] = slot
}

func (f *fnGen) finish() {
	// Total local bytes below saved %rbx/%r12, 16-byte aligned.
	frameBytes := alignUp(f.frameUsed, 16)

	w := &f.g.textBuf
	if f.label == "_fc_main" {
		fmt.Fprintf(w, ".global _fc_main\n")
	}
	fmt.Fprintf(w, "%s:\n", f.label)
	fmt.Fprintf(w, "    pushq %%rbp\n")
	fmt.Fprintf(w, "    movq %%rsp, %%rbp\n")
	fmt.Fprintf(w, "    pushq %%rbx\n")
	fmt.Fprintf(w, "    pushq %%r12\n")
	if frameBytes > 0 {
		fmt.Fprintf(w, "    subq $%d, %%rsp\n", frameBytes)
		// Zero local frame (Go-style zero values + deferActive=0 + scopePtr=0)
		qwords := frameBytes / 8
		if qwords <= 8 {
			for i := int64(0); i < qwords; i++ {
				fmt.Fprintf(w, "    movq $0, %d(%%rsp)\n", i*8)
			}
		} else {
			fmt.Fprintf(w, "    movq %%rsp, %%rdi\n")
			fmt.Fprintf(w, "    xorl %%eax, %%eax\n")
			fmt.Fprintf(w, "    movq $%d, %%rcx\n", qwords)
			fmt.Fprintf(w, "    cld\n")
			fmt.Fprintf(w, "    rep stosq\n")
		}
	}
	// Save incoming closure env pointer %r12 if needed.
	if f.envSaveOff != 0 {
		fmt.Fprintf(w, "    movq %%r12, %d(%%rbp)\n", f.envSaveOff)
	}

	// Emit body instructions.
	w.Write(f.body.Bytes())

	// Emit epilogue: destroy any active scopes, run active defers LIFO.
	fmt.Fprintf(w, "%s:\n", f.epilogueLbl)
	for i := len(f.allScopeOffs) - 1; i >= 0; i-- {
		off := f.allScopeOffs[i]
		skip := f.g.freshLabel("sc_skip")
		fmt.Fprintf(w, "    movq %d(%%rbp), %%rdi\n", off)
		fmt.Fprintf(w, "    testq %%rdi, %%rdi\n")
		fmt.Fprintf(w, "    jz %s\n", skip)
		fmt.Fprintf(w, "    movq $0, %d(%%rbp)\n", off)
		fmt.Fprintf(w, "    call _fc_rt_scope_destroy\n")
		fmt.Fprintf(w, "%s:\n", skip)
	}
	for i := len(f.defers) - 1; i >= 0; i-- {
		ds := f.defers[i]
		skip := f.g.freshLabel("def_skip")
		fmt.Fprintf(w, "    cmpb $0, %d(%%rbp)\n", ds.activeOff)
		fmt.Fprintf(w, "    je %s\n", skip)
		f.emitDeferredCall(w, ds)
		fmt.Fprintf(w, "%s:\n", skip)
	}
	if frameBytes > 0 {
		fmt.Fprintf(w, "    addq $%d, %%rsp\n", frameBytes)
	}
	fmt.Fprintf(w, "    popq %%r12\n")
	fmt.Fprintf(w, "    popq %%rbx\n")
	fmt.Fprintf(w, "    popq %%rbp\n")
	fmt.Fprintf(w, "    ret\n\n")
}

// ---------------------------------------------------------------------------
// Statement emission

func (f *fnGen) emitBlock(b *ast.BlockStmt) {
	if b == nil {
		return
	}
	for _, s := range b.Stmts {
		f.emitStmt(s)
	}
}

func (f *fnGen) emitStmt(s ast.Stmt) {
	switch s := s.(type) {
	case nil:
	case *ast.BlockStmt:
		f.emitBlock(s)
	case *ast.DeclStmt:
		if vd, ok := s.Decl.(*ast.VarDecl); ok {
			sym := f.g.info.Prog.Defs[vd.Name.ID()]
			if sym == nil {
				if vd.Value != nil {
					t := f.typeOf(vd.Value)
					tmp := f.allocLocal(layoutOf(t).size, layoutOf(t).align)
					f.emitExprTo(vd.Value, tmp, t)
				}
				return
			}
			off := f.ensureLocal(sym)
			t := f.symType(sym)
			if vd.Value != nil {
				f.emitExprTo(vd.Value, off, t)
			} else {
				f.emitZero(off, layoutOf(t).size)
			}
		}
	case *ast.ShortVarDecl:
		f.emitShortVarDecl(s)
	case *ast.AssignStmt:
		f.emitAssign(s)
	case *ast.IncDecStmt:
		f.emitIncDec(s)
	case *ast.ExprStmt:
		if call, ok := unparen(s.X).(*ast.CallExpr); ok {
			f.emitCallDiscard(call)
		} else {
			t := f.typeOf(s.X)
			tmp := f.allocLocal(layoutOf(t).size, layoutOf(t).align)
			f.emitExprTo(s.X, tmp, t)
		}
	case *ast.IfStmt:
		f.emitIf(s)
	case *ast.ForStmt:
		f.emitFor(s)
	case *ast.MatchStmt:
		f.emitMatchStmt(s)
	case *ast.ScopeStmt:
		f.emitScopeStmt(s)
	case *ast.DeferStmt:
		f.emitDeferStmt(s)
	case *ast.ReturnStmt:
		f.emitReturn(s)
	case *ast.BreakStmt:
		if len(f.loops) > 0 {
			lp := f.loops[len(f.loops)-1]
			f.emitDestroyScopesDownTo(lp.scopeDepth)
			fmt.Fprintf(&f.body, "    jmp %s\n", lp.breakLabel)
		}
	case *ast.ContinueStmt:
		if len(f.loops) > 0 {
			lp := f.loops[len(f.loops)-1]
			f.emitDestroyScopesDownTo(lp.scopeDepth)
			fmt.Fprintf(&f.body, "    jmp %s\n", lp.contLabel)
		}
	}
}

func (f *fnGen) emitShortVarDecl(s *ast.ShortVarDecl) {
	// Multi-result call on RHS?
	if len(s.Values) == 1 && len(s.Names) > 1 {
		call := unparen(s.Values[0]).(*ast.CallExpr)
		dsts := make([]int64, len(s.Names))
		dts := make([]types.Type, len(s.Names))
		for i, id := range s.Names {
			if id.Name == "_" {
				dsts[i] = 0
				continue
			}
			sym := f.g.info.SymbolOf(id)
			dsts[i] = f.ensureLocal(sym)
			dts[i] = f.symType(sym)
		}
		f.emitMultiCallTo(call, dsts, dts)
		return
	}
	// Evaluate all RHS into temps first (handles simultaneous assignment `a, b := b, a`),
	// then copy into targets.
	tmps := make([]int64, len(s.Values))
	tts := make([]types.Type, len(s.Values))
	for i, val := range s.Values {
		var t types.Type
		if s.Names[i].Name != "_" {
			sym := f.g.info.SymbolOf(s.Names[i])
			t = f.symType(sym)
		} else {
			t = f.typeOf(val)
			if f.g.info.IsImplicitBytes(val) {
				t = &types.Slice{Elem: types.Typ[types.U8]}
			}
		}
		l := layoutOf(t)
		tmps[i] = f.allocLocal(l.size, l.align)
		tts[i] = t
		f.emitExprTo(val, tmps[i], t)
	}
	for i, id := range s.Names {
		if id.Name == "_" {
			continue
		}
		sym := f.g.info.SymbolOf(id)
		off := f.ensureLocal(sym)
		f.emitCopyFrame(off, tmps[i], layoutOf(tts[i]).size)
	}
}

func (f *fnGen) emitAssign(s *ast.AssignStmt) {
	if s.Op != ast.Assign {
		f.emitCompoundAssign(s)
		return
	}
	// Multi-result call on RHS
	if len(s.RHS) == 1 && len(s.LHS) > 1 {
		call := unparen(s.RHS[0]).(*ast.CallExpr)
		tu := f.typeOf(call).(*types.Tuple)
		tmps := make([]int64, len(s.LHS))
		dts := make([]types.Type, len(s.LHS))
		for i, t := range tu.Types {
			l := layoutOf(t)
			tmps[i] = f.allocLocal(l.size, l.align)
			dts[i] = t
		}
		f.emitMultiCallTo(call, tmps, dts)
		for i, l := range s.LHS {
			if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
				continue
			}
			f.emitAddr(l)
			fmt.Fprintf(&f.body, "    movq %%rax, %%rdi\n")
			f.emitCopyFromFrameToReg(tmps[i], layoutOf(dts[i]).size)
		}
		return
	}
	// Evaluate LHS addresses left-to-right, then RHS values into temps, then store.
	addrSlots := make([]int64, len(s.LHS))
	rhsTmps := make([]int64, len(s.RHS))
	rhsTypes := make([]types.Type, len(s.RHS))
	for i, l := range s.LHS {
		if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
			continue
		}
		addrSlots[i] = f.allocLocal(8, 8)
		f.emitAddr(l)
		fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", addrSlots[i])
	}
	for i, r := range s.RHS {
		var t types.Type
		if id, ok := s.LHS[i].(*ast.Ident); ok && id.Name == "_" {
			t = f.typeOf(r)
			if f.g.info.IsImplicitBytes(r) {
				t = &types.Slice{Elem: types.Typ[types.U8]}
			}
		} else {
			t = f.typeOf(s.LHS[i])
		}
		l := layoutOf(t)
		rhsTmps[i] = f.allocLocal(l.size, l.align)
		rhsTypes[i] = t
		f.emitExprTo(r, rhsTmps[i], t)
	}
	for i, l := range s.LHS {
		if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
			continue
		}
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", addrSlots[i])
		f.emitCopyFromFrameToReg(rhsTmps[i], layoutOf(rhsTypes[i]).size)
	}
}

func (f *fnGen) emitCompoundAssign(s *ast.AssignStmt) {
	lt := f.typeOf(s.LHS[0])
	addrSlot := f.allocLocal(8, 8)
	f.emitAddr(s.LHS[0])
	fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", addrSlot)
	// Evaluate RHS scalar
	rhsSlot := f.allocLocal(8, 8)
	if s.Op == ast.ShlAssign || s.Op == ast.ShrAssign {
		f.emitScalar(s.RHS[0], f.typeOf(s.RHS[0]))
	} else {
		f.emitScalar(s.RHS[0], lt)
	}
	fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", rhsSlot)
	// Load LHS scalar
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", addrSlot)
	f.emitLoadScalarReg("rdx", lt)
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rcx\n", rhsSlot)
	bop := map[ast.AssignOp]ast.BinaryOp{
		ast.AddAssign: ast.OpAdd,
		ast.SubAssign: ast.OpSub,
		ast.MulAssign: ast.OpMul,
		ast.QuoAssign: ast.OpDiv,
		ast.RemAssign: ast.OpRem,
		ast.AndAssign: ast.OpAnd,
		ast.OrAssign:  ast.OpOr,
		ast.XorAssign: ast.OpXor,
		ast.ShlAssign: ast.OpShl,
		ast.ShrAssign: ast.OpShr,
	}[s.Op]
	f.emitIntBinOp(bop, lt)
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", addrSlot)
	f.emitStoreScalarReg("rdx", lt)
}

func (f *fnGen) emitIncDec(s *ast.IncDecStmt) {
	t := f.typeOf(s.X)
	addrSlot := f.allocLocal(8, 8)
	f.emitAddr(s.X)
	fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", addrSlot)
	f.emitLoadScalarReg("rax", t)
	if s.Inc {
		fmt.Fprintf(&f.body, "    incq %%rax\n")
	} else {
		fmt.Fprintf(&f.body, "    decq %%rax\n")
	}
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdx\n", addrSlot)
	f.emitStoreScalarReg("rdx", t)
}

func (f *fnGen) emitIf(s *ast.IfStmt) {
	if s.Init != nil {
		f.emitStmt(s.Init)
	}
	elseLbl := f.g.freshLabel("else")
	endLbl := f.g.freshLabel("endif")
	f.emitScalar(s.Cond, types.Typ[types.Bool])
	fmt.Fprintf(&f.body, "    testb %%al, %%al\n")
	if s.Else != nil {
		fmt.Fprintf(&f.body, "    jz %s\n", elseLbl)
	} else {
		fmt.Fprintf(&f.body, "    jz %s\n", endLbl)
	}
	f.emitBlock(s.Then)
	if s.Else != nil {
		fmt.Fprintf(&f.body, "    jmp %s\n", endLbl)
		fmt.Fprintf(&f.body, "%s:\n", elseLbl)
		f.emitStmt(s.Else)
	}
	fmt.Fprintf(&f.body, "%s:\n", endLbl)
}

func (f *fnGen) emitFor(s *ast.ForStmt) {
	if s.Init != nil {
		f.emitStmt(s.Init)
	}
	condLbl := f.g.freshLabel("for_cond")
	postLbl := f.g.freshLabel("for_post")
	endLbl := f.g.freshLabel("for_end")
	f.loops = append(f.loops, loopLabels{contLabel: postLbl, breakLabel: endLbl, scopeDepth: len(f.scopeSlots)})
	fmt.Fprintf(&f.body, "%s:\n", condLbl)
	if s.Cond != nil {
		f.emitScalar(s.Cond, types.Typ[types.Bool])
		fmt.Fprintf(&f.body, "    testb %%al, %%al\n")
		fmt.Fprintf(&f.body, "    jz %s\n", endLbl)
	}
	f.emitBlock(s.Body)
	fmt.Fprintf(&f.body, "%s:\n", postLbl)
	if s.Post != nil {
		f.emitStmt(s.Post)
	}
	fmt.Fprintf(&f.body, "    jmp %s\n", condLbl)
	fmt.Fprintf(&f.body, "%s:\n", endLbl)
	f.loops = f.loops[:len(f.loops)-1]
}

func (f *fnGen) emitScopeStmt(s *ast.ScopeStmt) {
	sym := f.g.info.Prog.Defs[s.Name.ID()]
	off := f.ensureLocal(sym)
	f.emitExprTo(s.Value, off, f.symType(sym))
	f.scopeSlots = append(f.scopeSlots, off)
	f.emitBlock(s.Body)
	f.scopeSlots = f.scopeSlots[:len(f.scopeSlots)-1]
	// Normal exit barrier: wait + destroy scope.
	skip := f.g.freshLabel("sc_end")
	fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", off)
	fmt.Fprintf(&f.body, "    testq %%rdi, %%rdi\n")
	fmt.Fprintf(&f.body, "    jz %s\n", skip)
	fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", off)
	fmt.Fprintf(&f.body, "    call _fc_rt_scope_destroy\n")
	fmt.Fprintf(&f.body, "%s:\n", skip)
}

func (f *fnGen) emitDestroyScopesDownTo(depth int) {
	for i := len(f.scopeSlots) - 1; i >= depth; i-- {
		off := f.scopeSlots[i]
		skip := f.g.freshLabel("sc_jmp")
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", off)
		fmt.Fprintf(&f.body, "    testq %%rdi, %%rdi\n")
		fmt.Fprintf(&f.body, "    jz %s\n", skip)
		fmt.Fprintf(&f.body, "    movq $0, %d(%%rbp)\n", off)
		fmt.Fprintf(&f.body, "    call _fc_rt_scope_destroy\n")
		fmt.Fprintf(&f.body, "%s:\n", skip)
	}
}

func (f *fnGen) emitDeferStmt(ds *ast.DeferStmt) {
	slot := f.deferMap[ds]
	if slot == nil {
		return
	}
	ci := slot.callInfo
	// Evaluate receiver / callee value right now (at defer statement).
	switch ci.Kind {
	case types.CallMethod, types.CallBuiltinMeth:
		if ci.AutoAddr {
			f.emitAddr(ci.Recv)
			fmt.Fprintf(&f.body, "    movq %%rax, %d(%%rbp)\n", slot.recvOff)
		} else {
			f.emitExprTo(ci.Recv, slot.recvOff, slot.recvType)
		}
	case types.CallValue:
		f.emitExprTo(slot.callExpr.Fun, slot.recvOff, slot.recvType)
	}
	for i, a := range slot.callExpr.Args {
		f.emitExprTo(a, slot.argOffs[i], slot.argTypes[i])
	}
	fmt.Fprintf(&f.body, "    movb $1, %d(%%rbp)\n", slot.activeOff)
}

func (f *fnGen) emitReturn(s *ast.ReturnStmt) {
	// Evaluate return values BEFORE destroying scopes or running defers!
	if len(s.Results) == 1 && len(f.results) > 1 {
		call := unparen(s.Results[0]).(*ast.CallExpr)
		dsts := make([]int64, len(f.results))
		for i := range f.results {
			dsts[i] = 16 + f.abi.retOffs[i]
		}
		f.emitMultiCallTo(call, dsts, f.results)
	} else {
		for i, r := range s.Results {
			f.emitExprTo(r, 16+f.abi.retOffs[i], f.results[i])
		}
	}
	f.emitDestroyScopesDownTo(0)
	fmt.Fprintf(&f.body, "    jmp %s\n", f.epilogueLbl)
}

func (f *fnGen) emitMatchStmt(s *ast.MatchStmt) {
	st := f.typeOf(s.X)
	sl := layoutOf(st)
	scrutOff := f.allocLocal(sl.size, sl.align)
	f.emitExprTo(s.X, scrutOff, st)

	endLbl := f.g.freshLabel("mend")
	for _, arm := range s.Arms {
		nextLbl := f.g.freshLabel("marm")
		bodyLbl := f.g.freshLabel("mbody")
		f.emitMatchPatternTest(arm.Pattern, scrutOff, st, bodyLbl, nextLbl)
		fmt.Fprintf(&f.body, "%s:\n", bodyLbl)
		if arm.Expr != nil {
			if call, ok := unparen(arm.Expr).(*ast.CallExpr); ok {
				f.emitCallDiscard(call)
			} else {
				et := f.typeOf(arm.Expr)
				tmp := f.allocLocal(layoutOf(et).size, layoutOf(et).align)
				f.emitExprTo(arm.Expr, tmp, et)
			}
		} else {
			f.emitBlock(arm.Block)
		}
		fmt.Fprintf(&f.body, "    jmp %s\n", endLbl)
		fmt.Fprintf(&f.body, "%s:\n", nextLbl)
	}
	fmt.Fprintf(&f.body, "%s:\n", endLbl)
}

func (f *fnGen) emitMatchExpr(e *ast.MatchExpr, dstOff int64, dstType types.Type) {
	st := f.typeOf(e.X)
	sl := layoutOf(st)
	scrutOff := f.allocLocal(sl.size, sl.align)
	f.emitExprTo(e.X, scrutOff, st)

	endLbl := f.g.freshLabel("me_end")
	for _, arm := range e.Arms {
		nextLbl := f.g.freshLabel("me_arm")
		bodyLbl := f.g.freshLabel("me_body")
		f.emitMatchPatternTest(arm.Pattern, scrutOff, st, bodyLbl, nextLbl)
		fmt.Fprintf(&f.body, "%s:\n", bodyLbl)
		f.emitExprTo(arm.Expr, dstOff, dstType)
		fmt.Fprintf(&f.body, "    jmp %s\n", endLbl)
		fmt.Fprintf(&f.body, "%s:\n", nextLbl)
	}
	fmt.Fprintf(&f.body, "%s:\n", endLbl)
}

// emitMatchPatternTest tests whether scrutinee at scrutOff (type st) matches p.
// On match, binds any payload variables and jumps to matchLbl; else jumps to missLbl.
func (f *fnGen) emitMatchPatternTest(p ast.Pattern, scrutOff int64, st types.Type, matchLbl, missLbl string) {
	switch p := p.(type) {
	case *ast.WildcardPattern:
		fmt.Fprintf(&f.body, "    jmp %s\n", matchLbl)
	case *ast.OrPattern:
		for _, alt := range p.Alts {
			tryNext := f.g.freshLabel("or_next")
			f.emitMatchPatternTest(alt, scrutOff, st, matchLbl, tryNext)
			fmt.Fprintf(&f.body, "%s:\n", tryNext)
		}
		fmt.Fprintf(&f.body, "    jmp %s\n", missLbl)
	case *ast.CtorPattern:
		if vr := f.g.info.VariantAt(p); vr != nil {
			en := st.Underlying().(*types.Enum)
			el := enumLayoutOf(en)
			switch el.tagSize {
			case 1:
				fmt.Fprintf(&f.body, "    movzbl %d(%%rbp), %%eax\n", scrutOff)
			case 2:
				fmt.Fprintf(&f.body, "    movzwl %d(%%rbp), %%eax\n", scrutOff)
			case 4:
				fmt.Fprintf(&f.body, "    movl %d(%%rbp), %%eax\n", scrutOff)
			}
			fmt.Fprintf(&f.body, "    cmpl $%d, %%eax\n", vr.Index)
			fmt.Fprintf(&f.body, "    jne %s\n", missLbl)
			// Bind payload variables
			for i, a := range p.Args {
				id, ok := a.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				sym := f.g.info.Prog.Defs[id.ID()]
				if sym == nil {
					continue
				}
				boff := f.ensureLocal(sym)
				pt := f.subst(vr.Payload[i])
				srcOff := scrutOff + el.varOffsets[vr.Index][i]
				f.emitCopyFrame(boff, srcOff, layoutOf(pt).size)
			}
			fmt.Fprintf(&f.body, "    jmp %s\n", matchLbl)
			return
		}
		if cv := f.g.info.PatConst(p); cv != nil {
			f.emitConstMatchTest(cv, scrutOff, st, matchLbl, missLbl)
			return
		}
		fmt.Fprintf(&f.body, "    jmp %s\n", missLbl)
	case *ast.LiteralPattern:
		lv := f.g.info.ValueOf(p.Lit)
		f.emitConstMatchTest(lv, scrutOff, st, matchLbl, missLbl)
	}
}

func (f *fnGen) emitConstMatchTest(cv types.Value, scrutOff int64, st types.Type, matchLbl, missLbl string) {
	if types.IsString(st) {
		sv, _ := cv.(types.StringVal)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rdi\n", scrutOff)
		fmt.Fprintf(&f.body, "    movq %d(%%rbp), %%rsi\n", scrutOff+8)
		if len(sv) == 0 {
			fmt.Fprintf(&f.body, "    xorq %%rdx, %%rdx\n")
			fmt.Fprintf(&f.body, "    xorq %%rcx, %%rcx\n")
		} else {
			slbl := f.g.internStr(string(sv))
			fmt.Fprintf(&f.body, "    leaq %s(%%rip), %%rdx\n", slbl)
			fmt.Fprintf(&f.body, "    movq $%d, %%rcx\n", len(sv))
		}
		fmt.Fprintf(&f.body, "    call _fc_rt_bytes_eq\n")
		fmt.Fprintf(&f.body, "    testq %%rax, %%rax\n")
		fmt.Fprintf(&f.body, "    jz %s\n", missLbl)
		fmt.Fprintf(&f.body, "    jmp %s\n", matchLbl)
		return
	}
	f.emitLoadScalarFromFrame(scrutOff, st)
	var imm uint64
	switch v := cv.(type) {
	case types.BoolVal:
		if bool(v) {
			imm = 1
		}
	case types.IntVal:
		imm = intValBits(v)
	case types.ErrorVal:
		imm = errSentinelID(v.Name)
	}
	// Sign/zero normalize imm to st
	l := layoutOf(st)
	if isSignedInt(st) {
		switch l.size {
		case 1:
			imm = uint64(int64(int8(imm)))
		case 2:
			imm = uint64(int64(int16(imm)))
		case 4:
			imm = uint64(int64(int32(imm)))
		}
	} else {
		switch l.size {
		case 1:
			imm &= 0xff
		case 2:
			imm &= 0xffff
		case 4:
			imm &= 0xffffffff
		}
	}
	fmt.Fprintf(&f.body, "    movabsq $%d, %%rcx\n", int64(imm))
	fmt.Fprintf(&f.body, "    cmpq %%rcx, %%rax\n")
	fmt.Fprintf(&f.body, "    jne %s\n", missLbl)
	fmt.Fprintf(&f.body, "    jmp %s\n", matchLbl)
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
