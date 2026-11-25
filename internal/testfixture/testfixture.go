// Package testfixture holds test helpers shared across packages: copies of
// the checked-in snapshot fixtures under other names. Only tests use it.
package testfixture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// V2Dir is testdata/workspace-v2/snapshot: format 2, workspace "v2", source
// main, id snapshot-20261120T000000Z.
func V2Dir() string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "testdata", "workspace-v2", "snapshot")
}

// V2As copies the format-2 fixture as workspace with snapshot id and source
// (main, head or published), and returns the copy's directory.
func V2As(t testing.TB, workspace, id, source string) string {
	t.Helper()
	src := V2Dir()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == "manifest.json" {
			b = []byte(strings.NewReplacer(`"workspace": "v2"`, `"workspace": "`+workspace+`"`,
				"snapshot-20261120T000000Z", id, `"source": "main"`, `"source": "`+source+`"`).Replace(string(b)))
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}
