package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shermanhuman/cartographer/internal/fresh"
	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/store"
)

// NoMapAdvice says how to get a map from cwd. A checkout whose
// .cartographer/repo.toml names a project that isn't registered gets that
// project's project add command (the anchor, or this checkout when it is the
// anchor); anything else gets store.NoMapAdvice. Registering stays the
// user's call. A registered project waiting for its first map never reaches
// here: ErrFirstBuild says what its build is doing.
func NoMapAdvice(ctx context.Context, cwd, stateDir string) string {
	co := fresh.Find(ctx, cwd)
	if co.Root == "" {
		return store.NoMapAdvice
	}
	b, err := os.ReadFile(filepath.Join(co.Root, project.SettingsFile))
	if err != nil {
		return store.NoMapAdvice
	}
	st, err := project.ParseSettings(b, "")
	if err != nil || st.Project == "" {
		return store.NoMapAdvice
	}
	if _, err := project.Load(stateDir, st.Project); err == nil {
		return store.NoMapAdvice
	}
	target := st.Anchor
	if target == "" {
		target = co.Root
	}
	return fmt.Sprintf("this checkout belongs to project %s (%s), which isn't registered; registering it is the user's call: `cartographer project add %s`", st.Project, project.SettingsFile, target)
}
