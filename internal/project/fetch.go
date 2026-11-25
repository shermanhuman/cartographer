package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/shermanhuman/cartographer/internal/gitenv"
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
// inherited GUI prompt never opens. The machine's credential helpers still
// run; gh is only a separate retry (ghRetryEnv).
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
	return gitenv.Promptless(os.Environ(), helper), nil
}

// ghRetryEnv is env for a second try after the machine's own credential
// helpers failed to authenticate (the macOS keychain is unavailable to a
// process outside the login session): the helper list is reset and `gh auth
// git-credential` is the only helper, so no other helper is asked to store
// the token gh hands out (git's approve step offers the credential to every
// helper). It goes through GIT_CONFIG_*, so no config file changes, and an
// existing GIT_CONFIG_COUNT is extended. nil without a working gh on PATH.
func ghRetryEnv(env []string) []string {
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
	return append(append([]string(nil), env...),
		fmt.Sprintf("GIT_CONFIG_KEY_%d=credential.helper", n),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=", n),
		fmt.Sprintf("GIT_CONFIG_KEY_%d=credential.helper", n+1),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=!%s auth git-credential", n+1, gh),
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+2),
	)
}

// authFailure reports a git error that says the remote wanted credentials
// it didn't get.
func authFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"Authentication failed", "could not read Username", "could not read Password", "terminal prompts disabled", "returned error: 401", "returned error: 403", "Repository not found"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// gitAuth is gitRun, retried once with gh as the only credential helper
// when the first try failed to authenticate.
func gitAuth(ctx context.Context, env []string, dir string, args ...string) (string, error) {
	out, err := gitRun(ctx, env, dir, args...)
	if authFailure(err) && ctx.Err() == nil {
		if ghEnv := ghRetryEnv(env); ghEnv != nil {
			if out2, err2 := gitRun(ctx, ghEnv, dir, args...); err2 == nil {
				return out2, nil
			} else {
				err = fmt.Errorf("%w; with gh as the credential helper: %v", err, err2)
			}
		}
	}
	return out, err
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
	out, err := gitAuth(ctx, env, dir, "ls-remote", "--symref", "--", url, "HEAD")
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
	if _, err := gitAuth(ctx, env, dir, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--no-recurse-submodules",
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
