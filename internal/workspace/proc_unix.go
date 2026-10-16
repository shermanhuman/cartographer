//go:build unix

package workspace

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// groups holds the process group ids of running plugin and prepare
// commands, for KillGroups.
var groups sync.Map

// procGroup is a command run in its own process group.
type procGroup struct {
	cmd       *exec.Cmd
	mu        sync.Mutex
	cancelled bool
}

// ownGroup runs cmd in its own process group so cancelling ends the whole
// group, grandchildren (sh -c '… &', mix) included: SIGTERM on cancel; the
// command gets cmd.WaitDelay before Go kills it, and once Wait returns the
// rest of the group is killed (done).
func ownGroup(cmd *exec.Cmd) *procGroup {
	g := &procGroup{cmd: cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		g.mu.Lock()
		g.cancelled = true
		g.mu.Unlock()
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return g
}

// started registers the group once cmd has started.
func (g *procGroup) started() { groups.Store(g.cmd.Process.Pid, struct{}{}) }

// done runs after Wait: a cancelled command's remaining group members
// (those ignoring SIGTERM) are killed now, synchronously, while the group
// id can't yet belong to anyone else; then it is forgotten.
func (g *procGroup) done() {
	pgid := g.cmd.Process.Pid
	g.mu.Lock()
	cancelled := g.cancelled
	g.mu.Unlock()
	if cancelled {
		syscall.Kill(-pgid, syscall.SIGKILL)
	}
	groups.Delete(pgid)
}

// KillGroups SIGKILLs the process group of every running plugin and
// prepare command: a second interrupt exits without waiting for them, and
// they no longer get the terminal's signals themselves.
func KillGroups() {
	groups.Range(func(k, _ any) bool {
		syscall.Kill(-k.(int), syscall.SIGKILL)
		return true
	})
}
