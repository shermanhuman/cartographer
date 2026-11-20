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

	Functions       int `json:"functions"`
	FunctionsLinked int `json:"functions_linked"`

	// Note explains a row with no call figures: "no extractor", or "not
	// resolved by design" for an inventory-only extractor (JavaScript).
	Note string `json:"note,omitempty"`
}

// Languages and the extensions that count as theirs.
var languageExts = map[string][]string{
	"go":         {".go"},
	"rust":       {".rs"},
	"elixir":     {".ex", ".exs"},
	"javascript": {".js", ".mjs", ".cjs", ".jsx"},
	"typescript": {".ts", ".tsx"},
	"python":     {".py"},
}

// inventoryOnly languages have an extractor that lists files and functions
// but resolves no calls.
var inventoryOnly = map[string]bool{"javascript": true}

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
		if lang, ok := extLang[ext]; ok {
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
		r := row(repo, lang)
		r.CallSites, r.InRepo, r.Dispatch, r.External, r.Unresolved, r.Refs, r.NotExtracted = s.CallSites, s.InRepo, s.Dispatch, s.External, s.Unresolved, s.Refs, s.NotExtracted
		withStats[key{repo, lang}] = true
	}
	rows.Close()

	// Functions, and those with a resolved edge either way, by the language
	// of their file.
	rows, err = db.QueryContext(ctx, `
	  SELECT f.repo, f.file, EXISTS(SELECT 1 FROM edges e WHERE e.snapshot_id = f.snapshot_id AND (e.from_id = f.id OR e.to_id = f.id))
	    FROM functions f WHERE f.snapshot_id = ?`, q.Snapshot)
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
		if !ok || withStats[key{repo, lang}] || inventoryOnly[lang] {
			continue
		}
		r := row(repo, lang)
		r.CallSites += total
		r.InRepo += resolved
		r.Unresolved += total - resolved
		counted[key{repo, lang}] = true
	}
	rows.Close()

	var out []Coverage
	for k, r := range rowsBy {
		if r.FilesTracked == 0 && r.FilesExtracted == 0 {
			continue
		}
		switch {
		case withStats[k], counted[k]:
		case r.FilesExtracted == 0:
			r.Note = "no extractor"
		case inventoryOnly[k.lang]:
			r.Note = "not resolved by design"
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

// Line renders a coverage row for status: files, call sites in the repo,
// functions linked.
func (c Coverage) Line() string {
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
