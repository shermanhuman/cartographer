// Package facts defines the facts interchange format that extractor plugins
// emit, one document per repository (or one for a database schema). Version 2
// adds routes, HTTP references, config URLs, services, per-language stats and
// test functions; version 3 adds browser events (events, event_handlers,
// hook_mounts) and same-origin HTTP references. Older documents are still
// read. See docs/facts-format.md.
package facts

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Version is the facts format version this package writes; MinVersion is the
// oldest it reads. A document may not carry a later version's sections.
const (
	Version    = 3
	MinVersion = 1
)

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

	// Version 2.
	Routes     []Route     `json:"routes,omitempty"`
	HTTPRefs   []HTTPRef   `json:"http_refs,omitempty"`
	ConfigURLs []ConfigURL `json:"config_urls,omitempty"`
	Services   []Service   `json:"services,omitempty"`
	Stats      []Stats     `json:"stats,omitempty"`

	// Version 3.
	Events        []Event        `json:"events,omitempty"`
	EventHandlers []EventHandler `json:"event_handlers,omitempty"`
	HookMounts    []HookMount    `json:"hook_mounts,omitempty"`
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

// MaxArity bounds functions[].arity (Erlang's limit): the core expands
// min_arity..arity per function, so the range must stay small.
const MaxArity = 255

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
	// Test marks a test function (version 2): a Rust #[test] or a function in
	// a #[cfg(test)] module, where the file category can't say so.
	Test bool `json:"test,omitempty"`
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

// Route is an HTTP endpoint a repository serves (version 2). Handler is the
// function the router dispatches to, when the extractor could name it.
type Route struct {
	ID        string `json:"id"`
	Method    string `json:"method,omitempty"` // GET, POST, ...; "" for any
	Path      string `json:"path"`             // as registered, e.g. /api/v1/x/:id
	Handler   string `json:"handler,omitempty"`
	File      string `json:"file"`
	Line      int    `json:"line,omitempty"`
	Precision string `json:"precision,omitempty"` // of the route -> handler edge
}

// HTTPRef is a request a function builds to a path (version 2). Base names
// where the base URL comes from (a config key or env var, see ConfigURL), when
// the extractor knows; the core links refs to other repos' routes.
type HTTPRef struct {
	From   string `json:"from"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path"` // interpolations as :name
	Base   string `json:"base,omitempty"`
	// Origin "same" (version 3) marks a browser request with no host: it
	// goes to the server that served the page, the caller's own repo.
	Origin string `json:"origin,omitempty"`
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
}

// Event is one place that sends a LiveView event (version 3): a phx-*
// attribute or JS.push in a template, or a hook's pushEvent. To lists the
// handle_event functions the producer resolved it to; a hook's send names
// its Hooks instead and the core resolves it through hook_mounts.
type Event struct {
	From      string   `json:"from"`
	Event     string   `json:"event,omitempty"` // empty when Dynamic
	Dynamic   bool     `json:"dynamic,omitempty"`
	Via       string   `json:"via"`              // phx-click, JS.push, pushEvent, ...
	Target    string   `json:"target,omitempty"` // "", myself, dynamic
	Hooks     []string `json:"hooks,omitempty"`
	To        []string `json:"to,omitempty"`
	File      string   `json:"file"`
	Line      int      `json:"line,omitempty"`
	Precision string   `json:"precision,omitempty"`
}

// EventHandler is one event name a handle_event clause matches (version 3):
// its head, a guard list or a module attribute. A catch-all clause that hands
// the event on names its Delegate and no Event.
type EventHandler struct {
	Function string `json:"function"`
	Event    string `json:"event,omitempty"`
	Source   string `json:"source"` // head, guard, attribute, delegate
	Delegate string `json:"delegate,omitempty"`
	Line     int    `json:"line"`
}

// HookMount is one phx-hook in a template (version 3), with the handle_event
// functions that receive the hook's pushEvent there.
type HookMount struct {
	Hook      string   `json:"hook"`
	From      string   `json:"from"`
	Receivers []string `json:"receivers,omitempty"`
	File      string   `json:"file"`
	Line      int      `json:"line,omitempty"`
}

// ConfigURL is a URL found in a repository's configuration (version 2): a
// config file key or an environment variable default.
type ConfigURL struct {
	Key   string `json:"key"`   // e.g. cold.admin_url, env:BREAKDOWN_ADMIN_URL
	Value string `json:"value"` // the URL
	File  string `json:"file"`
	Line  int    `json:"line,omitempty"`
}

// Service is a deployable unit from manifests (version 2): a Kubernetes
// Deployment or Service.
type Service struct {
	Kind      string            `json:"kind"` // deployment | service
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Image     string            `json:"image,omitempty"`    // deployment
	Selector  map[string]string `json:"selector,omitempty"` // service
	Labels    map[string]string `json:"labels,omitempty"`   // deployment pod labels
	Env       map[string]string `json:"env,omitempty"`      // literal env values that are URLs
	File      string            `json:"file"`
	Line      int               `json:"line,omitempty"`
}

// Stats counts call sites by outcome for one language (version 2). A call
// site counts once however many edges it gets.
type Stats struct {
	Language     string `json:"language"`
	CallSites    int    `json:"call_sites"`
	InRepo       int    `json:"in_repo"`
	Dispatch     int    `json:"dispatch"`
	External     int    `json:"external"`
	Unresolved   int    `json:"unresolved"`
	Refs         int    `json:"refs"`
	NotExtracted int    `json:"not_extracted"`
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

// Version2Sections are the document sections a version 1 document may
// not carry (with functions[].test).
var Version2Sections = []string{"routes", "http_refs", "config_urls", "services", "stats"}

// Version3Sections are the sections a version 1 or 2 document may not carry
// (with http_refs[].origin).
var Version3Sections = []string{"events", "event_handlers", "hook_mounts"}

// Validate checks required fields, id uniqueness and internal references.
func (d *Doc) Validate() error {
	var p []string
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	if d.FactsVersion < MinVersion || d.FactsVersion > Version {
		add("$.facts_version: want %d to %d, got %d", MinVersion, Version, d.FactsVersion)
	}
	if d.FactsVersion == 1 {
		counts := []int{len(d.Routes), len(d.HTTPRefs), len(d.ConfigURLs), len(d.Services), len(d.Stats)}
		for i, name := range Version2Sections { // in order: errors read the same every time
			if counts[i] > 0 {
				add("$.%s: needs facts_version 2", name)
			}
		}
		for i, f := range d.Functions {
			if f.Test {
				add("$.functions[%d].test: needs facts_version 2", i)
			}
		}
	}
	if d.FactsVersion < 3 {
		counts := []int{len(d.Events), len(d.EventHandlers), len(d.HookMounts)}
		for i, name := range Version3Sections {
			if counts[i] > 0 {
				add("$.%s: needs facts_version 3", name)
			}
		}
		for i, r := range d.HTTPRefs {
			if r.Origin != "" {
				add("$.http_refs[%d].origin: needs facts_version 3", i)
			}
		}
	}
	precision := func(v, where string) {
		switch v {
		case "", Precise, Heuristic, Candidate:
		default:
			add("%s: unknown precision %q", where, v)
		}
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
		if f.Arity < 0 || f.Arity > MaxArity {
			add("%s.arity: must be between 0 and %d", w, MaxArity)
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
		precision(c.Precision, w+".precision")
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

	routes := map[string]bool{}
	for i, r := range d.Routes {
		w := fmt.Sprintf("$.routes[%d]", i)
		if r.ID == "" {
			add("%s.id: required", w)
		}
		if routes[r.ID] {
			add("%s.id: duplicate %q", w, r.ID)
		}
		routes[r.ID] = true
		if !strings.HasPrefix(r.Path, "/") {
			add("%s.path: must start with /", w)
		}
		if r.Method != "" && r.Method != strings.ToUpper(r.Method) {
			add("%s.method: must be upper case", w)
		}
		if r.Handler != "" && !funcs[r.Handler] {
			add("%s.handler: unknown function %q", w, r.Handler)
		}
		checkFile(r.File, w+".file")
		precision(r.Precision, w+".precision")
	}
	for i, r := range d.HTTPRefs {
		w := fmt.Sprintf("$.http_refs[%d]", i)
		if !funcs[r.From] {
			add("%s.from: unknown function %q", w, r.From)
		}
		if !strings.HasPrefix(r.Path, "/") {
			add("%s.path: must start with /", w)
		}
		if r.Method != "" && r.Method != strings.ToUpper(r.Method) {
			add("%s.method: must be upper case", w)
		}
		switch {
		case r.Origin == "":
		case r.Origin != "same":
			add("%s.origin: want \"same\" or none, got %q", w, r.Origin)
		case r.Base != "":
			add("%s: a same-origin ref has no base", w)
		}
		checkFile(r.File, w+".file")
	}
	fn := func(id, where string) {
		if !funcs[id] {
			add("%s: unknown function %q", where, id)
		}
	}
	for i, e := range d.Events {
		w := fmt.Sprintf("$.events[%d]", i)
		fn(e.From, w+".from")
		for j, to := range e.To {
			fn(to, fmt.Sprintf("%s.to[%d]", w, j))
		}
		if e.Via == "" {
			add("%s.via: required", w)
		}
		if (e.Event == "") != e.Dynamic {
			add("%s: an event has a name or is dynamic, not both or neither", w)
		}
		switch e.Target {
		case "", "myself", "dynamic":
		default:
			add("%s.target: unknown target %q", w, e.Target)
		}
		checkFile(e.File, w+".file")
		precision(e.Precision, w+".precision")
	}
	for i, h := range d.EventHandlers {
		w := fmt.Sprintf("$.event_handlers[%d]", i)
		fn(h.Function, w+".function")
		switch h.Source {
		case "head", "guard", "attribute":
			if h.Event == "" || h.Delegate != "" {
				add("%s: a %s handler names an event and no delegate", w, h.Source)
			}
		case "delegate":
			if h.Event != "" || h.Delegate == "" {
				add("%s: a delegate names the delegate and no event", w)
			} else {
				fn(h.Delegate, w+".delegate")
			}
		default:
			add("%s.source: unknown source %q", w, h.Source)
		}
		if h.Line <= 0 {
			add("%s.line: must be > 0", w)
		}
	}
	for i, m := range d.HookMounts {
		w := fmt.Sprintf("$.hook_mounts[%d]", i)
		if m.Hook == "" {
			add("%s.hook: required", w)
		}
		fn(m.From, w+".from")
		for j, r := range m.Receivers {
			fn(r, fmt.Sprintf("%s.receivers[%d]", w, j))
		}
		checkFile(m.File, w+".file")
	}
	for i, c := range d.ConfigURLs {
		w := fmt.Sprintf("$.config_urls[%d]", i)
		if c.Key == "" || c.Value == "" {
			add("%s: key and value are required", w)
		}
		checkFile(c.File, w+".file")
	}
	for i, s := range d.Services {
		w := fmt.Sprintf("$.services[%d]", i)
		if s.Kind != "deployment" && s.Kind != "service" {
			add("%s.kind: want deployment or service, got %q", w, s.Kind)
		}
		if s.Name == "" {
			add("%s.name: required", w)
		}
		checkFile(s.File, w+".file")
	}
	langs := map[string]bool{}
	for i, s := range d.Stats {
		w := fmt.Sprintf("$.stats[%d]", i)
		if s.Language == "" {
			add("%s.language: required", w)
		}
		if langs[s.Language] {
			add("%s.language: duplicate %q", w, s.Language)
		}
		langs[s.Language] = true
		for _, n := range []int{s.CallSites, s.InRepo, s.Dispatch, s.External, s.Unresolved, s.Refs, s.NotExtracted} {
			if n < 0 {
				add("%s: counts must be >= 0", w)
				break
			}
		}
	}

	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}
