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
	d := &Doc{FactsVersion: 2, Producer: Producer{Name: "x", CallPrecision: "maybe"}}
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "$.facts_version") || !strings.Contains(err.Error(), "call_precision") {
		t.Fatalf("got %v", err)
	}
}
