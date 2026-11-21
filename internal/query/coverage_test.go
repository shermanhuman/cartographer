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
