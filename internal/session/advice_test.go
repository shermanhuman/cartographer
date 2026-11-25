package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/store"
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

// A session over an empty store answers with ErrNoMap, which still is
// store.ErrNoSnapshot and names the checkout's project (R1-M1's wiring).
func TestEmptyStoreIsErrNoMap(t *testing.T) {
	ctx := context.Background()
	member := checkout(t, "project = \"app\"\nanchor = \"acme/app-admin\"\n")
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sess := &Session{Store: s, Cwd: member, StateDir: t.TempDir()}
	_, _, err = sess.Query(ctx)
	var nm *ErrNoMap
	if !errors.Is(err, store.ErrNoSnapshot) || !errors.As(err, &nm) || !strings.Contains(nm.Advice(), "project add acme/app-admin") {
		t.Fatalf("empty store: %v", err)
	}
}

// A registration file that can't be read isn't "unregistered": the advice
// stays generic rather than telling the user to register again.
func TestNoMapAdviceUnreadableRegistration(t *testing.T) {
	sd := t.TempDir()
	os.MkdirAll(filepath.Join(sd, "projects"), 0o755)
	os.WriteFile(filepath.Join(sd, "projects", "app.json"), []byte("{"), 0o644)
	anchor := checkout(t, "project = \"app\"\nmembers = [{repo = \"acme/app\"}]\n")
	if got := NoMapAdvice(context.Background(), anchor, sd); strings.Contains(got, "belongs to") {
		t.Errorf("an unreadable registration read as unregistered: %q", got)
	}
}
