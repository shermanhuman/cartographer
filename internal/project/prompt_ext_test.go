package project_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/project"
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
