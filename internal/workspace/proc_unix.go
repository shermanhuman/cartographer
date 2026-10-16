//go:build unix

package workspace

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ownGroup runs cmd in its own process group and makes cancelling it end
// the whole group: SIGTERM, then SIGKILL once cmd.WaitDelay has passed, so
// grandchildren (sh -c '… &', mix) don't outlive the build.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		err := syscall.Kill(-pgid, syscall.SIGTERM)
		time.AfterFunc(cmd.WaitDelay, func() { syscall.Kill(-pgid, syscall.SIGKILL) })
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}
