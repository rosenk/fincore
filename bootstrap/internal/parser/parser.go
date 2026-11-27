// Package parser implements a recursive-descent parser for FinCore
// (spec section 3).
//
// Error handling: the parser stops at the first error in each file (lexical
// or syntactic) and reports it as a single diagnostic (E1001, or E1002 for
// unterminated literals/comments). This keeps the output deterministic and
// identical to what the self-hosted parser is expected to produce.
//
// Resolved grammar ambiguities (all Go-like):
//
//   - Composite literals vs blocks: in the header of if/for/match/scope (and
//     in `Code n` levels) a `T{` is not parsed as a struct literal unless the
//     expression is parenthesized or nested inside (), [] or a call.
//   - `type A [N]T` (array) vs `type A[N usize] T` (type parameters): it is
//     a type parameter list iff the '[' is followed by IDENT and then by
//     IDENT, `any` or ','.
//   - `X[...]` after any operand is an IndexExpr (indexing or generic
//     instantiation, decided by the resolver); `X[lo:hi]` is a SliceExpr.
//   - Casts `u32(x)` are CallExprs.
//   - A semicolon may be omitted before a closing ')' or '}' (Go rule), so
//     `{ return x }` and single-line groups are valid.
//   - Variadic parameters `xs ...T` (the `...` token of section 2.5) are
//     accepted for the last parameter only.
package parser

import (
	"fincore/bootstrap/internal/ast"
	"fincore/bootstrap/internal/diag"
	"fincore/bootstrap/internal/lexer"
)

// MaxNesting bounds syntactic nesting depth to keep the parser (and later
// recursive passes) from overflowing on pathological input.
const MaxNesting = 1000

type bailout struct{ d diag.Diagnostic }

type parser struct {
	file    string
	toks    []lexer.Token
	pos     int
	tok     lexer.Token
	prevEnd diag.Pos
	exprLev int // < 0: in control clause (no struct literals)
	nest    int
	tupleOK bool // the next operand may be a tuple (match scrutinee, v0.2)
}

// ParseFile parses one source file. On error it returns a nil file and a
// list with exactly one diagnostic. ids may be nil.
func ParseFile(path string, src []byte, ids *ast.IDAlloc) (f *ast.File, diags diag.List) {
	if ids == nil {
		ids = &ast.IDAlloc{}
	}
	toks, _ := lexer.Tokenize(path, src)
	p := &parser{file: path, toks: toks}
	defer func() {
		if r := recover(); r != nil {
			b, ok := r.(bailout)
			if !ok {
				panic(r)
			}
			f = nil
			diags = diag.List{b.d}
		}
	}()
	p.pos = -1
	p.next()
	f = p.parseFile()
	assignIDs(f, ids)
	return f, nil
}

// assignIDs numbers all nodes of the finished tree in post-order (children
// before parents, children in source order). Doing this after parsing keeps
// IDs dense even when the parser restructures nodes (e.g. converting an
// operand expression into the NamedType of a struct literal).
func assignIDs(n ast.Node, ids *ast.IDAlloc) {
	for _, c := range ast.Children(n) {
		assignIDs(c, ids)
	}
	ast.Init(n, ids.Next(), n.Span())
}

// ---------------------------------------------------------------------------
// token handling

func (p *parser) next() {
	if p.pos >= 0 {
		p.prevEnd = p.tok.Span.End
	}
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	p.tok = p.toks[p.pos]
	if p.tok.Kind == lexer.ILLEGAL {
		panic(bailout{*p.tok.Err})
	}
}

func (p *parser) peek(k int) lexer.Token {
	i := p.pos + k
	if i >= len(p.toks) {
		i = len(p.toks) - 1
	}
	return p.toks[i]
}

func (p *parser) errorAt(sp diag.SourceSpan, format string, args ...any) {
	panic(bailout{diag.New(diag.E1001, sp, format, args...)})
}

func (p *parser) errorExpected(what string) {
	p.errorAt(p.tok.Span, "expected %s, found %s", what, p.tok.Describe())
}

func (p *parser) expect(k lexer.Kind) lexer.Token {
	if p.tok.Kind != k {
		p.errorExpected("'" + k.String() + "'")
	}
	t := p.tok
	p.next()
	return t
}

// expectSemi requires a statement/declaration terminator, which may be
// omitted before a closing ')' or '}'.
func (p *parser) expectSemi(context string) {
	switch p.tok.Kind {
	case lexer.SEMICOLON:
		p.next()
	case lexer.RPAREN, lexer.RBRACE:
	default:
		p.errorAt(p.tok.Span, "unexpected %s after %s; expected ';' or newline", p.tok.Describe(), context)
	}
}

func (p *parser) enter() {
	p.nest++
	if p.nest > MaxNesting {
		p.errorAt(p.tok.Span, "nesting too deep (limit %d)", MaxNesting)
	}
}

func (p *parser) leave() { p.nest-- }

func (p *parser) start() diag.Pos { return p.tok.Span.Start }

// finish sets the span [start, end of last consumed token). NodeIDs are
// assigned afterwards by assignIDs.
func finish[T ast.Node](p *parser, n T, start diag.Pos) T {
	ast.SetSpan(n, diag.SourceSpan{File: p.file, Start: start, End: p.prevEnd})
	return n
}

// ---------------------------------------------------------------------------
// file level

func (p *parser) parseFile() *ast.File {
	start := p.start()
	f := &ast.File{Path: p.file}
	if p.tok.Kind != lexer.PACKAGE {
		p.errorAt(p.tok.Span, "expected package clause, found %s", p.tok.Describe())
	}
	p.next()
	f.Package = p.ident()
	p.expectFileSemi("package clause")
	for p.tok.Kind == lexer.IMPORT {
		f.Imports = append(f.Imports, p.importDecl())
		p.expectFileSemi("import declaration")
	}
	for p.tok.Kind != lexer.EOF {
		f.Decls = append(f.Decls, p.topDecl())
		p.expectFileSemi("declaration")
	}
	return finish(p, f, start)
}

func (p *parser) expectFileSemi(context string) {
	switch p.tok.Kind {
	case lexer.SEMICOLON:
		p.next()
	case lexer.EOF:
	default:
		p.errorAt(p.tok.Span, "unexpected %s after %s; expected ';' or newline", p.tok.Describe(), context)
	}
}

func (p *parser) ident() *ast.Ident {
	start := p.start()
	if p.tok.Kind != lexer.IDENT {
		p.errorExpected("identifier")
	}
	name := p.tok.Lit
	p.next()
	return finish(p, &ast.Ident{Name: name}, start)
}

func (p *parser) importDecl() *ast.ImportDecl {
	start := p.start()
	p.expect(lexer.IMPORT)
	d := &ast.ImportDecl{}
	if p.tok.Kind == lexer.LPAREN {
		d.Grouped = true
		p.next()
		for p.tok.Kind != lexer.RPAREN {
			d.Specs = append(d.Specs, p.importSpec())
			p.expectSemi("import spec")
		}
		p.next()
	} else {
		d.Specs = append(d.Specs, p.importSpec())
	}
	return finish(p, d, start)
}

func (p *parser) importSpec() *ast.ImportSpec {
	start := p.start()
	s := &ast.ImportSpec{}
	if p.tok.Kind == lexer.IDENT {
		s.Name = p.ident()
	}
	if p.tok.Kind != lexer.STRING {
		p.errorExpected("import path string")
	}
	s.Path = p.basicLit()
	if s.Path.Str == "" {
		p.errorAt(s.Path.Range, "empty import path")
	}
	return finish(p, s, start)
}

func (p *parser) topDecl() ast.Decl {
	switch p.tok.Kind {
	case lexer.CONST:
		return p.constDecl()
	case lexer.TYPE:
		return p.typeDecl()
	case lexer.VAR:
		return p.varDecl()
	case lexer.FUNC, lexer.DRIVER, lexer.COMPTIME:
		return p.funcDecl()
	case lexer.IMPORT:
		p.errorAt(p.tok.Span, "imports must appear before other declarations")
	}
	p.errorAt(p.tok.Span, "expected declaration (const, type, var or func), found %s", p.tok.Describe())
	return nil
}

// ---------------------------------------------------------------------------
// declarations

func (p *parser) constDecl() *ast.ConstDecl {
	start := p.start()
	p.expect(lexer.CONST)
	d := &ast.ConstDecl{}
	if p.tok.Kind == lexer.LPAREN {
		d.Grouped = true
		p.next()
		for p.tok.Kind != lexer.RPAREN {
			d.Specs = append(d.Specs, p.constSpec())
			p.expectSemi("constant declaration")
		}
		p.next()
	} else {
		d.Specs = append(d.Specs, p.constSpec())
	}
	return finish(p, d, start)
}

func (p *parser) constSpec() *ast.ConstSpec {
	start := p.start()
	s := &ast.ConstSpec{Name: p.ident()}
	if p.tok.Kind != lexer.ASSIGN {
		s.Type = p.parseType()
	}
	if p.tok.Kind != lexer.ASSIGN {
		p.errorAt(p.tok.Span, "expected '=' in constant declaration, found %s (constants must be initialized)", p.tok.Describe())
	}
	p.next()
	s.Value = p.expr()
	return finish(p, s, start)
}

func (p *parser) typeDecl() *ast.TypeDecl {
	start := p.start()
	p.expect(lexer.TYPE)
	d := &ast.TypeDecl{Name: p.ident()}
	if p.tok.Kind == lexer.LBRACK && p.looksLikeTypeParams() {
		d.TypeParams = p.typeParams()
	}
	if p.tok.Kind == lexer.ASSIGN {
		d.Alias = true
		p.next()
	}
	d.Type = p.parseType()
	return finish(p, d, start)
}

// looksLikeTypeParams decides `type A[N usize] ...` vs `type A [N]T`.
func (p *parser) looksLikeTypeParams() bool {
	t1, t2 := p.peek(1), p.peek(2)
	if t1.Kind != lexer.IDENT {
		return false
	}
	switch t2.Kind {
	case lexer.IDENT, lexer.ANY, lexer.COMMA:
		return true
	}
	return false
}

func (p *parser) varDecl() *ast.VarDecl {
	start := p.start()
	p.expect(lexer.VAR)
	d := &ast.VarDecl{Name: p.ident()}
	if p.tok.Kind == lexer.ASSIGN {
		p.next()
		d.Value = p.expr()
	} else {
		d.Type = p.parseType()
		if p.tok.Kind == lexer.ASSIGN {
			p.next()
			d.Value = p.expr()
		}
	}
	return finish(p, d, start)
}

func (p *parser) funcDecl() *ast.FuncDecl {
	start := p.start()
	d := &ast.FuncDecl{}
	switch p.tok.Kind {
	case lexer.DRIVER:
		d.Modifier = ast.ModDriver
		p.next()
	case lexer.COMPTIME:
		d.Modifier = ast.ModComptime
		p.next()
	}
	p.expect(lexer.FUNC)
	if p.tok.Kind == lexer.LPAREN {
		d.Recv = p.receiver()
	}
	d.Name = p.ident()
	if p.tok.Kind == lexer.LBRACK {
		d.TypeParams = p.typeParams()
	}
	d.Sig = p.signature()
	if p.tok.Kind == lexer.DECREASES {
		d.Decreases = p.decreases()
	}
	if p.tok.Kind != lexer.LBRACE {
		if p.tok.Kind == lexer.SEMICOLON && p.tok.Lit == "\n" {
			p.errorAt(p.tok.Span, "unexpected newline; the '{' of a function body must be on the same line")
		}
		p.errorExpected("'{' (function body)")
	}
	d.Body = p.block()
	return finish(p, d, start)
}

func (p *parser) receiver() *ast.Receiver {
	start := p.start()
	p.expect(lexer.LPAREN)
	r := &ast.Receiver{Name: p.ident()}
	if p.tok.Kind == lexer.MUL {
		r.Pointer = true
		p.next()
	}
	r.TypeName = p.ident()
	if p.tok.Kind == lexer.LBRACK {
		p.next()
		r.TypeParams = p.identList()
		p.expect(lexer.RBRACK)
	}
	p.expect(lexer.RPAREN)
	return finish(p, r, start)
}

func (p *parser) identList() []*ast.Ident {
	ids := []*ast.Ident{p.ident()}
	for p.tok.Kind == lexer.COMMA {
		p.next()
		ids = append(ids, p.ident())
	}
	return ids
}

func (p *parser) typeParams() *ast.TypeParamList {
	start := p.start()
	p.expect(lexer.LBRACK)
	l := &ast.TypeParamList{}
	for {
		l.Params = append(l.Params, p.typeParam())
		if p.tok.Kind != lexer.COMMA {
			break
		}
		p.next()
	}
	p.expect(lexer.RBRACK)
	return finish(p, l, start)
}

func (p *parser) typeParam() *ast.TypeParam {
	start := p.start()
	tp := &ast.TypeParam{Names: p.identList()}
	if p.tok.Kind == lexer.ANY {
		s := p.start()
		p.next()
		tp.Constraint = finish(p, &ast.AnyType{}, s)
	} else {
		if !p.startsType() {
			p.errorExpected("type constraint (any, usize or a type)")
		}
		tp.Constraint = p.parseType()
	}
	return finish(p, tp, start)
}

func (p *parser) signature() *ast.Signature {
	start := p.start()
	s := &ast.Signature{}
	p.expect(lexer.LPAREN)
	p.exprLev++
	for p.tok.Kind != lexer.RPAREN {
		prm := p.param()
		s.Params = append(s.Params, prm)
		if p.tok.Kind != lexer.COMMA {
			if p.tok.Kind != lexer.RPAREN {
				p.errorExpected("',' or ')' in parameter list")
			}
			break
		}
		p.next()
	}
	for i, prm := range s.Params {
		if prm.Variadic && i != len(s.Params)-1 {
			p.errorAt(prm.Range, "can only use ... with final parameter")
		}
	}
	p.exprLev--
	p.expect(lexer.RPAREN)
	switch {
	case p.tok.Kind == lexer.LPAREN:
		s.ResultParen = true
		p.next()
		p.exprLev++
		for {
			s.Results = append(s.Results, p.parseType())
			if p.tok.Kind != lexer.COMMA {
				break
			}
			p.next()
		}
		p.exprLev--
		p.expect(lexer.RPAREN)
	case p.startsType():
		s.Results = append(s.Results, p.parseType())
	}
	return finish(p, s, start)
}

func (p *parser) param() *ast.Param {
	start := p.start()
	prm := &ast.Param{Names: p.identList()}
	if p.tok.Kind == lexer.ELLIPSIS {
		if len(prm.Names) != 1 {
			p.errorAt(p.tok.Span, "variadic parameter must have exactly one name")
		}
		prm.Variadic = true
		p.next()
	}
	if !p.startsType() {
		p.errorExpected("parameter type")
	}
	prm.Type = p.parseType()
	return finish(p, prm, start)
}

func (p *parser) decreases() *ast.DecreasesClause {
	start := p.start()
	p.expect(lexer.DECREASES)
	p.expect(lexer.LPAREN)
	old := p.exprLev
	p.exprLev = 0
	m := p.expr()
	p.exprLev = old
	p.expect(lexer.RPAREN)
	return finish(p, &ast.DecreasesClause{Measure: m}, start)
}

// ---------------------------------------------------------------------------
// types

// startsType reports whether the current token can begin a Type.
func (p *parser) startsType() bool {
	switch p.tok.Kind {
	case lexer.IDENT, lexer.LBRACK, lexer.MUL, lexer.STRUCT, lexer.ENUM, lexer.FUNC:
		return true
	case lexer.DRIVER:
		return p.peek(1).Kind == lexer.FUNC
	}
	return false
}

func (p *parser) isCodeKeyword() bool {
	if p.tok.Kind != lexer.IDENT || p.tok.Lit != "Code" {
		return false
	}
	switch p.peek(1).Kind {
	case lexer.INT, lexer.IDENT, lexer.LPAREN:
		return true
	}
	return false
}

func (p *parser) isElKeyword() bool {
	return p.tok.Kind == lexer.IDENT && p.tok.Lit == "El" && p.peek(1).Kind == lexer.LPAREN
}

func (p *parser) parseType() ast.Type {
	p.enter()
	defer p.leave()
	start := p.start()
	switch p.tok.Kind {
	case lexer.IDENT:
		if p.tok.Lit == "Code" {
			p.next()
			old := p.exprLev
			p.exprLev = -1
			lvl := p.expr()
			p.exprLev = old
			return finish(p, &ast.CodeType{Level: lvl}, start)
		}
		if p.isElKeyword() {
			p.next()
			p.expect(lexer.LPAREN)
			old := p.exprLev
			p.exprLev = 0
			code := p.expr()
			p.exprLev = old
			p.expect(lexer.RPAREN)
			return finish(p, &ast.ElType{Code: code}, start)
		}
		return p.namedType()
	case lexer.LBRACK:
		p.next()
		if p.tok.Kind == lexer.RBRACK {
			p.next()
			elem := p.parseType()
			return finish(p, &ast.SliceType{Elem: elem}, start)
		}
		old := p.exprLev
		p.exprLev = 0
		n := p.expr()
		p.exprLev = old
		p.expect(lexer.RBRACK)
		elem := p.parseType()
		return finish(p, &ast.ArrayType{Len: n, Elem: elem}, start)
	case lexer.MUL:
		p.next()
		elem := p.parseType()
		return finish(p, &ast.PointerType{Elem: elem}, start)
	case lexer.STRUCT:
		return p.structType()
	case lexer.ENUM:
		return p.enumType()
	case lexer.FUNC, lexer.DRIVER:
		return p.funcType()
	case lexer.ANY:
		p.errorAt(p.tok.Span, "'any' may only be used as a type parameter constraint")
	}
	p.errorExpected("type")
	return nil
}

func (p *parser) namedType() *ast.NamedType {
	start := p.start()
	t := &ast.NamedType{Name: p.ident()}
	if p.tok.Kind == lexer.PERIOD {
		p.next()
		t.Pkg = t.Name
		t.Name = p.ident()
	}
	if p.tok.Kind == lexer.LBRACK {
		p.next()
		old := p.exprLev
		p.exprLev = 0
		for {
			t.Args = append(t.Args, p.typeOrExpr())
			if p.tok.Kind != lexer.COMMA {
				break
			}
			p.next()
		}
		p.exprLev = old
		p.expect(lexer.RBRACK)
	}
	return finish(p, t, start)
}

// typeOrExpr parses a TypeOrExpr in a type context (generic arguments of a
// NamedType). Tokens that can only start a type ('[', '*', struct, enum,
// func, Code n, El(...)) yield a *ast.TypeExpr; everything else is an
// expression (an identifier T stays an *ast.Ident).
func (p *parser) typeOrExpr() ast.Expr {
	switch {
	case p.tok.Kind == lexer.LBRACK, p.tok.Kind == lexer.MUL, p.tok.Kind == lexer.STRUCT,
		p.tok.Kind == lexer.ENUM, p.tok.Kind == lexer.FUNC,
		p.tok.Kind == lexer.DRIVER && p.peek(1).Kind == lexer.FUNC,
		p.isCodeKeyword(), p.isElKeyword():
		start := p.start()
		t := p.parseType()
		return finish(p, &ast.TypeExpr{Type: t}, start)
	}
	return p.expr()
}

func (p *parser) structType() *ast.StructType {
	start := p.start()
	p.expect(lexer.STRUCT)
	p.expectOpenBrace("struct")
	st := &ast.StructType{}
	for p.tok.Kind != lexer.RBRACE {
		fs := p.start()
		f := &ast.FieldDecl{Names: p.identList()}
		if !p.startsType() {
			p.errorExpected("field type")
		}
		f.Type = p.parseType()
		st.Fields = append(st.Fields, finish(p, f, fs))
		p.expectSemi("field declaration")
	}
	p.next()
	return finish(p, st, start)
}

func (p *parser) enumType() *ast.EnumType {
	start := p.start()
	p.expect(lexer.ENUM)
	p.expectOpenBrace("enum")
	et := &ast.EnumType{}
	for p.tok.Kind != lexer.RBRACE {
		vs := p.start()
		v := &ast.Variant{Name: p.ident()}
		if p.tok.Kind == lexer.LPAREN {
			v.HasParens = true
			p.next()
			for {
				v.Payload = append(v.Payload, p.parseType())
				if p.tok.Kind != lexer.COMMA {
					break
				}
				p.next()
			}
			p.expect(lexer.RPAREN)
		}
		et.Variants = append(et.Variants, finish(p, v, vs))
		p.expectSemi("enum variant")
	}
	p.next()
	return finish(p, et, start)
}

func (p *parser) expectOpenBrace(what string) {
	if p.tok.Kind != lexer.LBRACE {
		if p.tok.Kind == lexer.SEMICOLON && p.tok.Lit == "\n" {
			p.errorAt(p.tok.Span, "unexpected newline; the '{' of %s must be on the same line", what)
		}
		p.errorExpected("'{'")
	}
	p.next()
}

func (p *parser) funcType() *ast.FuncType {
	start := p.start()
	ft := &ast.FuncType{}
	if p.tok.Kind == lexer.DRIVER {
		ft.Driver = true
		p.next()
	}
	p.expect(lexer.FUNC)
	ft.Sig = p.signature()
	return finish(p, ft, start)
}

// ---------------------------------------------------------------------------
// statements

func (p *parser) block() *ast.BlockStmt {
	start := p.start()
	p.expectOpenBrace("block")
	old := p.exprLev
	p.exprLev = 0
	b := &ast.BlockStmt{}
	for p.tok.Kind != lexer.RBRACE {
		if p.tok.Kind == lexer.EOF {
			p.errorAt(p.tok.Span, "unexpected EOF; expected '}'")
		}
		b.Stmts = append(b.Stmts, p.stmt())
		p.expectSemi("statement")
	}
	p.exprLev = old
	p.next()
	return finish(p, b, start)
}

func (p *parser) stmt() ast.Stmt {
	p.enter()
	defer p.leave()
	start := p.start()
	switch p.tok.Kind {
	case lexer.CONST:
		d := p.constDecl()
		return finish(p, &ast.DeclStmt{Decl: d}, start)
	case lexer.VAR:
		d := p.varDecl()
		return finish(p, &ast.DeclStmt{Decl: d}, start)
	case lexer.IF:
		return p.ifStmt()
	case lexer.MATCH:
		return p.matchStmt()
	case lexer.FOR:
		return p.forStmt()
	case lexer.DRIVER:
		if p.peek(1).Kind == lexer.FOR {
			return p.forStmt()
		}
	case lexer.SCOPE:
		return p.scopeStmt()
	case lexer.DEFER:
		p.next()
		x := p.expr()
		return finish(p, &ast.DeferStmt{X: x}, start)
	case lexer.RETURN:
		p.next()
		r := &ast.ReturnStmt{}
		if p.tok.Kind != lexer.SEMICOLON && p.tok.Kind != lexer.RBRACE {
			r.Results = p.exprList()
		}
		return finish(p, r, start)
	case lexer.BREAK:
		p.next()
		return finish(p, &ast.BreakStmt{}, start)
	case lexer.CONTINUE:
		p.next()
		return finish(p, &ast.ContinueStmt{}, start)
	case lexer.LBRACE:
		return p.block()
	case lexer.SEMICOLON:
		p.errorAt(p.tok.Span, "unexpected %s; expected statement", p.tok.Describe())
	case lexer.TYPE, lexer.IMPORT, lexer.PACKAGE, lexer.COMPTIME:
		p.errorAt(p.tok.Span, "unexpected %s in statement position", p.tok.Describe())
	}
	return p.simpleStmt()
}

func (p *parser) simpleStmt() ast.Stmt {
	start := p.start()
	s, x := p.simpleStmtOrExpr()
	if s != nil {
		return s
	}
	return finish(p, &ast.ExprStmt{X: x}, start)
}

// simpleStmtOrExpr parses a SimpleStmt. A bare expression is returned
// unwrapped as x (s == nil), so that if/for conditions do not allocate a
// throw-away ExprStmt node (keeping NodeIDs dense).
func (p *parser) simpleStmtOrExpr() (s ast.Stmt, x ast.Expr) {
	start := p.start()
	lhs := p.exprList()
	switch p.tok.Kind {
	case lexer.DEFINE:
		names := make([]*ast.Ident, len(lhs))
		for i, e := range lhs {
			id, ok := e.(*ast.Ident)
			if !ok {
				p.errorAt(e.Span(), "non-name %s on left side of :=", ast.Format(e))
			}
			names[i] = id
		}
		p.next()
		rhs := p.exprList()
		return finish(p, &ast.ShortVarDecl{Names: names, Values: rhs}, start), nil
	case lexer.ASSIGN, lexer.ADD_ASSIGN, lexer.SUB_ASSIGN, lexer.MUL_ASSIGN, lexer.QUO_ASSIGN:
		var op ast.AssignOp
		switch p.tok.Kind {
		case lexer.ASSIGN:
			op = ast.Assign
		case lexer.ADD_ASSIGN:
			op = ast.AddAssign
		case lexer.SUB_ASSIGN:
			op = ast.SubAssign
		case lexer.MUL_ASSIGN:
			op = ast.MulAssign
		case lexer.QUO_ASSIGN:
			op = ast.QuoAssign
		}
		opTok := p.tok
		p.next()
		rhs := p.exprList()
		if op != ast.Assign && (len(lhs) != 1 || len(rhs) != 1) {
			p.errorAt(opTok.Span, "assignment operator %s requires single-valued expressions", op)
		}
		return finish(p, &ast.AssignStmt{LHS: lhs, Op: op, RHS: rhs}, start), nil
	case lexer.INC, lexer.DEC:
		if len(lhs) != 1 {
			p.errorAt(p.tok.Span, "unexpected %s after expression list", p.tok.Describe())
		}
		inc := p.tok.Kind == lexer.INC
		p.next()
		return finish(p, &ast.IncDecStmt{X: lhs[0], Inc: inc}, start), nil
	}
	if len(lhs) > 1 {
		p.errorExpected("':=', '=' or assignment operator after expression list")
	}
	return nil, lhs[0]
}

// controlHeaderSemi reports whether the current token is an explicit ';'
// separating clauses of an if/for header.
func (p *parser) explicitSemi() bool {
	return p.tok.Kind == lexer.SEMICOLON && p.tok.Lit == ";"
}

func (p *parser) newlineBeforeBrace(what string) {
	if p.tok.Kind == lexer.SEMICOLON && p.tok.Lit == "\n" {
		p.errorAt(p.tok.Span, "unexpected newline; the '{' of %s must be on the same line", what)
	}
}

func (p *parser) condOf(s ast.Stmt, x ast.Expr, what string) ast.Expr {
	if s != nil {
		p.errorAt(s.Span(), "cannot use %s as value in %s condition", ast.Format(s), what)
	}
	return x
}

// wrap turns the result of simpleStmtOrExpr into a statement.
func (p *parser) wrap(s ast.Stmt, x ast.Expr) ast.Stmt {
	if s != nil {
		return s
	}
	return finish(p, &ast.ExprStmt{X: x}, x.Span().Start)
}

func (p *parser) ifStmt() *ast.IfStmt {
	start := p.start()
	p.expect(lexer.IF)
	s := &ast.IfStmt{}
	old := p.exprLev
	p.exprLev = -1
	if p.tok.Kind == lexer.LBRACE {
		p.errorAt(p.tok.Span, "missing condition in if statement")
	}
	first, firstX := p.simpleStmtOrExpr()
	if p.explicitSemi() {
		s.Init = p.wrap(first, firstX)
		p.next()
		if p.tok.Kind == lexer.LBRACE {
			p.errorAt(p.tok.Span, "missing condition in if statement")
		}
		s.Cond = p.expr()
	} else {
		s.Cond = p.condOf(first, firstX, "if")
	}
	p.exprLev = old
	p.newlineBeforeBrace("an if statement")
	s.Then = p.block()
	if p.tok.Kind == lexer.ELSE {
		p.next()
		switch p.tok.Kind {
		case lexer.IF:
			s.Else = p.ifStmt()
		case lexer.LBRACE:
			s.Else = p.block()
		default:
			p.errorExpected("'if' or '{' after else")
		}
	}
	return finish(p, s, start)
}

func (p *parser) forStmt() *ast.ForStmt {
	start := p.start()
	f := &ast.ForStmt{}
	if p.tok.Kind == lexer.DRIVER {
		f.Driver = true
		p.next()
	}
	p.expect(lexer.FOR)
	old := p.exprLev
	p.exprLev = -1
	if p.tok.Kind != lexer.LBRACE && p.tok.Kind != lexer.DECREASES {
		var first ast.Stmt
		var firstX ast.Expr
		if !p.explicitSemi() {
			first, firstX = p.simpleStmtOrExpr()
		}
		if p.explicitSemi() {
			f.ThreeClause = true
			if first != nil || firstX != nil {
				f.Init = p.wrap(first, firstX)
			}
			p.next()
			if !p.explicitSemi() {
				f.Cond = p.expr()
			}
			if !p.explicitSemi() {
				p.newlineBeforeBrace("a for statement")
				p.errorExpected("';' in for clause")
			}
			p.next()
			if p.tok.Kind != lexer.LBRACE && p.tok.Kind != lexer.DECREASES {
				f.Post = p.simpleStmt()
				if _, ok := f.Post.(*ast.ShortVarDecl); ok {
					p.errorAt(f.Post.Span(), "cannot declare in post statement of for loop")
				}
			}
		} else {
			f.Cond = p.condOf(first, firstX, "for")
		}
	}
	p.exprLev = old
	if p.tok.Kind == lexer.DECREASES {
		f.Decreases = p.decreases()
	}
	p.newlineBeforeBrace("a for statement")
	f.Body = p.block()
	return finish(p, f, start)
}

func (p *parser) scopeStmt() *ast.ScopeStmt {
	start := p.start()
	p.expect(lexer.SCOPE)
	s := &ast.ScopeStmt{Name: p.ident()}
	p.expect(lexer.DEFINE)
	old := p.exprLev
	p.exprLev = -1
	s.Value = p.expr()
	p.exprLev = old
	p.newlineBeforeBrace("a scope statement")
	s.Body = p.block()
	return finish(p, s, start)
}

func (p *parser) matchStmt() *ast.MatchStmt {
	start := p.start()
	p.expect(lexer.MATCH)
	m := &ast.MatchStmt{}
	old := p.exprLev
	p.exprLev = -1
	m.X = p.scrutinee()
	p.exprLev = 0
	p.expectOpenBrace("a match statement")
	for p.tok.Kind != lexer.RBRACE {
		m.Arms = append(m.Arms, p.matchArm())
		p.expectSemi("match arm")
	}
	p.next()
	p.exprLev = old
	return finish(p, m, start)
}

// scrutinee parses the scrutinee of a match: an expression or (v0.2) a
// tuple `(X1, X2, ...)` of two or more expressions.
func (p *parser) scrutinee() ast.Expr {
	p.tupleOK = true
	x := p.expr()
	p.tupleOK = false
	return x
}

// matchArm parses a statement arm (v0.2: optional guard; the body may be
// a block, an expression, a simple statement, return, break or continue;
// a statement body is wrapped in a Block).
func (p *parser) matchArm() *ast.MatchArm {
	start := p.start()
	a := &ast.MatchArm{Pattern: p.pattern()}
	a.Guard = p.guard()
	p.expect(lexer.ARROW)
	switch p.tok.Kind {
	case lexer.LBRACE:
		a.Block = p.block()
	case lexer.RETURN, lexer.BREAK, lexer.CONTINUE:
		a.Block = p.armBlock(p.stmt())
	default:
		s, x := p.simpleStmtOrExpr()
		if s == nil {
			a.Expr = x
			break
		}
		if _, ok := s.(*ast.ShortVarDecl); ok {
			p.errorAt(s.Span(), "cannot declare variables in a match arm without a block")
		}
		a.Block = p.armBlock(s)
	}
	return finish(p, a, start)
}

// armBlock wraps the statement body of a match arm in a Block.
func (p *parser) armBlock(s ast.Stmt) *ast.BlockStmt {
	return finish(p, &ast.BlockStmt{Stmts: []ast.Stmt{s}}, s.Span().Start)
}

// guard parses an optional `if Expr` after an arm's pattern (v0.2).
func (p *parser) guard() ast.Expr {
	if p.tok.Kind != lexer.IF {
		return nil
	}
	p.next()
	return p.expr()
}

// pattern parses `Pattern { "|" Pattern }` (v0.2 or-patterns); a single
// alternative is returned unwrapped.
func (p *parser) pattern() ast.Pattern {
	return p.patterns(false)
}

// patterns parses alternatives separated by '|'. In a payload position
// (payload) a bare identifier is a binding (or a variant of the payload's
// enum type, decided by the type checker).
func (p *parser) patterns(payload bool) ast.Pattern {
	start := p.start()
	first := p.patternAlt(payload)
	if p.tok.Kind != lexer.OR {
		return first
	}
	op := &ast.OrPattern{Alts: []ast.Pattern{first}}
	for p.tok.Kind == lexer.OR {
		p.next()
		op.Alts = append(op.Alts, p.patternAlt(payload))
	}
	return finish(p, op, start)
}

// payloadList parses the patterns between the parentheses of a variant
// pattern: `[ PayloadPattern { "," PayloadPattern } ]` where a payload
// pattern is `..` (at most once, checked by the type checker) or Patterns.
func (p *parser) payloadList() []ast.Pattern {
	if p.tok.Kind == lexer.RPAREN {
		return nil
	}
	var out []ast.Pattern
	for {
		if p.tok.Kind == lexer.DOTDOT {
			start := p.start()
			p.next()
			out = append(out, finish(p, &ast.RestPattern{}, start))
		} else {
			out = append(out, p.patterns(true))
		}
		if p.tok.Kind != lexer.COMMA {
			return out
		}
		p.next()
	}
}

// matchExpr parses a match expression (v0.2):
//
//	MatchExpr = "match" Expr "{" ExprArm { ( ";" | "," ) ExprArm } [ ";" | "," ] "}" .
//	ExprArm   = Patterns "->" Expr .
func (p *parser) matchExpr() *ast.MatchExpr {
	start := p.start()
	p.expect(lexer.MATCH)
	m := &ast.MatchExpr{}
	old := p.exprLev
	p.exprLev = -1
	m.X = p.scrutinee()
	p.exprLev = 0
	p.expectOpenBrace("a match expression")
	for {
		armStart := p.start()
		a := &ast.MatchArm{Pattern: p.pattern()}
		a.Guard = p.guard()
		p.expect(lexer.ARROW)
		a.Expr = p.expr()
		m.Arms = append(m.Arms, finish(p, a, armStart))
		switch p.tok.Kind {
		case lexer.SEMICOLON, lexer.COMMA:
			p.next()
		case lexer.RBRACE:
		default:
			p.errorAt(p.tok.Span, "unexpected %s after match arm; expected ';', ',' or newline", p.tok.Describe())
		}
		if p.tok.Kind == lexer.RBRACE {
			break
		}
	}
	p.next()
	p.exprLev = old
	return finish(p, m, start)
}

// patternAlt parses one alternative (v0.2 grammar):
//
//	PatternAlt = "_" | IDENT [ "." IDENT ] [ "(" [ PayloadList ] ")" ]
//	           | [ "-" ] Literal | "(" Patterns { "," Patterns } ")" .
//
// In a payload position a bare IDENT (not followed by '.' or '(') is an
// *ast.Ident. A parenthesized single pattern is returned unwrapped.
func (p *parser) patternAlt(payload bool) ast.Pattern {
	p.enter()
	defer p.leave()
	start := p.start()
	switch p.tok.Kind {
	case lexer.IDENT:
		if payload {
			if k := p.peek(1).Kind; k != lexer.PERIOD && k != lexer.LPAREN {
				return p.ident()
			}
		}
		if p.tok.Lit == "_" {
			p.next()
			return finish(p, &ast.WildcardPattern{}, start)
		}
		cp := &ast.CtorPattern{Name: p.ident()}
		if p.tok.Kind == lexer.PERIOD {
			p.next()
			cp.Qual = cp.Name
			cp.Name = p.ident()
		}
		if p.tok.Kind == lexer.LPAREN {
			cp.HasParens = true
			p.next()
			cp.Args = p.payloadList()
			p.expect(lexer.RPAREN)
		}
		return finish(p, cp, start)
	case lexer.INT, lexer.STRING, lexer.CHAR, lexer.TRUE, lexer.FALSE, lexer.NIL:
		lit := p.operand()
		return finish(p, &ast.LiteralPattern{Lit: lit}, start)
	case lexer.SUB:
		if p.peek(1).Kind == lexer.INT {
			p.next()
			lit := p.basicLit()
			neg := finish(p, &ast.UnaryExpr{Op: ast.OpNeg, X: lit}, start)
			return finish(p, &ast.LiteralPattern{Lit: neg}, start)
		}
	case lexer.LPAREN:
		p.next()
		first := p.patterns(payload)
		if p.tok.Kind != lexer.COMMA {
			p.expect(lexer.RPAREN)
			return first
		}
		tp := &ast.TuplePattern{Elems: []ast.Pattern{first}}
		for p.tok.Kind == lexer.COMMA {
			p.next()
			tp.Elems = append(tp.Elems, p.patterns(payload))
		}
		p.expect(lexer.RPAREN)
		return finish(p, tp, start)
	}
	p.errorExpected("pattern ('_', identifier, variant or literal)")
	return nil
}

// ---------------------------------------------------------------------------
// expressions

func (p *parser) exprList() []ast.Expr {
	l := []ast.Expr{p.expr()}
	for p.tok.Kind == lexer.COMMA {
		p.next()
		l = append(l, p.expr())
	}
	return l
}

func (p *parser) expr() ast.Expr {
	p.enter()
	defer p.leave()
	return p.binary(1)
}

func binOp(k lexer.Kind) (ast.BinaryOp, bool) {
	switch k {
	case lexer.LOR:
		return ast.OpLOr, true
	case lexer.LAND:
		return ast.OpLAnd, true
	case lexer.EQL:
		return ast.OpEq, true
	case lexer.NEQ:
		return ast.OpNe, true
	case lexer.LSS:
		return ast.OpLt, true
	case lexer.LEQ:
		return ast.OpLe, true
	case lexer.GTR:
		return ast.OpGt, true
	case lexer.GEQ:
		return ast.OpGe, true
	case lexer.ADD:
		return ast.OpAdd, true
	case lexer.SUB:
		return ast.OpSub, true
	case lexer.OR:
		return ast.OpOr, true
	case lexer.XOR:
		return ast.OpXor, true
	case lexer.MUL:
		return ast.OpMul, true
	case lexer.QUO:
		return ast.OpDiv, true
	case lexer.REM:
		return ast.OpRem, true
	case lexer.AND:
		return ast.OpAnd, true
	case lexer.SHL:
		return ast.OpShl, true
	case lexer.SHR:
		return ast.OpShr, true
	}
	return 0, false
}

// binary parses a left-associative chain of operators with precedence >= prec.
func (p *parser) binary(prec int) ast.Expr {
	start := p.start()
	x := p.unary()
	for {
		if p.tok.Kind == lexer.IS && ast.OpLt.Precedence() >= prec {
			// v0.2: `X is Pattern` has the precedence of the relational
			// operators.
			p.next()
			x = finish(p, &ast.IsExpr{X: x, Pattern: p.pattern()}, start)
			continue
		}
		op, ok := binOp(p.tok.Kind)
		if !ok || op.Precedence() < prec {
			return x
		}
		opSpan := p.tok.Span
		p.next()
		y := p.binary(op.Precedence() + 1)
		x = finish(p, &ast.BinaryExpr{Op: op, OpPos: opSpan, X: x, Y: y}, start)
	}
}

func (p *parser) unary() ast.Expr {
	start := p.start()
	var op ast.UnaryOp
	switch p.tok.Kind {
	case lexer.NOT:
		op = ast.OpNot
	case lexer.SUB:
		op = ast.OpNeg
	case lexer.AND:
		op = ast.OpAddr
	case lexer.MUL:
		op = ast.OpDeref
	default:
		return p.postfix()
	}
	p.enter()
	defer p.leave()
	p.next()
	x := p.unary()
	return finish(p, &ast.UnaryExpr{Op: op, X: x}, start)
}

func (p *parser) postfix() ast.Expr {
	start := p.start()
	x := p.operand()
	for {
		switch p.tok.Kind {
		case lexer.LPAREN:
			p.next()
			p.exprLev++
			c := &ast.CallExpr{Fun: x}
			for p.tok.Kind != lexer.RPAREN {
				c.Args = append(c.Args, p.expr())
				if p.tok.Kind != lexer.COMMA {
					if p.tok.Kind != lexer.RPAREN {
						p.errorExpected("',' or ')' in argument list")
					}
					break
				}
				p.next()
			}
			p.exprLev--
			p.expect(lexer.RPAREN)
			x = finish(p, c, start)
		case lexer.LBRACK:
			p.next()
			p.exprLev++
			var lo ast.Expr
			if p.tok.Kind != lexer.COLON {
				if p.tok.Kind == lexer.RBRACK {
					p.errorExpected("index expression")
				}
				lo = p.expr()
			}
			if p.tok.Kind == lexer.COLON {
				p.next()
				var hi ast.Expr
				if p.tok.Kind != lexer.RBRACK {
					hi = p.expr()
				}
				p.exprLev--
				p.expect(lexer.RBRACK)
				x = finish(p, &ast.SliceExpr{X: x, Lo: lo, Hi: hi}, start)
				continue
			}
			idx := []ast.Expr{lo}
			for p.tok.Kind == lexer.COMMA {
				p.next()
				idx = append(idx, p.expr())
			}
			p.exprLev--
			p.expect(lexer.RBRACK)
			x = finish(p, &ast.IndexExpr{X: x, Indices: idx}, start)
		case lexer.PERIOD:
			p.next()
			sel := p.ident()
			x = finish(p, &ast.SelectorExpr{X: x, Sel: sel}, start)
		case lexer.LBRACE:
			if p.exprLev < 0 || !isTypeName(x) {
				return x
			}
			x = p.structLit(x, start)
		default:
			return x
		}
	}
}

// isTypeName reports whether x can be the type of a struct literal:
// T, pkg.T, T[...], pkg.T[...].
func isTypeName(x ast.Expr) bool {
	switch x := x.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		_, ok := x.X.(*ast.Ident)
		return ok
	case *ast.IndexExpr:
		switch y := x.X.(type) {
		case *ast.Ident:
			return true
		case *ast.SelectorExpr:
			_, ok := y.X.(*ast.Ident)
			return ok
		}
	}
	return false
}

func (p *parser) structLit(x ast.Expr, start diag.Pos) *ast.StructLit {
	nt := &ast.NamedType{}
	base := x
	if ix, ok := x.(*ast.IndexExpr); ok {
		base = ix.X
		nt.Args = ix.Indices
	}
	switch b := base.(type) {
	case *ast.Ident:
		nt.Name = b
	case *ast.SelectorExpr:
		nt.Pkg = b.X.(*ast.Ident)
		nt.Name = b.Sel
	}
	ast.SetSpan(nt, x.Span())
	p.expect(lexer.LBRACE)
	old := p.exprLev
	p.exprLev = 0
	sl := &ast.StructLit{Type: nt}
	for p.tok.Kind != lexer.RBRACE {
		fs := p.start()
		if p.tok.Kind != lexer.IDENT || p.peek(1).Kind != lexer.COLON {
			p.errorExpected("field initializer 'name: value'")
		}
		fi := &ast.FieldInit{Name: p.ident()}
		p.expect(lexer.COLON)
		fi.Value = p.expr()
		sl.Fields = append(sl.Fields, finish(p, fi, fs))
		if p.tok.Kind != lexer.COMMA {
			if p.tok.Kind != lexer.RBRACE {
				if p.tok.Kind == lexer.SEMICOLON && p.tok.Lit == "\n" {
					p.errorAt(p.tok.Span, "unexpected newline in struct literal; possibly missing comma or '}'")
				}
				p.errorExpected("',' or '}' in struct literal")
			}
			break
		}
		p.next()
	}
	p.exprLev = old
	p.expect(lexer.RBRACE)
	return finish(p, sl, start)
}

func (p *parser) basicLit() *ast.BasicLit {
	start := p.start()
	t := p.tok
	lit := &ast.BasicLit{Raw: t.Lit}
	switch t.Kind {
	case lexer.INT:
		lit.Kind = ast.IntLit
		v, msg := lexer.ParseInt(t.Lit)
		if msg != "" { // already validated by the lexer; defensive
			p.errorAt(t.Span, "%s", msg)
		}
		lit.Int = v
	case lexer.STRING:
		lit.Kind = ast.StringLit
		lit.Str = t.Value
	case lexer.CHAR:
		lit.Kind = ast.CharLit
		lit.Int = uint64(t.Value[0])
	default:
		p.errorExpected("literal")
	}
	p.next()
	return finish(p, lit, start)
}

// operand parses a Primary.
func (p *parser) operand() ast.Expr {
	start := p.start()
	tuple := p.tupleOK
	p.tupleOK = false
	switch p.tok.Kind {
	case lexer.INT, lexer.STRING, lexer.CHAR:
		return p.basicLit()
	case lexer.TRUE, lexer.FALSE:
		v := p.tok.Kind == lexer.TRUE
		p.next()
		return finish(p, &ast.BoolLit{Value: v}, start)
	case lexer.NIL:
		p.next()
		return finish(p, &ast.NilLit{}, start)
	case lexer.IDENT:
		if p.isCodeKeyword() || p.isElKeyword() {
			t := p.parseType()
			return finish(p, &ast.TypeExpr{Type: t}, start)
		}
		return p.ident()
	case lexer.LPAREN:
		p.next()
		p.exprLev++
		x := p.expr()
		if tuple && p.tok.Kind == lexer.COMMA {
			t := &ast.TupleExpr{Elems: []ast.Expr{x}}
			for p.tok.Kind == lexer.COMMA {
				p.next()
				t.Elems = append(t.Elems, p.expr())
			}
			p.exprLev--
			p.expect(lexer.RPAREN)
			return finish(p, t, start)
		}
		p.exprLev--
		p.expect(lexer.RPAREN)
		return finish(p, &ast.ParenExpr{X: x}, start)
	case lexer.FUNC, lexer.DRIVER:
		if p.tok.Kind == lexer.DRIVER && p.peek(1).Kind != lexer.FUNC {
			p.errorAt(p.tok.Span, "unexpected keyword driver; expected 'driver func' or 'driver for'")
		}
		drv := p.tok.Kind == lexer.DRIVER
		if drv {
			p.next()
		}
		p.expect(lexer.FUNC)
		sig := p.signature()
		if p.tok.Kind == lexer.LBRACE {
			body := p.block()
			return finish(p, &ast.FuncLit{Driver: drv, Sig: sig, Body: body}, start)
		}
		ft := finish(p, &ast.FuncType{Driver: drv, Sig: sig}, start)
		return finish(p, &ast.TypeExpr{Type: ft}, start)
	case lexer.LBRACK, lexer.STRUCT, lexer.ENUM:
		t := p.parseType()
		return finish(p, &ast.TypeExpr{Type: t}, start)
	case lexer.MATCH:
		return p.matchExpr()
	}
	p.errorExpected("expression")
	return nil
}
