package query

import (
	"context"
	"strings"
	"testing"
)

// Coverage counts linked functions with one pass over the edges, not a
// correlated subquery per function, and counts them right.
func TestCoverageLinkedFunctionsInOnePass(t *testing.T) {
	q, ctx := newQV2(t), context.Background()
	rows, err := q.S.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+coverageFunctionsSQL, q.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	rows.Close()
	if p := strings.Join(plan, "\n"); strings.Contains(p, "CORRELATED") {
		t.Errorf("coverage plan has a correlated subquery:\n%s", p)
	}
	cov, err := q.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var want, got int
	q.S.DB.QueryRowContext(ctx, `SELECT count(*) FROM functions f WHERE f.snapshot_id = ?1 AND EXISTS(SELECT 1 FROM edges e WHERE e.snapshot_id = ?1 AND (e.from_id = f.id OR e.to_id = f.id))`, q.Snapshot).Scan(&want)
	for _, c := range cov {
		got += c.FunctionsLinked
	}
	if want == 0 || got != want {
		t.Errorf("%d functions linked, want %d", got, want)
	}
}

// An inventory extension in capitals counts as its language, as the
// extracted file does.
func TestCoverageInventoryExtensionsIgnoreCase(t *testing.T) {
	q, ctx := newQV2(t), context.Background()
	var repo string
	q.S.DB.QueryRowContext(ctx, `SELECT repo FROM files WHERE snapshot_id = ? LIMIT 1`, q.Snapshot).Scan(&repo)
	if _, err := q.S.DB.ExecContext(ctx, `INSERT INTO inventory(snapshot_id, repo, ext, files) VALUES (?, ?, '.JS', 1)`, q.Snapshot, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := q.S.DB.ExecContext(ctx, `INSERT INTO files(snapshot_id, repo, path, language, category) VALUES (?, ?, 'assets/APP.JS', 'javascript', 'source')`, q.Snapshot, repo); err != nil {
		t.Fatal(err)
	}
	cov, err := q.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cov {
		if c.Repo == repo && c.Language == "javascript" && (c.FilesTracked != 1 || c.FilesExtracted != 1) {
			t.Errorf("javascript in %s: %d/%d files, want 1/1", repo, c.FilesExtracted, c.FilesTracked)
		}
	}
}

// status says when coverage can't be computed instead of leaving it out.
func TestStatusSaysCoverageUnavailable(t *testing.T) {
	q, ctx := newQV2(t), context.Background()
	if _, err := q.S.DB.ExecContext(ctx, `ALTER TABLE inventory RENAME TO inventory_gone`); err != nil {
		t.Fatal(err)
	}
	d, err := q.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.Body, "\n"); !strings.Contains(got, "coverage unavailable: ") {
		t.Errorf("status:\n%s", got)
	}
}

func TestHTTPCoverageLineShowsExternal(t *testing.T) {
	c := Coverage{Repo: "admin", Language: "http", HTTPRefs: 1, HTTPLinked: 0, HTTPExternal: 34}
	if got := c.Line(); !strings.Contains(got, "0 of 1 requests linked") || !strings.Contains(got, "external 34") {
		t.Errorf("line = %q", got)
	}
}

func TestElixirLineShowsDynamic(t *testing.T) {
	c := Coverage{Repo: "admin", Language: "elixir", FilesExtracted: 1, FilesTracked: 1, CallSites: 10, InRepo: 2, External: 3, Unresolved: 1, Dynamic: 4}
	if got := c.Line(); !strings.Contains(got, "external 3 · unresolved 1 · dynamic 4") {
		t.Errorf("line = %q", got)
	}
}

// Decision 22: Elixir dynamic calls are their own count, out of unresolved.
func TestCoverageDynamicLeavesUnresolved(t *testing.T) {
	q, ctx := newQV2(t), context.Background()
	db := q.S.DB
	if _, err := db.ExecContext(ctx, `INSERT INTO stats (snapshot_id, repo, language, call_sites, in_repo, dispatch, external, unresolved, refs, not_extracted) VALUES (?, 'dynrepo', 'elixir', 10, 2, 0, 3, 6, 0, 0)`, q.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO files (snapshot_id, repo, path) VALUES (?, 'dynrepo', 'lib/a.ex')`, q.Snapshot); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"lib/a.ex", "test/b.exs", "lib/c.html.heex", "assets/c.js"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO calls (snapshot_id, from_id, kind, repo, file, line) VALUES (?, 'dynrepo:M.f/0', 'dynamic', 'dynrepo', ?, 1)`, q.Snapshot, f); err != nil {
			t.Fatal(err)
		}
	}
	cov, err := q.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cov {
		if c.Repo == "dynrepo" && c.Language == "elixir" {
			if c.Dynamic != 3 || c.Unresolved != 3 {
				t.Errorf("dynamic %d unresolved %d, want 3 and 3", c.Dynamic, c.Unresolved)
			}
			return
		}
	}
	t.Fatal("no dynrepo elixir row")
}

// HTTP coverage counts external refs apart, out of refs and linked.
func TestCoverageHTTPExternal(t *testing.T) {
	q, ctx := newQV2(t), context.Background()
	db := q.S.DB
	for i, origin := range []string{"external", "external", "", "same"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO http_refs (snapshot_id, repo, from_id, method, path, base, origin, file, line) VALUES (?, 'httprepo', 'httprepo:M.f/0', 'GET', '/x', '', ?, 'm.ex', ?)`, q.Snapshot, origin, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO http_links (snapshot_id, from_id, route_id, repo, file, line, precision) VALUES (?, 'httprepo:M.f/0', 'r', 'httprepo', 'm.ex', 3, 'candidate')`, q.Snapshot); err != nil {
		t.Fatal(err)
	}
	cov, err := q.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cov {
		if c.Repo == "httprepo" && c.Language == "http" {
			if c.HTTPRefs != 2 || c.HTTPLinked != 1 || c.HTTPExternal != 2 {
				t.Errorf("refs %d linked %d external %d, want 2, 1 and 2", c.HTTPRefs, c.HTTPLinked, c.HTTPExternal)
			}
			return
		}
	}
	t.Fatal("no httprepo http row")
}
