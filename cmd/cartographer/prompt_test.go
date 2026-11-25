package main

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// Every command makes the process promptless first, so a detached child, a
// plugin or mise started without an explicit environment inherits it.
func TestRunMakesTheProcessPromptless(t *testing.T) {
	t.Setenv("SSH_ASKPASS", "/usr/bin/ksshaskpass")
	t.Setenv("GIT_ASKPASS", "/usr/bin/ksshaskpass")
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &out); err != nil {
		t.Fatal(err)
	}
	if v, ok := os.LookupEnv("GIT_ASKPASS"); !ok || v != "" {
		t.Errorf("GIT_ASKPASS = %q (set %v), want set and empty", v, ok)
	}
	if _, ok := os.LookupEnv("SSH_ASKPASS"); ok {
		t.Error("SSH_ASKPASS still set")
	}
	if v := os.Getenv("SSH_ASKPASS_REQUIRE"); v != "never" {
		t.Errorf("SSH_ASKPASS_REQUIRE = %q", v)
	}
	if v := os.Getenv("GIT_TERMINAL_PROMPT"); v != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q", v)
	}
}
