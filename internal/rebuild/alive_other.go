//go:build !unix

package rebuild

// alive can't check a pid here; the lock's age (Stale) decides alone.
func alive(pid int) bool { return true }
