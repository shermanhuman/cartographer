package facts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = "../../testdata/workspace-mini/snapshot/facts"

func load(t *testing.T, name string) *Doc {
	t.Helper()
	f, err := os.Open(filepath.Join(fixture, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFixturesValidate(t *testing.T) {
	for _, name := range []string{"shop-app.json", "shop-worker.json", "schema.json"} {
		if err := load(t, name).Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBrokenReferencesReportJSONPaths(t *testing.T) {
	d := load(t, "shop-app.json")
	d.Functions[0].Module = "shop-app:Nope"
	d.Calls[0].From = "shop-app:Missing.fun/0"
	d.Calls[1].To, d.Calls[1].Target = "", nil
	d.TableRefs[0].File = "lib/unknown.ex"

	err := d.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	for _, want := range []string{
		`$.functions[0].module: unknown module "shop-app:Nope"`,
		`$.calls[0].from: unknown function "shop-app:Missing.fun/0"`,
		`$.calls[1]: one of to or target is required`,
		`$.table_refs[0].file: "lib/unknown.ex" is not in $.files`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"facts_version":1,"producer":{"name":"x"},"bogus":1}`))
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}

func TestVersionAndPrecisionChecked(t *testing.T) {
	d := &Doc{FactsVersion: 3, Producer: Producer{Name: "x", CallPrecision: "maybe"}}
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "$.facts_version") || !strings.Contains(err.Error(), "call_precision") {
		t.Fatalf("got %v", err)
	}
}

func TestMinArityRangeAndTableRefLine(t *testing.T) {
	for _, c := range []struct {
		min, arity int
		ok         bool
	}{{0, 0, true}, {1, 2, true}, {2, 2, true}, {3, 2, false}, {-1, 2, false}, {0, MaxArity, true}, {0, MaxArity + 1, false}, {0, 1_000_000_000, false}} {
		d := load(t, "shop-app.json")
		d.Functions[0].MinArity, d.Functions[0].Arity = c.min, c.arity
		err := d.Validate()
		bad := err != nil && (strings.Contains(err.Error(), "$.functions[0].min_arity") || strings.Contains(err.Error(), "$.functions[0].arity"))
		if bad == c.ok {
			t.Errorf("min_arity %d arity %d: got %v", c.min, c.arity, err)
		}
	}
	d := load(t, "shop-app.json")
	d.TableRefs[0].Line = 0
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "$.table_refs[0].line: must be > 0") {
		t.Errorf("got %v", err)
	}
}

func TestVersion1RejectsVersion2Sections(t *testing.T) {
	d := load(t, "shop-app.json")
	d.FactsVersion = 1
	d.Routes = []Route{{ID: "r", Path: "/x", File: d.Files[0].Path}}
	d.Functions[0].Test = true
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "$.routes: needs facts_version 2") || !strings.Contains(err.Error(), "$.functions[0].test: needs facts_version 2") {
		t.Fatalf("got %v", err)
	}
	d.FactsVersion = 2
	if err := d.Validate(); err != nil {
		t.Fatalf("version 2 with routes: %v", err)
	}
}

func TestVersion2Sections(t *testing.T) {
	d := load(t, "shop-app.json")
	d.FactsVersion = 2
	f := d.Functions[0]
	d.Routes = []Route{
		{ID: "r1", Method: "POST", Path: "/a", Handler: f.ID, File: f.File, Precision: Precise},
		{ID: "r1", Method: "post", Path: "a", Handler: "nope", File: f.File, Precision: "maybe"},
	}
	d.HTTPRefs = []HTTPRef{{From: "nope", Method: "get", Path: "x", File: f.File}}
	d.ConfigURLs = []ConfigURL{{Key: "", Value: "http://x", File: f.File}}
	d.Services = []Service{{Kind: "pod", Name: "", File: f.File}}
	d.Stats = []Stats{{Language: "go", Refs: -1}, {Language: "go"}}
	err := d.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{
		`$.routes[1].id: duplicate "r1"`, "$.routes[1].path: must start with /", "$.routes[1].method: must be upper case",
		`$.routes[1].handler: unknown function "nope"`, `$.routes[1].precision: unknown precision "maybe"`,
		`$.http_refs[0].from: unknown function "nope"`, "$.http_refs[0].path: must start with /", "$.http_refs[0].method: must be upper case",
		"$.config_urls[0]: key and value are required", `$.services[0].kind: want deployment or service, got "pod"`, "$.services[0].name: required",
		"$.stats[0]: counts must be >= 0", `$.stats[1].language: duplicate "go"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "$.routes[0]") {
		t.Errorf("valid route flagged: %v", err)
	}
}

func TestDecodeRejectsUnknownFieldsInVersion2(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"facts_version":2,"producer":{"name":"x"},"routes":[{"id":"r","path":"/","file":"a","verb":"GET"}]}`))
	if err == nil || !strings.Contains(err.Error(), "verb") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}
