// Package scip converts a SCIP index of a repository (from scip-go or
// rust-analyzer) plus its source tree into one facts version 2 document, and
// runs those indexers.
//
// Every reference to a callable symbol is first classified as an invocation
// or not: it is one only when the source after the occurrence is "(",
// optionally after generic arguments ("[...]" in Go, "::<...>" in Rust).
// Anything else (a function or method value, a registration, a use path) is
// counted as a ref and is never a call. An invocation is then in-repo (a call
// with to), external (a symbol from outside the repository: counted only),
// dispatch (a Go interface method or a Rust trait method: one heuristic edge
// to each implementation) or unresolved (call syntax with no symbol: a call
// with a target). Stats count each call site once.
package scip

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	scipb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/shermanhuman/cartographer/internal/facts"
)

// Languages.
const (
	Go   = "go"
	Rust = "rust"
)

// Call kinds this package writes.
const (
	KindCall       = "call"       // the compiler's resolution
	KindDispatch   = "dispatch"   // one edge per implementation of an interface or trait method
	KindUnresolved = "unresolved" // call syntax the indexer gave no symbol
)

// Index is one SCIP index. Dir is the directory, relative to the repository
// root and with "/" separators, that its document paths are relative to (a Go
// module root; "." for the repository root).
type Index struct {
	Dir   string
	Index *scipb.Index
}

// ReadIndex reads a SCIP index file.
func ReadIndex(file string) (*scipb.Index, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var idx scipb.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("scip: %s: %w", file, err)
	}
	return &idx, nil
}

// Options name the repository a document describes.
type Options struct {
	Lang     string // Go or Rust
	Repo     string // repository name: the id prefix
	Root     string // repository root on disk
	Revision string
}

// Convert turns the indexes of one repository into a facts document.
func Convert(opt Options, indexes []Index) (*facts.Doc, error) {
	if opt.Lang != Go && opt.Lang != Rust {
		return nil, fmt.Errorf("scip: language %q: want go or rust", opt.Lang)
	}
	if opt.Repo == "" {
		return nil, errors.New("scip: a repository name is required")
	}
	if len(indexes) == 0 {
		return nil, errors.New("scip: no index")
	}
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, err
	}
	c := &conv{
		opt:     opt,
		root:    root,
		syms:    map[string]*scipb.SymbolInformation{},
		parsed:  map[string]*scipb.Symbol{},
		norm:    map[string]string{},
		fnBySym: map[string]*fn{},
		impls:   map[string][]string{},
		inRepo:  map[string]bool{},
		stats:   facts.Stats{Language: opt.Lang},
	}
	tool := indexes[0].Index.GetMetadata().GetToolInfo()
	c.out = &facts.Doc{
		FactsVersion: facts.Version,
		Producer:     facts.Producer{Name: tool.GetName(), Version: tool.GetVersion(), CallPrecision: facts.Precise},
		Repo:         opt.Repo,
		Revision:     opt.Revision,
	}
	if err := c.load(indexes); err != nil {
		return nil, err
	}
	c.functions()
	if opt.Lang == Go {
		c.goPrepare()
	} else {
		c.rustPrepare()
	}
	c.occurrences()
	if opt.Lang == Go {
		c.goSyntax()
	} else {
		c.rustSyntax()
	}
	c.finish()
	if err := c.out.Validate(); err != nil {
		return nil, err
	}
	return c.out, nil
}

type doc struct {
	path      string // repository-relative
	src       []byte
	lines     []int // byte offset of each line start
	d         *scipb.Document
	fns       []*fn         // functions defined here, by enclosing range start
	occ       map[int][]int // byte offset of an occurrence start -> occurrence indexes
	occStarts []int         // sorted keys of occ, built on demand
	test      bool          // a test file
	extra     any           // language-specific (Go: *ast.File)
}

type fn struct {
	sym   string
	info  *scipb.SymbolInformation
	d     *doc
	name  span // the name at the definition
	body  span // the enclosing range
	f     facts.Function
	key   moduleKey
	trait string // Rust: the trait an impl method implements, without generic arguments
}

// span is a byte range in a document's source.
type span struct{ start, end int }

func (s span) contains(o int) bool { return o >= s.start && o < s.end }

type moduleKey struct {
	id, name  string
	namespace []string
}

type conv struct {
	opt     Options
	root    string
	out     *facts.Doc
	docs    []*doc
	byPath  map[string]*doc
	syms    map[string]*scipb.SymbolInformation // normalized symbol -> information
	parsed  map[string]*scipb.Symbol            // normalized symbol -> parsed
	norm    map[string]string                   // raw symbol -> normalized
	fns     []*fn
	fnBySym map[string]*fn
	impls   map[string][]string // interface or trait method -> implementations
	inRepo  map[string]bool     // package (manager + name) with a definition in the repository
	modules map[string]*facts.Module
	stats   facts.Stats
	dropped int
	notes   []facts.Note
	rust    *rustState
}

// normalize drops the package version from a global symbol, so ids and
// cross-index references don't depend on it. Local symbols are document
// scoped and returned as they are.
func (c *conv) normalize(raw string) string {
	if n, ok := c.norm[raw]; ok {
		return n
	}
	n := raw
	if !scipb.IsLocalSymbol(raw) {
		if s, err := scipb.ParseSymbol(raw); err == nil && s.Package != nil {
			n = s.Scheme + " " + s.Package.Manager + " " + s.Package.Name + " " + scipb.DescriptorOnlyFormatter.FormatSymbol(s)
			c.parsed[n] = s
		}
	}
	c.norm[raw] = n
	return n
}

func (c *conv) symbol(n string) *scipb.Symbol { return c.parsed[n] }

func (c *conv) pkgOf(n string) string {
	if s := c.parsed[n]; s != nil && s.Package != nil {
		return s.Package.Manager + " " + s.Package.Name
	}
	return ""
}

func (c *conv) load(indexes []Index) error {
	c.byPath = map[string]*doc{}
	for _, ix := range indexes {
		for _, s := range ix.Index.GetExternalSymbols() {
			c.addSymbol(s)
		}
		for _, d := range ix.Index.GetDocuments() {
			rel := path.Clean(path.Join(filepath.ToSlash(ix.Dir), filepath.ToSlash(d.RelativePath)))
			if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
				c.dropped++ // e.g. scip-go's generated test mains in the build cache
				continue
			}
			src, err := os.ReadFile(filepath.Join(c.root, filepath.FromSlash(rel)))
			if err != nil {
				c.dropped++
				continue
			}
			if _, dup := c.byPath[rel]; dup {
				continue
			}
			dd := &doc{path: rel, src: src, d: d, occ: map[int][]int{}}
			dd.lines = lineStarts(src)
			c.docs = append(c.docs, dd)
			c.byPath[rel] = dd
			for _, s := range d.Symbols {
				c.addSymbol(s)
			}
		}
	}
	if len(c.docs) == 0 {
		return errors.New("scip: the index has no documents inside the repository root")
	}
	sort.Slice(c.docs, func(i, j int) bool { return c.docs[i].path < c.docs[j].path })
	for _, d := range c.docs {
		for i, o := range d.d.Occurrences {
			r, ok := o.SourceRange()
			if !ok {
				continue
			}
			off := d.offset(r.Start)
			d.occ[off] = append(d.occ[off], i)
			if scipb.SymbolRole_Definition.Matches(o) && !scipb.IsLocalSymbol(o.Symbol) {
				if p := c.pkgOf(c.normalize(o.Symbol)); p != "" {
					c.inRepo[p] = true
				}
			}
		}
	}
	if c.dropped > 0 {
		c.notes = append(c.notes, facts.Note{Kind: "dropped_documents", Message: fmt.Sprintf("%d indexed documents outside the repository root (generated test mains) were dropped", c.dropped)})
	}
	return nil
}

func (c *conv) addSymbol(s *scipb.SymbolInformation) {
	n := c.normalize(s.Symbol)
	if old, ok := c.syms[n]; ok && old.Kind != scipb.SymbolInformation_UnspecifiedKind {
		return
	}
	c.syms[n] = s
}

func lineStarts(src []byte) []int {
	ls := []int{0}
	for i, b := range src {
		if b == '\n' {
			ls = append(ls, i+1)
		}
	}
	return ls
}

// offset converts a SCIP position to a byte offset, honouring the
// document's position encoding (UTF-8 bytes unless it says otherwise).
func (d *doc) offset(p scipb.Position) int {
	line := int(p.Line)
	if line < 0 {
		return 0
	}
	if line >= len(d.lines) {
		return len(d.src)
	}
	start := d.lines[line]
	end := len(d.src)
	if line+1 < len(d.lines) {
		end = d.lines[line+1]
	}
	ch := int(p.Character)
	switch d.d.PositionEncoding {
	case scipb.PositionEncoding_UTF16CodeUnitOffsetFromLineStart, scipb.PositionEncoding_UTF32CodeUnitOffsetFromLineStart:
		units, i := 0, start
		for i < end && units < ch {
			r, size := utf8.DecodeRune(d.src[i:end])
			if d.d.PositionEncoding == scipb.PositionEncoding_UTF16CodeUnitOffsetFromLineStart {
				units += utf16.RuneLen(r)
			} else {
				units++
			}
			i += size
		}
		return i
	}
	return min(start+ch, end)
}

// lineOf is the 1-based line of a byte offset.
func (d *doc) lineOf(off int) int {
	return sort.Search(len(d.lines), func(i int) bool { return d.lines[i] > off })
}

func (d *doc) occSpan(o *scipb.Occurrence) (span, bool) {
	r, ok := o.SourceRange()
	if !ok {
		return span{}, false
	}
	return span{d.offset(r.Start), d.offset(r.End)}, true
}

// symbolAt is the normalized symbol of the first global occurrence starting
// at off, or "".
func (d *doc) symbolAt(c *conv, off int) string {
	for _, i := range d.occ[off] {
		if s := d.d.Occurrences[i].Symbol; !scipb.IsLocalSymbol(s) {
			return c.normalize(s)
		}
	}
	return ""
}

var callableKinds = map[string]map[scipb.SymbolInformation_Kind]string{
	Go: {
		scipb.SymbolInformation_Function:            "function",
		scipb.SymbolInformation_Method:              "method",
		scipb.SymbolInformation_MethodSpecification: "interface_method",
	},
	Rust: {
		scipb.SymbolInformation_Function:     "function",
		scipb.SymbolInformation_Method:       "method",
		scipb.SymbolInformation_StaticMethod: "associated_function",
		scipb.SymbolInformation_TraitMethod:  "trait_method",
	},
}

// callable says whether a symbol names a function or method. Without kind
// information (symbols outside the repository) a method descriptor counts;
// maybe reports a Go term on a type (an interface method or a field), which
// counts only where it is invoked.
func (c *conv) callable(n string) (yes, maybe bool) {
	if info := c.syms[n]; info != nil && info.Kind != scipb.SymbolInformation_UnspecifiedKind {
		_, ok := callableKinds[c.opt.Lang][info.Kind]
		return ok, false
	}
	s := c.symbol(n)
	if s == nil || len(s.Descriptors) == 0 {
		return false, false
	}
	last := s.Descriptors[len(s.Descriptors)-1]
	if last.Suffix == scipb.Descriptor_Method {
		return true, false
	}
	if c.opt.Lang == Go && last.Suffix == scipb.Descriptor_Term && len(s.Descriptors) >= 2 &&
		s.Descriptors[len(s.Descriptors)-2].Suffix == scipb.Descriptor_Type && !c.inRepo[c.pkgOf(n)] {
		return false, true
	}
	return false, false
}

func (c *conv) kindOf(n string) scipb.SymbolInformation_Kind {
	if info := c.syms[n]; info != nil {
		return info.Kind
	}
	return scipb.SymbolInformation_UnspecifiedKind
}

// functions makes a function for every definition of a callable symbol.
func (c *conv) functions() {
	if c.opt.Lang == Rust {
		c.rust = newRustState(c)
	}
	for _, d := range c.docs {
		for _, o := range d.d.Occurrences {
			if !scipb.SymbolRole_Definition.Matches(o) || scipb.IsLocalSymbol(o.Symbol) {
				continue
			}
			n := c.normalize(o.Symbol)
			kind, ok := callableKinds[c.opt.Lang][c.kindOf(n)]
			if !ok {
				continue
			}
			name, ok := d.occSpan(o)
			if !ok {
				continue
			}
			body := name
			if r, ok := o.EnclosingSourceRange(); ok {
				body = span{d.offset(r.Start), d.offset(r.End)}
			}
			f := &fn{sym: n, info: c.syms[n], d: d, name: name, body: body}
			f.f = facts.Function{Name: c.descName(n), Kind: kind, File: d.path, Line: d.lineOf(name.start)}
			var err error
			if c.opt.Lang == Go {
				err = c.goName(f)
			} else {
				err = c.rustName(f)
			}
			if err != nil {
				c.notes = append(c.notes, facts.Note{Kind: "signature_unparsed", File: d.path, Message: fmt.Sprintf("line %d: %s: %v", f.f.Line, f.f.Name, err)})
				continue
			}
			f.f.MinArity = f.f.Arity
			sum := sha256.Sum256(d.src[body.start:body.end])
			f.f.Fingerprint = hex.EncodeToString(sum[:8])
			if _, dup := c.fnBySym[n]; dup {
				continue
			}
			c.fns = append(c.fns, f)
			c.fnBySym[n] = f
		}
	}
	// One id per function: a second definition with the same id (several Go
	// init functions, a Rust bin and lib with the same root function) merges
	// into the first, and its calls are the first's.
	byID := map[string]*fn{}
	c.modules = map[string]*facts.Module{}
	sort.SliceStable(c.fns, func(i, j int) bool {
		a, b := c.fns[i], c.fns[j]
		if a.d.path != b.d.path {
			return a.d.path < b.d.path
		}
		return a.name.start < b.name.start
	})
	for _, f := range c.fns {
		f.f.ID = c.opt.Repo + ":" + f.f.ID
		f.f.Module = c.opt.Repo + ":" + f.key.id
		if first, ok := byID[f.f.ID]; ok {
			if f.f.Name != "init" {
				c.notes = append(c.notes, facts.Note{Kind: "duplicate_definition", File: f.d.path, Message: fmt.Sprintf("line %d: %s is also defined in %s:%d", f.f.Line, f.f.ID, first.f.File, first.f.Line)})
			}
			f.f = first.f
			continue
		}
		byID[f.f.ID] = f
		if _, ok := c.modules[f.f.Module]; !ok {
			c.modules[f.f.Module] = &facts.Module{ID: f.f.Module, Name: f.key.name, Namespace: f.key.namespace, File: f.d.path, Line: f.f.Line, Language: c.opt.Lang}
		}
		c.out.Functions = append(c.out.Functions, f.f)
	}
	for _, d := range c.docs {
		d.fns = nil
	}
	for _, f := range c.fns {
		f.d.fns = append(f.d.fns, f)
	}
}

// descName is the name in a symbol's last descriptor.
func (c *conv) descName(n string) string {
	s := c.symbol(n)
	if s == nil || len(s.Descriptors) == 0 {
		return ""
	}
	return s.Descriptors[len(s.Descriptors)-1].Name
}

// fromAt is the innermost function whose body holds off.
func (d *doc) fromAt(off int) *fn {
	var best *fn
	for _, f := range d.fns {
		if f.body.contains(off) && f.body.end > f.name.end && (best == nil || f.body.end-f.body.start < best.body.end-best.body.start) {
			best = f
		}
	}
	return best
}

// invoked says whether the source after end is a call: "(" after optional
// generic arguments.
func (c *conv) invoked(src []byte, end int) bool {
	i := end
	skip := func(newlines bool) {
		for i < len(src) && (src[i] == ' ' || src[i] == '\t' || (newlines && (src[i] == '\n' || src[i] == '\r'))) {
			i++
		}
	}
	if c.opt.Lang == Go {
		skip(false)
		if i < len(src) && src[i] == '[' {
			if i = matchClose(src, i, '[', ']'); i < 0 {
				return false
			}
			skip(false)
		}
		return i < len(src) && src[i] == '('
	}
	skip(true)
	if bytes.HasPrefix(src[i:], []byte("::")) {
		i += 2
		skip(true)
		if i >= len(src) || src[i] != '<' {
			return false
		}
		if i = matchAngle(src, i); i < 0 {
			return false
		}
		skip(true)
	}
	return i < len(src) && src[i] == '('
}

// matchClose returns the offset after the bracket closing the one at i, or -1.
func matchClose(src []byte, i int, open, close byte) int {
	depth := 0
	for ; i < len(src); i++ {
		switch src[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i + 1
			}
		case '\n':
			if open == '<' {
				continue
			}
		}
	}
	return -1
}

// matchAngle is matchClose for Rust generic arguments, where "->" is not a
// closing bracket.
func matchAngle(src []byte, i int) int {
	depth := 0
	for ; i < len(src); i++ {
		switch src[i] {
		case '<':
			depth++
		case '>':
			if i > 0 && src[i-1] == '-' {
				continue
			}
			depth--
			if depth == 0 {
				return i + 1
			}
		case ';', '{', '}':
			return -1
		}
	}
	return -1
}

// occurrences classifies every reference to a callable.
func (c *conv) occurrences() {
	for _, d := range c.docs {
		for _, o := range d.d.Occurrences {
			if scipb.SymbolRole_Definition.Matches(o) || scipb.IsLocalSymbol(o.Symbol) || o.Symbol == "" {
				continue
			}
			n := c.normalize(o.Symbol)
			yes, maybe := c.callable(n)
			if !yes && !maybe {
				continue
			}
			sp, ok := d.occSpan(o)
			if !ok || !nameMatches(d.src[sp.start:sp.end], c.descName(n)) {
				continue // operators, macro-generated spans
			}
			from := d.fromAt(sp.start)
			if from == nil {
				continue // package-level code, use and import paths
			}
			if !c.invoked(d.src, sp.end) {
				if yes {
					c.stats.Refs++
				}
				continue
			}
			c.call(from, n, d, sp.start)
		}
	}
}

func nameMatches(text []byte, name string) bool {
	t := strings.TrimPrefix(string(text), "r#")
	return t == name
}

// call records one invocation of the symbol n from f.
func (c *conv) call(from *fn, n string, d *doc, off int) {
	c.stats.CallSites++
	line := d.lineOf(off)
	add := func(to *fn, kind, precision string) {
		c.out.Calls = append(c.out.Calls, facts.Call{From: from.f.ID, To: to.f.ID, Kind: kind, File: d.path, Line: line, Precision: precision})
	}
	target, ok := c.fnBySym[n]
	if !ok {
		// Outside the repository, or code with no definition in its
		// documents: derive impls (Clone::clone), proc-macro output.
		c.stats.External++
		return
	}
	if impls, dispatch := c.dispatchTargets(target); dispatch {
		c.stats.Dispatch++
		if len(impls) == 0 {
			add(target, KindDispatch, facts.Precise)
			return
		}
		for _, to := range impls {
			add(to, KindDispatch, facts.Heuristic)
		}
		return
	}
	c.stats.InRepo++
	add(target, KindCall, facts.Precise)
}

// dispatchTargets reports whether a call to f dispatches, and to which
// functions.
func (c *conv) dispatchTargets(f *fn) ([]*fn, bool) {
	var out []*fn
	seen := map[string]bool{}
	addSym := func(s string) {
		if g, ok := c.fnBySym[s]; ok && !seen[g.f.ID] {
			seen[g.f.ID] = true
			out = append(out, g)
		}
	}
	switch c.opt.Lang {
	case Go:
		if f.info.GetKind() != scipb.SymbolInformation_MethodSpecification {
			return nil, false
		}
		for _, s := range c.impls[f.sym] {
			addSym(s)
		}
	case Rust:
		trait, ok := c.rust.traitOf(f.sym)
		if !ok {
			return nil, false
		}
		if f.info.GetKind() == scipb.SymbolInformation_Method {
			addSym(f.sym) // a provided method: types that don't override it run it
		}
		for _, s := range c.impls[trait+"\x00"+f.f.Name] {
			addSym(s)
		}
		if len(out) == 1 && out[0] == f {
			return nil, false
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].f.ID < out[j].f.ID })
	return out, true
}

// argCount counts the arguments of the call whose "(" follows end (after
// generic arguments), roughly: top-level commas.
func (c *conv) argCount(src []byte, end int) int {
	i := bytes.IndexByte(src[end:], '(')
	if i < 0 {
		return 0
	}
	i += end + 1
	depth, n, any := 0, 0, false
	for ; i < len(src); i++ {
		switch b := src[i]; b {
		case '(', '[', '{':
			depth++
			any = true
		case ')', ']', '}':
			if depth == 0 {
				if any {
					n++
				}
				return n
			}
			depth--
		case ',':
			if depth == 0 {
				n++
				any = false
			}
		case '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
			any = true
		case ' ', '\t', '\n', '\r':
		default:
			any = true
		}
	}
	return n
}

// unresolved records call syntax at off with no symbol.
func (c *conv) unresolved(from *fn, d *doc, off int, name string, arity int) {
	c.stats.CallSites++
	c.stats.Unresolved++
	c.out.Calls = append(c.out.Calls, facts.Call{From: from.f.ID, Target: &facts.Target{Name: name, Arity: arity}, Kind: KindUnresolved, File: d.path, Line: d.lineOf(off)})
}

func (c *conv) finish() {
	for _, d := range c.docs {
		lang := d.d.Language
		switch strings.ToLower(lang) {
		case "go":
			lang = Go
		case "rust":
			lang = Rust
		case "":
			lang = c.opt.Lang
		}
		cat := "source"
		if d.test {
			cat = "test"
		}
		c.out.Files = append(c.out.Files, facts.File{Path: d.path, Language: lang, Category: cat})
	}
	for _, m := range c.modules {
		c.out.Modules = append(c.out.Modules, *m)
	}
	sort.Slice(c.out.Modules, func(i, j int) bool { return c.out.Modules[i].ID < c.out.Modules[j].ID })
	sort.Slice(c.out.Functions, func(i, j int) bool { return c.out.Functions[i].ID < c.out.Functions[j].ID })
	sort.SliceStable(c.out.Calls, func(i, j int) bool {
		a, b := c.out.Calls[i], c.out.Calls[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	sort.Slice(c.out.Routes, func(i, j int) bool { return c.out.Routes[i].ID < c.out.Routes[j].ID })
	sort.SliceStable(c.out.HTTPRefs, func(i, j int) bool {
		a, b := c.out.HTTPRefs[i], c.out.HTTPRefs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	c.out.Stats = []facts.Stats{c.stats}
	sort.SliceStable(c.notes, func(i, j int) bool {
		if c.notes[i].Kind != c.notes[j].Kind {
			return c.notes[i].Kind < c.notes[j].Kind
		}
		return c.notes[i].File < c.notes[j].File
	})
	c.out.Notes = slices.Clip(c.notes)
}
