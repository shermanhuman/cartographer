package scip

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	scipb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/shermanhuman/cartographer/internal/facts"
)

// go test ./internal/scip -update re-runs the indexers on the fixtures and
// rewrites the committed indexes and goldens. The indexers must be on PATH:
//
//	mise exec github:sourcegraph/scip-go@0.2.9 aqua:rust-lang/rust-analyzer@2026-11-16 rust@1.99.1 -- go test ./internal/scip -update
var update = flag.Bool("update", false, "re-index the fixtures and rewrite the goldens")

type fixture struct {
	dir, lang string
}

var (
	goFixture    = fixture{"../../testdata/scip-go", Go}
	statsFixture = fixture{"../../testdata/scip-go-stats", Go}
	rustFixture  = fixture{"../../testdata/scip-rust", Rust}
)

func haveTools(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		arg := "--version"
		if n == "go" {
			arg = "version"
		}
		if err := exec.Command(n, arg).Run(); err != nil {
			t.Skipf("%s is not usable on PATH: %v", n, err)
		}
	}
}

func tools(lang string) []string {
	if lang == Go {
		return []string{"scip-go", "go"}
	}
	return []string{"rust-analyzer", "cargo"}
}

func indexerEnv(t *testing.T) []string {
	return setEnv(os.Environ(), "CARGO_TARGET_DIR="+t.TempDir())
}

// regenerate indexes a fixture and writes its index.scip, with the
// machine-specific metadata (project root, arguments) removed.
func regenerate(t *testing.T, fx fixture) {
	t.Helper()
	haveTools(t, tools(fx.lang)...)
	idx, err := RunIndexer(context.Background(), RunOptions{Options: Options{Lang: fx.lang, Repo: "fixture", Root: fx.dir}, RustBuildScripts: true, Env: indexerEnv(t), Log: testWriter{t}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 1 {
		t.Fatalf("want one index, got %d", len(idx))
	}
	ix := idx[0].Index
	ix.Metadata.ProjectRoot = "file:///fixture"
	ix.Metadata.ToolInfo.Arguments = nil
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(ix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.dir, "index.scip"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// convertFixture converts a fixture's committed index and compares it with
// its golden.
func convertFixture(t *testing.T, fx fixture) *facts.Doc {
	t.Helper()
	if *update {
		regenerate(t, fx)
	}
	ix, err := ReadIndex(filepath.Join(fx.dir, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Convert(Options{Lang: fx.lang, Repo: "fixture", Root: fx.dir, Revision: "r1"}, []Index{{Dir: ".", Index: ix}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.MarshalIndent(d, "", "  ")
	got = append(got, '\n')
	golden := filepath.Join(fx.dir, "facts.golden.json")
	if *update {
		os.WriteFile(golden, got, 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("facts differ from %s (go test -update rewrites it):\n%s", golden, lineDiff(string(want), string(got)))
	}
	for _, n := range d.Notes {
		if n.Kind == "signature_unparsed" || n.Kind == "parse_error" {
			t.Errorf("note: %+v", n)
		}
	}
	return d
}

// lineDiff lists lines only in want (-) or got (+), for a readable failure.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var out []string
	for _, l := range w {
		if !slices.Contains(g, l) {
			out = append(out, "- "+l)
		}
	}
	for _, l := range g {
		if !slices.Contains(w, l) {
			out = append(out, "+ "+l)
		}
	}
	if len(out) > 40 {
		out = append(out[:40], "...")
	}
	return strings.Join(out, "\n")
}

func fnIDs(d *facts.Doc) []string {
	var ids []string
	for _, f := range d.Functions {
		ids = append(ids, f.ID)
	}
	return ids
}

func function(t *testing.T, d *facts.Doc, id string) facts.Function {
	t.Helper()
	for _, f := range d.Functions {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no function %s in %v", id, fnIDs(d))
	return facts.Function{}
}

// edges lists "to kind precision" for every call from one function at one line.
func edges(d *facts.Doc, from string, line int) []string {
	var out []string
	for _, c := range d.Calls {
		if c.From == from && c.Line == line {
			to := c.To
			if to == "" {
				to = "?" + c.Target.Name
			}
			out = append(out, to+" "+c.Kind+" "+c.Precision)
		}
	}
	slices.Sort(out)
	return out
}

func wantEdges(t *testing.T, d *facts.Doc, from string, line int, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := edges(d, from, line); !slices.Equal(got, want) {
		t.Errorf("calls from %s at line %d:\n got %q\nwant %q", from, line, got, want)
	}
}

func TestConvertGo(t *testing.T) {
	d := convertFixture(t, goFixture)
	const app = "fixture:app."
	run := app + "Server.run/1"
	// f() beside the method value s.f: one call, and register(s.f) is the
	// only other call on its line.
	wantEdges(t, d, run, 21, app+"Server.f/0 call precise")
	wantEdges(t, d, run, 22, app+"register/1 call precise")
	// The interface call dispatches to both implementations; the concrete
	// one is precise.
	wantEdges(t, d, run, 23, app+"diskStore.Save/2 dispatch heuristic", app+"memStore.Save/2 dispatch heuristic")
	wantEdges(t, d, run, 24, app+"memStore.Save/2 call precise")
	wantEdges(t, d, run, 25, app+"Keys/1 call precise") // Keys[string](...)
	wantEdges(t, d, run, 26)                            // strings.ToUpper: external
	// Same-named methods on two types; variadic counts once; generated test
	// main dropped; GOOS fixed to linux.
	ids := fnIDs(d)
	for _, id := range []string{app + "memStore.Save/2", app + "diskStore.Save/2", app + "Store.Save/2", app + "Keys/1", "fixture:osdep.OnLinux/0", "fixture:cmd/tool.main/0"} {
		if !slices.Contains(ids, id) {
			t.Errorf("missing %s", id)
		}
	}
	for _, id := range ids {
		if strings.Contains(id, "OnDarwin") || strings.Contains(id, ".test") || strings.Contains(id, "..") {
			t.Errorf("unexpected function %s", id)
		}
	}
	// The root package's ids start with ./ (its config.Load is not
	// config/'s Load), and package app_test's helper is not app's.
	for _, id := range []string{"fixture:./config.Load/0", "fixture:./Routes/1", "fixture:config.Load/0", app + "helper/0", "fixture:app_test.helper/0", "fixture:app_test.TestExternal/1"} {
		if !slices.Contains(ids, id) {
			t.Errorf("missing %s", id)
		}
	}
	wantEdges(t, d, "fixture:app_test.TestExternal/1", 9, "fixture:app_test.helper/0 call precise")
	for _, n := range d.Notes {
		if n.Kind == "duplicate_definition" {
			t.Errorf("note: %+v", n)
		}
	}
	for _, f := range d.Files {
		if strings.HasPrefix(f.Path, "..") {
			t.Errorf("file outside root: %s", f.Path)
		}
		if want := map[bool]string{true: "test", false: "source"}[strings.HasSuffix(f.Path, "_test.go")]; f.Category != want || f.Language != "go" {
			t.Errorf("file %+v: want category %s, language go", f, want)
		}
	}
	// Routes: the wrapped handler resolves to the innermost function value;
	// the closure and the computed pattern are not extracted.
	var routes []string
	for _, r := range d.Routes {
		routes = append(routes, r.ID+" -> "+r.Handler+" "+r.Precision)
	}
	wantRoutes := []string{
		"fixture:route:./:GET /healthz -> fixture:./health/2 precise",
		"fixture:route:app:* /items/{id} -> " + app + "Server.items/2 precise",
		"fixture:route:app:POST /dial/events -> " + app + "Server.dialEvent/2 heuristic",
	}
	if !slices.Equal(routes, wantRoutes) {
		t.Errorf("routes:\n got %q\nwant %q", routes, wantRoutes)
	}
	// HTTP refs: post fixes POST; do takes the call site's method; get
	// forwards its path to do; a computed path is not extracted.
	var refs []string
	for _, r := range d.HTTPRefs {
		refs = append(refs, r.From+" "+r.Method+" "+r.Path+" base="+r.Base)
	}
	wantRefs := []string{
		app + "adminClient.Check/2 POST /api/v1/sales/cold/check base=baseURL",
		app + "adminClient.Suppress/2 POST /api/v1/sales/cold/suppress base=baseURL",
		app + "adminClient.Lookup/2 GET /api/v1/people/:id base=baseURL",
		app + "adminClient.Lookup/2 DELETE /api/v1/cache base=baseURL",
		app + "adminClient.Message/3 GET /gmail/v1/messages/:id base=baseURL",
	}
	if !slices.Equal(refs, wantRefs) {
		t.Errorf("http refs:\n got %q\nwant %q", refs, wantRefs)
	}
	if s := d.Stats[0]; s.NotExtracted != 3 || s.Refs != 5 || s.Unresolved != 0 || s.CallSites != s.InRepo+s.Dispatch+s.External+s.Unresolved {
		t.Errorf("stats %+v: want 3 not extracted (closure, computed pattern, computed path), 5 refs (s.f, four handlers), sites = sum", s)
	}
	if d.Producer.Name != "scip-go" || d.Producer.Version != "0.2.9" || d.FactsVersion != 2 || d.Revision != "r1" {
		t.Errorf("header: %+v", d)
	}
}

func TestConvertGoStats(t *testing.T) {
	d := convertFixture(t, statsFixture)
	want := facts.Stats{Language: "go", CallSites: 2, InRepo: 1, External: 1, Refs: 2}
	if len(d.Stats) != 1 || d.Stats[0] != want {
		t.Errorf("stats %+v, want %+v", d.Stats, want)
	}
	wantEdges(t, d, "fixture:./Use/0", 15, "fixture:./helper/0 call precise") // the root package
	if len(d.Calls) != 1 {
		t.Errorf("calls %+v: want only the in-repo one", d.Calls)
	}
}

func TestConvertRust(t *testing.T) {
	d := convertFixture(t, rustFixture)
	const geo = "fixture:crates/shapes::geo."
	// Two crates each with main: distinct ids.
	function(t, d, "fixture:crates/alpha.main/0")
	function(t, d, "fixture:crates/beta.main/0")
	// Concrete calls are precise; an associated function; trait impls
	// carry [impl Trait], two From impls stay apart.
	wantEdges(t, d, geo+"grow/1", 59, geo+"Circle.scale/1 call precise")
	wantEdges(t, d, geo+"grow/1", 60, geo+"Circle[impl Shape].area/0 call precise")
	wantEdges(t, d, "fixture:crates/alpha.main/0", 5, geo+"Circle.new/1 call precise")
	wantEdges(t, d, "fixture:crates/alpha.main/0", 9, geo+"Circle[impl From<f64>].from/1 call precise")
	function(t, d, geo+"Circle[impl From<u32>].from/1")
	if f := function(t, d, geo+"Circle.new/1"); f.Kind != "associated_function" || f.Module != "fixture:crates/shapes::geo.Circle" {
		t.Errorf("Circle::new: %+v", f)
	}
	// dyn and generic calls dispatch to both impls.
	both := []string{geo + "Circle[impl Shape].area/0 dispatch heuristic", geo + "Square[impl Shape].area/0 dispatch heuristic"}
	wantEdges(t, d, geo+"total/1", 49, both...)
	wantEdges(t, d, geo+"area_of/1", 54, both...)
	// A turbofish function value is a ref; the turbofish call is a call.
	wantEdges(t, d, "fixture:crates/alpha.main/0", 12)
	wantEdges(t, d, "fixture:crates/alpha.main/0", 13, geo+"area_of/1 call precise")
	// Tests: #[test] and a helper in #[cfg(test)], and the #[test] in an
	// integration test target (its own crate, a test file); nothing else.
	for _, f := range d.Functions {
		want := strings.Contains(f.ID, "::tests.") || f.ID == "fixture:crates/shapes/tests/api.api/0"
		if f.Test != want {
			t.Errorf("%s: test = %v", f.ID, f.Test)
		}
	}
	wantEdges(t, d, "fixture:crates/shapes/tests/api.setup/0", 11, geo+"Circle.new/1 call precise")
	for _, f := range d.Files {
		if want := map[bool]string{true: "test", false: "source"}[strings.Contains(f.Path, "/tests/")]; f.Category != want || f.Language != "rust" {
			t.Errorf("file %+v: want category %s, language rust", f, want)
		}
	}
	// The default target: linux_only present, macos_only absent. Build
	// scripts and proc macros ran: built_by_script exists, and generated()
	// resolves to a symbol with no definition in a document (external, like
	// a derive impl). Code a #[cfg] turns off is not unresolved.
	ids := fnIDs(d)
	function(t, d, geo+"linux_only/0")
	function(t, d, "fixture:crates/shapes.built_by_script/0")
	function(t, d, "fixture:crates/shapes/build.main/0")
	for _, id := range ids {
		if strings.Contains(id, "macos_only") {
			t.Errorf("unexpected %s", id)
		}
	}
	wantEdges(t, d, "fixture:crates/beta.main/0", 4)
	function(t, d, geo+"copy_tile/1")
	for _, c := range d.Calls {
		if c.From == geo+"copy_tile/1" {
			t.Errorf("copy_tile calls nothing in the repository: %+v", c)
		}
	}
	if s := d.Stats[0]; s.CallSites != s.InRepo+s.Dispatch+s.External+s.Unresolved || s.Dispatch != 3 || s.Refs != 1 || s.Unresolved != 0 {
		t.Errorf("stats %+v", s)
	}
}

// The indexer runs with GOOS=linux whatever the environment says.
func TestIndexGoFixesGOOS(t *testing.T) {
	haveTools(t, tools(Go)...)
	// In the extractor's environment, not the test binary's: the test binary
	// itself would build for darwin otherwise.
	env := append(os.Environ(), "GOOS=darwin", "CGO_ENABLED=1")
	d, err := Extract(context.Background(), RunOptions{Options: Options{Lang: Go, Repo: "fixture", Root: goFixture.dir}, Env: env, Log: testWriter{t}})
	if err != nil {
		t.Fatal(err)
	}
	ids := fnIDs(d)
	if !slices.Contains(ids, "fixture:osdep.OnLinux/0") || slices.Contains(ids, "fixture:osdep.OnDarwin/0") {
		t.Errorf("GOOS=darwin leaked into the indexer: %v", ids)
	}
}

// Indexing the fixture now gives the committed facts, and turning build
// scripts off loses what they and the proc macro make.
func TestIndexRust(t *testing.T) {
	haveTools(t, tools(Rust)...)
	opt := RunOptions{Options: Options{Lang: Rust, Repo: "fixture", Root: rustFixture.dir, Revision: "r1"}, RustBuildScripts: true, Env: indexerEnv(t), Log: testWriter{t}}
	on, err := Extract(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := ReadIndex(filepath.Join(rustFixture.dir, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	committed, err := Convert(opt.Options, []Index{{Dir: ".", Index: ix}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(on)
	b, _ := json.Marshal(committed)
	if !bytes.Equal(a, b) {
		t.Errorf("a fresh index differs from the committed one:\n%s", lineDiff(string(b), string(a)))
	}
	opt.RustBuildScripts = false
	off, err := Extract(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fnIDs(off), "fixture:crates/shapes.built_by_script/0") {
		t.Error("build scripts ran with --rust-build-scripts=false")
	}
	wantEdges(t, off, "fixture:crates/beta.main/0", 3, "?built_by_script unresolved ")
	wantEdges(t, off, "fixture:crates/beta.main/0", 4, "?generated unresolved ")
}

func TestRustArity(t *testing.T) {
	for sig, want := range map[string]int{
		"fn area(&self) -> f64":                                 0,
		"pub fn scale(&mut self, k: f64)":                       1,
		"pub fn area_of<S>(s: &S) -> f64\nwhere\n    S: Shape,": 1,
		"fn f<'a>(&'a self, m: HashMap<K, V>, g: impl Fn(u8, u8) -> u8, (a, b): (u8, u8),) -> bool": 3,
		"pub(crate) async unsafe fn r#match(self: Box<Self>, x: [u8; 4])":                           1,
		"fn new() -> Self": 0,
	} {
		got, err := rustArity(sig)
		if err != nil || got != want {
			t.Errorf("rustArity(%q) = %d, %v; want %d", sig, got, err, want)
		}
	}
	if _, err := rustArity("struct X"); err == nil {
		t.Error("an unparsable signature must fail")
	}
}

func TestStripLifetimes(t *testing.T) {
	for in, want := range map[string]string{"Prepared<'a>": "Prepared", "Typing<'_, '_>": "Typing", "Map<'a, K, V>": "Map<K, V>", "From<f64>": "From<f64>", "Visitor<'de>": "Visitor"} {
		if got := stripLifetimes(in); got != want {
			t.Errorf("stripLifetimes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInvoked(t *testing.T) {
	c := &conv{opt: Options{Lang: Go}}
	for src, want := range map[string]bool{"f()": true, "f[int](x)": true, "f)": false, "f[i]": false, "f": false, "f (x)": true} {
		if got := c.invoked([]byte(src), 1); got != want {
			t.Errorf("go %q: %v", src, got)
		}
	}
	c.opt.Lang = Rust
	for src, want := range map[string]bool{"f()": true, "f::<Vec<u8>>()": true, "f::<fn() -> u8>(g)": true, "f::<T>;": false, "f::g()": false, "f\n  (x)": true} {
		if got := c.invoked([]byte(src), 1); got != want {
			t.Errorf("rust %q: %v", src, got)
		}
	}
}

func TestGoModulesAndEnv(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"go.mod", "svc/go.mod", "vendor/x/go.mod", "testdata/y/go.mod", ".hidden/go.mod"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("module x\n"), 0o644)
	}
	mods, err := GoModules(root)
	if err != nil || !slices.Equal(mods, []string{".", "svc"}) {
		t.Errorf("modules %v, %v", mods, err)
	}
	env := setEnv([]string{"GOOS=darwin", "PATH=/bin", "CGO_ENABLED=1"}, GoEnv...)
	if !slices.Equal(env, []string{"PATH=/bin", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}) {
		t.Errorf("env %v", env)
	}
	var cfg map[string]any
	json.Unmarshal(RustConfig(false), &cfg)
	if cfg["cargo"].(map[string]any)["target"] != RustTarget || cfg["cargo"].(map[string]any)["buildScripts"] == nil {
		t.Errorf("config %v", cfg)
	}
	json.Unmarshal(RustConfig(true), &cfg)
	if _, ok := cfg["cargo"].(map[string]any)["buildScripts"]; ok {
		t.Errorf("build scripts on: config %v", cfg)
	}
}

// Documents outside the root are dropped even when another index puts
// them there through its directory.
func TestDropsDocumentsOutsideRoot(t *testing.T) {
	ix, err := ReadIndex(filepath.Join(goFixture.dir, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	ix = proto.Clone(ix).(*scipb.Index)
	ix.Documents = append(ix.Documents, &scipb.Document{RelativePath: "../../elsewhere.go", Language: "go"})
	d, err := Convert(Options{Lang: Go, Repo: "fixture", Root: goFixture.dir}, []Index{{Dir: ".", Index: ix}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Notes[0].Kind != "dropped_documents" || !strings.HasPrefix(d.Notes[0].Message, "2 ") {
		t.Errorf("notes %+v", d.Notes)
	}
}
