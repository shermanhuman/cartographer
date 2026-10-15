// Package ident splits code identifiers into lowercase words for search and
// vocabulary: "BreakdownAdmin.Crm.syncShops/2" -> breakdown admin crm sync shops.
package ident

import (
	"strings"
	"unicode"
)

// Words returns the lowercase words (length >= 2, no digits-only) in s, in
// order, without duplicates.
func Words(s string) []string {
	var out []string
	seen := map[string]bool{}
	emit := func(w []rune) {
		if len(w) < 2 {
			return
		}
		word := strings.ToLower(string(w))
		if allDigits(word) || seen[word] {
			return
		}
		seen[word] = true
		out = append(out, word)
	}
	var cur []rune
	rs := []rune(s)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			emit(cur)
			cur = nil
			continue
		}
		if len(cur) > 0 && unicode.IsUpper(r) {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			// fooBar -> foo|Bar ; HTTPServer -> HTTP|Server
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				emit(cur)
				cur = nil
			}
		}
		cur = append(cur, r)
	}
	emit(cur)
	return out
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
