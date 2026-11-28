package redact

import (
	"strings"
	"unicode"
)

// Truncate shortens s to at most n runes plus "...", cutting at the last
// whitespace so no word is split. A split word can no longer be recognised
// as personal data or a secret ("alice@exampl", "4111 1111 11"), so the
// redactor would let the visible part through. If the first word alone is
// longer than n, Truncate drops it and returns just "...".
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	head := string(r[:n])
	// Unless the cut falls exactly on a word boundary, drop the split word.
	if !unicode.IsSpace(r[n]) {
		cut := strings.LastIndexFunc(head, unicode.IsSpace)
		if cut < 0 {
			return "..."
		}
		head = head[:cut]
	}
	// A number written in groups ("4111 1111 1111 1111", "+233 24 123 4567")
	// spans several words, so its first groups survive the cut above. Drop
	// trailing number-like words too.
	words := strings.Fields(head)
	for len(words) > 0 && numberLike(words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		return "..."
	}
	// Keep the original spacing of what remains.
	last := words[len(words)-1]
	end := strings.LastIndex(head, last) + len(last)
	return head[:end] + "..."
}

// numberLike reports whether w is made only of digits and the characters
// used to group phone and card numbers.
func numberLike(w string) bool {
	hasDigit := false
	for _, c := range w {
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case strings.ContainsRune("+-.()/", c):
		default:
			return false
		}
	}
	return hasDigit
}
