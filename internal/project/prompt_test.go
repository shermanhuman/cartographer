package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/gitenv"
	"github.com/shermanhuman/cartographer/internal/gitenv/gitenvtest"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A lazy blob fetch in a partial clone (ReadAt's cat-file) that meets an
// auth prompt never runs an inherited askpass program, once the process is
// promptless as run() makes it.
func TestPartialCloneLazyReadNeverPrompts(t *testing.T) {
	h := gitenvtest.New(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	runGit(t, base, "init", "-q", "-b", "main", origin)
	os.WriteFile(filepath.Join(origin, "file.txt"), []byte("hello\n"), 0o644)
	runGit(t, origin, "add", "file.txt")
	runGit(t, origin, "commit", "-q", "-m", "one")
	runGit(t, origin, "config", "uploadpack.allowFilter", "true")
	runGit(t, origin, "config", "uploadpack.allowAnySHA1InWant", "true")
	clone := filepath.Join(base, "clone")
	runGit(t, base, "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+origin, clone)
	if missing := runGit(t, clone, "rev-list", "--objects", "--missing=print", "HEAD"); !strings.Contains(missing, "?") {
		t.Fatalf("the blob isn't missing, so no lazy fetch would happen:\n%s", missing)
	}
	runGit(t, clone, "config", "remote.origin.url", h.URL)
	head := runGit(t, clone, "rev-parse", "HEAD")

	t.Setenv("SSH_ASKPASS", h.Recorder)
	t.Setenv("GIT_ASKPASS", h.Recorder)
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	gitenv.Apply() // what run() does first; t.Setenv restores the variables after
	if _, err := ReadAt(context.Background(), clone, head, "file.txt"); err == nil {
		t.Fatal("read a blob the 401 promisor never served")
	}
	h.Check(t, "partial-clone lazy read")
}
