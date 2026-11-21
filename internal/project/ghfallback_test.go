package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGHRetryEnvResetsHelpersAfterExistingConfig(t *testing.T) {
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got := strings.Join(ghRetryEnv([]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=1"}), "\n")
	for _, want := range []string{
		"GIT_CONFIG_KEY_2=credential.helper\nGIT_CONFIG_VALUE_2=\n",
		"GIT_CONFIG_KEY_3=credential.helper\nGIT_CONFIG_VALUE_3=!" + gh + " auth git-credential",
		"GIT_CONFIG_COUNT=4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	t.Setenv("PATH", t.TempDir()) // no gh
	if got := ghRetryEnv(nil); got != nil {
		t.Errorf("without gh: %v", got)
	}
	for msg, want := range map[string]bool{
		"git fetch: exit status 128: fatal: could not read Username for 'https://github.com': terminal prompts disabled": true,
		"git fetch: exit status 128: fatal: Authentication failed for 'https://github.com/a/b/'":                         true,
		"git fetch: exit status 128: fatal: unable to access: Could not resolve host: github.com":                        false,
	} {
		if authFailure(errors.New(msg)) != want {
			t.Errorf("authFailure(%q) != %v", msg, want)
		}
	}
}

// git's approve step offers a working credential to every helper in the
// list: with gh appended after the machine's helper (the old fallback) the
// machine's store received gh's token; with the retry env it never does.
func TestGHRetryEnvApprovesToGHOnly(t *testing.T) {
	dir := t.TempDir()
	storeLog := filepath.Join(dir, "store.log")
	store := filepath.Join(dir, "store.sh")
	os.WriteFile(store, []byte("#!/bin/sh\necho \"$1\" >> '"+storeLog+"'\ncat > /dev/null\n"), 0o755)
	gh := filepath.Join(dir, "gh")
	os.WriteFile(gh, []byte("#!/bin/sh\ncat > /dev/null\nexit 0\n"), 0o755)
	cfg := filepath.Join(dir, "gitconfig")
	os.WriteFile(cfg, []byte("[credential]\n\thelper = !"+store+"\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	approve := func(env []string) string {
		os.Remove(storeLog)
		cmd := exec.Command("git", "credential", "approve")
		cmd.Env = env
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\nusername=x\npassword=tok\n\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git credential approve: %v\n%s", err, out)
		}
		b, _ := os.ReadFile(storeLog)
		return string(b)
	}
	base := os.Environ()
	old := append(append([]string(nil), base...), "GIT_CONFIG_KEY_0=credential.https://github.com.helper", "GIT_CONFIG_VALUE_0=!"+gh+" auth git-credential", "GIT_CONFIG_COUNT=1")
	if got := approve(old); !strings.Contains(got, "store") {
		t.Fatalf("the old fallback's leak didn't reproduce: %q", got)
	}
	if got := approve(ghRetryEnv(base)); strings.Contains(got, "store") {
		t.Errorf("the machine's helper was asked to store gh's token: %q", got)
	}
}
