package bundle

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestReadRejectsFactsOutsideSnapshot(t *testing.T) {
	dir := copyMini(t)
	os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.json"), []byte(`{"facts_version":1,"producer":{"name":"x"}}`), 0o644)
	for _, rel := range []string{"../outside.json", "facts/../../outside.json", "/etc/passwd", `facts\shop-app.json`} {
		edit(t, filepath.Join(dir, "manifest.json"), `"facts/schema.json"`, `"`+strings.ReplaceAll(rel, `\`, `\\`)+`"`)
		_, err := Read(dir)
		if err == nil || !strings.Contains(err.Error(), "unsafe facts path") {
			t.Errorf("%s: got %v", rel, err)
		}
		edit(t, filepath.Join(dir, "manifest.json"), `"`+strings.ReplaceAll(rel, `\`, `\\`)+`"`, `"facts/schema.json"`)
	}
	// A symlink inside the snapshot can't lead out of it either.
	if err := os.Symlink(filepath.Join(filepath.Dir(dir), "outside.json"), filepath.Join(dir, "facts", "link.json")); err != nil {
		t.Skip(err)
	}
	edit(t, filepath.Join(dir, "manifest.json"), `"facts/schema.json"`, `"facts/link.json"`)
	if _, err := Read(dir); err == nil {
		t.Error("read a facts file through a symlink leading outside the snapshot")
	}
}

// Workspaces built in the same second get different ids; within one
// workspace lexical order stays chronological.
func TestSnapshotIDPerWorkspace(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	a, b := SnapshotID("acme", at), SnapshotID("other", at)
	if a == b {
		t.Fatalf("same id for two workspaces: %s", a)
	}
	if !strings.HasPrefix(a, "snapshot-20261015T120000Z-") || len(a) != len("snapshot-20261015T120000Z-")+8 {
		t.Errorf("id = %s", a)
	}
	if later := SnapshotID("acme", at.Add(time.Second)); !(a < later) {
		t.Errorf("%s !< %s", a, later)
	}
	if IDWorkspaceTag(a) != WorkspaceTag("acme") || IDWorkspaceTag("snapshot-20261015T120000Z") != "" {
		t.Errorf("tags: %q %q", IDWorkspaceTag(a), WorkspaceTag("acme"))
	}
}

func TestReadRejectsIDOfAnotherWorkspace(t *testing.T) {
	dir := copyMini(t)
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	edit(t, filepath.Join(dir, "manifest.json"), "snapshot-20261015T120000Z", SnapshotID("other", at))
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), `not workspace "mini"`) {
		t.Fatalf("got %v", err)
	}
	dir = copyMini(t)
	edit(t, filepath.Join(dir, "manifest.json"), "snapshot-20261015T120000Z", SnapshotID("mini", at))
	if _, err := Read(dir); err != nil {
		t.Fatal(err)
	}
}

func TestReadRejectsNegativeContextDepth(t *testing.T) {
	dir := copyMini(t)
	edit(t, filepath.Join(dir, "manifest.json"), `"depth": 2`, `"depth": -1`)
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "contexts.depth") {
		t.Fatalf("got %v", err)
	}
}

func TestMapIDIgnoresIDAndTimeButNotContent(t *testing.T) {
	dir := copyMini(t)
	base, err := MapID(dir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := MapID(dir, "0.2.0"); again != base {
		t.Fatal("not stable")
	}
	if other, _ := MapID(dir, "0.2.1"); other == base {
		t.Error("binary version not covered")
	}
	m := filepath.Join(dir, "manifest.json")
	raw, _ := os.ReadFile(m)
	var man Manifest
	json.Unmarshal(raw, &man)
	edit(t, m, man.CreatedAt, "2030-01-01T00:00:00Z")
	edit(t, m, `"`+man.ID+`"`, `"snapshot-20300101T000000Z-`+WorkspaceTag(man.Workspace)+`"`)
	if got, _ := MapID(dir, "0.2.0"); got != base {
		t.Error("id or created_at changed the map id")
	}
	// A changed fact, a changed doc and a renamed doc each change it.
	for name, change := range map[string]func(d string){
		"fact": func(d string) {
			edit(t, filepath.Join(d, "facts", "shop-worker.json"), "shop-worker:Worker.Nightly.run/0", "shop-worker:Worker.Nightly.go/0")
		},
		"doc": func(d string) { appendTo(t, firstDoc(t, d), "\nmore\n") },
		"rename": func(d string) {
			p := firstDoc(t, d)
			if err := os.Rename(p, strings.TrimSuffix(p, ".md")+"-renamed.md"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		d := copyMini(t)
		change(d)
		if got, _ := MapID(d, "0.2.0"); got == base {
			t.Errorf("%s change kept the map id", name)
		}
	}
}

func firstDoc(t *testing.T, dir string) string {
	t.Helper()
	var found string
	filepath.WalkDir(filepath.Join(dir, "docs"), func(p string, e fs.DirEntry, err error) error {
		if err == nil && found == "" && !e.IsDir() && strings.HasSuffix(p, ".md") {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("fixture has no doc")
	}
	return found
}

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func TestFormat1RejectsFormat2Content(t *testing.T) {
	dir := copyMini(t)
	edit(t, filepath.Join(dir, "manifest.json"), `"snapshot_format": 1`, `"snapshot_format": 1, "source": "main"`)
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "format 2 fields") {
		t.Errorf("got %v", err)
	}
	dir = copyMini(t)
	edit(t, filepath.Join(dir, "facts", "shop-worker.json"), `"facts_version": 1`, `"facts_version": 2`)
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "facts_version 2 in a snapshot_format 1") {
		t.Errorf("got %v", err)
	}
	edit(t, filepath.Join(dir, "manifest.json"), `"snapshot_format": 1`, `"snapshot_format": 2`)
	if _, err := Read(dir); err != nil {
		t.Errorf("format 2 with facts 2: %v", err)
	}
	edit(t, filepath.Join(dir, "manifest.json"), `"snapshot_format": 2`, `"snapshot_format": 3`)
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "newer cartographer") {
		t.Errorf("got %v", err)
	}
}
