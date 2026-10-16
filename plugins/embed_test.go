package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
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

// Concurrent extractions (parallel builds) must neither fail nor leave a
// .complete directory with missing files.
func TestExtractConcurrent(t *testing.T) {
	want := 0
	fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			want++
		}
		return nil
	})
	for iter := 0; iter < 20; iter++ {
		cache := t.TempDir()
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := Extract(cache); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("iteration %d: %v", iter, err)
		}
		dir, err := Extract(cache)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() != ".complete" {
				n++
			}
			return nil
		})
		if n != want {
			t.Fatalf("iteration %d: %d files, want %d", iter, n, want)
		}
		if left, _ := filepath.Glob(filepath.Join(cache, "plugins", "*.tmp*")); len(left) != 0 {
			t.Fatalf("iteration %d: temp dirs left: %v", iter, left)
		}
	}
}
