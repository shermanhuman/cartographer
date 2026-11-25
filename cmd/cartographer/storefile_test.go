package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/testfixture"
)

// At a later schema the default store is that schema's own file; the old
// carto.db is never opened. A project registered against carto.db is
// removed from the new file, whether carto.db is there or not, and only its
// own maps go.
func TestEachSchemaHasItsOwnStoreFile(t *testing.T) {
	data := t.TempDir()
	t.Setenv("CARTOGRAPHER_DATA", data)
	t.Setenv("CARTOGRAPHER_DB", "")
	if got := defaultDB(); got != filepath.Join(data, "carto.db") {
		t.Fatalf("schema 4 default = %s, want carto.db", got)
	}
	defer store.SetSchemaVersionForTest(5)()
	v5 := filepath.Join(data, "carto-v5.db")
	if got := defaultDB(); got != v5 {
		t.Fatalf("schema 5 default = %s, want %s", got, v5)
	}
	// refresh --head never writes any default store, this schema's or an
	// older one's.
	for _, db := range []string{v5, filepath.Join(data, "carto.db")} {
		if err := headStoreAllowed(db); err == nil {
			t.Errorf("refresh --head allowed into the default store %s", db)
		}
	}
	ctx := context.Background()
	sd := project.StateDir()
	for _, oldPresent := range []bool{false, true} {
		old := filepath.Join(data, "carto.db")
		os.Remove(old)
		os.Remove(v5)
		var oldBytes []byte
		if oldPresent {
			oldBytes = []byte("an older schema's store, not to be touched")
			os.WriteFile(old, oldBytes, 0o644)
		}
		s, err := store.Open(v5)
		if err != nil {
			t.Fatal(err)
		}
		for _, ws := range []string{"alpha", "beta"} {
			if _, err := s.Import(ctx, testfixture.V2As(t, ws, "snapshot-20261121T000000Z-"+ws, "main")); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
		if err := project.Save(sd, &project.Registration{Name: "alpha", Anchor: "acme/alpha", Store: old}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := run(ctx, []string{"project", "remove", "alpha"}, &out); err != nil {
			t.Fatalf("old file present=%v: %v", oldPresent, err)
		}
		if !strings.Contains(out.String(), v5) {
			t.Errorf("old file present=%v: remove says %q, want it to name %s", oldPresent, out.String(), v5)
		}
		s, err = store.Open(v5)
		if err != nil {
			t.Fatal(err)
		}
		left := map[string]bool{}
		all, _ := s.Snapshots(ctx)
		for _, si := range all {
			left[si.Workspace] = true
		}
		s.Close()
		if left["alpha"] || !left["beta"] {
			t.Errorf("old file present=%v: after remove the new store holds %v; want beta only", oldPresent, left)
		}
		b, err := os.ReadFile(old)
		if oldPresent && !bytes.Equal(b, oldBytes) {
			t.Errorf("the old store changed")
		}
		if !oldPresent && err == nil {
			t.Errorf("removal created the old store")
		}
	}
}
