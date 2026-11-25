package store

import "testing"

func TestDefaultFilePerSchema(t *testing.T) {
	for v, want := range map[int]string{1: "carto.db", 4: "carto.db", 5: "carto-v5.db", 12: "carto-v12.db"} {
		if got := DefaultFile(v); got != want {
			t.Errorf("DefaultFile(%d) = %q, want %q", v, got, want)
		}
	}
	for name, want := range map[string]bool{"carto.db": true, "carto-v5.db": true, "carto-v4.db": false, "carto-v05.db": false, "mine.db": false, "carto-v5.db.bak": false} {
		if got := IsDefaultFile(name); got != want {
			t.Errorf("IsDefaultFile(%q) = %v", name, got)
		}
	}
	defer SetSchemaVersionForTest(5)()
	for in, want := range map[string]string{"/d/carto.db": "/d/carto-v5.db", "/d/carto-v6.db": "/d/carto-v5.db", "/d/mine.db": "/d/mine.db", "": ""} {
		if got := Resolve(in); got != want {
			t.Errorf("Resolve(%q) at schema 5 = %q, want %q", in, got, want)
		}
	}
}
