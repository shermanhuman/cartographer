//go:build !unix

package main

import "os/exec"

// detach is a no-op where there are no process sessions.
func detach(cmd *exec.Cmd) {}
