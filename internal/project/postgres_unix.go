//go:build unix

package project

import (
	"os"
	"os/exec"
	"syscall"
)

func setGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killGroup SIGKILLs the process group p leads.
func killGroup(p *os.Process) { syscall.Kill(-p.Pid, syscall.SIGKILL) }
