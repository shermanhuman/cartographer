package project

// SetHookLocked sets a function every cycle runs first, under the lock.
func SetHookLocked(f func()) (restore func()) {
	old := hookLocked
	hookLocked = f
	return func() { hookLocked = old }
}
