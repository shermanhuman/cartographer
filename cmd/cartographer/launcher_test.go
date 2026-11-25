package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// launcherScripts returns the MCP and hook launchers' sh scripts.
func launcherScripts(t *testing.T) (mcp, hook string) {
	t.Helper()
	var m struct {
		MCPServers map[string]struct{ Args []string } `json:"mcpServers"`
	}
	b, err := os.ReadFile("../../agent-plugin/.mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var h struct {
		Hooks map[string][]struct{ Hooks []struct{ Args []string } }
	}
	b, err = os.ReadFile("../../agent-plugin/hooks/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	return m.MCPServers["cartographer"].Args[1], h.Hooks["PreToolUse"][0].Hooks[0].Args[1]
}

// fakeBin writes a cartographer that prints its name, args and prompt
// variables, and exits with code.
func fakeBin(t *testing.T, path, name, code string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("#!/bin/sh\necho \""+name+" $* ASKPASS=[$GIT_ASKPASS] REQ=$SSH_ASKPASS_REQUIRE SSHA=[$SSH_ASKPASS]\"\nexit "+code+"\n"), 0o755)
}

// The launchers run mise's configured cartographer; without one they run a
// genuine non-mise build on PATH, never a stale mise install, a mise shim or
// an alias into mise; with nothing found MCP exits 127 and the hook 0.
func TestLaunchersPickTheRightCartographer(t *testing.T) {
	mcp, hook := launcherScripts(t)
	root := t.TempDir()
	good := filepath.Join(root, "data/mise/installs/github-shermanhuman-cartographer/0.4.1/cartographer")
	stale := filepath.Join(root, "data/mise/installs/github-shermanhuman-cartographer/0.1.2/cartographer")
	shim := filepath.Join(root, "data/mise/shims/cartographer")
	alias := filepath.Join(root, "alias/cartographer")
	plain := filepath.Join(root, "opt/bin/cartographer")
	fakeBin(t, good, "good", "3")
	fakeBin(t, stale, "stale", "0")
	fakeBin(t, shim, "shim", "0")
	fakeBin(t, plain, "plain", "0")
	os.MkdirAll(filepath.Dir(alias), 0o755)
	os.Symlink(stale, alias)
	miseDir := filepath.Join(root, "misebin")
	os.MkdirAll(miseDir, 0o755)
	writeMise := func(resolves bool) {
		body := "#!/bin/sh\n[ \"$1 $2\" = \"which cartographer\" ] || exit 2\n"
		if resolves {
			body += "echo '" + good + "'\n"
		} else {
			body += "echo 'mise ERROR cartographer is not a mise bin' >&2; exit 1\n"
		}
		os.WriteFile(filepath.Join(miseDir, "mise"), []byte(body), 0o755)
	}
	run := func(script string, path ...string) (string, int) {
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env = []string{"PATH=" + strings.Join(append(path, "/usr/bin", "/bin"), ":"), "HOME=" + root,
			"GIT_ASKPASS=/usr/bin/ksshaskpass", "SSH_ASKPASS=/usr/bin/ksshaskpass", "SSH_ASKPASS_REQUIRE=force"}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		code := 0
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out)), code
	}
	cases := []struct {
		name     string
		mise     int // 0 absent, 1 resolves, 2 has none
		path     []string
		mcpOut   string
		mcpCode  int
		hookRuns string
	}{
		{"mise resolves, stale install first on PATH", 1, []string{filepath.Dir(stale), miseDir}, "good mcp", 3, "good hook grep"},
		{"mise has none, non-mise build on PATH", 2, []string{miseDir, filepath.Dir(plain)}, "plain mcp", 0, "plain hook grep"},
		{"mise has none, only a stale install", 2, []string{miseDir, filepath.Dir(stale)}, "", 127, ""},
		{"mise has none, only a shim", 2, []string{miseDir, filepath.Dir(shim)}, "", 127, ""},
		{"mise has none, only an alias into mise", 2, []string{miseDir, filepath.Dir(alias)}, "", 127, ""},
		{"no mise, non-mise build on PATH", 0, []string{filepath.Dir(plain)}, "plain mcp", 0, "plain hook grep"},
	}
	for _, c := range cases {
		os.Remove(filepath.Join(miseDir, "mise"))
		if c.mise > 0 {
			writeMise(c.mise == 1)
		}
		out, code := run(mcp, c.path...)
		if code != c.mcpCode || (c.mcpOut != "" && !strings.HasPrefix(out, c.mcpOut)) || (c.mcpOut == "" && !strings.Contains(out, "cartographer not found")) {
			t.Errorf("%s: mcp launcher ran %q, exit %d; want %q, exit %d", c.name, out, code, c.mcpOut, c.mcpCode)
		}
		if c.mcpOut != "" && !strings.Contains(out, "ASKPASS=[] REQ=never SSHA=[]") {
			t.Errorf("%s: launched with a prompt variable: %q", c.name, out)
		}
		out, code = run(hook, c.path...)
		if code != 0 || (c.hookRuns != "" && !strings.HasPrefix(out, c.hookRuns)) || (c.hookRuns == "" && out != "") {
			t.Errorf("%s: hook launcher ran %q, exit %d; want %q, exit 0", c.name, out, code, c.hookRuns)
		}
	}
}
