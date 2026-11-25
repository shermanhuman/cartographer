package store

import "testing"

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
	} {
		if got := externalBase(base, own, namespaces); got != want {
			t.Errorf("externalBase(%q) = %v, want %v", base, got, want)
		}
	}
}
