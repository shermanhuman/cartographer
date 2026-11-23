package facts

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// schemaNode is the part of a JSON Schema these checks read.
type schemaNode struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Items      json.RawMessage            `json:"items"`
	Pattern    string                     `json:"pattern"`
	If         json.RawMessage            `json:"if"`
	Then       *schemaNode                `json:"then"`
}

func readSchema(t *testing.T) *schemaNode {
	t.Helper()
	b, err := os.ReadFile("../../schema/facts.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var s schemaNode
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func node(t *testing.T, raw json.RawMessage, where string) *schemaNode {
	t.Helper()
	var n schemaNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	return &n
}

// The schema and the Doc structs name the same fields, all the way down.
func TestSchemaMatchesStructs(t *testing.T) {
	var walk func(typ reflect.Type, n *schemaNode, where string)
	walk = func(typ reflect.Type, n *schemaNode, where string) {
		var fields []string
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			fields = append(fields, name)
			raw, ok := n.Properties[name]
			if !ok {
				t.Errorf("%s.%s: in the struct, not in the schema", where, name)
				continue
			}
			ft := f.Type
			sub := node(t, raw, where+"."+name)
			if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
				ft, sub = ft.Elem(), node(t, sub.Items, where+"."+name+"[]")
			}
			if ft.Kind() == reflect.Struct {
				walk(ft, sub, where+"."+name)
			}
		}
		var props []string
		for k := range n.Properties {
			if !slices.Contains(fields, k) {
				props = append(props, k)
			}
		}
		sort.Strings(props)
		for _, k := range props {
			t.Errorf("%s.%s: in the schema, not in the struct", where, k)
		}
	}
	walk(reflect.TypeFor[Doc](), readSchema(t), "$")
}

// The schema is as strict as Validate: a version 1 document carries no
// version 2 section, and route and reference paths start with /.
func TestSchemaIsAsStrictAsValidate(t *testing.T) {
	s := readSchema(t)
	if !strings.Contains(string(s.If), `"const": 1`) || s.Then == nil {
		t.Fatalf("no if facts_version 1 / then: %s", s.If)
	}
	for _, sec := range append(slices.Clone(Version2Sections), "functions") {
		raw, ok := s.Then.Properties[sec]
		if !ok {
			t.Errorf("then: %s not restricted for version 1", sec)
			continue
		}
		if sec == "functions" {
			if !strings.Contains(string(raw), `"test"`) {
				t.Errorf("then: functions[].test allowed in version 1: %s", raw)
			}
		} else if string(raw) != "false" {
			t.Errorf("then: %s is %s, want false", sec, raw)
		}
	}
	for _, sec := range []string{"routes", "http_refs"} {
		items := node(t, node(t, s.Properties[sec], sec).Items, sec)
		if p := node(t, items.Properties["path"], sec+".path").Pattern; p != "^/" {
			t.Errorf("%s[].path pattern %q, want ^/", sec, p)
		}
	}
	// Validate reports version 1 problems in a fixed order.
	d := &Doc{FactsVersion: 1, Producer: Producer{Name: "x"}, Routes: []Route{{ID: "r", Path: "/", File: "f"}}, HTTPRefs: []HTTPRef{{From: "f", Path: "/", File: "f"}}, ConfigURLs: []ConfigURL{{Key: "k", Value: "http://x", File: "f"}}, Services: []Service{{Kind: "service", Name: "s", File: "f"}}, Stats: []Stats{{Language: "go"}}}
	first := d.Validate().Error()
	for range 20 {
		if got := d.Validate().Error(); got != first {
			t.Fatalf("Validate's order changed:\n%s\nthen\n%s", first, got)
		}
	}
}
