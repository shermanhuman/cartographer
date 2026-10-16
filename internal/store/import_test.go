package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/facts"
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

// Reimporting an id replaces it only within its workspace: another
// workspace's snapshot with the same id is never silently overwritten.
func TestImportRefusesOtherWorkspacesID(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.ImportBundle(ctx, emptyBundle("snapshot-X", "A", "2026-10-15T12:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportBundle(ctx, emptyBundle("snapshot-X", "A", "2026-10-15T12:00:00Z")); err != nil {
		t.Errorf("same workspace reimport: %v", err)
	}
	err = s.ImportBundle(ctx, emptyBundle("snapshot-X", "B", "2026-10-15T12:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), `belongs to workspace "A"`) {
		t.Errorf("cross-workspace replace: %v", err)
	}
	if all, _ := s.Snapshots(ctx); len(all) != 1 || all[0].Workspace != "A" {
		t.Errorf("snapshots = %+v", all)
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

// freshness imports the fixture with its annotations replaced by a and
// returns the annotation's overall status and step statuses.
func freshness(t *testing.T, a bundle.Annotation) (string, []StepStatus) {
	t.Helper()
	b, err := bundle.Read(mini)
	if err != nil {
		t.Fatal(err)
	}
	b.Annotations = []bundle.Annotation{a}
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.ImportBundle(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	var status, stepsJSON string
	if err := s.DB.QueryRow(`SELECT status, steps FROM annotations WHERE id = ?`, a.ID).Scan(&status, &stepsJSON); err != nil {
		t.Fatal(err)
	}
	var steps []StepStatus
	json.Unmarshal([]byte(stepsJSON), &steps)
	return status, steps
}

const (
	ctrl   = "shop-app:ShopWeb.OrderController.create/2" // fingerprint h1
	charge = "shop-app:Shop.Billing.charge/2"            // fingerprint h3
)

func TestAnnotationCiteAddedAfterReview(t *testing.T) {
	for _, stamp := range []map[string]string{
		{ctrl: "h1"},
		{ctrl: "h1", charge: ""},
	} {
		status, steps := freshness(t, bundle.Annotation{Kind: "journey", ID: "j", Steps: []bundle.Step{
			{ID: "s", Cites: []string{ctrl, charge}, Reviewed: stamp},
		}})
		if status != "needs_review" || !reflect.DeepEqual(steps[0].Reasons, []string{"unreviewed: " + charge}) {
			t.Errorf("%v: status %q, step %+v", stamp, status, steps[0])
		}
	}
	// only empty stamps: never reviewed
	status, steps := freshness(t, bundle.Annotation{Kind: "journey", ID: "j", Steps: []bundle.Step{
		{ID: "s", Cites: []string{ctrl}, Reviewed: map[string]string{ctrl: ""}},
	}})
	if status != "unreviewed" || steps[0].Status != "unreviewed" || len(steps[0].Reasons) != 0 {
		t.Errorf("empty stamps: status %q, step %+v", status, steps[0])
	}
}

func TestAnnotationUncitedStepDoesNotDragStatus(t *testing.T) {
	status, steps := freshness(t, bundle.Annotation{Kind: "journey", ID: "j", Steps: []bundle.Step{
		{ID: "cited", Cites: []string{ctrl, charge}, Reviewed: map[string]string{ctrl: "h1", charge: "h3"}},
		{ID: "intro"},
	}})
	if status != "reviewed" || steps[1].Status != "reviewed" {
		t.Errorf("status %q, steps %+v", status, steps)
	}
}

func TestAnnotationMissingCite(t *testing.T) {
	status, steps := freshness(t, bundle.Annotation{Kind: "note", ID: "n",
		Cites:    []string{ctrl, "shop-app:Gone.fun/0", "public.orders"},
		Reviewed: map[string]string{ctrl: "h1"},
	})
	if status != "needs_review" || !reflect.DeepEqual(steps[0].Reasons, []string{"missing: shop-app:Gone.fun/0"}) {
		t.Errorf("status %q, step %+v", status, steps[0])
	}
}

func TestContainsWord(t *testing.T) {
	for _, c := range []struct {
		s, name string
		want    bool
	}{
		{"see Shop.Orders", "Shop.Orders", true},
		{"Shop.Orders.", "Shop.Orders", true},
		{"`Shop.Orders`,", "Shop.Orders", true},
		{"Shop.Orders.create/2", "Shop.Orders", true},
		{"Shop.OrdersTest", "Shop.Orders", false},
		{"Shop.Orders.Line", "Shop.Orders", false},
		{"MyShop.Orders", "Shop.Orders", false},
		{"X.Shop.Orders", "Shop.Orders", false},
		{"Shop.Orders_v2", "Shop.Orders", false},
		{"Shop.OrdersTest and Shop.Orders", "Shop.Orders", true},
		{"nothing here", "Shop.Orders", false},
		{"", "Shop.Orders", false},
	} {
		if got := containsWord(c.s, c.name); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v, want %v", c.s, c.name, got, c.want)
		}
	}
}

// Two facts documents for one repository whose modules share a context.
func TestContextsSharedAcrossDocs(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := emptyBundle("snapshot-x", "W", "2026-10-01T00:00:00Z")
	b.Manifest.Repos = []bundle.Repo{{Name: "r"}}
	b.Facts = []*facts.Doc{
		{FactsVersion: 1, Producer: facts.Producer{Name: "p1"}, Repo: "r", Modules: []facts.Module{{ID: "r:App.Accounts.A", Name: "App.Accounts.A", File: "a.ex"}}},
		{FactsVersion: 1, Producer: facts.Producer{Name: "p2"}, Repo: "r", Modules: []facts.Module{{ID: "r:App.Accounts.B", Name: "App.Accounts.B", File: "b.ex"}}},
	}
	if err := s.ImportBundle(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM contexts WHERE id = 'ctx:r:App.Accounts'`); n != 1 {
		t.Errorf("contexts = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM entities WHERE id = 'ctx:r:App.Accounts'`); n != 1 {
		t.Errorf("context entities = %d", n)
	}
}

// Only fingerprinted functions are stamped by review, so other cites
// (modules, tables, files, contexts, functions without a fingerprint) are
// judged on existence alone.
func TestAnnotationNonFunctionCites(t *testing.T) {
	const (
		validate = "shop-app:Shop.Orders.validate/1" // no fingerprint
		mod      = "shop-app:Shop.Orders"
		tbl      = "public.orders"
		file     = "file:shop-app:lib/shop_web/order_controller.ex"
	)
	for _, c := range []struct {
		name       string
		step       bundle.Step
		want       string
		wantReason []string
	}{
		{"module and table", bundle.Step{ID: "s", Cites: []string{mod, tbl}}, "reviewed", nil},
		{"file and context", bundle.Step{ID: "s", Cites: []string{file, "ctx:shop-app:Shop.Orders"}}, "reviewed", nil},
		{"function without fingerprint", bundle.Step{ID: "s", Cites: []string{validate}}, "reviewed", nil},
		{"stamped plus unfingerprinted", bundle.Step{ID: "s", Cites: []string{ctrl, validate, mod}, Reviewed: map[string]string{ctrl: "h1"}}, "reviewed", nil},
		{"fingerprinted function never stamped", bundle.Step{ID: "s", Cites: []string{mod, charge}}, "unreviewed", nil},
		{"missing module", bundle.Step{ID: "s", Cites: []string{"shop-app:Gone"}}, "needs_review", []string{"missing: shop-app:Gone"}},
	} {
		status, steps := freshness(t, bundle.Annotation{Kind: "journey", ID: "j", Steps: []bundle.Step{c.step}})
		if status != c.want || steps[0].Status != c.want || !reflect.DeepEqual(steps[0].Reasons, c.wantReason) {
			t.Errorf("%s: status %q, step %+v, want %q %v", c.name, status, steps[0], c.want, c.wantReason)
		}
	}
}

func TestTableKind(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := emptyBundle("snapshot-x", "W", "2026-10-01T00:00:00Z")
	b.Facts = []*facts.Doc{{FactsVersion: 1, Producer: facts.Producer{Name: "pg"}, Tables: []facts.Table{
		{ID: "public.v", Name: "v", Kind: "view"},
		{ID: "public.t", Name: "t"},
	}}}
	if err := s.ImportBundle(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"public.v": "view", "public.t": "table"} {
		var kind string
		if err := s.DB.QueryRow(`SELECT kind FROM db_tables WHERE id = ?`, id).Scan(&kind); err != nil || kind != want {
			t.Errorf("%s kind = %q, %v; want %q", id, kind, err, want)
		}
	}
}

func TestKnownOtherReportsErrors(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	im := &importer{ctx: context.Background(), tx: tx, sid: "s"}
	if _, err := im.knownOther("public.orders"); err == nil {
		t.Error("a failed lookup must be an error, not a missing cite")
	}
}
