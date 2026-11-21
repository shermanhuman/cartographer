package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shermanhuman/cartographer/internal/workspace"
)

// FetchBudget bounds all of one cycle's fetches together (they run in
// parallel after the anchor's); the build's own limit starts after it.
var FetchBudget = 90 * time.Second

// RefPrefix is where fetched branches land in a checkout. Nothing else in
// the checkout changes: no branch, file, remote-tracking ref, tag or
// FETCH_HEAD.
const RefPrefix = "refs/cartographer/"

// askpass is a credential prompt that answers nothing, so git never waits
// for a person (a detached rebuild has no one to ask).
const askpass = "#!/bin/sh\nexit 0\n"

// gitEnv is the environment of fetches: no terminal prompt, and GIT_ASKPASS
// and SSH_ASKPASS replaced by a no-op helper in the state directory, so an
// inherited GUI prompt never opens. Credential helpers still run.
func gitEnv(stateDir string) ([]string, error) {
	helper := filepath.Join(stateDir, "askpass")
	if b, err := os.ReadFile(helper); err != nil || string(b) != askpass {
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			return nil, err
		}
		if err := workspace.WriteAtomic(helper, []byte(askpass)); err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(helper, 0o755); err != nil {
		return nil, err
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GIT_ASKPASS", "SSH_ASKPASS", "GIT_TERMINAL_PROMPT", "SSH_ASKPASS_REQUIRE":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+helper, "SSH_ASKPASS="+helper)
	return append(env, ghFallback(env)...), nil
}

// ghFallback adds `gh auth git-credential` as a last credential helper for
// https://github.com, after the machine's own: git asks each in turn until
// one answers, so it is used only when the configured helper can't (the
// macOS keychain is unavailable to a process outside the login session). It
// is added only when a working gh is on PATH, through GIT_CONFIG_* so no
// config file changes; an existing GIT_CONFIG_COUNT is extended.
func ghFallback(env []string) []string {
	gh, err := exec.LookPath("gh")
	if err != nil || exec.Command(gh, "--version").Run() != nil {
		return nil
	}
	n := 0
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			fmt.Sscanf(v, "%d", &n)
		}
	}
	// os/exec keeps the last of duplicate variables, so this COUNT wins.
	return []string{
		fmt.Sprintf("GIT_CONFIG_KEY_%d=credential.https://github.com.helper", n),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=!%s auth git-credential", n, gh),
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1),
	}
}

// gitRun runs git in dir in its own process group (a cancelled fetch ends
// its remote helper too) and returns stdout.
func gitRun(ctx context.Context, env []string, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Stdin = nil
	if err := workspace.RunGroup(cmd); err != nil {
		msg := strings.TrimSpace(errb.String())
		if ctx.Err() != nil {
			msg = strings.TrimSpace(msg + " (" + ctx.Err().Error() + ")")
		}
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, msg)
	}
	return out.String(), nil
}

// DefaultBranch asks the remote for its default branch.
func DefaultBranch(ctx context.Context, env []string, dir, url string) (string, error) {
	out, err := gitRun(ctx, env, dir, "ls-remote", "--symref", "--", url, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			if branch, _, ok := strings.Cut(rest, "\t"); ok {
				return branch, nil
			}
		}
	}
	return "", fmt.Errorf("%s names no default branch", url)
}

// Fetch fetches branch of slug's GitHub URL into refs/cartographer/<branch>
// in the checkout at dir and returns the fetched commit.
func Fetch(ctx context.Context, env []string, dir, slug, branch string) (string, error) {
	if strings.HasPrefix(branch, "-") || branch == "" {
		return "", fmt.Errorf("invalid branch %q", branch)
	}
	if _, err := gitRun(ctx, env, dir, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--no-recurse-submodules",
		"--", GitHubURL(slug), "+refs/heads/"+branch+":"+RefPrefix+branch); err != nil {
		return "", fmt.Errorf("%s %s: %w", GitHubURL(slug), branch, err)
	}
	return Fetched(ctx, dir, branch)
}

// Fetched returns the commit last fetched for branch ("" and an error when
// none was).
func Fetched(ctx context.Context, dir, branch string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", "--end-of-options", RefPrefix+branch+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("never fetched (no %s%s in %s)", RefPrefix, branch, dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// ErrNoFile is ReadAt's answer for a path the commit doesn't have (and
// what a Pins reader returns for a missing file).
var ErrNoFile = errors.New("no such file")

// ReadAt returns path's content at commit in the checkout at dir.
func ReadAt(ctx context.Context, dir, commit, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "blob", commit+":"+path)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() == nil {
			if exec.Command("git", "-C", dir, "cat-file", "-e", commit+"^{commit}").Run() == nil {
				return nil, ErrNoFile
			}
		}
		return nil, fmt.Errorf("git cat-file %s:%s: %v: %s", commit, path, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}
