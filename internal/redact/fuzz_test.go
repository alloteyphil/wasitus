package redact

import (
	"maps"
	"strings"
	"testing"
)

// leaks are values that must never survive redaction, whatever text
// surrounds them.
func leaks() []string {
	return []string{
		"orders-ops@wallet-service.example",
		"+233 24 123 4567",
		"+1-415-555-0100",
		"024 123 4567",
		"0244123456",
		"4111 1111 1111 1111",
		"378282246310005",
		jwt,
		ghClassic,
		ghFine,
		awsKeyID,
		"Bearer " + sample,
		"password=" + sample,
		`"client_secret": "` + sample + `"`,
		labURLAuth,
	}
}

// FuzzString checks the promises of String on any input: it never panics,
// it is idempotent, and a known secret placed in the text never survives.
func FuzzString(f *testing.F) {
	for _, tc := range redactCases {
		f.Add(tc.in)
	}
	for _, in := range keepCases {
		f.Add(in)
	}
	for _, in := range []string{
		"\xff\xfe\x00",
		`password="`,
		`\"token\":\"`,
		"token=]]]]]]",
		"+",
		"++233 24 123 4567",
		"0.0244123456",
		"4111 1111 1111 1111 4111 1111 1111 1111",
		"https://:@",
		"Bearer ",
		"eyJ.eyJ.",
		"password=[token][email]",
	} {
		f.Add(in)
	}

	r := New()
	secrets := leaks()
	f.Fuzz(func(t *testing.T, s string) {
		out := r.String(s)
		if again := r.String(out); again != out {
			t.Fatalf("not idempotent:\n  in    %q\n  once  %q\n  twice %q", s, out, again)
		}
		// The planted copy of each secret must be removed. The fuzzed text
		// around it may legitimately contain the same characters inside
		// something that is not a secret (the digits of 0244123456 inside
		// invoice INV-2026-0244123456, or in 0.0244123456), so compare counts
		// rather than requiring the characters to vanish everywhere.
		for _, secret := range secrets {
			in := s + " " + secret + " " + s
			if got := r.String(in); strings.Count(got, secret) >= strings.Count(in, secret) {
				t.Fatalf("%q survived in %q", secret, got)
			}
		}
	})
}

// FuzzMap checks that Map never panics, leaves its input alone, and redacts
// every value as String does, or whole when the key names a secret.
func FuzzMap(f *testing.F) {
	f.Add("user.email", "orders-ops@wallet-service.example")
	f.Add("db.password", sample)
	f.Add("http.status_code", "402")
	f.Add("Authorization", "")
	f.Add("", "\xff")

	r := New()
	f.Fuzz(func(t *testing.T, key, value string) {
		in := map[string]string{key: value, "fixed": "0244123456"}
		before := maps.Clone(in)
		out := r.Map(in)
		if !maps.Equal(in, before) {
			t.Fatalf("Map changed its input: %q", in)
		}
		if out["fixed"] != phone {
			t.Fatalf(`out["fixed"] = %q, want %q`, out["fixed"], phone)
		}
		if want := r.String(value); out[key] != want && out[key] != token && key != "fixed" {
			t.Fatalf("out[%q] = %q, want %q or %q", key, out[key], want, token)
		}
		if again := r.Map(out); !maps.Equal(again, out) {
			t.Fatalf("Map not idempotent: %q then %q", out, again)
		}
	})
}
