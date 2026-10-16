//go:build !unix

package workspace

import "os/exec"

// procGroup is a no-op where there are no process groups: cancelling
// kills the command itself only.
type procGroup struct{}

func ownGroup(cmd *exec.Cmd) *procGroup { return &procGroup{} }
func (g *procGroup) started()           {}
func (g *procGroup) done()              {}

// KillGroups does nothing here: console interrupts reach the commands.
func KillGroups() {}
