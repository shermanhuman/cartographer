//go:build cartographer_extra

package osdep

// OnExtra exists only with -tags=cartographer_extra, which the user's
// GOFLAGS (or go env file) must not reach the indexer with.
func OnExtra() bool { return true }
