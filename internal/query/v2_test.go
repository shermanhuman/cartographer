package query

import (
	"context"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/store"
)

// The facts-v2 fixture: in sales, campaignEnrollSave calls coldVerdicts,
// which dispatches (heuristic) to adminClient.Check, which posts to admin's
// route POST /api/v1/sales/cold/check (an HTTP link, heuristic through
// config.yaml cold.admin_url and admin's Service); the route's handler is
// SalesColdController.check. checkSelfTest is marked test in a source file;
// TestAdminClientCheck sits in a test file.
const (
	v2Start   = "sales:internal/dialer.Server.campaignEnrollSave/2"
	v2Caller  = "sales:internal/dialer.adminClient.Check/1"
	v2Route   = "admin:route:POST /api/v1/sales/cold/check"
	v2Handler = "admin:AdminWeb.SalesColdController.check/2"
)

func newQV2(t *testing.T) *Q {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Import(context.Background(), "../../testdata/workspace-v2/snapshot"); err != nil {
		t.Fatal(err)
	}
	q, err := New(context.Background(), s, "")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

const v2Chain = "Server.campaignEnrollSave → Server.coldVerdicts ⇢ dispatch (heuristic) → adminClient.Check ⇢ http POST /api/v1/sales/cold/check (heuristic: config.yaml cold.admin_url → admin) → SalesColdController.check"

func TestPathThroughRouteNamesHopKinds(t *testing.T) {
	q, ctx := newQV2(t), context.Background()
	hops := []string{
		"0 FN " + v2Start,
		"1 FN sales:internal/dialer.Server.coldVerdicts/1  internal/dialer/campaign.go:40  ← call [precise]",
		"2 FN " + v2Caller + "  internal/dialer/cold_admin.go:90  ← dispatch [heuristic]",
		"3 ROUTE " + v2Route + "  lib/admin_web/router.ex:12  \"POST /api/v1/sales/cold/check\"  ← http [heuristic: config.yaml cold.admin_url → admin]",
		"4 FN " + v2Handler + "  lib/admin_web/controllers/sales_cold_controller.ex:4  ← route [precise]",
	}
	out := render(t)(q.Path(ctx, v2Start, v2Handler))
	has(t, out, append([]string{"4 hop(s), calls\n", v2Chain}, hops...)...)
	out = render(t)(q.Path(ctx, v2Handler, v2Start))
	has(t, out, append([]string{"4 hop(s), calls (reverse direction)", v2Chain}, hops...)...)
	// A route is a path end too.
	out = render(t)(q.Path(ctx, v2Start, "POST /api/v1/sales/cold/check"))
	has(t, out, "3 hop(s), calls\n", "3 ROUTE "+v2Route)
}

func TestRouteIsAnEntity(t *testing.T) {
	q, ctx := newQV2(t), context.Background()
	for _, ref := range []string{v2Route, "POST /api/v1/sales/cold/check", "/api/v1/sales/cold/check"} {
		e, err := q.Resolve(ctx, ref)
		if err != nil || e.ID != v2Route || e.Kind != "route" || e.Repo != "admin" || e.File != "lib/admin_web/router.ex" || e.Line != 12 {
			t.Errorf("Resolve(%q) = %+v, %v", ref, e, err)
		}
	}
	has(t, render(t)(q.Find(ctx, "sales cold check", 0, 0)), "ROUTE "+v2Route)
	has(t, render(t)(q.Show(ctx, v2Route)),
		"route POST /api/v1/sales/cold/check · repo admin · lib/admin_web/router.ex:12",
		"handler: FN "+v2Handler+"  lib/admin_web/controllers/sales_cold_controller.ex:4  [precise]",
		"callers through HTTP links (1):",
		"  FN "+v2Caller+"  internal/dialer/cold_admin.go:95  [heuristic http: config.yaml cold.admin_url → admin]")
	// A handler's callers run through its route and the HTTP link.
	has(t, render(t)(q.Calls(ctx, v2Handler, 2, true, 0, 0)),
		"d1 ROUTE "+v2Route+"  lib/admin_web/router.ex:12  [precise route]",
		"d2 FN "+v2Caller+"  internal/dialer/cold_admin.go:90  [heuristic http: config.yaml cold.admin_url → admin]  via "+v2Route+"  ⇄ cross-repo")
	has(t, render(t)(q.Calls(ctx, v2Caller, 1, false, 0, 0)), "d1 ROUTE "+v2Route)
	// A file stands for its routes too: the router reaches the handler.
	has(t, render(t)(q.Calls(ctx, "file:admin:lib/admin_web/router.ex", 1, false, 0, 0)), "d1 FN "+v2Handler)
}

// Tests are told apart by the function's test flag as well as by its file.
func TestImpactHonoursFunctionTestFlag(t *testing.T) {
	q, ctx := newQV2(t), context.Background()
	out := render(t)(q.Impact(ctx, v2Handler, 5, 0, 0))
	has(t, out, "6 callers in 2 repo(s) · 5 cross-repo · 2 tests")
	tests := out[strings.Index(out, "\ntests\n"):]
	has(t, tests, "sales:internal/dialer.checkSelfTest/0", "sales:internal/dialer.TestAdminClientCheck/1")
	if strings.Contains(tests, "campaignEnrollSave") {
		t.Errorf("code listed under tests:\n%s", out)
	}
}

func TestShortLabel(t *testing.T) {
	for _, c := range []struct {
		e    Entity
		want string
	}{
		{Entity{ID: "admin:AdminWeb.SalesColdController.check/2", Kind: "function", Repo: "admin", Label: "AdminWeb.SalesColdController.check/2"}, "SalesColdController.check"},
		{Entity{ID: "sales:internal/dialer.adminClient.Check/1", Kind: "function", Repo: "sales", Label: "internal/dialer.Check/1"}, "adminClient.Check"},
		{Entity{ID: "sales:cmd/sales.main/0", Kind: "function", Repo: "sales", Label: "cmd/sales.main/0"}, "main"},
		{Entity{ID: "x:run/0", Kind: "function", Repo: "x", Label: "run/0"}, "run"},
		{Entity{ID: v2Route, Kind: "route", Label: "POST /api/v1/sales/cold/check"}, "POST /api/v1/sales/cold/check"},
	} {
		if got := ShortLabel(c.e); got != c.want {
			t.Errorf("ShortLabel(%s) = %q, want %q", c.e.ID, got, c.want)
		}
	}
}
