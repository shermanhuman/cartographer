// Package facts defines the facts v1 interchange format that extractor
// plugins emit, one document per repository (or one for a database schema).
// See docs/facts-format.md.
package facts

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Version is the facts format version this package reads and writes.
const Version = 1

// Precision says how an edge was established.
const (
	Precise   = "precise"   // compiler/AST-grade resolution
	Heuristic = "heuristic" // name-matched, may be wrong
	Candidate = "candidate" // textual co-occurrence only
)

// Doc is one plugin output.
type Doc struct {
	FactsVersion int        `json:"facts_version"`
	Producer     Producer   `json:"producer"`
	Repo         string     `json:"repo,omitempty"`
	Revision     string     `json:"revision,omitempty"`
	Files        []File     `json:"files,omitempty"`
	Modules      []Module   `json:"modules,omitempty"`
	Functions    []Function `json:"functions,omitempty"`
	Calls        []Call     `json:"calls,omitempty"`
	Tables       []Table    `json:"tables,omitempty"`
	ForeignKeys  []FK       `json:"foreign_keys,omitempty"`
	TableRefs    []TableRef `json:"table_refs,omitempty"`
	Notes        []Note     `json:"notes,omitempty"`
}

type Producer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// CallPrecision is the precision of calls this producer resolves
	// syntactically (e.g. "precise" for the Elixir AST plugin).
	CallPrecision string `json:"call_precision,omitempty"`
}

type File struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
	Category string `json:"category,omitempty"`
}

type Module struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Namespace []string `json:"namespace,omitempty"`
	File      string   `json:"file"`
	Line      int      `json:"line,omitempty"`
	Language  string   `json:"language,omitempty"`
}

type Function struct {
	ID       string `json:"id"`
	Module   string `json:"module,omitempty"` // module id
	Name     string `json:"name"`
	Arity    int    `json:"arity"`
	MinArity int    `json:"min_arity"`
	Kind     string `json:"kind,omitempty"` // def, defp, function, ...
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Clauses  []int  `json:"clauses,omitempty"`
	// Fingerprint is an opaque hash of the function's source; annotations
	// that cite the function go stale when it changes.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Call is one call site. Plugins either resolve To themselves or leave it
// empty and describe Target; the core resolves Target against all functions.
type Call struct {
	From      string  `json:"from"`
	To        string  `json:"to,omitempty"`
	Target    *Target `json:"target,omitempty"`
	Kind      string  `json:"kind,omitempty"` // remote, local, dynamic, ...
	File      string  `json:"file,omitempty"`
	Line      int     `json:"line,omitempty"`
	Precision string  `json:"precision,omitempty"`
}

type Target struct {
	Module string `json:"module,omitempty"` // module name, not id
	Name   string `json:"name"`
	Arity  int    `json:"arity"`
}

type Table struct {
	ID          string       `json:"id"` // e.g. public.users
	Schema      string       `json:"schema,omitempty"`
	Name        string       `json:"name"`
	Kind        string       `json:"kind,omitempty"` // table, partitioned_table, view, materialized_view
	Columns     []Column     `json:"columns,omitempty"`
	Constraints []Constraint `json:"constraints,omitempty"`
	Indexes     []string     `json:"indexes,omitempty"`
}

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
}

type Constraint struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Definition string `json:"definition,omitempty"`
}

type FK struct {
	Name        string   `json:"name,omitempty"`
	FromTable   string   `json:"from_table"`
	FromColumns []string `json:"from_columns"`
	ToTable     string   `json:"to_table"`
	ToColumns   []string `json:"to_columns"`
}

// TableRef is a textual mention of a table in source; always a candidate.
type TableRef struct {
	Table    string `json:"table"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function,omitempty"`
	Text     string `json:"text,omitempty"`
}

type Note struct {
	Kind    string `json:"kind"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// Decode reads one facts document, rejecting unknown fields.
func Decode(r io.Reader) (*Doc, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var d Doc
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("facts: decode: %w", err)
	}
	return &d, nil
}

// ValidationError lists every problem found, each prefixed with a JSON path.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "facts: invalid document:\n  " + strings.Join(e.Problems, "\n  ")
}

// Validate checks required fields, id uniqueness and internal references.
func (d *Doc) Validate() error {
	var p []string
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	if d.FactsVersion != Version {
		add("$.facts_version: want %d, got %d", Version, d.FactsVersion)
	}
	if d.Producer.Name == "" {
		add("$.producer.name: required")
	}
	switch d.Producer.CallPrecision {
	case "", Precise, Heuristic, Candidate:
	default:
		add("$.producer.call_precision: unknown precision %q", d.Producer.CallPrecision)
	}

	files := map[string]bool{}
	for i, f := range d.Files {
		if f.Path == "" {
			add("$.files[%d].path: required", i)
		}
		files[f.Path] = true
	}
	checkFile := func(path, where string) {
		if path == "" {
			add("%s: required", where)
		} else if len(d.Files) > 0 && !files[path] {
			add("%s: %q is not in $.files", where, path)
		}
	}

	modules := map[string]bool{}
	for i, m := range d.Modules {
		w := fmt.Sprintf("$.modules[%d]", i)
		if m.ID == "" || m.Name == "" {
			add("%s: id and name are required", w)
		}
		if modules[m.ID] {
			add("%s.id: duplicate %q", w, m.ID)
		}
		modules[m.ID] = true
		checkFile(m.File, w+".file")
	}

	funcs := map[string]bool{}
	for i, f := range d.Functions {
		w := fmt.Sprintf("$.functions[%d]", i)
		if f.ID == "" || f.Name == "" {
			add("%s: id and name are required", w)
		}
		if funcs[f.ID] {
			add("%s.id: duplicate %q", w, f.ID)
		}
		funcs[f.ID] = true
		if f.Module != "" && !modules[f.Module] {
			add("%s.module: unknown module %q", w, f.Module)
		}
		if f.MinArity > f.Arity || f.MinArity < 0 {
			add("%s.min_arity: must be between 0 and arity", w)
		}
		checkFile(f.File, w+".file")
	}

	for i, c := range d.Calls {
		w := fmt.Sprintf("$.calls[%d]", i)
		if !funcs[c.From] {
			add("%s.from: unknown function %q", w, c.From)
		}
		if c.To == "" && c.Target == nil {
			add("%s: one of to or target is required", w)
		}
		if c.To != "" && !funcs[c.To] {
			add("%s.to: unknown function %q", w, c.To)
		}
		switch c.Precision {
		case "", Precise, Heuristic, Candidate:
		default:
			add("%s.precision: unknown precision %q", w, c.Precision)
		}
	}

	tables := map[string]bool{}
	for i, t := range d.Tables {
		w := fmt.Sprintf("$.tables[%d]", i)
		if t.ID == "" || t.Name == "" {
			add("%s: id and name are required", w)
		}
		if tables[t.ID] {
			add("%s.id: duplicate %q", w, t.ID)
		}
		tables[t.ID] = true
	}
	for i, fk := range d.ForeignKeys {
		w := fmt.Sprintf("$.foreign_keys[%d]", i)
		if !tables[fk.FromTable] {
			add("%s.from_table: unknown table %q", w, fk.FromTable)
		}
		if !tables[fk.ToTable] {
			add("%s.to_table: unknown table %q", w, fk.ToTable)
		}
	}
	for i, r := range d.TableRefs {
		w := fmt.Sprintf("$.table_refs[%d]", i)
		if r.Table == "" {
			add("%s.table: required", w)
		}
		checkFile(r.File, w+".file")
		if r.Line <= 0 {
			add("%s.line: must be > 0", w)
		}
		if r.Function != "" && !funcs[r.Function] {
			add("%s.function: unknown function %q", w, r.Function)
		}
	}

	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}
