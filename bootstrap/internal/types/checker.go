package types

import (
	"fmt"
	"strings"

	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/resolve"
)

// Evaluator evaluates calls of comptime functions at compile time (spec
// 5.3). It is implemented by package comptime and injected via Config, so
// that the interpreter can itself consume the typed model.
type Evaluator interface {
	// CallComptime evaluates fn(args...) with a fresh fuel budget. A nil
	// *EvalError means success. The returned value is a TupleVal for
	// functions with several results.
	CallComptime(h Host, fn *resolve.Symbol, args []Value) (Value, *EvalError)
}

// Host is the checker interface available to an Evaluator.
type Host interface {
	// Info returns the typed model being built.
	Info() *Info
	// EnsureBody type-checks the body of the function fn if that has not
	// happened yet. It returns false if the body has type errors or is
	// currently being checked (a constant evaluation cycle).
	EnsureBody(fn *resolve.Symbol) bool
	// ConstValue returns the value of a constant symbol, checking its
	// declaration first if necessary (nil if erroneous).
	ConstValue(sym *resolve.Symbol) Value
}

// EvalError is a failed compile-time evaluation. Code 0 means the failure
// was already reported (e.g. a type error in the evaluated body).
type EvalError struct {
	Code diag.Code
	Msg  string
}

// Config configures Check.
type Config struct {
	// Evaluator for comptime calls. If nil, comptime calls are reported
	// as E4001 errors.
	Evaluator Evaluator
}

// Checker type-checks a resolved program. Use Check.
type Checker struct {
	cfg   Config
	info  *Info
	prog  *resolve.Program
	univ  *universe
	diags diag.List

	objState  []uint8 // by SymID: 0 unchecked, 1 in progress, 2 done
	bodyState []uint8 // by SymID: 0 unchecked, 1 in progress, 2 ok, 3 errors

	instCache map[string]*Named    // lookups only
	instByKey map[string]*Instance // lookups only
	pending   map[*Named][]*Named  // instances created before their origin was complete
	methods   map[*Named][]*Method // user methods per origin type (lookups only)

	// Function context.
	fn       *FuncInfo
	loops    int // enclosing for loops in the current function
	constCtx int // >0 while checking an expression that must be constant

	// unbound holds the let-else bindings (v0.2) of if statements whose
	// then block can complete normally; using one is E2002.
	unbound map[*resolve.Symbol]bool
}

// Check type-checks prog and returns the typed model and diagnostics
// (sorted). The model is complete even if there are errors; erroneous
// expressions have type Typ[Invalid] or no entry.
func Check(prog *resolve.Program, cfg Config) (*Info, diag.List) {
	n := prog.IDs.Count() + 1
	c := &Checker{
		cfg:  cfg,
		prog: prog,
		info: &Info{
			Prog:          prog,
			Types:         make([]Type, n),
			Kinds:         make([]ExprKind, n),
			Values:        make([]Value, n),
			ImplicitBytes: make([]bool, n),
			Calls:         make([]*Call, n),
			Selections:    make([]*Selection, n),
			Instances:     make([]*Instance, n),
			Variants:      make([]*Variant, n),
			Exhaustive:    make([]bool, n),
			funcOf:        map[ast.Node]*FuncInfo{},
			methodOf:      map[*resolve.Symbol]*Method{},
		},
		instCache: map[string]*Named{},
		instByKey: map[string]*Instance{},
		pending:   map[*Named][]*Named{},
		methods:   map[*Named][]*Method{},
	}
	c.growSyms()
	c.univ = c.newUniverse()
	for _, pkg := range prog.Packages {
		for _, sym := range pkg.Decls {
			c.objDecl(sym)
		}
	}
	for _, pkg := range prog.Packages {
		for _, sym := range pkg.Decls {
			if sym.Kind == resolve.SymFunc {
				c.ensureBody(sym)
			}
		}
	}
	c.checkRecursiveTypes()
	c.checkMain()
	c.finish()
	c.diags.Sort()
	return c.info, c.diags
}

// Info implements Host.
func (c *Checker) Info() *Info { return c.info }

// EnsureBody implements Host. A body that is currently being checked
// (a function evaluated from a constant inside its own body or call
// chain) is reported as E4001.
func (c *Checker) EnsureBody(fn *resolve.Symbol) bool {
	c.objDecl(fn)
	if fn != nil && int(fn.ID) < len(c.bodyState) && c.bodyState[fn.ID] == 1 {
		c.errorf(diag.E4001, fn.Ident.Range, "comptime evaluation cycle: %s is evaluated while its own body is being checked", fn.Name)
		c.bodyState[fn.ID] = 3
		return false
	}
	return c.ensureBody(fn)
}

// ConstValue implements Host.
func (c *Checker) ConstValue(sym *resolve.Symbol) Value {
	c.objDecl(sym)
	return c.info.SymValue(sym)
}

func (c *Checker) growSyms() {
	n := len(c.prog.Syms)
	for len(c.info.SymTypes) < n {
		c.info.SymTypes = append(c.info.SymTypes, nil)
		c.info.SymValues = append(c.info.SymValues, nil)
		c.objState = append(c.objState, 0)
		c.bodyState = append(c.bodyState, 0)
	}
}

func (c *Checker) errorf(code diag.Code, sp diag.SourceSpan, format string, args ...any) {
	c.diags.Addf(code, sp, format, args...)
}

func (c *Checker) setSymType(s *resolve.Symbol, t Type) {
	if s == nil {
		return
	}
	c.growSyms()
	c.info.SymTypes[s.ID] = t
	if s.Ident != nil {
		c.info.Types[s.Ident.ID()] = t
	}
}

// symType returns the type of a symbol, checking its declaration first.
func (c *Checker) symType(s *resolve.Symbol) Type {
	c.objDecl(s)
	return c.info.SymTypes[s.ID]
}

// ---------------------------------------------------------------------------
// Package-level declarations

func (c *Checker) objDecl(s *resolve.Symbol) {
	if s == nil {
		return
	}
	c.growSyms()
	switch c.objState[s.ID] {
	case 2:
		return
	case 1:
		switch s.Kind {
		case resolve.SymType:
			if _, ok := c.info.SymTypes[s.ID].(*Named); ok {
				return // reference to a type under construction (e.g. via a pointer)
			}
			c.errorf(diag.E2002, s.Ident.Range, "invalid recursive type alias %s", s.Name)
		case resolve.SymConst:
			c.errorf(diag.E2002, s.Ident.Range, "initialization cycle: constant %s refers to itself", s.Name)
		case resolve.SymVar:
			c.errorf(diag.E2002, s.Ident.Range, "initialization cycle: variable %s refers to itself", s.Name)
		default:
			return
		}
		c.info.SymTypes[s.ID] = Typ[Invalid]
		return
	}
	if s.Builtin != 0 {
		c.objState[s.ID] = 2
		switch {
		case s.Builtin.IsType():
			if t := c.predeclaredType(s.Builtin); t != nil {
				c.info.SymTypes[s.ID] = t
			}
		case s.Builtin.IsConst():
			t, v := c.predeclaredConst(s.Builtin)
			c.info.SymTypes[s.ID], c.info.SymValues[s.ID] = t, v
		}
		return
	}
	c.objState[s.ID] = 1
	switch d := s.Decl.(type) {
	case *ast.ConstSpec:
		c.constSpec(s, d)
	case *ast.TypeDecl:
		c.typeDecl(s, d)
	case *ast.VarDecl:
		c.varDecl(s, d)
	case *ast.FuncDecl:
		c.funcDecl(s, d)
	}
	c.objState[s.ID] = 2
}

// withoutFunc runs f outside any function context (package-level decls
// may be checked lazily from inside a function body).
func (c *Checker) withoutFunc(f func()) {
	fn, loops, cc := c.fn, c.loops, c.constCtx
	c.fn, c.loops, c.constCtx = nil, 0, 0
	f()
	c.fn, c.loops, c.constCtx = fn, loops, cc
}

func (c *Checker) constSpec(s *resolve.Symbol, d *ast.ConstSpec) {
	if s.IsGlobal() {
		c.withoutFunc(func() { c.constSpecIn(s, d) })
		return
	}
	c.constSpecIn(s, d)
}

func (c *Checker) constSpecIn(s *resolve.Symbol, d *ast.ConstSpec) {
	c.constCtx++
	defer func() { c.constCtx-- }()
	var t Type
	if d.Type != nil {
		t = c.typExpr(d.Type)
	}
	x := c.exprValue(d.Value)
	if x.invalid() || IsInvalid(t) && d.Type != nil {
		c.setSymType(s, Typ[Invalid])
		return
	}
	if x.mode != KindConst {
		c.errorf(diag.E2002, d.Value.Span(), "%s is not constant", exprString(d.Value))
		c.setSymType(s, Typ[Invalid])
		return
	}
	if t != nil {
		if !c.assignment(&x, t, "constant declaration") {
			c.setSymType(s, Typ[Invalid])
			return
		}
	}
	if IsScoped(x.typ) {
		c.errorf(diag.E2002, d.Value.Span(), "constant %s cannot have Scoped type %s", s.Name, x.typ)
		c.setSymType(s, Typ[Invalid])
		return
	}
	c.setSymType(s, x.typ)
	c.info.SymValues[s.ID] = x.val
	c.info.Kinds[d.Name.ID()] = KindConst
	c.info.Values[d.Name.ID()] = x.val
}

func (c *Checker) typeDecl(s *resolve.Symbol, d *ast.TypeDecl) {
	c.withoutFunc(func() {
		if d.Alias {
			if d.TypeParams != nil {
				c.errorf(diag.E2002, d.Name.Range, "generic type alias %s is not supported", s.Name)
			}
			t := c.typExpr(d.Type)
			c.setSymType(s, t)
			return
		}
		n := &Named{Name: s.Name, Sym: s, Pkg: s.Pkg}
		c.setSymType(s, n)
		c.info.Named = append(c.info.Named, n)
		if d.TypeParams != nil {
			n.TypeParams = c.typeParams(d.TypeParams)
		}
		under := c.typExpr(d.Type)
		switch u := under.(type) {
		case *Named:
			if u.underlying == nil {
				c.errorf(diag.E2002, d.Name.Range, "invalid recursive type %s", s.Name)
				n.underlying = Typ[Invalid]
			} else {
				n.underlying = u.underlying
				if u.Builtin != 0 && u.classSet {
					n.class, n.classSet = u.class, true
				}
			}
		case *TypeParam:
			c.errorf(diag.E2002, d.Type.Span(), "cannot use a type parameter as the underlying type of %s", s.Name)
			n.underlying = Typ[Invalid]
		default:
			n.underlying = under
		}
		for _, inst := range c.pending[n] {
			c.completeInstance(inst)
		}
		delete(c.pending, n)
	})
}

func (c *Checker) varDecl(s *resolve.Symbol, d *ast.VarDecl) {
	if s.IsGlobal() {
		c.withoutFunc(func() { c.varDeclIn(s, d) })
		return
	}
	c.varDeclIn(s, d)
}

func (c *Checker) varDeclIn(s *resolve.Symbol, d *ast.VarDecl) {
	global := s.IsGlobal()
	var t Type
	if d.Type != nil {
		t = c.typExpr(d.Type)
		if IsInvalid(t) {
			c.setSymType(s, Typ[Invalid])
			if d.Value != nil {
				c.exprValue(d.Value)
			}
			return
		}
		if global && IsScoped(t) {
			// S1 (E3001) takes precedence over E2005/E2004 (decision 6).
			c.errorf(diag.E3001, d.Name.Range, "package-level variable %s cannot have Scoped type %s", s.Name, t)
			c.setSymType(s, t)
			return
		}
	}
	if d.Value == nil {
		if IsScoped(t) {
			c.errorf(diag.E2005, d.Name.Range, "variable %s of Scoped type %s must be initialized", s.Name, t)
		}
		c.setSymType(s, t)
		return
	}
	if global {
		c.constCtx++
	}
	x := c.exprValue(d.Value)
	if global {
		c.constCtx--
	}
	if x.invalid() {
		if t == nil {
			t = Typ[Invalid]
		}
		c.setSymType(s, t)
		return
	}
	if t != nil {
		c.assignment(&x, t, "variable declaration")
	} else {
		t = c.defaultType(&x, "variable declaration")
		if global && IsScoped(t) {
			c.errorf(diag.E3001, d.Name.Range, "package-level variable %s cannot have Scoped type %s", s.Name, t)
		}
	}
	if global && x.mode != KindConst && !x.invalid() {
		c.errorf(diag.E2002, d.Value.Span(), "package-level variable initializer must be a constant expression")
	}
	c.setSymType(s, t)
}

// typeParams creates the type/const parameters of a declaration.
func (c *Checker) typeParams(l *ast.TypeParamList) []*TypeParam {
	var out []*TypeParam
	for _, p := range l.Params {
		var constT Type
		if _, isAny := p.Constraint.(*ast.AnyType); !isAny {
			ct := c.typExpr(p.Constraint)
			if _, _, ok := IntInfo(ct); ok {
				constT = ct
			} else if !IsInvalid(ct) {
				c.errorf(diag.E2002, p.Constraint.Span(), "unsupported constraint %s (want any or an integer type such as usize)", ct)
			}
		}
		for _, id := range p.Names {
			sym := c.prog.Defs[id.ID()]
			tp := &TypeParam{Name: id.Name, Sym: sym, Index: len(out), Const: constT}
			out = append(out, tp)
			c.setSymType(sym, tp)
		}
	}
	return out
}

func (c *Checker) funcDecl(s *resolve.Symbol, d *ast.FuncDecl) {
	c.withoutFunc(func() {
		eff := Total
		switch d.Modifier {
		case ast.ModDriver:
			eff = Driver
		case ast.ModComptime:
			eff = Comptime
		}
		fi := &FuncInfo{Sym: s, Decl: d, Effect: eff, Body: d.Body}
		if d.Decreases != nil {
			fi.Decreases = d.Decreases.Measure
		}
		c.info.funcOf[d] = fi
		if d.Recv != nil {
			c.receiver(fi, d)
		}
		if d.TypeParams != nil {
			tps := c.typeParams(d.TypeParams)
			if d.Recv != nil {
				c.errorf(diag.E2002, d.TypeParams.Range, "methods cannot have their own type parameters")
			} else {
				fi.TypeParams = tps
			}
		}
		sig := c.signature(d.Sig, eff)
		if d.Recv == nil {
			sig.TypeParams = fi.TypeParams
		}
		fi.Sig = sig
		if fi.Method != nil {
			fi.Method.Sig = sig
		}
		c.setSymType(s, sig)
		fi.Params = c.paramSyms(d.Sig, sig)
	})
}

// paramSyms records the types of parameter symbols and returns them.
func (c *Checker) paramSyms(as *ast.Signature, sig *Signature) []*resolve.Symbol {
	var out []*resolve.Symbol
	i := 0
	for _, p := range as.Params {
		for _, id := range p.Names {
			sym := c.prog.Defs[id.ID()]
			if sym == nil { // blank `_`
				sym = &resolve.Symbol{Kind: resolve.SymVar, VarKind: resolve.VarParam, Name: "_", Ident: id, Decl: p}
			}
			t := sig.Params[i]
			if sig.Variadic && i == len(sig.Params)-1 {
				t = &Slice{Elem: t}
			}
			if sym.ID != 0 {
				c.setSymType(sym, t)
			} else {
				c.info.Types[id.ID()] = t
			}
			out = append(out, sym)
			i++
		}
	}
	return out
}

func (c *Checker) receiver(fi *FuncInfo, d *ast.FuncDecl) {
	r := d.Recv
	tsym := c.prog.Uses[r.TypeName.ID()]
	m := &Method{Name: d.Name.Name, PtrRecv: r.Pointer, Sym: fi.Sym, Decl: d}
	fi.Method = m
	c.info.methodOf[fi.Sym] = m
	fi.RecvSym = c.prog.Defs[r.Name.ID()]
	if tsym == nil {
		c.setSymType(fi.RecvSym, Typ[Invalid])
		return
	}
	named, ok := c.symType(tsym).(*Named)
	if !ok || named.Sym != tsym {
		c.errorf(diag.E2002, r.TypeName.Range, "invalid receiver type %s (must be a named type declared in this package)", r.TypeName.Name)
		c.setSymType(fi.RecvSym, Typ[Invalid])
		return
	}
	m.Recv = named
	c.info.Types[r.TypeName.ID()] = named
	var recvT Type = named
	if len(r.TypeParams) != len(named.TypeParams) {
		c.errorf(diag.E2003, r.Range, "receiver %s needs %d type parameters, got %d", named.Name, len(named.TypeParams), len(r.TypeParams))
	} else if len(named.TypeParams) > 0 {
		var args []Type
		for i, id := range r.TypeParams {
			sym := c.prog.Defs[id.ID()]
			tp := &TypeParam{Name: id.Name, Sym: sym, Index: i, Const: named.TypeParams[i].Const}
			c.setSymType(sym, tp)
			m.RecvTypeParams = append(m.RecvTypeParams, tp)
			args = append(args, tp)
		}
		fi.TypeParams = m.RecvTypeParams
		recvT = c.instantiate(named, args, nil)
	}
	if r.Pointer {
		recvT = &Pointer{Elem: recvT}
	}
	c.setSymType(fi.RecvSym, recvT)
}

// methodsOf returns the user methods of a (origin) named type.
func (c *Checker) methodsOf(n *Named) []*Method {
	if n.Origin != nil {
		n = n.Origin
	}
	if n.Builtin != 0 {
		return c.univ.methods[n]
	}
	if ms, ok := c.methods[n]; ok {
		return ms
	}
	var out []*Method
	if n.Sym != nil && n.Sym.Pkg != nil {
		for _, ms := range n.Sym.Pkg.Methods {
			if ms.RecvType != n.Name {
				continue
			}
			c.objDecl(ms)
			if m := c.info.methodOf[ms]; m != nil && m.Recv == n {
				out = append(out, m)
			}
		}
	}
	c.methods[n] = out
	return out
}

func (c *Checker) lookupMethod(n *Named, name string) *Method {
	for _, m := range c.methodsOf(n) {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Function bodies

func (c *Checker) ensureBody(s *resolve.Symbol) bool {
	if s == nil || s.Kind != resolve.SymFunc {
		return false
	}
	c.objDecl(s)
	switch c.bodyState[s.ID] {
	case 1, 3:
		return false
	case 2:
		return true
	}
	d, ok := s.Decl.(*ast.FuncDecl)
	if !ok || d.Body == nil {
		return false
	}
	fi := c.info.funcOf[d]
	if fi == nil {
		return false
	}
	c.bodyState[s.ID] = 1
	before := len(c.diags)
	saveFn, saveLoops, saveCC := c.fn, c.loops, c.constCtx
	c.fn, c.loops, c.constCtx = fi, 0, 0
	c.funcBody(fi)
	c.fn, c.loops, c.constCtx = saveFn, saveLoops, saveCC
	if len(c.diags) > before {
		c.bodyState[s.ID] = 3
		return false
	}
	c.bodyState[s.ID] = 2
	return true
}

func (c *Checker) funcBody(fi *FuncInfo) {
	if fi.Decreases != nil {
		c.measure(fi.Decreases)
	}
	c.stmtList(fi.Body.Stmts)
	if len(fi.Sig.Results) > 0 && !c.isTerminatingList(fi.Body.Stmts) {
		end := fi.Body.Range
		end.Start = end.End
		if end.Start.Col > 1 {
			end.Start.Col--
			end.Start.Offset--
		}
		c.errorf(diag.E2003, end, "missing return at end of %s", fi.Name())
	}
}

// measure checks a decreases(M) expression: M must be a usize value.
func (c *Checker) measure(e ast.Expr) {
	x := c.exprValue(e)
	if x.invalid() {
		return
	}
	c.assignment(&x, Typ[Usize], "decreases clause")
}

// ---------------------------------------------------------------------------
// Whole-program checks

// checkRecursiveTypes reports named types that contain themselves by value.
func (c *Checker) checkRecursiveTypes() {
	state := map[*Named]int{}
	var visit func(t Type) bool
	var visitNamed func(n *Named) bool
	visit = func(t Type) bool {
		switch t := t.(type) {
		case *Named:
			return visitNamed(t)
		case *Array:
			return visit(t.Elem)
		case *Struct:
			for _, f := range t.Fields {
				if visit(f.Type) {
					return true
				}
			}
		case *Enum:
			for _, v := range t.Variants {
				for _, p := range v.Payload {
					if visit(p) {
						return true
					}
				}
			}
		}
		return false
	}
	visitNamed = func(n *Named) bool {
		if n.Origin != nil {
			n = n.Origin
		}
		switch state[n] {
		case 1:
			return true
		case 2:
			return false
		}
		state[n] = 1
		bad := visit(n.underlying)
		state[n] = 2
		return bad
	}
	for _, n := range c.info.Named {
		if state[n] == 0 && visitNamed(n) && n.Sym != nil && n.Sym.Ident != nil {
			c.errorf(diag.E2002, n.Sym.Ident.Range, "invalid recursive type %s (contains itself by value)", n.Name)
		}
	}
}

// checkMain validates the entry point of the root package (spec 6.1,
// decision 16): `driver func main(<subset of Net, Signals, Clock, FS, Env,
// Stdio, Exec>) error`.
func (c *Checker) checkMain() {
	root := c.prog.Root
	if root == nil || root.Name != "main" || root.Scope == nil {
		return
	}
	sym := root.Scope.LookupLocal("main")
	if sym == nil {
		return
	}
	d, ok := sym.Decl.(*ast.FuncDecl)
	if sym.Kind != resolve.SymFunc || !ok {
		c.errorf(diag.E5002, sym.Ident.Range, "main must be a function")
		return
	}
	fi := c.info.funcOf[d]
	c.info.Main = fi
	bad := func(format string, args ...any) {
		c.errorf(diag.E5002, d.Name.Range, "invalid main signature: "+format+" (want driver func main(<capabilities>) error)", args...)
	}
	switch {
	case d.Modifier != ast.ModDriver:
		bad("main must be a driver func")
		return
	case d.TypeParams != nil:
		bad("main cannot have type parameters")
		return
	case fi == nil || fi.Sig == nil:
		return
	}
	seen := map[resolve.Builtin]bool{}
	for i, p := range fi.Sig.Params {
		if IsInvalid(p) {
			return
		}
		if !isCapability(p) || (fi.Sig.Variadic && i == len(fi.Sig.Params)-1) {
			bad("parameter of type %s is not a capability (Net, Signals, Clock, FS, Env, Stdio, Exec)", p)
			return
		}
		if seen[BuiltinOf(p)] {
			bad("duplicate capability %s", p)
			return
		}
		seen[BuiltinOf(p)] = true
	}
	if len(fi.Sig.Results) != 1 || basicKind(fi.Sig.Results[0]) != Error {
		bad("main must return exactly error")
	}
}

// finish orders Info.Funcs and gives remaining untyped expressions their
// default types.
func (c *Checker) finish() {
	var add func(fi *FuncInfo)
	add = func(fi *FuncInfo) {
		c.info.Funcs = append(c.info.Funcs, fi)
		for _, l := range fi.Lits {
			add(l)
		}
	}
	for _, pkg := range c.prog.Packages {
		for _, sym := range pkg.Decls {
			if d, ok := sym.Decl.(*ast.FuncDecl); ok && sym.Kind == resolve.SymFunc {
				if fi := c.info.funcOf[d]; fi != nil {
					add(fi)
				}
			}
		}
	}
	for i, t := range c.info.Types {
		if t == nil {
			continue
		}
		if IsUntyped(t) && basicKind(t) != UntypedNil {
			c.info.Types[i] = Default(t)
		} else if cd, ok := t.(*Code); ok && cd.Level < 0 {
			c.info.Types[i] = Default(t)
		}
	}
	c.info.UntypedConsts = make([]bool, len(c.info.SymTypes))
	for i, t := range c.info.SymTypes {
		if t != nil && IsUntyped(t) {
			if i < len(c.prog.Syms) && c.prog.Syms[i] != nil && c.prog.Syms[i].Kind == resolve.SymConst {
				c.info.UntypedConsts[i] = true
			}
			c.info.SymTypes[i] = Default(t)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers

func exprString(e ast.Expr) string {
	var b strings.Builder
	writeExpr(&b, e)
	s := b.String()
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

func writeExpr(b *strings.Builder, e ast.Expr) {
	switch e := e.(type) {
	case *ast.Ident:
		b.WriteString(e.Name)
	case *ast.BasicLit:
		b.WriteString(e.Raw)
	case *ast.BoolLit:
		fmt.Fprint(b, e.Value)
	case *ast.NilLit:
		b.WriteString("nil")
	case *ast.BinaryExpr:
		writeExpr(b, e.X)
		b.WriteString(" " + e.Op.String() + " ")
		writeExpr(b, e.Y)
	case *ast.UnaryExpr:
		b.WriteString(e.Op.String())
		writeExpr(b, e.X)
	case *ast.ParenExpr:
		b.WriteString("(")
		writeExpr(b, e.X)
		b.WriteString(")")
	case *ast.CallExpr:
		writeExpr(b, e.Fun)
		b.WriteString("(")
		for i, a := range e.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			writeExpr(b, a)
		}
		b.WriteString(")")
	case *ast.IndexExpr:
		writeExpr(b, e.X)
		b.WriteString("[")
		for i, a := range e.Indices {
			if i > 0 {
				b.WriteString(", ")
			}
			writeExpr(b, a)
		}
		b.WriteString("]")
	case *ast.SliceExpr:
		writeExpr(b, e.X)
		b.WriteString("[")
		if e.Lo != nil {
			writeExpr(b, e.Lo)
		}
		b.WriteString(":")
		if e.Hi != nil {
			writeExpr(b, e.Hi)
		}
		b.WriteString("]")
	case *ast.SelectorExpr:
		writeExpr(b, e.X)
		b.WriteString("." + e.Sel.Name)
	case *ast.FuncLit:
		b.WriteString("func literal")
	case *ast.StructLit:
		if e.Type.Pkg != nil {
			b.WriteString(e.Type.Pkg.Name + ".")
		}
		b.WriteString(e.Type.Name.Name + "{...}")
	case *ast.TypeExpr:
		b.WriteString("type expression")
	case *ast.MatchExpr:
		b.WriteString("match expression")
	case *ast.TupleExpr:
		b.WriteString("(")
		for i, a := range e.Elems {
			if i > 0 {
				b.WriteString(", ")
			}
			writeExpr(b, a)
		}
		b.WriteString(")")
	case *ast.IsExpr:
		writeExpr(b, e.X)
		b.WriteString(" is " + ast.Format(e.Pattern))
	default:
		b.WriteString("expression")
	}
}
