package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shermanhuman/cartographer/internal/workspace"
)

// extract scip puts its index in $TMPDIR, which the builders set to the
// build's scratch: run the real extract as a plugin with a fake scip-go that
// records where it was told to write.
func TestExtractSCIPUsesTheBuildScratch(t *testing.T) {
	scratch := t.TempDir()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.26\n"), 0o644)
	os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\n"), 0o644)
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "scip.log")
	os.WriteFile(filepath.Join(bin, "scip-go"), []byte("#!/bin/sh\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && { echo \"$2\" >> '"+log+"'; : > \"$2\"; }; shift; done\nexit 0\n"), 0o755)
	env := []string{"CARTOGRAPHER_TEST_CLI=1", "TMPDIR=" + scratch, "PATH=" + bin + ":" + os.Getenv("PATH")}
	workspace.RunPlugin(context.Background(), scratch, env,
		[]string{os.Args[0], "extract", "scip", "--lang", "go", "--repo", "x", "--root", root, "--revision", "abc"},
		filepath.Join(scratch, "out.json"), "x")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal("the fake scip-go never ran")
	}
	for _, p := range strings.Fields(string(b)) {
		if !strings.HasPrefix(p, scratch+string(filepath.Separator)) {
			t.Errorf("scip-go wrote its index to %s, outside the build's scratch %s", p, scratch)
		}
	}
}
