package osdep

// OnLinux is built with GOOS=linux, which the indexer always uses.
func OnLinux() string { return "linux" }
