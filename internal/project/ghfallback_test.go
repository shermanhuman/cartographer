package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGHFallbackAppendsAfterExistingConfig(t *testing.T) {
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got := strings.Join(ghFallback([]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=1"}), "\n")
	for _, want := range []string{
		"GIT_CONFIG_KEY_2=credential.https://github.com.helper",
		"GIT_CONFIG_VALUE_2=!" + gh + " auth git-credential",
		"GIT_CONFIG_COUNT=3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	t.Setenv("PATH", t.TempDir()) // no gh
	if got := ghFallback(nil); got != nil {
		t.Errorf("without gh: %v", got)
	}
}
