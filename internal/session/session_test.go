package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/store"
)

func gitInit(t *testing.T, dir, origin string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", origin}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
}

// importAs imports the mini fixture under another workspace name and
// snapshot id, with its repos renamed.
func importAs(t *testing.T, s *store.Store, workspace, id, repoPrefix string) {
	t.Helper()
	src := "../../testdata/workspace-mini/snapshot"
	dst := t.TempDir()
	filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		txt := string(b)
		if repoPrefix != "" {
			txt = strings.ReplaceAll(txt, "shop-", repoPrefix)
		}
		if rel == "manifest.json" {
			txt = strings.Replace(txt, `"workspace": "mini"`, `"workspace": "`+workspace+`"`, 1)
			txt = strings.Replace(txt, "snapshot-20261015T120000Z", id, 1)
		}
		if repoPrefix != "" {
			rel = strings.ReplaceAll(rel, "shop-", repoPrefix)
		}
		os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755)
		return os.WriteFile(filepath.Join(dst, rel), []byte(txt), 0o644)
	})
	if _, err := s.Import(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
}

func TestChooseNeverGuessesAcrossProjects(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(":memory:")
	defer s.Close()
	importAs(t, s, "mini", "snapshot-20261015T120000Z", "")
	parent := t.TempDir()
	app := filepath.Join(parent, "anything-named")
	gitInit(t, app, "git@github.com:example/shop-app.git") // matched by origin URL
	unrelated := filepath.Join(parent, "unrelated")
	gitInit(t, unrelated, "https://github.com/example/unrelated")
	plain := t.TempDir()

	sess := &Session{Store: s, Cwd: app}
	if id, err := sess.choose(ctx); err != nil || id != "snapshot-20261015T120000Z" {
		t.Errorf("mapped repo: %q, %v", id, err)
	}
	if !sess.Mapped(ctx) {
		t.Error("app should be mapped")
	}
	sess.Cwd = unrelated
	var nm *ErrNotMapped
	if _, err := sess.choose(ctx); !errors.As(err, &nm) {
		t.Errorf("unrelated repo must not get another project's map: %v", err)
	}
	sess.Cwd = plain
	if id, err := sess.choose(ctx); err != nil || id == "" {
		t.Errorf("outside git with one workspace: %q, %v", id, err)
	}
	if sess.Mapped(ctx) {
		t.Error("a non-repo directory is not mapped")
	}

	// A second project: outside git it's now ambiguous; its own repo picks it.
	importAs(t, s, "other", "snapshot-20261016T000000Z", "acme-")
	if _, err := sess.choose(ctx); !errors.As(err, &nm) || !strings.Contains(err.Error(), "mini") {
		t.Errorf("outside git with two workspaces: %v", err)
	}
	acme := filepath.Join(parent, "acme-app")
	gitInit(t, acme, "https://github.com/example/acme-app")
	sess.Cwd = acme
	if id, _ := sess.choose(ctx); id != "snapshot-20261016T000000Z" {
		t.Errorf("acme repo chose %q", id)
	}
	sess.Cwd = app
	if id, _ := sess.choose(ctx); id != "snapshot-20261015T120000Z" {
		t.Errorf("shop repo chose %q (newest overall must not win)", id)
	}
}
