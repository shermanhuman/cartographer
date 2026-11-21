package scip

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	scipb "github.com/scip-code/scip/bindings/go/scip"

	"github.com/shermanhuman/cartographer/internal/facts"
)

type rustState struct {
	c        *conv
	roots    map[string]string // package + target descriptors -> crate root file
	testMods []string          // module symbols (normalized, ending in "/") under #[cfg(test)]
	pkgDirs  map[string]string // directory -> nearest package directory
	// traitNames counts in-repo traits by short name; pkgs are the
	// packages (crates) functions are defined in.
	traitNames map[string]int
	pkgs       map[string]bool
}

// rustTarget is how many leading descriptors name the crate target:
// lib/ or custom-build/ (a build script), else bin/<name>/ (also test/,
// example/, bench/ with a name).
func rustTarget(ds []*scipb.Descriptor) int {
	if len(ds) > 0 && ds[0].Suffix == scipb.Descriptor_Namespace && (ds[0].Name == "lib" || ds[0].Name == "custom-build") {
		return 1
	}
	if len(ds) > 1 && ds[0].Suffix == scipb.Descriptor_Namespace && ds[1].Suffix == scipb.Descriptor_Namespace {
		return 2
	}
	return 0
}

func (c *conv) targetKey(n string) (string, int) {
	s := c.symbol(n)
	if s == nil {
		return "", 0
	}
	t := rustTarget(s.Descriptors)
	if t == 0 {
		return "", 0
	}
	return c.pkgOf(n) + " " + scipb.DescriptorOnlyFormatter.FormatDescriptors(s.Descriptors[:t]), t
}

var (
	reCfgTest = regexp.MustCompile(`#\[\s*cfg\s*\(([^\]]*)\)\s*\]`)
	reTestFn  = regexp.MustCompile(`#\[\s*(?:[A-Za-z_]\w*::)*test\s*[\](]`)
	reMod     = regexp.MustCompile(`\bmod\s*$`)
)

// cfgTest reports whether attrs hold a #[cfg(...)] whose predicate can
// only hold under cfg(test): false with test off, whatever the other
// options (features, targets) are. cfg(test) and cfg(all(test, unix)) are;
// cfg(not(any(test, miri))) and cfg(feature = "test-utils") are not.
func cfgTest(attrs string) bool {
	for _, m := range reCfgTest.FindAllStringSubmatch(attrs, -1) {
		p := &cfgParser{s: m[1]}
		n := p.expr()
		if n == nil || p.bad {
			continue
		}
		if n.eval(false) == cfgFalse && n.eval(true) != cfgFalse {
			return true
		}
	}
	return false
}

type tri int

const (
	cfgUnknown tri = iota
	cfgFalse
	cfgTrue
)

// cfgNode is a cfg predicate: an option (name, or name = "value"), or
// not/all/any over others.
type cfgNode struct {
	op   string // "", "not", "all", "any"
	name string // an option: "test", "unix", "feature" (with a value)
	kids []*cfgNode
}

// eval is the predicate's value with test on or off and every other
// option unknown.
func (n *cfgNode) eval(test bool) tri {
	switch n.op {
	case "not":
		switch n.kids[0].eval(test) {
		case cfgTrue:
			return cfgFalse
		case cfgFalse:
			return cfgTrue
		}
		return cfgUnknown
	case "all", "any":
		stop, all := cfgFalse, cfgTrue // all: one false decides
		if n.op == "any" {
			stop, all = cfgTrue, cfgFalse
		}
		res := all
		for _, k := range n.kids {
			switch k.eval(test) {
			case stop:
				return stop
			case cfgUnknown:
				res = cfgUnknown
			}
		}
		return res
	}
	if n.name == "test" {
		if test {
			return cfgTrue
		}
		return cfgFalse
	}
	return cfgUnknown
}

// cfgParser reads a cfg predicate; quoted values are skipped whole.
type cfgParser struct {
	s   string
	i   int
	bad bool
}

func (p *cfgParser) space() {
	for p.i < len(p.s) && strings.ContainsRune(" \t\r\n", rune(p.s[p.i])) {
		p.i++
	}
}

func (p *cfgParser) expr() *cfgNode {
	p.space()
	start := p.i
	for p.i < len(p.s) && (p.s[p.i] == '_' || p.s[p.i] == ':' || 'a' <= p.s[p.i] && p.s[p.i] <= 'z' || 'A' <= p.s[p.i] && p.s[p.i] <= 'Z' || '0' <= p.s[p.i] && p.s[p.i] <= '9') {
		p.i++
	}
	name := p.s[start:p.i]
	if name == "" {
		p.bad = true
		return nil
	}
	p.space()
	if p.i < len(p.s) && p.s[p.i] == '=' { // name = "value"
		p.i++
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			p.bad = true
			return nil
		}
		for p.i++; p.i < len(p.s) && p.s[p.i] != '"'; p.i++ {
			if p.s[p.i] == '\\' {
				p.i++
			}
		}
		if p.i >= len(p.s) { // unclosed, or ends in a backslash
			p.bad = true
			return nil
		}
		p.i++
		return &cfgNode{name: name + "="}
	}
	if p.i < len(p.s) && p.s[p.i] == '(' && (name == "not" || name == "all" || name == "any") {
		p.i++
		n := &cfgNode{op: name}
		for {
			p.space()
			if p.i < len(p.s) && p.s[p.i] == ')' {
				p.i++
				break
			}
			k := p.expr()
			if k == nil {
				p.bad = true
				return nil
			}
			n.kids = append(n.kids, k)
			p.space()
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
			}
		}
		if name == "not" && len(n.kids) != 1 {
			p.bad = true
			return nil
		}
		return n
	}
	return &cfgNode{name: name}
}

func newRustState(c *conv) *rustState {
	rs := &rustState{c: c, roots: map[string]string{}, pkgDirs: map[string]string{}}
	for _, d := range c.docs {
		d.test = strings.HasPrefix(d.path, "tests/") || strings.Contains(d.path, "/tests/")
		for _, o := range d.d.Occurrences {
			if scipb.IsLocalSymbol(o.Symbol) {
				continue
			}
			n := c.normalize(o.Symbol)
			s := c.symbol(n)
			if s == nil || len(s.Descriptors) == 0 || s.Descriptors[len(s.Descriptors)-1].Suffix != scipb.Descriptor_Namespace {
				continue
			}
			key, t := c.targetKey(n)
			if t == 0 {
				continue
			}
			sp, ok := d.occSpan(o)
			if !ok {
				continue
			}
			def := scipb.SymbolRole_Definition.Matches(o)
			if len(s.Descriptors) == t {
				if def {
					if _, ok := rs.roots[key]; !ok {
						rs.roots[key] = d.path
					}
				}
				continue
			}
			// A module: inline (its definition spans attributes and body) or
			// declared with `mod name;` (a reference after `mod`).
			var attrs string
			if def {
				if r, ok := o.EnclosingSourceRange(); ok && d.offset(r.Start) < sp.start {
					attrs = string(d.src[d.offset(r.Start):sp.start])
				}
			} else if reMod.Match(d.src[d.lines[d.lineOf(sp.start)-1]:sp.start]) {
				attrs = attrsBefore(d, sp.start)
			}
			if cfgTest(attrs) {
				rs.testMods = append(rs.testMods, n)
			}
		}
	}
	return rs
}

// attrsBefore is the text of the attribute and comment lines above off's
// line, and that line up to off.
func attrsBefore(d *doc, off int) string {
	l := d.lineOf(off) - 1
	start := d.lines[l]
	for l > 0 {
		prev := strings.TrimSpace(string(d.src[d.lines[l-1]:d.lines[l]]))
		if !strings.HasPrefix(prev, "#[") && !strings.HasPrefix(prev, "//") && !strings.HasPrefix(prev, "#!") {
			break
		}
		l--
		start = d.lines[l]
	}
	return string(d.src[start:off])
}

// crateDir names a crate: its package directory for the library and
// src/main.rs ("./" for a package at the repository root, which no
// directory can start with), else the target's root file without .rs
// (src/bin/tool, tests/api).
func (rs *rustState) crateDir(key, fallbackFile string, lib bool) string {
	file, ok := rs.roots[key]
	if !ok {
		file = fallbackFile
	}
	pkg := rs.pkgDir(path.Dir(file))
	if lib || file == path.Join(pkg, "src/main.rs") || !ok {
		if pkg == "." {
			return "./"
		}
		return pkg
	}
	f := strings.TrimSuffix(file, ".rs")
	if path.Base(f) == "main" {
		f = path.Dir(f)
	}
	return f
}

// pkgDir is the nearest directory at or above dir with a Cargo.toml.
func (rs *rustState) pkgDir(dir string) string {
	if p, ok := rs.pkgDirs[dir]; ok {
		return p
	}
	p := dir
	for {
		if _, err := os.Stat(filepath.Join(rs.c.root, filepath.FromSlash(p), "Cargo.toml")); err == nil {
			break
		}
		if p == "." || p == "/" {
			p = "."
			break
		}
		p = path.Dir(p)
	}
	rs.pkgDirs[dir] = p
	return p
}

var (
	reLifetime   = regexp.MustCompile(`'[A-Za-z_]\w*\s*,?\s*`)
	reEmptyAngle = regexp.MustCompile(`<\s*>`)
	reTrailComma = regexp.MustCompile(`,\s*>`)
)

// stripLifetimes drops lifetime arguments, so renaming one keeps the id:
// Prepared<'a> -> Prepared, Visitor<'de> -> Visitor, Map<'a, K> -> Map<K>.
func stripLifetimes(s string) string {
	if !strings.Contains(s, "'") {
		return s
	}
	s = reLifetime.ReplaceAllString(s, "")
	s = reTrailComma.ReplaceAllString(s, ">")
	return reEmptyAngle.ReplaceAllString(s, "")
}

// traitName strips generic arguments and the path: `fmt::Display<T>` -> Display.
func traitName(s string) string {
	if i := strings.IndexByte(s, '<'); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "::"); i >= 0 {
		s = s[i+2:]
	}
	return strings.TrimSpace(s)
}

// traitOf is the normalized symbol of the in-repo trait a trait member
// (Shape#area()) belongs to.
func (rs *rustState) traitOf(n string) (string, bool) {
	s := rs.c.symbol(n)
	if s == nil || len(s.Descriptors) < 2 {
		return "", false
	}
	p := s.Descriptors[len(s.Descriptors)-2]
	if p.Suffix != scipb.Descriptor_Type || p.Name == "impl" {
		return "", false
	}
	parent := s.Scheme + " " + s.Package.Manager + " " + s.Package.Name + " " + scipb.DescriptorOnlyFormatter.FormatDescriptors(s.Descriptors[:len(s.Descriptors)-1])
	if rs.c.kindOf(parent) != scipb.SymbolInformation_Trait {
		return "", false
	}
	return parent, true
}

// rustName sets a Rust function's id:
// <crate dir>::<module path>.<Type>[impl <Trait>].<name>/<arity>.
func (c *conv) rustName(f *fn) error {
	s := c.symbol(f.sym)
	if s == nil {
		return errors.New("unparsable symbol")
	}
	key, t := c.targetKey(f.sym)
	if t == 0 {
		return fmt.Errorf("no crate target in %q", f.sym)
	}
	ds := s.Descriptors[t : len(s.Descriptors)-1]
	var mods []string
	for len(ds) > 0 && ds[0].Suffix == scipb.Descriptor_Namespace {
		mods = append(mods, ds[0].Name)
		ds = ds[1:]
	}
	var owner []string
	for i := 0; i < len(ds); i++ {
		switch d := ds[i]; {
		case d.Suffix == scipb.Descriptor_Type && d.Name == "impl":
			var tps []string
			for i+1 < len(ds) && ds[i+1].Suffix == scipb.Descriptor_TypeParameter {
				tps = append(tps, stripLifetimes(ds[i+1].Name))
				i++
			}
			if len(tps) == 0 {
				return errors.New("impl without a type")
			}
			o := tps[0]
			if len(tps) > 1 {
				o += "[impl " + tps[1] + "]"
				f.trait = traitName(tps[1])
			}
			owner = append(owner, o)
		case d.Suffix == scipb.Descriptor_Type || d.Suffix == scipb.Descriptor_Method:
			owner = append(owner, stripLifetimes(d.Name))
		}
	}
	sig := ""
	if f.info.GetSignatureDocumentation() != nil {
		sig = f.info.SignatureDocumentation.Text
	}
	arity, err := rustArity(sig)
	if err != nil {
		return err
	}
	f.f.Arity = min(arity, facts.MaxArity)
	crate := c.rust.crateDir(key, f.d.path, strings.HasSuffix(key, " lib/"))
	mod := crate
	if len(mods) > 0 {
		mod += "::" + strings.Join(mods, "::")
	}
	f.key = moduleKey{id: mod, name: mod, namespace: append([]string{crate}, mods...)}
	if len(owner) > 0 {
		o := strings.Join(owner, ".")
		f.key.id += "." + o
		f.key.name = f.key.id
	}
	f.f.ID = fmt.Sprintf("%s.%s/%d", f.key.id, f.f.Name, f.f.Arity)
	// Tests: #[test] (also tokio::test) or anywhere under #[cfg(test)].
	if reTestFn.MatchString(string(f.d.src[f.body.start:f.name.start])) || cfgTest(string(f.d.src[f.body.start:f.name.start])) {
		f.f.Test = true
	}
	for _, m := range c.rust.testMods {
		if strings.HasPrefix(f.sym, m) {
			f.f.Test = true
		}
	}
	return nil
}

var reFnName = regexp.MustCompile(`\bfn\s+(?:r#)?[A-Za-z_][A-Za-z0-9_]*`)

// rustArity counts the declared parameters in a signature like
// "pub fn area_of<S>(s: &S) -> f64"; self is not counted.
func rustArity(sig string) (int, error) {
	loc := reFnName.FindStringIndex(sig)
	if loc == nil {
		return 0, fmt.Errorf("no fn in signature %q", sig)
	}
	src := []byte(sig)
	i := loc[1]
	for i < len(src) && (src[i] == ' ' || src[i] == '\n') {
		i++
	}
	if i < len(src) && src[i] == '<' {
		if i = matchAngle(src, i); i < 0 {
			return 0, fmt.Errorf("unbalanced generics in %q", sig)
		}
	}
	for i < len(src) && (src[i] == ' ' || src[i] == '\n') {
		i++
	}
	if i >= len(src) || src[i] != '(' {
		return 0, fmt.Errorf("no parameter list in %q", sig)
	}
	var parts []string
	depth, start := 0, i+1
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '(', '[', '{', '<':
			depth++
		case '>':
			if src[j-1] != '-' {
				depth--
			}
		case ')', ']', '}':
			depth--
			if depth == 0 {
				parts = append(parts, string(src[start:j]))
				n := 0
				for k, p := range parts {
					p = strings.TrimSpace(p)
					if p == "" || (k == 0 && isSelfParam(p)) {
						continue
					}
					n++
				}
				return n, nil
			}
		case ',':
			if depth == 1 {
				parts = append(parts, string(src[start:j]))
				start = j + 1
			}
		}
	}
	return 0, fmt.Errorf("unbalanced parameters in %q", sig)
}

var reSelf = regexp.MustCompile(`^(?:&\s*(?:'\w+\s+)?)?(?:mut\s+)?self\b`)

func isSelfParam(p string) bool { return reSelf.MatchString(p) }

// rustPrepare collects trait impl methods by their trait's identity (the
// trait's SCIP symbol, from the path in the impl's header) and name, so a
// call through a::Service::run dispatches to impls of a::Service only, not
// of another trait named Service. An impl whose header names its trait
// nowhere the index saw (one a macro wrote) goes under the trait's short
// name, used only while one in-repo trait has that name.
func (c *conv) rustPrepare() {
	for _, f := range c.fns {
		if f.trait == "" {
			continue
		}
		k := c.implTrait(f)
		if k == "" {
			k = "?" + f.trait
		}
		k += "\x00" + f.f.Name
		c.impls[k] = append(c.impls[k], f.sym)
	}
	for k := range c.impls {
		sort.Strings(c.impls[k])
	}
	c.rust.traitNames = map[string]int{}
	for n, info := range c.syms {
		if info.Kind == scipb.SymbolInformation_Trait && c.fnPkg(n) {
			if s := c.symbol(n); s != nil && len(s.Descriptors) > 0 {
				c.rust.traitNames[s.Descriptors[len(s.Descriptors)-1].Name]++
			}
		}
	}
}

// fnPkg reports whether symbol n is in a package the repository defines
// functions in (an in-repo crate).
func (c *conv) fnPkg(n string) bool {
	if c.rust.pkgs == nil {
		c.rust.pkgs = map[string]bool{}
		for _, f := range c.fns {
			c.rust.pkgs[c.pkgOf(f.sym)] = true
		}
	}
	return c.rust.pkgs[c.pkgOf(n)]
}

// implTrait is the normalized symbol of the trait f's impl block implements:
// the occurrence of a type named f.trait in the block's header (impl<T>
// a::Service for X<T> where ...: the first one), or "".
func (c *conv) implTrait(f *fn) string {
	d := f.d
	if d.blocks == nil {
		d.blocks = rustBlocks(d.src)
	}
	var head, open = -1, -1
	for _, b := range d.blocks { // innermost: the last opened that holds f
		if b.open < f.name.start && f.name.start < b.close {
			head, open = b.head, b.open
		}
	}
	if open < 0 {
		return ""
	}
	d.hasGlobalOccurrence(0, 0) // builds occStarts
	for i := sort.SearchInts(d.occStarts, head); i < len(d.occStarts) && d.occStarts[i] < open; i++ {
		for _, oi := range d.occ[d.occStarts[i]] {
			n := c.normalize(d.d.Occurrences[oi].Symbol)
			if s := c.symbol(n); s != nil && len(s.Descriptors) > 0 {
				if last := s.Descriptors[len(s.Descriptors)-1]; last.Suffix == scipb.Descriptor_Type && last.Name == f.trait {
					return n
				}
			}
		}
	}
	return ""
}

// rblock is a {} block: the start of the item it opens (after the
// previous ; or } at its depth), its { and its }.
type rblock struct{ head, open, close int }

// rustBlocks lists src's {} blocks, skipping comments, strings, raw
// strings and char literals.
func rustBlocks(src []byte) []rblock {
	var out []rblock
	var stack []int
	heads := []int{0}
	for i := 0; i < len(src); {
		b := src[i]
		switch {
		case b == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case b == '/' && i+1 < len(src) && src[i+1] == '*':
			depth := 0
			for i < len(src) {
				if src[i] == '/' && i+1 < len(src) && src[i+1] == '*' {
					depth++
					i += 2
				} else if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
		case b == '"':
			i = skipString(src, i)
		case b == '\'':
			switch {
			case i+2 < len(src) && src[i+1] == '\\':
				j := i + 2
				for j < len(src) && src[j] != '\'' && src[j] != '\n' {
					j++
				}
				i = j + 1
			case i+2 < len(src) && src[i+2] == '\'':
				i += 3
			default: // a lifetime
				i++
			}
		case isIdentStart(b):
			j := i
			for j < len(src) && isIdent(src[j]) {
				j++
			}
			if w := string(src[i:j]); (w == "r" || w == "br") && j < len(src) && (src[j] == '"' || src[j] == '#') {
				k := j
				for k < len(src) && src[k] == '#' {
					k++
				}
				if k < len(src) && src[k] == '"' {
					end := strings.Index(string(src[k+1:]), "\""+strings.Repeat("#", k-j))
					if end < 0 {
						return out
					}
					j = k + 1 + end + 1 + (k - j)
				}
			}
			i = j
		case b == '{':
			stack = append(stack, len(out))
			out = append(out, rblock{head: heads[len(heads)-1], open: i, close: len(src)})
			heads = append(heads, i+1)
			i++
		case b == '}':
			if len(stack) > 0 {
				out[stack[len(stack)-1]].close = i
				stack = stack[:len(stack)-1]
				heads = heads[:len(heads)-1]
			}
			heads[len(heads)-1] = i + 1
			i++
		case b == ';':
			heads[len(heads)-1] = i + 1
			i++
		default:
			i++
		}
	}
	return out
}

var rustKeywords = map[string]bool{
	"as": true, "async": true, "await": true, "break": true, "const": true, "continue": true, "crate": true,
	"dyn": true, "else": true, "enum": true, "extern": true, "false": true, "fn": true, "for": true, "if": true,
	"impl": true, "in": true, "let": true, "loop": true, "match": true, "mod": true, "move": true, "mut": true,
	"pub": true, "ref": true, "return": true, "self": true, "Self": true, "static": true, "struct": true,
	"super": true, "trait": true, "true": true, "type": true, "unsafe": true, "use": true, "where": true,
	"while": true, "yield": true, "box": true, "union": true,
	"Fn": true, "FnMut": true, "FnOnce": true,
}

// rustSyntax finds call syntax inside functions that the indexer gave no
// symbol: name(...) or name::<T>(...) with no occurrence on name.
func (c *conv) rustSyntax() {
	for _, d := range c.docs {
		src := d.src
		prev := ""
		for i := 0; i < len(src); {
			b := src[i]
			switch {
			case b == '/' && i+1 < len(src) && src[i+1] == '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			case b == '/' && i+1 < len(src) && src[i+1] == '*':
				depth := 0
				for i < len(src) {
					if src[i] == '/' && i+1 < len(src) && src[i+1] == '*' {
						depth++
						i += 2
					} else if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
						depth--
						i += 2
						if depth == 0 {
							break
						}
					} else {
						i++
					}
				}
				continue
			case b == '#' && i+1 < len(src) && (src[i+1] == '[' || (src[i+1] == '!' && i+2 < len(src) && src[i+2] == '[')):
				j := i + 1
				if src[j] == '!' {
					j++
				}
				if k := matchClose(src, j, '[', ']'); k > 0 {
					i = k
					// Code a #[cfg] turns off has no global occurrences
					// but macro paths (rust-analyzer still resolves locals
					// and macro names in it): skip it rather than count
					// its calls as unresolved.
					if attr := strings.TrimSpace(string(src[j+1 : k-1])); strings.HasPrefix(attr, "cfg") && !strings.HasPrefix(attr, "cfg_attr") {
						if end := cfgExtent(src, i); end > i && !d.hasGlobalOccurrence(i, end) {
							i = end
						}
					}
				} else {
					i = j
				}
				prev = ""
				continue
			case b == '"':
				i = skipString(src, i)
				prev = "lit"
				continue
			case b == '\'':
				// a char literal ('a', '\n') or a lifetime ('a)
				if i+2 < len(src) && src[i+1] == '\\' {
					j := i + 2
					for j < len(src) && src[j] != '\'' && src[j] != '\n' {
						j++
					}
					i = j + 1
				} else if i+2 < len(src) && src[i+2] == '\'' {
					i += 3
				} else {
					i++
					for i < len(src) && isIdent(src[i]) {
						i++
					}
				}
				prev = "lit"
				continue
			case isIdentStart(b):
				j := i
				for j < len(src) && isIdent(src[j]) {
					j++
				}
				word := string(src[i:j])
				if (word == "r" || word == "b" || word == "br" || word == "rb") && j < len(src) && (src[j] == '"' || src[j] == '#') {
					// raw or byte string
					k := j
					for k < len(src) && src[k] == '#' {
						k++
					}
					if k < len(src) && src[k] == '"' {
						hashes := k - j
						end := strings.Index(string(src[k+1:]), "\""+strings.Repeat("#", hashes))
						if end < 0 {
							i = len(src)
						} else {
							i = k + 1 + end + 1 + hashes
						}
						prev = "lit"
						continue
					}
				}
				start := i
				if word == "r" && j < len(src) && src[j] == '#' {
					j++
					k := j
					for k < len(src) && isIdent(src[k]) {
						k++
					}
					word, j = string(src[j:k]), k
				}
				if word == "macro_rules" {
					// skip the definition: macro_rules! name { ... }
					k := j
					for k < len(src) && src[k] != '{' && src[k] != '(' && src[k] != '[' {
						k++
					}
					if k < len(src) {
						if e := matchClose(src, k, src[k], closer(src[k])); e > 0 {
							j = e
						}
					}
				} else if !rustKeywords[word] && prev != "fn" && !(word[0] >= '0' && word[0] <= '9') && c.invoked(src, j) && len(d.occ[start]) == 0 {
					if from := d.fromAt(start); from != nil && !macroBang(src, j) {
						c.unresolved(from, d, start, word, c.argCount(src, j))
					}
				}
				prev = word
				i = j
				continue
			case b >= '0' && b <= '9':
				for i < len(src) && (isIdent(src[i]) || (src[i] == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9')) {
					i++
				}
				prev = "lit"
				continue
			}
			if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
				prev = string(b)
			}
			i++
		}
	}
}

// cfgExtent is the end of the item, block or statement starting after i:
// a {...} block, or up to a ";" or the close of the first top-level {...}.
func cfgExtent(src []byte, i int) int {
	depth := 0
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '"':
			j = skipString(src, j) - 1
		case '/':
			if j+1 < len(src) && src[j+1] == '/' {
				for j < len(src) && src[j] != '\n' {
					j++
				}
			}
		case '(', '[', '{':
			depth++
		case ')', ']':
			depth--
		case '}':
			depth--
			if depth == 0 {
				return j + 1
			}
			if depth < 0 {
				return j
			}
		case ';', ',':
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(src)
}

// hasGlobalOccurrence says whether an occurrence of a global symbol other
// than a macro starts in [start, end).
func (d *doc) hasGlobalOccurrence(start, end int) bool {
	if d.occStarts == nil {
		for off, is := range d.occ {
			for _, i := range is {
				if sym := d.d.Occurrences[i].Symbol; !scipb.IsLocalSymbol(sym) && !strings.HasSuffix(sym, "!") {
					d.occStarts = append(d.occStarts, off)
					break
				}
			}
		}
		sort.Ints(d.occStarts)
	}
	i := sort.SearchInts(d.occStarts, start)
	return i < len(d.occStarts) && d.occStarts[i] < end
}

func macroBang(src []byte, j int) bool { return j < len(src) && src[j] == '!' }

func closer(b byte) byte {
	switch b {
	case '(':
		return ')'
	case '[':
		return ']'
	}
	return '}'
}

func skipString(src []byte, i int) int {
	for i++; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return i
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b >= 0x80
}

func isIdent(b byte) bool { return isIdentStart(b) || (b >= '0' && b <= '9') }
