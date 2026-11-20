// Package stats has exactly one in-repo invocation, one external invocation,
// one external function value and one in-repo method value: 2 call sites
// (1 in-repo, 1 external) and 2 refs.
package stats

import "strings"

func helper() string { return "x" }

type T struct{}

func (T) M() {}

func Use() (func(string) string, func()) {
	_ = helper()
	_ = strings.ToUpper("x")
	return strings.ToLower, T{}.M
}
