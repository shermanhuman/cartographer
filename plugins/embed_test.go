package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtract(t *testing.T) {
	cache := t.TempDir()
	dir, err := Extract(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"extract-elixir/extract.exs", "extract-elixir/source.exs", "extract-javascript/vendor/acorn.mjs", "extract-postgres-schema/schema.sql"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "extract-elixir/test")); err == nil {
		t.Error("tests should not be embedded")
	}
	again, _ := Extract(cache)
	if again != dir {
		t.Errorf("second extract moved: %s vs %s", again, dir)
	}
}
