package query

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Coverage is how much of one language in one repo the map covers: files
// extracted out of the files tracked with that language's extensions (from
// the manifest's inventory, so no checkout is needed), call sites by outcome
// (from the extractor's stats), and functions with at least one resolved
// edge in or out.
type Coverage struct {
	Repo     string `json:"repo"`
	Language string `json:"language"`

	FilesExtracted int `json:"files_extracted"`
	FilesTracked   int `json:"files_tracked"`

	CallSites    int `json:"call_sites"`
	InRepo       int `json:"in_repo"`
	Dispatch     int `json:"dispatch"`
	External     int `json:"external"`
	Unresolved   int `json:"unresolved"`
	Refs         int `json:"refs"`
	NotExtracted int `json:"not_extracted"`

	// Dynamic: Elixir calls through a value (fun.(x), apply/3,
	// mod.fun()), counted from the stored kind 'dynamic' rows and left out
	// of Unresolved (decision 22).
	Dynamic int `json:"dynamic,omitempty"`

	Functions       int `json:"functions"`
	FunctionsLinked int `json:"functions_linked"`

	// Language "events": LiveView event sends (phx-* attributes, JS.push,
	// hook pushEvent) linked to a handle_event, unlinked, and dynamic (a
	// name built at runtime); handle_event clauses that take a named event,
	// and those at least one send reaches.
	Sends          int `json:"sends,omitempty"`
	SendsLinked    int `json:"sends_linked,omitempty"`
	SendsDynamic   int `json:"sends_dynamic,omitempty"`
	Handlers       int `json:"handler_clauses,omitempty"`
	HandlersLinked int `json:"handler_clauses_linked,omitempty"`
	// HTTPExternal: refs to outside services (origin 'external'), left out
	// of HTTPRefs and HTTPLinked.
	HTTPExternal int `json:"http_external,omitempty"`
	// Language "http": requests with a path (refs), those linked to a
	// route, and requests with no path to link (NotExtracted).
	HTTPRefs   int `json:"http_refs,omitempty"`
	HTTPLinked int `json:"http_linked,omitempty"`

	// Note explains a row with no call figures: "no extractor".
	Note string `json:"note,omitempty"`
}

// Languages and the extensions that count as theirs.
var languageExts = map[string][]string{
	"go":         {".go"},
	"rust":       {".rs"},
	"elixir":     {".ex", ".exs"},
	"javascript": {".js", ".mjs", ".cjs", ".jsx"},
	"typescript": {".ts", ".tsx", ".mts", ".cts"},
	"python":     {".py"},
}

// coverageFunctionsSQL lists a snapshot's functions with whether any
// resolved edge starts or ends at each. The linked ids are gathered once: a
// correlated EXISTS over the edges view rebuilt the view per function
// (about 26 s on an 11k-function map).
const coverageFunctionsSQL = `
  WITH linked(id) AS (
    SELECT from_id FROM edges WHERE snapshot_id = ?1
    UNION
    SELECT to_id FROM edges WHERE snapshot_id = ?1)
  SELECT f.repo, f.file, f.id IN linked FROM functions f WHERE f.snapshot_id = ?1`

// Coverage computes one row per (repo, language) that the repo tracks files
// in, for the current snapshot. Snapshots without an inventory (format 1)
// fall back to the files the map knows.
func (q *Q) Coverage(ctx context.Context) ([]Coverage, error) {
	db := q.S.DB
	extLang := map[string]string{}
	for lang, exts := range languageExts {
		for _, e := range exts {
			extLang[e] = lang
		}
	}
	type key struct{ repo, lang string }
	rowsBy := map[key]*Coverage{}
	row := func(repo, lang string) *Coverage {
		k := key{repo, lang}
		if rowsBy[k] == nil {
			rowsBy[k] = &Coverage{Repo: repo, Language: lang}
		}
		return rowsBy[k]
	}

	rows, err := db.QueryContext(ctx, `SELECT repo, ext, files FROM inventory WHERE snapshot_id = ?`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	haveInventory := false
	for rows.Next() {
		var repo, ext string
		var n int
		if err := rows.Scan(&repo, &ext, &n); err != nil {
			rows.Close()
			return nil, err
		}
		haveInventory = true
		if lang, ok := extLang[strings.ToLower(ext)]; ok {
			row(repo, lang).FilesTracked += n
		}
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `SELECT repo, path FROM files WHERE snapshot_id = ?`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var repo, p string
		if err := rows.Scan(&repo, &p); err != nil {
			rows.Close()
			return nil, err
		}
		lang, ok := extLang[strings.ToLower(path.Ext(p))]
		if !ok {
			continue
		}
		r := row(repo, lang)
		r.FilesExtracted++
		if !haveInventory {
			r.FilesTracked++
		}
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `SELECT repo, language, call_sites, in_repo, dispatch, external, unresolved, refs, not_extracted FROM stats WHERE snapshot_id = ?`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	withStats, counted := map[key]bool{}, map[key]bool{}
	for rows.Next() {
		var repo, lang string
		var s Coverage
		if err := rows.Scan(&repo, &lang, &s.CallSites, &s.InRepo, &s.Dispatch, &s.External, &s.Unresolved, &s.Refs, &s.NotExtracted); err != nil {
			rows.Close()
			return nil, err
		}
		switch lang {
		case "heex":
			// Template sends: counted from the event tables below, with the
			// hooks' pushEvent sends and the linker's links.
			continue
		case "http":
			row(repo, "http").NotExtracted += s.NotExtracted
			continue
		}
		r := row(repo, lang)
		r.CallSites, r.InRepo, r.Dispatch, r.External, r.Unresolved, r.Refs, r.NotExtracted = s.CallSites, s.InRepo, s.Dispatch, s.External, s.Unresolved, s.Refs, s.NotExtracted
		withStats[key{repo, lang}] = true
	}
	rows.Close()

	// Functions, and those with a resolved edge either way, by the language
	// of their file.
	rows, err = db.QueryContext(ctx, coverageFunctionsSQL, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var repo, file string
		var linked bool
		if err := rows.Scan(&repo, &file, &linked); err != nil {
			rows.Close()
			return nil, err
		}
		lang, ok := extLang[strings.ToLower(path.Ext(file))]
		if !ok {
			continue
		}
		r := row(repo, lang)
		r.Functions++
		if linked {
			r.FunctionsLinked++
		}
	}
	rows.Close()

	// Without an extractor's stats (facts version 1), count its call sites
	// from the calls table: resolved ones as in the repo, the rest unresolved.
	rows, err = db.QueryContext(ctx, `
	  SELECT c.repo, f.file, count(c.to_id), count(*) FROM calls c
	    JOIN functions f ON f.snapshot_id = c.snapshot_id AND f.id = c.from_id
	   WHERE c.snapshot_id = ? GROUP BY c.repo, f.file`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var repo, file string
		var resolved, total int
		if err := rows.Scan(&repo, &file, &resolved, &total); err != nil {
			rows.Close()
			return nil, err
		}
		lang, ok := extLang[strings.ToLower(path.Ext(file))]
		if !ok || withStats[key{repo, lang}] {
			continue
		}
		r := row(repo, lang)
		r.CallSites += total
		r.InRepo += resolved
		r.Unresolved += total - resolved
		counted[key{repo, lang}] = true
	}
	rows.Close()

	// Elixir dynamic calls (in .ex, .exs and .heex files): their own count,
	// out of unresolved.
	rows, err = db.QueryContext(ctx, `
	  SELECT repo, count(*) FROM calls
	   WHERE snapshot_id = ? AND kind = 'dynamic'
	     AND (file LIKE '%.ex' OR file LIKE '%.exs' OR file LIKE '%.heex')
	   GROUP BY repo`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var repo string
		var n int
		if err := rows.Scan(&repo, &n); err != nil {
			rows.Close()
			return nil, err
		}
		r := row(repo, "elixir")
		r.Dynamic = n
		r.Unresolved = max(0, r.Unresolved-n)
	}
	rows.Close()

	if err := q.eventCoverage(ctx, func(repo string) *Coverage { return row(repo, "events") }); err != nil {
		return nil, err
	}
	if err := q.httpCoverage(ctx, func(repo string) *Coverage { return row(repo, "http") }); err != nil {
		return nil, err
	}

	var out []Coverage
	for k, r := range rowsBy {
		if r.FilesTracked == 0 && r.FilesExtracted == 0 && r.Sends == 0 && r.Handlers == 0 && r.HTTPRefs == 0 && r.HTTPExternal == 0 && r.NotExtracted == 0 {
			continue
		}
		switch {
		case withStats[k], counted[k], k.lang == "events", k.lang == "http":
		case r.FilesExtracted == 0:
			r.Note = "no extractor"
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Language < out[j].Language
	})
	return out, nil
}

// eventCoverage counts each repo's sends (linked when any event link starts
// at the send), and the handle_event clauses that take a named event
// (reached when a send of one of the clause's names links to its function:
// a fallback clause for the same name counts too).
func (q *Q) eventCoverage(ctx context.Context, row func(string) *Coverage) error {
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT s.repo, count(*), sum(s.dynamic),
	         sum(EXISTS (SELECT 1 FROM event_links l WHERE l.snapshot_id = s.snapshot_id AND l.from_id = s.from_id
	                     AND l.file = s.file AND l.line = s.line AND l.event = s.event))
	    FROM event_sends s WHERE s.snapshot_id = ? GROUP BY s.repo`, q.Snapshot)
	if err != nil {
		return err
	}
	for rows.Next() {
		var repo string
		var n, dyn, linked int
		if err := rows.Scan(&repo, &n, &dyn, &linked); err != nil {
			rows.Close()
			return err
		}
		r := row(repo)
		r.Sends, r.SendsDynamic, r.SendsLinked = n, dyn, linked
	}
	rows.Close()
	rows, err = q.S.DB.QueryContext(ctx, `
	  SELECT repo, count(*), sum(linked) FROM (
	    SELECT h.repo, h.function_id, h.line,
	           max(EXISTS (SELECT 1 FROM event_links l WHERE l.snapshot_id = h.snapshot_id AND l.to_id = h.function_id AND l.event = h.event)) AS linked
	      FROM event_handlers h WHERE h.snapshot_id = ? AND h.event != ''
	     GROUP BY h.repo, h.function_id, h.line)
	   GROUP BY repo`, q.Snapshot)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var repo string
		var n, linked int
		if err := rows.Scan(&repo, &n, &linked); err != nil {
			return err
		}
		r := row(repo)
		r.Handlers, r.HandlersLinked = n, linked
	}
	return rows.Err()
}

// httpCoverage counts each repo's HTTP refs, and those linked to a route.
func (q *Q) httpCoverage(ctx context.Context, row func(string) *Coverage) error {
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT r.repo, sum(r.origin != 'external'),
	         sum(r.origin != 'external' AND EXISTS (SELECT 1 FROM http_links l WHERE l.snapshot_id = r.snapshot_id AND l.from_id = r.from_id
	                     AND l.file = r.file AND l.line = r.line)),
	         sum(r.origin = 'external')
	    FROM http_refs r WHERE r.snapshot_id = ? GROUP BY r.repo`, q.Snapshot)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var repo string
		var n, linked, external int
		if err := rows.Scan(&repo, &n, &linked, &external); err != nil {
			return err
		}
		r := row(repo)
		r.HTTPRefs, r.HTTPLinked, r.HTTPExternal = n, linked, external
	}
	return rows.Err()
}

// Line renders a coverage row for status: files, call sites in the repo,
// functions linked; for "events", sends and handler clauses linked; for
// "http", requests linked to a route.
func (c Coverage) Line() string {
	switch c.Language {
	case "events":
		return fmt.Sprintf("%-26s %-10s %d of %d sends linked (%s) · dynamic %d · unlinked %d · %d of %d handle_event clauses reached (%s)",
			c.Repo, c.Language, c.SendsLinked, c.Sends, pct(c.SendsLinked, c.Sends), c.SendsDynamic, c.Sends-c.SendsLinked-c.SendsDynamic,
			c.HandlersLinked, c.Handlers, pct(c.HandlersLinked, c.Handlers))
	case "http":
		s := fmt.Sprintf("%-26s %-10s %d of %d requests linked to a route (%s)", c.Repo, c.Language, c.HTTPLinked, c.HTTPRefs, pct(c.HTTPLinked, c.HTTPRefs))
		if c.HTTPExternal > 0 {
			s += fmt.Sprintf(" · external %d", c.HTTPExternal)
		}
		if c.NotExtracted > 0 {
			s += fmt.Sprintf(" · no path %d", c.NotExtracted)
		}
		return s
	}
	files := fmt.Sprintf("%d/%d files", c.FilesExtracted, c.FilesTracked)
	if c.Note != "" {
		return fmt.Sprintf("%-26s %-10s %-14s %s", c.Repo, c.Language, files, c.Note)
	}
	calls := "no call sites"
	if c.CallSites > 0 {
		calls = fmt.Sprintf("%d of %d call sites in repo (%s)", c.InRepo, c.CallSites, pct(c.InRepo, c.CallSites))
		var extra []string
		if c.Dispatch > 0 {
			extra = append(extra, fmt.Sprintf("dispatch %d", c.Dispatch))
		}
		if c.External > 0 {
			extra = append(extra, fmt.Sprintf("external %d", c.External))
		}
		if c.Unresolved > 0 {
			extra = append(extra, fmt.Sprintf("unresolved %d", c.Unresolved))
		}
		if c.Dynamic > 0 {
			extra = append(extra, fmt.Sprintf("dynamic %d", c.Dynamic))
		}
		if c.NotExtracted > 0 {
			extra = append(extra, fmt.Sprintf("not extracted %d", c.NotExtracted))
		}
		if len(extra) > 0 {
			calls += " · " + strings.Join(extra, " · ")
		}
	}
	return fmt.Sprintf("%-26s %-10s %-14s %s · %s of functions linked", c.Repo, c.Language, files, calls, pct(c.FunctionsLinked, c.Functions))
}

func pct(n, d int) string {
	if d == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(d))
}
