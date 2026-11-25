package project_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/project"
	"github.com/shermanhuman/cartographer/internal/store"
)

// mise's env output can't restore a prompt: Mise.Env makes the environment
// promptless last.
func TestMiseEnvOutputCantRestoreAPrompt(t *testing.T) {
	x := newH(t)
	misePath := filepath.Join(x.dir, "mise")
	b, _ := os.ReadFile(misePath)
	hostile := strings.Replace(string(b), `{"FAKE_MISE_ENV":"1"}`, `{"GIT_ASKPASS":"/rec","SSH_ASKPASS":"/rec","SSH_ASKPASS_REQUIRE":"force","GIT_TERMINAL_PROMPT":"1"}`, 1)
	if hostile == string(b) {
		t.Fatal("fake mise has no env answer to replace")
	}
	os.WriteFile(misePath, []byte(hostile), 0o755)
	x.fakeTool("node@26", "node", "#!/bin/sh\n")
	m := &project.Mise{Bin: misePath, Dir: x.dir}
	env, err := m.Env(context.Background(), []project.Tool{{Name: "node", Version: "26"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = append(got[k], v)
	}
	for k, want := range map[string]string{"GIT_ASKPASS": "", "SSH_ASKPASS_REQUIRE": "never", "GIT_TERMINAL_PROMPT": "0"} {
		if len(got[k]) != 1 || got[k][0] != want {
			t.Errorf("%s = %q, want only %q", k, got[k], want)
		}
	}
	if len(got["SSH_ASKPASS"]) != 0 {
		t.Errorf("SSH_ASKPASS = %q", got["SSH_ASKPASS"])
	}
}

// A registration recorded at a default store follows the store to the
// current schema's file, for every reader (Load, List, InStore); a custom
// store name stays as recorded.
func TestRegistrationFollowsTheDefaultStore(t *testing.T) {
	defer store.SetSchemaVersionForTest(5)()
	sd := t.TempDir()
	data := t.TempDir()
	for name, st := range map[string]string{"alpha": filepath.Join(data, "carto.db"), "beta": filepath.Join(data, "mine.db")} {
		if err := project.Save(sd, &project.Registration{Name: name, Anchor: "acme/" + name, Store: st}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := project.Load(sd, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(data, "carto-v5.db"); a.Store != want {
		t.Errorf("alpha's store = %s, want %s", a.Store, want)
	}
	regs, _ := project.List(sd)
	for _, r := range regs {
		if r.Name == "beta" && r.Store != filepath.Join(data, "mine.db") {
			t.Errorf("a custom store changed: %s", r.Store)
		}
	}
	if !a.InStore(filepath.Join(data, "carto-v5.db")) || a.InStore(filepath.Join(t.TempDir(), "carto-v5.db")) {
		t.Error("InStore matched the wrong store")
	}
}
