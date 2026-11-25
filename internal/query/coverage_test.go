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
