package bundle

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyMini copies the fixture snapshot so a test can alter it.
func copyMini(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(mini, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(mini, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func edit(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s: %q not found", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), old, new)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadRejectsDuplicateIDsAcrossFacts(t *testing.T) {
	dir := copyMini(t)
	// shop-worker redeclares one of shop-app's function ids
	edit(t, filepath.Join(dir, "facts", "shop-worker.json"), "shop-worker:Worker.Nightly.run/0", "shop-app:Shop.Orders.create/2")
	_, err := Read(dir)
	for _, want := range []string{`duplicate function id "shop-app:Shop.Orders.create/2"`, "facts/shop-app.json", "facts/shop-worker.json"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

func TestReadRejectsDuplicateAnnotationIDs(t *testing.T) {
	dir := copyMini(t)
	b, _ := os.ReadFile(filepath.Join(dir, "annotations", "journeys.json"))
	os.WriteFile(filepath.Join(dir, "annotations", "more.json"), b, 0o644)
	_, err := Read(dir)
	if err == nil || !strings.Contains(err.Error(), `duplicate annotation id "checkout" in journeys.json and more.json`) {
		t.Fatalf("got %v", err)
	}
}

func TestReadRejectsFactsRepoNotInManifest(t *testing.T) {
	dir := copyMini(t)
	edit(t, filepath.Join(dir, "manifest.json"), `"name": "shop-worker"`, `"name": "other"`)
	_, err := Read(dir)
	if err == nil || !strings.Contains(err.Error(), `repo "shop-worker" is not in the manifest`) {
		t.Fatalf("got %v", err)
	}
}
