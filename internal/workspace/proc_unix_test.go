//go:build unix

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Cancelling a plugin or prepare command ends its whole process group:
// a grandchild (sh -c '… &') doesn't outlive the build, even one that
// ignores SIGTERM (killed once the command has been waited for), and the
// group is forgotten.
func TestCancelKillsTheCommandsProcessGroup(t *testing.T) {
	for _, script := range []string{"sleep 600 & echo $! > \"$1\"; wait", "trap '' TERM; sleep 600 & echo $! > \"$1\"; wait"} {
		t.Run(script[:5], func(t *testing.T) { cancelGroup(t, script) })
	}
	n := 0
	groups.Range(func(any, any) bool { n++; return true })
	if n != 0 {
		t.Errorf("%d groups still registered", n)
	}
}

func cancelGroup(t *testing.T, script string) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runCmd(ctx, t.TempDir(), nil, []string{"sh", "-c", script, "sh", pidFile}, nil)
		done <- err
	}()
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		t.Fatal("the command did not start its grandchild")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled command succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runCmd did not return after cancel")
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("grandchild %d survived the cancel", pid)
}
