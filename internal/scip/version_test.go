package scip

import "testing"

func TestToolVersionDropsPlatformSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"0.3.3121-standalone":                         "0.3.3121-standalone",
		"0.3.3121-standalone (946b80a756 2026-11-15)": "0.3.3121-standalone",
		"": "",
	} {
		if got := toolVersion(in); got != want {
			t.Errorf("toolVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
