package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/project"
)

func checkout(t *testing.T, repoToml string) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir, "https://github.com/acme/app.git")
	if repoToml != "" {
		os.MkdirAll(filepath.Join(dir, ".cartographer"), 0o755)
		os.WriteFile(filepath.Join(dir, project.SettingsFile), []byte(repoToml), 0o644)
	}
	return dir
}

func TestNoMapAdvice(t *testing.T) {
	ctx := context.Background()
	sd := t.TempDir()
	anchor := checkout(t, "project = \"app\"\nmembers = [{repo = \"acme/app\"}]\n")
	member := checkout(t, "project = \"app\"\nanchor = \"acme/app-admin\"\n")
	plain := checkout(t, "")
	cases := []struct {
		name, cwd, want, not string
	}{
		{"outside any checkout", t.TempDir(), "project add owner/name", "belongs to"},
		{"a checkout with no repo.toml", plain, "project add owner/name", "belongs to"},
		{"the anchor checkout, unregistered", anchor, "project add " + anchor, "owner/name"},
		{"a member checkout, unregistered", member, "project add acme/app-admin", "owner/name"},
	}
	for _, c := range cases {
		got := NoMapAdvice(ctx, c.cwd, sd)
		if !strings.Contains(got, c.want) || strings.Contains(got, c.not) {
			t.Errorf("%s: %q; want %q and not %q", c.name, got, c.want, c.not)
		}
	}
	// Registered: the generic advice (its first build speaks for itself).
	if err := project.Save(sd, &project.Registration{Name: "app", Anchor: "acme/app"}); err != nil {
		t.Fatal(err)
	}
	if got := NoMapAdvice(ctx, anchor, sd); strings.Contains(got, "belongs to") {
		t.Errorf("registered project: %q", got)
	}
	// ErrNotMapped (other maps exist) carries the same advice.
	e := &ErrNotMapped{Workspaces: []string{"other"}, Cwd: member, StateDir: t.TempDir()}
	if msg := e.Error(); !strings.Contains(msg, "have: other") || !strings.Contains(msg, "project add acme/app-admin") {
		t.Errorf("ErrNotMapped: %q", msg)
	}
}
