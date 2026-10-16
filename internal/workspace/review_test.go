package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/store"
)

// Review edits only the reviewed stamps: authored key order, unescaped
// characters and number literals survive, and other annotations are untouched.
func TestReviewPreservesAuthoredJSON(t *testing.T) {
	ws, _, _ := setup(t)
	c, _ := Load(ws)
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sid, err := s.Import(context.Background(), "../../testdata/workspace-mini/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws, "annotations/journeys.json")
	write(t, path, `{
  "zeta": 1,
  "annotations": [
    {
      "title": "Other",
      "kind": "note",
      "id": "other",
      "cites": ["shop-app:Shop.Billing.charge/2"],
      "body": "a -> b & <c>"
    },
    {
      "title": "Checkout -> charge & <done>",
      "kind": "journey",
      "id": "checkout",
      "weight": 12345678901234567890,
      "ratio": 1.50,
      "steps": [
        {
          "title": "Place",
          "id": "place",
          "cites": ["shop-app:ShopWeb.OrderController.create/2", "shop-app:Shop.Orders"],
          "reviewed": {"shop-app:ShopWeb.OrderController.create/2": "stale"},
          "outcome": "row · exists"
        },
        {"id": "intro", "title": "Intro", "reviewed": {"x": "y"}}
      ]
    }
  ],
  "alpha": true
}
`)
	n, err := Review(context.Background(), c, s.DB, sid, "checkout")
	if err != nil || n != 1 {
		t.Fatalf("Review = %d, %v", n, err)
	}
	got := mustRead(t, path)
	for _, want := range []string{
		`"title": "Checkout -> charge & <done>"`,
		`"body": "a -> b & <c>"`,
		`"weight": 12345678901234567890`,
		`"ratio": 1.50`,
		`"outcome": "row · exists"`,
		`"shop-app:ShopWeb.OrderController.create/2": "h1"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	// Keys stay in authored order; a new stamp would go last.
	order := []string{`"zeta"`, `"annotations"`, `"title": "Other"`, `"kind": "note"`, `"id": "other"`,
		`"title": "Checkout`, `"kind": "journey"`, `"id": "checkout"`, `"weight"`, `"ratio"`, `"steps"`,
		`"title": "Place"`, `"id": "place"`, `"cites"`, `"reviewed"`, `"outcome"`, `"id": "intro"`, `"alpha"`}
	last := -1
	for _, k := range order {
		i := strings.Index(got[last+1:], k)
		if i < 0 {
			t.Fatalf("%s missing or out of order in\n%s", k, got)
		}
		last += 1 + i
	}
	// The intro step cites nothing, so its stale stamps go.
	if strings.Contains(got, `"x": "y"`) {
		t.Errorf("uncited step kept its stamps:\n%s", got)
	}
	// The other annotation is not stamped.
	if strings.Count(got, `"reviewed"`) != 1 {
		t.Errorf("want exactly one reviewed object:\n%s", got)
	}
	if !strings.HasSuffix(got, "}\n") {
		t.Errorf("no trailing newline")
	}
	if tmp, _ := filepath.Glob(filepath.Join(ws, "annotations", ".*")); len(tmp) != 0 {
		t.Errorf("temporary files left: %v", tmp)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
