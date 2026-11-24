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
	d := &Doc{FactsVersion: 4, Producer: Producer{Name: "x", CallPrecision: "maybe"}}
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

// Version 3: events, handlers and hook mounts name functions in the
// document, keep their shapes, and need facts_version 3, as does origin.
func TestVersion3EventsValidated(t *testing.T) {
	base := func() *Doc {
		return &Doc{FactsVersion: 3, Producer: Producer{Name: "x"},
			Functions: []Function{{ID: "r:M.render/1", Name: "render", Arity: 1, MinArity: 1, File: "m.ex"}, {ID: "r:M.handle_event/3", Name: "handle_event", Arity: 3, MinArity: 3, File: "m.ex"}}}
	}
	ok := base()
	ok.Events = []Event{{From: "r:M.render/1", Event: "save", Via: "phx-click", To: []string{"r:M.handle_event/3"}, File: "m.ex", Line: 3, Precision: Precise},
		{From: "r:M.render/1", Dynamic: true, Via: "phx-click", File: "m.ex", Line: 4}}
	ok.EventHandlers = []EventHandler{{Function: "r:M.handle_event/3", Event: "save", Source: "head", Line: 9},
		{Function: "r:M.handle_event/3", Source: "delegate", Delegate: "r:M.render/1", Line: 12}}
	ok.HookMounts = []HookMount{{Hook: "Pad", From: "r:M.render/1", Receivers: []string{"r:M.handle_event/3"}, File: "m.ex", Line: 5}}
	ok.HTTPRefs = []HTTPRef{{From: "r:M.render/1", Path: "/x", Origin: "same", File: "m.ex"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}

	bad := base()
	bad.Events = []Event{{From: "r:Nope.f/0", Via: "", Event: "x", Dynamic: true, Target: "else", To: []string{"r:Gone/1"}, File: "m.ex", Precision: "maybe"}}
	bad.EventHandlers = []EventHandler{{Function: "r:M.handle_event/3", Source: "delegate", Event: "x", Line: 0}, {Function: "r:M.handle_event/3", Source: "head", Line: 2}, {Function: "r:M.handle_event/3", Source: "delegate", Delegate: "r:Gone/3", Line: 3}}
	bad.HookMounts = []HookMount{{From: "r:M.render/1", Receivers: []string{"r:Gone/3"}, File: "m.ex"}}
	bad.HTTPRefs = []HTTPRef{{From: "r:M.render/1", Path: "/x", Origin: "other", File: "m.ex"}, {From: "r:M.render/1", Path: "/x", Origin: "same", Base: "API", File: "m.ex"}}
	err := bad.Validate()
	for _, want := range []string{
		`$.events[0].from: unknown function "r:Nope.f/0"`, `$.events[0].to[0]: unknown function "r:Gone/1"`, `$.events[0].via: required`,
		`$.events[0]: an event has a name or is dynamic`, `$.events[0].target: unknown target "else"`, `$.events[0].precision: unknown precision "maybe"`,
		`$.event_handlers[0]: a delegate names the delegate and no event`, `$.event_handlers[0].line: must be > 0`,
		`$.event_handlers[1]: a head handler names an event and no delegate`, `$.event_handlers[2].delegate: unknown function "r:Gone/3"`,
		`$.hook_mounts[0].hook: required`, `$.hook_mounts[0].receivers[0]: unknown function "r:Gone/3"`,
		`$.http_refs[0].origin: want "same" or none, got "other"`, `$.http_refs[1]: a same-origin ref has no base`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}

	old := base()
	old.FactsVersion = 2
	old.HookMounts = ok.HookMounts
	old.HTTPRefs = ok.HTTPRefs
	if err := old.Validate(); err == nil || !strings.Contains(err.Error(), "$.hook_mounts: needs facts_version 3") || !strings.Contains(err.Error(), "$.http_refs[0].origin: needs facts_version 3") {
		t.Fatalf("version 2 with version 3 fields: %v", err)
	}
}
