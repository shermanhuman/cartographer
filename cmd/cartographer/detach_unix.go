//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detach puts the rebuild child in its own session, so it outlives the
// command that started it and never receives its terminal's signals.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
