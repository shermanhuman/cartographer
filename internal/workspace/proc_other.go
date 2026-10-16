//go:build !unix

package workspace

import "os/exec"

// ownGroup leaves cmd as is: cancelling kills the command itself only.
func ownGroup(cmd *exec.Cmd) {}
