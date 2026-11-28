package redact

import "testing"

// TestReviewGaps covers personal data and secrets an independent review found
// passing through unchanged, and evidence that must still survive.
func TestReviewGaps(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		in   string
		want string
	}{
		// key => value (Ruby, PHP, Perl logs) used to print the whole secret.
		{"arrow separator", "api_key => 'abc123'", "api_key => '[token]'"},
		{"arrow password", `password => "hunter2"`, `password => "[token]"`},
		{"pwd key", "pwd=hunter2", "pwd=[token]"},
		{"passphrase key", "passphrase: correct horse", "passphrase: [token] horse"},
		{"private_key key", `private_key="abc"`, `private_key="[token]"`},

		// Secrets with a known shape.
		{"pem block", "key -----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY----- done", "key [token] done"},
		{"cut pem block", "key -----BEGIN PRIVATE KEY-----\nMIIEow", "key [token]"},
		{"slack bot token", "token xoxb-1234567890-abcdefghij", "token [token]"},
		{"stripe live key", "using sk_live_abcdefghijklmnop", "using [token]"},
		{"google api key", "AIzaSyA1234567890abcdefghijklmnopqrstuv failed", "[token] failed"},
		{"slack webhook", "POST https://hooks.slack.com/services/T000/B000/XXXX failed", "POST https://[token] failed"},
		{"aws presigned signature", "GET https://b.s3.amazonaws.com/f?X-Amz-Signature=abc123&X-Amz-Date=2026", "GET https://b.s3.amazonaws.com/f?X-Amz-Signature=[token]&X-Amz-Date=2026"},
		{"azure sas sig", "https://a.blob.core.windows.net/c?sv=2020&sig=abc%2Fdef", "https://a.blob.core.windows.net/c?sv=2020&sig=[token]"},
		{"key query param", "GET /maps?key=abc123&q=x", "GET /maps?key=[token]&q=x"},

		// Phone numbers.
		{"bracket area code", "call (024) 123 4567 now", "call [phone] now"},
		{"bracket with country code", "call +1 (415) 555-0100", "call [phone]"},
		{"bracket trunk zero", "call +44 (0)20 7946 0958", "call [phone]"},
		{"bare ghana msisdn", "msisdn:233241234567 failed", "msisdn:[phone] failed"},
		{"bare nigeria msisdn", "to 2348031234567", "to [phone]"},
		{"00 international", "to 00233241234567", "to [phone]"},

		// Evidence that must survive.
		{"epoch milliseconds", "at 1764311234567 ms", "at 1764311234567 ms"},
		{"order id", "order ord_000312 failed", "order ord_000312 failed"},
		{"http status in brackets", "upstream (503) 12 retries", "upstream (503) 12 retries"},
		{"prose credentials", "invalid credentials: user not found", "invalid credentials: user not found"},
		{"passed count", "passed: 12", "passed: 12"},
		{"query without secrets", "GET /search?q=shoes&page=2", "GET /search?q=shoes&page=2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.String(tc.in); got != tc.want {
				t.Errorf("String(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
			if got := r.String(r.String(tc.in)); got != r.String(tc.in) {
				t.Errorf("not idempotent: %q", got)
			}
		})
	}
}
