package project

import (
	"context"

	"github.com/shermanhuman/cartographer/internal/store"
)

// SetPrune replaces the prune run after each import.
func SetPrune(f func(context.Context, *store.Store) error) (restore func()) {
	old := prune
	prune = f
	return func() { prune = old }
}

// SetHookLocked sets a function every cycle runs first, under the lock.
func SetHookLocked(f func()) (restore func()) {
	old := hookLocked
	hookLocked = f
	return func() { hookLocked = old }
}
