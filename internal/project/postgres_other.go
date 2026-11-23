//go:build !unix

package project

import (
	"os"
	"os/exec"
)

func setGroup(*exec.Cmd) {}

func killGroup(p *os.Process) { p.Kill() }
