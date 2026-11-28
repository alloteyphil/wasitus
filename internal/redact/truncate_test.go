package redact

import (
	"strings"
	"testing"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short stays", "hello world", 20, "hello world"},
		{"exact length stays", "hello", 5, "hello"},
		{"cut drops the split word", "contact alice.smith@example.com now", 20, "contact..."},
		{"cut on a boundary keeps the word", "hello world again", 11, "hello world..."},
		{"grouped card number is dropped whole", "card 4111 1111 1111 1111 declined", 17, "card..."},
		{"grouped phone number is dropped whole", "call +233 24 123 4567 now", 14, "call..."},
		{"numbers inside text before the cut stay", "HTTP 502 from orders after retry", 22, "HTTP 502 from orders..."},
		{"one long word", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", 10, "..."},
		{"runes, not bytes", "héllo wörld ünïcode", 12, "héllo wörld..."},
		{"trailing spaces trimmed", "abc    def", 6, "abc..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Truncate(tc.in, tc.n); got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// TestTruncateNeverLeaksPartialPII is the regression test: cutting raw text
// before redaction used to leave fragments the redactor could not recognise.
func TestTruncateNeverLeaksPartialPII(t *testing.T) {
	r := New()
	secrets := []string{
		"alice.smith@example.com",
		"4111111111111111",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"+233241234567",
		"4111 1111 1111 1111",
		"+233 24 123 4567",
	}
	for _, secret := range secrets {
		msg := "payment for order failed for customer " + secret + " please retry"
		for n := 1; n < len([]rune(msg)); n++ {
			got := r.String(Truncate(msg, n))
			for k := 6; k < len(secret); k++ { // any visible leading fragment of 6+ characters
				if frag := secret[:k]; strings.Contains(got, frag) {
					t.Fatalf("n=%d leaked fragment %q of %q: %q", n, frag, secret, got)
				}
			}
		}
	}
}
