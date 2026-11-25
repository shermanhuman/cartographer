package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

// Schema prepare and capture keep their temp files in the build's scratch,
// even when the repo's own [schema] env names another TMPDIR.
func TestSchemaStepTempFilesGoInScratch(t *testing.T) {
	scratch := t.TempDir()
	log := filepath.Join(t.TempDir(), "tmpdir.log")
	_, err := RunSchema(context.Background(), SchemaStep{
		Root: t.TempDir(), Dir: t.TempDir(),
		Prepare:  `echo "prepare $TMPDIR" >> '` + log + `'`,
		Command:  []string{"sh", "-c", `echo "capture $TMPDIR" >> '` + log + `'; exit 1`},
		Env:      map[string]string{"TMPDIR": "/somewhere/else"},
		Key:      "k",
		CacheDir: t.TempDir(),
		Scratch:  scratch,
	})
	if err == nil {
		t.Fatal("a capture that exits 1 succeeded")
	}
	b, _ := os.ReadFile(log)
	if got, want := strings.TrimSpace(string(b)), "prepare "+scratch+"\ncapture "+scratch; got != want {
		t.Errorf("TMPDIR seen:\n%s\nwant:\n%s", got, want)
	}
}

// The builder runs a plugin with TMPDIR set to its scratch, over the
// plugin's own env, and the scratch goes with the build.
func TestBuilderGivesPluginsItsScratch(t *testing.T) {
	ws, _, _ := setup(t)
	seen := filepath.Join(t.TempDir(), "tmpdir.log")
	toml := filepath.Join(ws, FileName)
	b, _ := os.ReadFile(toml)
	s := strings.Replace(string(b), `env = { SF = "set" }`, `env = { SF = "set", TMPDIR = "/elsewhere" }`, 1)
	s = strings.Replace(s, `command = ["sh", "-c", "test -f`, `command = ["sh", "-c", "echo \"$TMPDIR\" >> `+seen+` && test -f`, 1)
	os.WriteFile(toml, []byte(s), 0o644)
	c, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), c, BuildOptions{Out: filepath.Join(t.TempDir(), "snap"), CacheDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(mustRead(t, seen))
	if got == "" || got == "/elsewhere" || !strings.Contains(filepath.Base(got), "cartographer-build-") {
		t.Fatalf("the plugin saw TMPDIR=%q, want the build's scratch", got)
	}
	if _, err := os.Stat(got); err == nil {
		t.Errorf("the scratch %s outlived the build", got)
	}
}
