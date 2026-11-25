package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	if _, err := project.Load(stateDir, st.Project); !errors.Is(err, fs.ErrNotExist) {
		return store.NoMapAdvice // registered, or a registration that can't be read
	}
	target := st.Anchor
	if target == "" {
		target = co.Root
	}
	return fmt.Sprintf("this checkout belongs to project %s (%s), which isn't registered; registering it is the user's call: `cartographer project add %s`", st.Project, project.SettingsFile, target)
}

// ErrNoMap is the store holding no map at all, from a session that knows
// its checkout: errors.Is it is store.ErrNoSnapshot, and Advice names the
// checkout's project when its repo.toml declares one (worked out only when
// asked, so a cheap caller pays nothing).
type ErrNoMap struct{ Cwd, StateDir string }

func (e *ErrNoMap) Error() string        { return "no map yet: " + e.Advice() }
func (e *ErrNoMap) Is(target error) bool { return target == store.ErrNoSnapshot }

// Advice is NoMapAdvice for the session's checkout.
func (e *ErrNoMap) Advice() string { return NoMapAdvice(context.Background(), e.Cwd, e.StateDir) }

// AdviceFor is err's advice when it carries one (ErrNoMap), else the generic
// store.NoMapAdvice.
func AdviceFor(err error) string {
	var nm *ErrNoMap
	if errors.As(err, &nm) {
		return nm.Advice()
	}
	return store.NoMapAdvice
}
