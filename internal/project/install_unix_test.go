//go:build unix

package project_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
)

// A cancelled install ends the processes it started and returns at once
// (A-M6), though a child still holds its output pipe.
func TestCancelledInstallEndsItsChildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := filepath.Join(dir, "mise")
	os.WriteFile(bin, []byte(`#!/bin/sh
case $1 in
where) exit 1 ;;
install) sleep 300 & echo $! > '`+pidFile+`'; wait ;;
esac
`), 0o755)
	m := &project.Mise{Bin: bin, Dir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := m.BinPaths(ctx, project.Tool{Name: "slow", Version: "1"}); done <- err }()
	var pid int
	for deadline := time.Now().Add(10 * time.Second); pid == 0; {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if time.Now().After(deadline) {
			t.Fatal("the install never started its child")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled install succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a cancelled install didn't return (its child holds the output pipe)")
	}
	alive := true
	for deadline := time.Now().Add(5 * time.Second); alive && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		alive = syscall.Kill(pid, 0) == nil
	}
	if alive {
		t.Errorf("the install's child %d outlived the cancel", pid)
	}
}
