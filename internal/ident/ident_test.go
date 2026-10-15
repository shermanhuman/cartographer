package ident

import (
	"reflect"
	"testing"
)

func TestWords(t *testing.T) {
	cases := map[string][]string{
		"BreakdownAdmin.Crm.syncShops/2": {"breakdown", "admin", "crm", "sync", "shops"},
		"HTTPServer":                      {"http", "server"},
		"vehicle_vin_models":              {"vehicle", "vin", "models"},
		"public.orders":                   {"public", "orders"},
		"a.b":                             nil,
		"OAuth2Token":                     {"auth2", "token"}, // single-letter fragments are dropped
	}
	for in, want := range cases {
		if got := Words(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Words(%q) = %v, want %v", in, got, want)
		}
	}
}
