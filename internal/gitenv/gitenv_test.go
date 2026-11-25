package gitenv

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func lookup(env []string, k string) (string, int) {
	var v string
	n := 0
	for _, kv := range env {
		if key, val, _ := strings.Cut(kv, "="); key == k {
			v, n = val, n+1
		}
	}
	return v, n
}

func TestPromptless(t *testing.T) {
	in := []string{"PATH=/bin", "GIT_ASKPASS=/usr/bin/ksshaskpass", "SSH_ASKPASS=/usr/bin/ksshaskpass", "SSH_ASKPASS_REQUIRE=force", "GIT_TERMINAL_PROMPT=1", "HOME=/h"}
	out := Promptless(in, "")
	for k, want := range map[string]string{"GIT_ASKPASS": "", "GIT_TERMINAL_PROMPT": "0", "SSH_ASKPASS_REQUIRE": "never"} {
		if v, n := lookup(out, k); n != 1 || v != want {
			t.Errorf("%s = %q (%d entries), want %q once", k, v, n, want)
		}
	}
	if _, n := lookup(out, "SSH_ASKPASS"); n != 0 {
		t.Error("SSH_ASKPASS kept")
	}
	if !slices.Contains(out, "PATH=/bin") || !slices.Contains(out, "HOME=/h") {
		t.Errorf("other variables lost: %q", out)
	}
	if v, _ := lookup(Promptless(in, "/state/askpass"), "GIT_ASKPASS"); v != "/state/askpass" {
		t.Errorf("helper not used: %q", v)
	}
	if in[1] != "GIT_ASKPASS=/usr/bin/ksshaskpass" {
		t.Error("input modified")
	}
}

func TestApply(t *testing.T) {
	t.Setenv("SSH_ASKPASS", "/usr/bin/ksshaskpass")
	t.Setenv("GIT_ASKPASS", "/usr/bin/ksshaskpass")
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	Apply()
	env := Promptless(nil, "") // the expected shape
	for _, k := range []string{"GIT_ASKPASS", "GIT_TERMINAL_PROMPT", "SSH_ASKPASS_REQUIRE"} {
		want, _ := lookup(env, k)
		if got, ok := lookupEnv(k); !ok || got != want {
			t.Errorf("after Apply %s = %q (set %v), want %q", k, got, ok, want)
		}
	}
	if _, ok := lookupEnv("SSH_ASKPASS"); ok {
		t.Error("SSH_ASKPASS still set")
	}
}

func lookupEnv(k string) (string, bool) { return os.LookupEnv(k) }
