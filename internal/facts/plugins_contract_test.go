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
