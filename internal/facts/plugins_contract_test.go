package facts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Contract tests: each bundled extractor plugin's output must decode (no
// unknown fields) and validate. Plugins whose runtime is missing are skipped.

const repoRoot = "../.."

func runPlugin(t *testing.T, runtime string, args ...string) *Doc {
	t.Helper()
	bin, err := exec.LookPath(runtime)
	if err != nil {
		t.Skipf("%s not on PATH (run under `mise exec --`)", runtime)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", runtime, strings.Join(args, " "), err, stderr.String())
	}
	d, err := Decode(&stdout)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	return d
}

func loadFile(t *testing.T, path string) *Doc {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return d
}

func TestElixirPluginContract(t *testing.T) {
	d := runPlugin(t, "elixir", "plugins/extract-elixir/extract.exs",
		"--repo", "shop", "--root", "plugins/extract-elixir/test/fixture",
		"--source-roots", "lib,test", "--revision", "0123456789abcdef",
		"--schema-facts", "plugins/extract-elixir/test/schema.json")

	if d.Producer.Name != "extract-elixir" || d.Producer.CallPrecision != Precise {
		t.Errorf("producer = %+v", d.Producer)
	}
	if d.FactsVersion != 2 || len(d.Stats) != 1 || d.Stats[0].Language != "elixir" ||
		d.Stats[0].CallSites != len(d.Calls) {
		t.Errorf("facts_version %d, stats %+v (want version 2, one elixir entry counting %d calls)",
			d.FactsVersion, d.Stats, len(d.Calls))
	}
	if d.Repo != "shop" || d.Revision != "0123456789abcdef" {
		t.Errorf("repo/revision = %q/%q", d.Repo, d.Revision)
	}
	if len(d.Modules) == 0 || len(d.Functions) == 0 || len(d.Calls) == 0 || len(d.TableRefs) == 0 {
		t.Fatalf("empty sections: %d modules, %d functions, %d calls, %d table_refs",
			len(d.Modules), len(d.Functions), len(d.Calls), len(d.TableRefs))
	}
	for i, c := range d.Calls {
		if c.To != "" || c.Target == nil {
			t.Errorf("calls[%d]: plugin must emit targets, not ids: %+v", i, c)
		}
	}
	for i, f := range d.Functions {
		if f.Fingerprint == "" || len(f.Clauses) == 0 {
			t.Errorf("functions[%d] %s: missing fingerprint or clauses", i, f.ID)
		}
	}

	schema := loadFile(t, filepath.Join(repoRoot, "plugins/extract-elixir/test/schema.json"))
	tables := map[string]bool{}
	for _, tb := range schema.Tables {
		tables[tb.ID] = true
	}
	for i, r := range d.TableRefs {
		if !tables[r.Table] {
			t.Errorf("table_refs[%d]: %q is not in the schema facts", i, r.Table)
		}
	}
}

// The extractor's version-2 sections on fixtures shaped like a Phoenix admin
// app, a client app with a Req wrapper, and a shared library whose one
// request is built across functions.
func TestElixirPluginWebContract(t *testing.T) {
	web := "plugins/extract-elixir/test/web_fixture/"
	run := func(repo, roots string) *Doc {
		return runPlugin(t, "elixir", "plugins/extract-elixir/extract.exs",
			"--repo", repo, "--root", web+repo, "--source-roots", roots)
	}

	admin := run("admin", "lib")
	var check *Route
	for i, r := range admin.Routes {
		if r.ID == "admin:route:AdminWeb.Router:POST /api/v1/sales/cold/check" {
			check = &admin.Routes[i]
		}
	}
	if check == nil || check.Handler != "admin:AdminWeb.API.V1.SalesColdController.check/2" || check.Precision != Precise {
		t.Errorf("admin: POST /api/v1/sales/cold/check = %+v; routes %+v", check, admin.Routes)
	}
	if admin.Stats[0].NotExtracted != 5 { // live_dashboard, forward, live, match, a non-literal path
		t.Errorf("admin: not_extracted = %d, want 5", admin.Stats[0].NotExtracted)
	}

	rosearch := run("rosearch", "lib")
	refs := map[string]string{}
	for _, r := range rosearch.HTTPRefs {
		refs[r.Method+" "+r.Path] = r.From
		if r.Base != "Rosearch.ApiClient" {
			t.Errorf("rosearch: ref %+v: base want Rosearch.ApiClient", r)
		}
	}
	if len(rosearch.HTTPRefs) != 5 || refs["GET /tenants/:p1/vehicle-cleanup"] != "rosearch:Rosearch.VehicleList.cleanup/3" {
		t.Errorf("rosearch: refs = %+v", rosearch.HTTPRefs)
	}
	if rosearch.Stats[0].NotExtracted != 2 {
		t.Errorf("rosearch: not_extracted = %d, want 2", rosearch.Stats[0].NotExtracted)
	}

	sites := run("sites", "shared-libs/lib")
	if len(sites.HTTPRefs) != 0 || sites.Stats[0].NotExtracted != 2 {
		t.Errorf("sites: refs %+v, not_extracted %d; want none and 2", sites.HTTPRefs, sites.Stats[0].NotExtracted)
	}
}

func TestJavaScriptPluginContract(t *testing.T) {
	d := runPlugin(t, "node", "plugins/extract-javascript/extract.mjs",
		"--repo", "shop", "--root", "plugins/extract-javascript/test/fixture")

	if d.Producer.Name != "extract-javascript" || d.Producer.CallPrecision != Heuristic {
		t.Errorf("producer = %+v", d.Producer)
	}
	if len(d.Functions) == 0 || len(d.Calls) == 0 {
		t.Fatalf("empty sections: %d functions, %d calls", len(d.Functions), len(d.Calls))
	}
	for i, c := range d.Calls {
		if !strings.HasPrefix(c.Kind, "js_") || c.Target == nil {
			t.Errorf("calls[%d]: want a js_* target, got %+v", i, c)
		}
	}
}

func TestPostgresSchemaSampleContract(t *testing.T) {
	d := loadFile(t, filepath.Join(repoRoot, "plugins/extract-postgres-schema/test/sample.json"))
	if d.Producer.Name != "extract-postgres-schema" || d.Repo != "" {
		t.Errorf("producer/repo = %+v / %q", d.Producer, d.Repo)
	}
	if len(d.Tables) == 0 || len(d.ForeignKeys) == 0 {
		t.Fatalf("empty sections: %d tables, %d foreign_keys", len(d.Tables), len(d.ForeignKeys))
	}
	kinds := map[string]string{}
	for _, tb := range d.Tables {
		kinds[tb.ID] = tb.Kind
	}
	if kinds["public.orders"] != "table" || kinds["public.order_totals"] != "view" {
		t.Errorf("table kinds = %v; want orders=table, order_totals=view", kinds)
	}
}

// TestValidateFactsFiles validates real plugin output during development:
//
//	CARTOGRAPHER_FACTS_FILES=/tmp/a.json:/tmp/b.json mise exec -- go test ./internal/facts -run ValidateFactsFiles -v
func TestValidateFactsFiles(t *testing.T) {
	list := os.Getenv("CARTOGRAPHER_FACTS_FILES")
	if list == "" {
		t.Skip("CARTOGRAPHER_FACTS_FILES not set")
	}
	for _, path := range filepath.SplitList(list) {
		d := loadFile(t, path)
		t.Logf("%s: valid; %d files, %d modules, %d functions, %d calls, %d tables, %d foreign_keys, %d table_refs, %d notes",
			path, len(d.Files), len(d.Modules), len(d.Functions), len(d.Calls),
			len(d.Tables), len(d.ForeignKeys), len(d.TableRefs), len(d.Notes))
	}
}
