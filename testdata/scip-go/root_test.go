// Package fixture_test is the module root's external test package: its
// ids read ./[test], apart from the root package's ./ ones.
package fixture_test

import "testing"

func helper() int { return 2 }

func TestRoot(t *testing.T) { _ = helper() }
