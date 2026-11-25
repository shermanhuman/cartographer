package workspace

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/gitenv/gitenvtest"
)

// A workspace fetch never runs an inherited askpass program: GIT_ASKPASS and
// SSH_ASKPASS each on their own.
func TestWorkspaceGitNeverPrompts(t *testing.T) {
	for _, k := range []string{"SSH_ASKPASS", "GIT_ASKPASS"} {
		t.Run(k, func(t *testing.T) {
			h := gitenvtest.New(t)
			t.Setenv(k, h.Recorder)
			t.Setenv("GIT_TERMINAL_PROMPT", "1")
			if _, err := git(context.Background(), t.TempDir(), "ls-remote", h.URL); err == nil {
				t.Fatal("ls-remote against a 401 endpoint succeeded")
			}
			h.Check(t, "workspace git")
		})
	}
}

// A plugin or [schema] command's own environment can't restore a prompt:
// runCmdIn makes it promptless last.
func TestOverlayCantRestoreAPrompt(t *testing.T) {
	h := gitenvtest.New(t)
	env := append(childEnv(nil), "PATH="+getenvPath(), "HOME="+getenv("HOME"), "GIT_CONFIG_GLOBAL="+getenv("GIT_CONFIG_GLOBAL"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_ASKPASS="+h.Recorder, "GIT_TERMINAL_PROMPT=1")
	if _, err := runCmdIn(context.Background(), t.TempDir(), env, []string{"git", "ls-remote", h.URL}, nil); err == nil {
		t.Fatal("ls-remote against a 401 endpoint succeeded")
	}
	h.Check(t, "overlay")
}

func getenv(k string) string { return os.Getenv(k) }
func getenvPath() string     { return os.Getenv("PATH") }

// Over ssh, OpenSSH is told never to use askpass, and the user's own ssh
// selection (GIT_SSH_COMMAND, GIT_SSH, core.sshCommand) still decides which
// ssh runs.
func TestSSHNeverAsksAndKeepsTheUsersSSH(t *testing.T) {
	gitenvtest.New(t) // hermetic config
	dir := t.TempDir()
	log := dir + "/ssh.log"
	fake := dir + "/fake-ssh"
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"$(basename $0) REQUIRE=$SSH_ASKPASS_REQUIRE ASKPASS=$SSH_ASKPASS\" >> '"+log+"'\nexit 255\n"), 0o755)
	os.Symlink(fake, dir+"/ssh")
	os.Symlink(fake, dir+"/user-ssh")
	t.Setenv("SSH_ASKPASS", "/usr/bin/ksshaskpass")
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	cases := []struct {
		name string
		set  func()
		want string
	}{
		{"default ssh from PATH", func() { t.Setenv("PATH", dir+":"+os.Getenv("PATH")) }, "ssh REQUIRE=never ASKPASS="},
		{"GIT_SSH_COMMAND", func() { t.Setenv("GIT_SSH_COMMAND", dir+"/user-ssh") }, "user-ssh REQUIRE=never ASKPASS="},
		{"GIT_SSH", func() { os.Unsetenv("GIT_SSH_COMMAND"); t.Setenv("GIT_SSH", dir+"/user-ssh") }, "user-ssh REQUIRE=never ASKPASS="},
		{"core.sshCommand", func() {
			os.Unsetenv("GIT_SSH")
			os.WriteFile(os.Getenv("GIT_CONFIG_GLOBAL"), []byte("[core]\n\tsshCommand = "+dir+"/user-ssh\n"), 0o644)
		}, "user-ssh REQUIRE=never ASKPASS="},
	}
	for _, c := range cases {
		os.Remove(log)
		c.set()
		git(context.Background(), t.TempDir(), "ls-remote", "ssh://git@example.invalid/repo.git")
		b, _ := os.ReadFile(log)
		lines := strings.Split(string(bytes.TrimSpace(b)), "\n")
		for _, got := range lines { // git may run ssh twice (a variant probe)
			if got != c.want {
				t.Errorf("%s: ssh ran as %q, want %q (all: %q)", c.name, got, c.want, lines)
				break
			}
		}
	}
}
