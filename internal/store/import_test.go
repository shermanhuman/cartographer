package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const mini = "../../testdata/workspace-mini/snapshot"

func imported(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id, err := s.Import(context.Background(), mini)
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func count(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestImportCounts(t *testing.T) {
	s, id := imported(t)
	if id != "snapshot-20261015T120000Z" {
		t.Fatalf("id = %q", id)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM repos`:        2,
		`SELECT count(*) FROM modules`:      5,
		`SELECT count(*) FROM functions`:    7,
		`SELECT count(*) FROM calls`:        9,
		`SELECT count(*) FROM db_tables`:    2,
		`SELECT count(*) FROM foreign_keys`: 1,
		`SELECT count(*) FROM table_refs`:   2,
		`SELECT count(*) FROM notes`:        1,
		`SELECT count(*) FROM docs`:         1,
		`SELECT count(*) FROM annotations`:  1,
	} {
		if got := count(t, s, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
}

func TestCallResolution(t *testing.T) {
	s, _ := imported(t)
	cases := []struct {
		from, target, wantTo, wantPrecision string
	}{
		// default args: create/2 has min_arity 1, so create/1 resolves
		{"shop-app:ShopWeb.OrderController.create/2", "Shop.Orders.create/1", "shop-app:Shop.Orders.create/2", "precise"},
		// local call without module resolves within the caller's module (target module given here)
		{"shop-app:Shop.Orders.create/2", "Shop.Orders.validate/1", "shop-app:Shop.Orders.validate/1", "precise"},
		// cross-repo: only a workspace-wide unique match, so heuristic
		{"shop-worker:Worker.Nightly.run/0", "Shop.Billing.charge/2", "shop-app:Shop.Billing.charge/2", "heuristic"},
		// external and dynamic stay unresolved
		{"shop-app:Shop.Billing.charge/2", "Stripe.charge/1", "", ""},
		{"shop-app:Shop.Billing.charge/2", "apply/3", "", ""},
	}
	for _, c := range cases {
		var to, precision string
		err := s.DB.QueryRow(`SELECT coalesce(to_id,''), precision FROM calls WHERE from_id = ? AND target = ? LIMIT 1`, c.from, c.target).Scan(&to, &precision)
		if err != nil {
			t.Fatalf("%s -> %s: %v", c.from, c.target, err)
		}
		if to != c.wantTo || precision != c.wantPrecision {
			t.Errorf("%s -> %s = (%q,%q), want (%q,%q)", c.from, c.target, to, precision, c.wantTo, c.wantPrecision)
		}
	}
	// two call sites to validate/1 stay two rows
	if n := count(t, s, `SELECT count(*) FROM calls WHERE to_id = 'shop-app:Shop.Orders.validate/1'`); n != 2 {
		t.Errorf("validate/1 call sites = %d, want 2", n)
	}
}

func TestContexts(t *testing.T) {
	s, _ := imported(t)
	var ctx string
	s.DB.QueryRow(`SELECT context FROM modules WHERE id = 'shop-app:Shop.Orders'`).Scan(&ctx)
	if ctx != "ctx:shop-app:Shop.Orders" {
		t.Errorf("context = %q", ctx)
	}
	if n := count(t, s, `SELECT count(*) FROM contexts WHERE repo = 'shop-worker'`); n != 1 {
		t.Errorf("shop-worker contexts = %d", n)
	}
}

func TestDocLinks(t *testing.T) {
	s, _ := imported(t)
	want := []string{
		"shop-app:Shop.Orders.create/2",
		"shop-app:Shop.Orders",
		"shop-app:Shop.Billing",
		"file:shop-app:lib/shop_web/order_controller.ex",
	}
	for _, id := range want {
		if n := count(t, s, `SELECT count(*) FROM doc_refs WHERE entity_id = ?`, id); n == 0 {
			t.Errorf("no doc_ref to %s", id)
		}
	}
	// Shop.Orders must not be matched inside Shop.OrdersTest
	if n := count(t, s, `SELECT count(*) FROM doc_refs WHERE entity_id = 'shop-app:Shop.OrdersTest'`); n != 0 {
		t.Errorf("unexpected doc_ref to Shop.OrdersTest")
	}
}

func TestAnnotationFreshness(t *testing.T) {
	s, _ := imported(t)
	var status, stepsJSON string
	if err := s.DB.QueryRow(`SELECT status, steps FROM annotations WHERE id = 'checkout'`).Scan(&status, &stepsJSON); err != nil {
		t.Fatal(err)
	}
	var steps []StepStatus
	json.Unmarshal([]byte(stepsJSON), &steps)
	if status != "needs_review" {
		t.Errorf("status = %q", status)
	}
	if steps[0].Status != "needs_review" || len(steps[0].Reasons) != 1 || steps[0].Reasons[0] != "changed: shop-app:Shop.Orders.create/2" {
		t.Errorf("place step = %+v", steps[0])
	}
	if steps[1].Status != "unreviewed" {
		t.Errorf("charge step = %+v", steps[1])
	}
}

func TestSearchIndexAndVocab(t *testing.T) {
	s, _ := imported(t)
	if n := count(t, s, `SELECT count(*) FROM entities WHERE entities MATCH 'billing'`); n < 2 {
		t.Errorf("billing matches = %d", n)
	}
	if n := count(t, s, `SELECT count FROM vocab WHERE word = 'orders'`); n < 3 {
		t.Errorf("vocab orders = %d", n)
	}
}

func TestReimportReplacesAndPrune(t *testing.T) {
	s, id := imported(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, mini); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM functions`); n != 7 {
		t.Errorf("functions after reimport = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM entities WHERE snapshot_id = ?`, id); n == 0 {
		t.Fatal("no entities")
	}
	if err := s.Prune(ctx, 0); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"snapshots", "functions", "calls", "entities", "vocab"} {
		if n := count(t, s, `SELECT count(*) FROM `+tbl); n != 0 {
			t.Errorf("%s not pruned: %d", tbl, n)
		}
	}
	if _, err := s.Latest(ctx); err != ErrNoSnapshot {
		t.Errorf("Latest after prune = %v", err)
	}
}

func TestAnnotationExtraFieldsPreserved(t *testing.T) {
	s, _ := imported(t)
	var body string
	s.DB.QueryRow(`SELECT body FROM annotations WHERE id = 'checkout'`).Scan(&body)
	for _, want := range []string{`"groups"`, `"outcome"`, `"An order row exists."`} {
		if !strings.Contains(body, want) {
			t.Errorf("annotation body lost %s: %s", want, body)
		}
	}
}
