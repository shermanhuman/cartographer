package store

import "testing"

func TestPathMatch(t *testing.T) {
	for _, c := range []struct {
		route, req string
		ok         bool
	}{
		{"/api/v1/sales/cold/check", "/api/v1/sales/cold/check", true},
		{"/api/v1/leads/:id", "/api/v1/leads/:p1", true},       // param to param
		{"/api/v1/leads/:id", "/api/v1/leads/42", true},        // a literal fills a route param
		{"/api/v1/leads/new", "/api/v1/leads/:p1", false},      // an interpolation never fills a literal
		{"/dial/{id}", "/dial/7", true},
		{"/files/{rest...}", "/files/a/b", true},
		{"/{$}", "/prices", false},                             // {$} anchors "/" only
		{"/{$}", "/", true},
		{"/a/b", "/a", false},
		{"/a", "/a/b", false},
		{"/a/b", "/a/b?x=1", true},
	} {
		if got := pathMatch(c.route, c.req); got != c.ok {
			t.Errorf("pathMatch(%q, %q) = %v, want %v", c.route, c.req, got, c.ok)
		}
	}
}
