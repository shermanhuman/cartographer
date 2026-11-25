// Package gitenv keeps git and OpenSSH from prompting. Cartographer runs git
// in the background (serve, MCP, detached rebuilds), where a prompt has no
// one to answer it: a terminal prompt hangs, and an inherited askpass program
// opens a dialog on the user's desktop. Credentials still come from the
// machine's own credential helpers; this package only removes the prompts.
//
// It imports nothing from cartographer, so any package can use it.
package gitenv

import (
	"os"
	"strings"
)

// promptKeys are the variables that make git or ssh ask a person.
var promptKeys = []string{"GIT_ASKPASS", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "GIT_TERMINAL_PROMPT"}

// Promptless returns env with every prompt variable replaced: no terminal
// prompt, OpenSSH's askpass never used, and GIT_ASKPASS set to askpass. An
// empty askpass is meaningful: git takes a set-but-empty GIT_ASKPASS as "no
// program" and doesn't fall back to core.askPass or SSH_ASKPASS. Later
// entries win in an exec environment, so callers apply this last, after any
// overlay of their own.
func Promptless(env []string, askpass string) []string {
	out := make([]string, 0, len(env)+len(promptKeys))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !isPromptKey(k) {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+askpass, "SSH_ASKPASS_REQUIRE=never")
}

func isPromptKey(k string) bool {
	for _, p := range promptKeys {
		if k == p {
			return true
		}
	}
	return false
}

// Apply makes this process's own environment promptless, so every child
// started without an explicit environment, and every child whose environment
// is built from os.Environ(), inherits it.
func Apply() {
	for _, k := range promptKeys {
		os.Unsetenv(k)
	}
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	os.Setenv("GIT_ASKPASS", "")
	os.Setenv("SSH_ASKPASS_REQUIRE", "never")
}
