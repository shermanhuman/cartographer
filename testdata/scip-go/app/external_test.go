package app_test

import "testing"

// helper is the external test package's own: it must not merge with
// package app's helper.
func helper() int { return 1 }

func TestExternal(t *testing.T) { _ = helper() }
