package scip

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"

	scipb "github.com/scip-code/scip/bindings/go/scip"

	"github.com/shermanhuman/cartographer/internal/facts"
)

// goFile is a parsed Go document.
type goFile struct {
	fset  *token.FileSet
	file  *ast.File
	funcs map[int]*ast.FuncType // name offset -> signature (functions, methods, interface methods)
	decls map[int]*ast.FuncDecl // name offset -> declaration
}

func (c *conv) goFile(d *doc) (*goFile, error) {
	if g, ok := d.extra.(*goFile); ok {
		return g, nil
	}
	if err, ok := d.extra.(error); ok {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, d.path, d.src, parser.SkipObjectResolution)
	if err != nil {
		d.extra = err
		return nil, err
	}
	g := &goFile{fset: fset, file: f, funcs: map[int]*ast.FuncType{}, decls: map[int]*ast.FuncDecl{}}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			g.funcs[g.off(n.Name.Pos())] = n.Type
			g.decls[g.off(n.Name.Pos())] = n
		case *ast.InterfaceType:
			for _, m := range n.Methods.List {
				if ft, ok := m.Type.(*ast.FuncType); ok {
					for _, name := range m.Names {
						g.funcs[g.off(name.Pos())] = ft
					}
				}
			}
		}
		return true
	})
	d.extra = g
	return g, nil
}

func (g *goFile) off(p token.Pos) int { return g.fset.File(p).Offset(p) }

// goArity counts declared parameters; a variadic one counts once.
func goArity(ft *ast.FuncType) int {
	n := 0
	for _, f := range ft.Params.List {
		n += max(1, len(f.Names))
	}
	return n
}

// goPackageDir is the directory part of a Go function's id and its
// module's id: the package directory; "./" for the module root package
// (which no directory can start with); and, for an external test package
// (package p_test beside package p), the directory with [test] ("app[test]",
// "./[test]"), so its helpers merge neither with the package's own nor with
// those of a real package directory named app_test (a nested module's): no
// package directory has "[" in it (the go tool rejects it in an import
// path). See docs/facts-format.md, "Ids".
func (c *conv) goPackageDir(d *doc, g *goFile) string {
	dir := path.Dir(d.path)
	if dir == "." {
		dir = "./"
	}
	if name := g.file.Name.Name; strings.HasSuffix(d.path, "_test.go") && strings.HasSuffix(name, "_test") && !c.goDirPackages()[path.Dir(d.path)][name] {
		return dir + "[test]"
	}
	return dir
}

// goNamespace is a Go package's namespace (contexts): its directory's
// elements, an external test package's being its package's.
func goNamespace(dir string) []string {
	dir = strings.TrimSuffix(dir, "[test]")
	if dir == "./" {
		return []string{"."}
	}
	return strings.Split(dir, "/")
}

// goDirPackages is, per directory, the package names its non-test files
// declare.
func (c *conv) goDirPackages() map[string]map[string]bool {
	if c.goPkgs != nil {
		return c.goPkgs
	}
	c.goPkgs = map[string]map[string]bool{}
	for _, d := range c.docs {
		if strings.HasSuffix(d.path, "_test.go") || !strings.HasSuffix(d.path, ".go") {
			continue
		}
		g, err := c.goFile(d)
		if err != nil {
			continue
		}
		dir := path.Dir(d.path)
		if c.goPkgs[dir] == nil {
			c.goPkgs[dir] = map[string]bool{}
		}
		c.goPkgs[dir][g.file.Name.Name] = true
	}
	return c.goPkgs
}

// goName sets a Go function's id: <package dir>.<Type>.<name>/<arity>,
// without .<Type> for functions. The root package's reads
// ./<Type>.<name>/<arity>: "./" already ends the directory part
// (goPackageDir).
func (c *conv) goName(f *fn) error {
	g, err := c.goFile(f.d)
	if err != nil {
		return err
	}
	ft, ok := g.funcs[f.name.start]
	if !ok {
		return errors.New("no declaration at the definition")
	}
	f.f.Arity = min(goArity(ft), facts.MaxArity)
	dir := c.goPackageDir(f.d, g)
	owner := ""
	if s := c.symbol(f.sym); s != nil && len(s.Descriptors) >= 2 {
		if p := s.Descriptors[len(s.Descriptors)-2]; p.Suffix == scipb.Descriptor_Type {
			owner = p.Name + "."
		}
	}
	prefix := dir + "."
	if dir == "./" { // the module root package
		prefix = dir
	}
	f.f.ID = fmt.Sprintf("%s%s%s/%d", prefix, owner, f.f.Name, f.f.Arity)
	f.key = moduleKey{id: dir, name: dir, namespace: goNamespace(dir)}
	return nil
}

func (c *conv) goPrepare() {
	for _, d := range c.docs {
		d.test = strings.HasSuffix(d.path, "_test.go")
	}
	// Interface method -> the in-repo methods scip-go says implement it.
	for n, info := range c.syms {
		if info.Kind != scipb.SymbolInformation_Method {
			continue
		}
		for _, r := range info.Relationships {
			if !r.IsImplementation {
				continue
			}
			t := c.normalize(r.Symbol)
			if tf, ok := c.fnBySym[t]; ok && tf.info.GetKind() == scipb.SymbolInformation_MethodSpecification {
				c.impls[t] = append(c.impls[t], n)
			}
		}
	}
	for k := range c.impls {
		sort.Strings(c.impls[k])
	}
}

var goBuiltins = map[string]bool{
	"append": true, "cap": true, "clear": true, "close": true, "complex": true, "copy": true, "delete": true,
	"imag": true, "len": true, "make": true, "max": true, "min": true, "new": true, "panic": true,
	"print": true, "println": true, "real": true, "recover": true,
	"bool": true, "byte": true, "complex64": true, "complex128": true, "error": true, "float32": true,
	"float64": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true, "rune": true,
	"string": true, "uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "any": true, "comparable": true,
}

// calleeIdent is the identifier naming a call's function, if any.
func calleeIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x
		case *ast.SelectorExpr:
			return x.Sel
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		default:
			return nil
		}
	}
}

const netHTTP = "scip-go gomod github.com/golang/go/src `net/http`/"

// goSyntax finds unresolved calls, ServeMux routes and HTTP references.
func (c *conv) goSyntax() {
	w := &goWrappers{byFn: map[string]*wrapper{}}
	type site struct {
		d    *doc
		g    *goFile
		call *ast.CallExpr
		sym  string
	}
	var sites []site
	for _, d := range c.docs {
		g, err := c.goFile(d)
		if err != nil {
			c.notes = append(c.notes, facts.Note{Kind: "parse_error", File: d.path, Message: err.Error()})
			continue
		}
		ast.Inspect(g.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id := calleeIdent(call.Fun)
			if id == nil {
				return true
			}
			off := g.off(id.Pos())
			sym := d.symbolAt(c, off)
			if len(d.occ[off]) == 0 {
				_, bare := call.Fun.(*ast.Ident)
				if bare && goBuiltins[id.Name] {
					return true
				}
				from := d.fromAt(off)
				if from == nil {
					return true
				}
				if !bare && id.Name == "Error" && len(call.Args) == 0 {
					// err.Error(): the universe error interface has no symbol.
					c.stats.CallSites++
					c.stats.External++
					return true
				}
				c.unresolved(from, d, off, id.Name, len(call.Args))
				return true
			}
			if d.test || sym == "" {
				return true
			}
			switch sym {
			case netHTTP + "ServeMux#HandleFunc().", netHTTP + "ServeMux#Handle().", netHTTP + "HandleFunc().", netHTTP + "Handle().":
				c.goRoute(d, g, call)
			case netHTTP + "NewRequest().", netHTTP + "NewRequestWithContext().":
				w.build(c, d, g, call, sym)
			default:
				sites = append(sites, site{d, g, call, sym})
			}
			return true
		})
	}
	// Functions that pass their own path parameter to a wrapper are wrappers
	// too (get -> do); repeat until nothing changes.
	for changed := true; changed; {
		changed = false
		for _, s := range sites {
			if wr := w.byFn[s.sym]; wr != nil && w.forward(c, s.d, s.g, s.call, wr) {
				changed = true
			}
		}
	}
	for _, s := range sites {
		if wr := w.byFn[s.sym]; wr != nil {
			w.ref(c, s.d, s.g, s.call, wr)
		}
	}
	c.out.HTTPRefs = append(c.out.HTTPRefs, w.direct...)
}

// goRoute extracts mux.HandleFunc("METHOD /path", handler) with a literal
// pattern; the handler is the innermost function value.
func (c *conv) goRoute(d *doc, g *goFile, call *ast.CallExpr) {
	if len(call.Args) != 2 {
		return
	}
	pat, ok := stringLit(call.Args[0])
	if !ok {
		c.stats.NotExtracted++ // a computed pattern
		return
	}
	method, p := "", pat
	if m, rest, ok := strings.Cut(pat, " "); ok && m != "" && !strings.Contains(m, "/") {
		method, p = strings.ToUpper(m), strings.TrimLeft(rest, " ")
	}
	if !strings.HasPrefix(p, "/") {
		if i := strings.Index(p, "/"); i >= 0 {
			p = p[i:] // host patterns: example.com/path
		}
	}
	h, wrapped := c.goHandler(d, g, call.Args[1])
	if h == nil || !strings.HasPrefix(p, "/") {
		c.stats.NotExtracted++ // a closure, a handler variable or value
		return
	}
	m := method
	if m == "" {
		m = "*"
	}
	router := c.goPackageDir(d, g) // the package, as its function ids
	r := facts.Route{ID: c.opt.Repo + ":route:" + router + ":" + m + " " + p, Method: method, Path: p, Handler: h.f.ID, File: d.path, Line: g.fset.Position(call.Pos()).Line, Precision: facts.Precise}
	if wrapped {
		r.Precision = facts.Heuristic
	}
	for _, old := range c.out.Routes {
		if old.ID == r.ID {
			c.notes = append(c.notes, facts.Note{Kind: "duplicate_route", File: d.path, Message: fmt.Sprintf("line %d: %s is also registered at %s:%d", r.Line, r.ID, old.File, old.Line)})
			return
		}
	}
	c.out.Routes = append(c.out.Routes, r)
}

// goHandler is the innermost in-repo function value in a handler
// expression: s.guard(s.signedIn(s.dialEvent)) -> dialEvent. wrapped says it
// was found inside a call other than a conversion to http.HandlerFunc.
func (c *conv) goHandler(d *doc, g *goFile, e ast.Expr) (*fn, bool) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return c.goHandler(d, g, x.X)
	case *ast.Ident, *ast.SelectorExpr:
		id := calleeIdent(x)
		if f, ok := c.fnBySym[d.symbolAt(c, g.off(id.Pos()))]; ok {
			return f, false
		}
	case *ast.CallExpr:
		conv := false
		if id := calleeIdent(x.Fun); id != nil {
			conv = d.symbolAt(c, g.off(id.Pos())) == netHTTP+"HandlerFunc#"
		}
		for i := len(x.Args) - 1; i >= 0; i-- {
			if f, wrapped := c.goHandler(d, g, x.Args[i]); f != nil {
				return f, wrapped || !conv
			}
		}
	}
	return nil, false
}

func stringLit(e ast.Expr) (string, bool) {
	if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.STRING {
		if s, err := strconv.Unquote(b.Value); err == nil {
			return s, true
		}
	}
	return "", false
}

// wrapper is a function that builds an http.Request from a receiver field
// plus its path parameter.
type wrapper struct {
	pathParam   int
	methodParam int    // -1 when the wrapper fixes the method (or it's unknown)
	method      string // fixed method, or ""
	base        string // the receiver field the URL starts with
	prefix      string // literal path between the field and the parameter
}

type goWrappers struct {
	byFn   map[string]*wrapper
	direct []facts.HTTPRef
}

// params lists a declaration's parameter names in order (receiver excluded).
func params(ft *ast.FuncType) []string {
	var out []string
	for _, f := range ft.Params.List {
		if len(f.Names) == 0 {
			out = append(out, "")
		}
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

func paramIndex(ft *ast.FuncType, e ast.Expr) int {
	id, ok := e.(*ast.Ident)
	if !ok {
		return -1
	}
	for i, p := range params(ft) {
		if p == id.Name && p != "_" {
			return i
		}
	}
	return -1
}

// enclosingDecl is the function declaration around off.
func (c *conv) enclosingDecl(d *doc, g *goFile, off int) (*fn, *ast.FuncDecl) {
	for f := d.fromAt(off); f != nil; {
		if decl, ok := g.decls[f.name.start]; ok {
			return f, decl
		}
		return f, nil
	}
	return nil, nil
}

// goMethod is the method an expression fixes: http.MethodPost or "POST".
func (c *conv) goMethod(d *doc, g *goFile, e ast.Expr) string {
	if s, ok := stringLit(e); ok {
		return strings.ToUpper(s)
	}
	if sel, ok := e.(*ast.SelectorExpr); ok {
		sym := d.symbolAt(c, g.off(sel.Sel.Pos()))
		if name, ok := strings.CutPrefix(sym, netHTTP+"Method"); ok {
			return strings.ToUpper(strings.TrimSuffix(name, "."))
		}
	}
	return ""
}

// build looks at one http.NewRequest[WithContext] call: recv.field + param
// makes its function a wrapper; recv.field + "/literal" is a reference.
func (w *goWrappers) build(c *conv, d *doc, g *goFile, call *ast.CallExpr, sym string) {
	mi, ui := 0, 1
	if strings.HasSuffix(sym, "NewRequestWithContext().") {
		mi, ui = 1, 2
	}
	if len(call.Args) <= ui {
		return
	}
	f, decl := c.enclosingDecl(d, g, g.off(call.Pos()))
	if f == nil || decl == nil || decl.Recv == nil || len(decl.Recv.List) == 0 || len(decl.Recv.List[0].Names) == 0 {
		return
	}
	recv := decl.Recv.List[0].Names[0].Name
	terms := plusTerms(call.Args[ui])
	if len(terms) < 2 {
		return
	}
	base, ok := fieldOf(terms[0], recv)
	if !ok {
		return
	}
	// base + "/literal/" + param: a wrapper.
	prefix := ""
	for _, t := range terms[1:] {
		if pi := paramIndex(decl.Type, t); pi >= 0 {
			wr := &wrapper{pathParam: pi, methodParam: paramIndex(decl.Type, call.Args[mi]), base: base, prefix: prefix}
			if wr.methodParam < 0 {
				wr.method = c.goMethod(d, g, call.Args[mi])
			}
			w.byFn[f.sym] = wr
			return
		}
		s, ok := stringLit(t)
		if !ok {
			break
		}
		prefix += s
	}
	if p, ok := pathOf("", terms[1:]); ok {
		w.direct = append(w.direct, facts.HTTPRef{From: f.f.ID, Method: c.goMethod(d, g, call.Args[mi]), Path: p, Base: base, File: d.path, Line: g.fset.Position(call.Pos()).Line})
	}
}

// forward makes the function around a wrapper call a wrapper when it passes
// its own path parameter through. It reports whether it added one.
func (w *goWrappers) forward(c *conv, d *doc, g *goFile, call *ast.CallExpr, wr *wrapper) bool {
	if len(call.Args) <= wr.pathParam {
		return false
	}
	f, decl := c.enclosingDecl(d, g, g.off(call.Pos()))
	if f == nil || decl == nil || w.byFn[f.sym] != nil {
		return false
	}
	pi := paramIndex(decl.Type, call.Args[wr.pathParam])
	if pi < 0 {
		return false
	}
	nw := &wrapper{pathParam: pi, methodParam: -1, method: wr.method, base: wr.base, prefix: wr.prefix}
	if wr.methodParam >= 0 && wr.methodParam < len(call.Args) {
		if nw.methodParam = paramIndex(decl.Type, call.Args[wr.methodParam]); nw.methodParam < 0 {
			nw.method = c.goMethod(d, g, call.Args[wr.methodParam])
		}
	}
	w.byFn[f.sym] = nw
	return true
}

// ref turns a call to a wrapper with a literal path into a reference.
func (w *goWrappers) ref(c *conv, d *doc, g *goFile, call *ast.CallExpr, wr *wrapper) {
	if len(call.Args) <= wr.pathParam {
		return
	}
	f, decl := c.enclosingDecl(d, g, g.off(call.Pos()))
	if f == nil {
		return
	}
	arg := call.Args[wr.pathParam]
	if decl != nil && paramIndex(decl.Type, arg) >= 0 && w.byFn[f.sym] != nil {
		return // forwarded by another wrapper
	}
	p, ok := pathOf(wr.prefix, plusTerms(arg))
	if !ok {
		c.stats.NotExtracted++ // a computed path
		return
	}
	method := wr.method
	if wr.methodParam >= 0 && wr.methodParam < len(call.Args) {
		method = c.goMethod(d, g, call.Args[wr.methodParam])
	}
	c.out.HTTPRefs = append(c.out.HTTPRefs, facts.HTTPRef{From: f.f.ID, Method: method, Path: p, Base: wr.base, File: d.path, Line: g.fset.Position(call.Pos()).Line})
}

// plusTerms flattens a + b + c.
func plusTerms(e ast.Expr) []ast.Expr {
	if p, ok := e.(*ast.ParenExpr); ok {
		return plusTerms(p.X)
	}
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
		return append(plusTerms(b.X), plusTerms(b.Y)...)
	}
	return []ast.Expr{e}
}

// fieldOf names the receiver field an expression reads: a.baseURL ->
// baseURL, a.cfg.URL -> cfg.URL.
func fieldOf(e ast.Expr, recv string) (string, bool) {
	var parts []string
	for {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			break
		}
		parts = append([]string{sel.Sel.Name}, parts...)
		e = sel.X
	}
	id, ok := e.(*ast.Ident)
	if !ok || id.Name != recv || len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "."), true
}

// pathOf renders prefix plus a literal path, or literals joined with
// values as :name ("/people/" + id and "/people/" + url.PathEscape(id) ->
// /people/:id). The query string is dropped.
func pathOf(prefix string, terms []ast.Expr) (string, bool) {
	var b strings.Builder
	b.WriteString(prefix)
	for i, t := range terms {
		if strings.Contains(b.String(), "?") {
			break
		}
		if s, ok := stringLit(t); ok {
			b.WriteString(s)
			continue
		}
		if i == 0 {
			return "", false
		}
		if call, ok := t.(*ast.CallExpr); ok && len(call.Args) == 1 {
			t = call.Args[0] // url.PathEscape(id)
		}
		var id *ast.Ident
		switch x := t.(type) {
		case *ast.Ident:
			id = x
		case *ast.SelectorExpr:
			id = x.Sel
		}
		if id == nil {
			return "", false
		}
		b.WriteString(":" + id.Name)
	}
	p, _, _ := strings.Cut(b.String(), "?")
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	return p, true
}
