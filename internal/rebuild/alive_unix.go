//go:build unix

package rebuild

import (
	"errors"
	"syscall"
)

// alive reports whether a process with this pid exists (signal 0 checks
// without signalling; EPERM means it exists under another user).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
