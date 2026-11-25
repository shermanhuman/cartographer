package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testLock = `%{
  "ecto": {:hex, :ecto, "3.14.3", "4e7f7728c1432b18d8a48e4aeef879e8b908ecefd66afde17adb8cca0a46eff4", [:mix], [{:decimal, "~> 3.0", [hex: :decimal, repo: "hexpm", optional: false]}], "hexpm", "da2548bd3ced0f56c27d54255389bbbb8f110150ce15cffc95ec0cafa364465c"},
  "local": {:git, "https://example.com/local.git", "abc", []},
}
`

func TestParseHexLock(t *testing.T) {
	got := parseHexLock([]byte(testLock))
	if len(got) != 1 || got[0] != (hexEntry{"ecto", "3.14.3", "da2548bd3ced0f56c27d54255389bbbb8f110150ce15cffc95ec0cafa364465c"}) {
		t.Fatalf("entries %+v", got)
	}
}

func hexGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Filling the Hex cache changes the inputs digest of an Elixir member, so
// the map rebuilds; a member without the Elixir extractor is unaffected.
func TestDigestFollowsTheHexCache(t *testing.T) {
	repo := t.TempDir()
	hexGit(t, repo, "init", "-q")
	os.MkdirAll(filepath.Join(repo, "web"), 0o755)
	os.WriteFile(filepath.Join(repo, "web", "mix.lock"), []byte(testLock), 0o644)
	os.MkdirAll(filepath.Join(repo, "deps", "x"), 0o755)
	os.WriteFile(filepath.Join(repo, "deps", "x", "mix.lock"), []byte(testLock), 0o644)
	hexGit(t, repo, "add", "-A")
	hexGit(t, repo, "commit", "-q", "-m", "lock")
	commit := hexGit(t, repo, "rev-parse", "HEAD")

	hexHome := t.TempDir()
	t.Setenv("HEX_HOME", hexHome)
	r := &Runner{Version: "test"}
	in := Input{Member: Member{Repo: "acme/web", Path: repo}, Commit: commit, Settings: &Settings{Extract: []string{"elixir"}}}
	other := Input{Member: Member{Repo: "acme/go", Path: repo}, Commit: commit, Settings: &Settings{Extract: []string{"go"}}}
	ctx := context.Background()

	var sb strings.Builder
	hexDigestLines(ctx, &sb, in)
	if !strings.Contains(sb.String(), "web/mix.lock ecto 3.14.3") || !strings.Contains(sb.String(), "missing") || strings.Contains(sb.String(), "deps/") {
		t.Fatalf("lines %q", sb.String())
	}

	before, beforeOther := r.digest(ctx, []Input{in}), r.digest(ctx, []Input{other})
	cache := filepath.Join(hexHome, "packages", "hexpm")
	os.MkdirAll(cache, 0o755)
	os.WriteFile(filepath.Join(cache, "ecto-3.14.3.tar"), []byte("tarball"), 0o644)
	if after := r.digest(ctx, []Input{in}); after == before {
		t.Error("filling the Hex cache left the Elixir member's digest unchanged")
	}
	if r.digest(ctx, []Input{other}) != beforeOther {
		t.Error("a non-Elixir member's digest changed with the Hex cache")
	}
}
