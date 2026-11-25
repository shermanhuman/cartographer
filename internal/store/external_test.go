package store

import (
	"context"
	"slices"
	"testing"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/facts"
)

// Decision 17: which request bases name an outside service.
func TestExternalBase(t *testing.T) {
	own := map[string]bool{"app.rosearch.io": true}
	namespaces := map[string]bool{"default": true, "breakdown": true}
	for base, want := range map[string]bool{
		"https://api.stripe.com":        true,
		"https://api.openai.com:8443":   true,
		"https://app.rosearch.io":       false, // the project's own host
		"http://localhost:4000":         false,
		"http://127.0.0.1:9000":         false,
		"http://admin":                  false, // a service name
		"http://admin.breakdown":        false, // name.namespace
		"http://admin.breakdown.svc":    false,
		"https://example.com":           true,  // two labels, not a namespace
		"https://#{host}":               false, // interpolated
		"BreakdownAdmin.Billing.Stripe": false, // a module, no host
		"":                              false,
		"https://api.stripe.com.":       true, // trailing dot
		"http://admin.":                 false,
		"http://10.0.0.5:4000":          false, // private network
		"http://192.168.1.2":            false,
		"http://[::1]:4000":             false,
		"https://203.0.113.7":           true,
		"HTTPS://API.STRIPE.COM":        true,
		"http://100.101.102.103:4000":   false, // Tailscale
	} {
		if got := externalBase(base, own, namespaces); got != want {
			t.Errorf("externalBase(%q) = %v, want %v", base, got, want)
		}
	}
}

// P3-3: an external call is never looked up by name.
func TestExternalKindIsNotResolvable(t *testing.T) {
	if resolvable("external") {
		t.Error("external is resolvable")
	}
	if !resolvable("local_or_import") {
		t.Error("local_or_import is not resolvable")
	}
}

// H1-m7: link() stores the outcome, so coverage can count it.
func TestLinkStoresExternalOrigin(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := emptyBundle("snapshot-X", "shop", "2026-11-25T00:00:00Z")
	b.Manifest.Repos = append(b.Manifest.Repos, bundle.Repo{Name: "sales", URL: "https://github.com/acme/sales", Revision: "r"})
	d := &facts.Doc{FactsVersion: 3, Producer: facts.Producer{Name: "t", Version: "1"}, Repo: "sales"}
	d.Modules = []facts.Module{{ID: "sales:M", Name: "M", File: "m.ex"}}
	d.Functions = []facts.Function{{ID: "sales:M.f/0", Module: "sales:M", Name: "f", File: "m.ex", Line: 1}}
	d.HTTPRefs = []facts.HTTPRef{
		{From: "sales:M.f/0", Method: "POST", Path: "/v1/charges", Base: "https://api.stripe.com", File: "m.ex", Line: 2},
		{From: "sales:M.f/0", Method: "GET", Path: "/check", Base: "M", File: "m.ex", Line: 3},
	}
	b.Facts = append(b.Facts, d)
	if err := s.ImportBundle(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB.Query(`SELECT path, origin FROM http_refs ORDER BY line`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p, o string
		if err := rows.Scan(&p, &o); err != nil {
			t.Fatal(err)
		}
		got = append(got, p+"="+o)
	}
	if want := []string{"/v1/charges=external", "/check="}; !slices.Equal(got, want) {
		t.Errorf("origins %q, want %q", got, want)
	}
}
