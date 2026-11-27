package redact

import (
	"encoding/base64"
	"fmt"
	"maps"
	"strings"
	"testing"
)

// The token-shaped fixtures are assembled at run time from harmless parts, so
// that secret scanners reading this file do not report them. None of them is
// a real credential, and every name, address and number is made up.
var (
	sample     = "zz-lab-sample-7" // stands in for a password or an API key
	ghClassic  = "gh" + "p_" + strings.Repeat("lab0", 9)
	ghOAuth    = "gh" + "o_" + strings.Repeat("lab1", 9)
	ghServer   = "gh" + "s_" + strings.Repeat("lab2", 9)
	ghUser     = "gh" + "u_" + strings.Repeat("lab3", 9)
	ghFine     = "github" + "_pat_" + strings.Repeat("LAB4", 6) + "_" + strings.Repeat("lab5", 14)
	awsKeyID   = "AK" + "IA" + strings.Repeat("LAB0", 4)
	awsTempID  = "AS" + "IA" + strings.Repeat("LAB1", 4)
	jwt        = b64(`{"alg":"HS256","typ":"JWT"}`) + "." + b64(`{"sub":"orders-service"}`) + "." + b64("lab-signature")
	jwtNoSig   = b64(`{"alg":"none"}`) + "." + b64(`{"sub":"wallet-service"}`) + "."
	labURLAuth = "https://deploy:" + sample + "@orders-service.lab/v1/orders"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

type redactCase struct {
	name, in, want string
}

// redactCases must be redacted. Fuzzing uses them as seeds too.
var redactCases = []redactCase{
	// Email addresses.
	{"email", "notify orders-ops@wallet-service.example now", "notify [email] now"},
	{"email with tag and brackets", "<a.b+tag@orders.lab.example>", "<[email]>"},
	{"email in a query", "/v1/users?email=ama.mensah@mockpay.example&page=2", "/v1/users?email=[email]&page=2"},

	// Phone numbers.
	{"Ghana international with spaces", "callback +233 24 123 4567", "callback [phone]"},
	{"US international with dashes", "+1-415-555-0100", "[phone]"},
	{"UK international with spaces", "+44 20 7946 0958", "[phone]"},
	{"international ungrouped", "msisdn=+233244123456", "msisdn=[phone]"},
	{"international with dots", "+233.24.123.4567", "[phone]"},
	{"Ghana local with spaces", "024 123 4567", "[phone]"},
	{"Ghana local ungrouped", "0244123456", "[phone]"},
	{"Ghana local with dashes", "024-412-3456", "[phone]"},
	{"Ghana local with dots", "024.412.3456", "[phone]"},
	{"local 4-3-3", "0244 123 456", "[phone]"},
	{"local landline 4-6", "0302 123456", "[phone]"},
	{"local 11 digits", "0803 123 4567", "[phone]"},
	{"phone at the end of a sentence", "call 0244123456.", "call [phone]."},
	{"phone in brackets", "(0244123456)", "([phone])"},
	{"phone after a date", "2026-11-14 0244123456", "2026-11-14 [phone]"},

	// Card numbers (public test numbers).
	{"card with spaces", "card 4111 1111 1111 1111 declined", "card [card] declined"},
	{"card with dashes", "4111-1111-1111-1111", "[card]"},
	{"card ungrouped", "pan=5555555555554444;", "pan=[card];"},
	{"card 13 digits", "4222222222222", "[card]"},
	{"card 15 digits 4-6-5", "3782 822463 10005", "[card]"},
	{"card 19 digits", "6250 9410 0652 8599 016", "[card]"},
	{"card 19 digits ungrouped", "5061040000000000009", "[card]"},
	{"card after a date", "2026-11-14 4111111111111111", "2026-11-14 [card]"},
	{"phone then card", "+233 24 123 4567 4111 1111 1111 1111", "[phone] [card]"},

	// Tokens.
	{"bearer JWT", "Authorization: Bearer " + jwt, "Authorization: Bearer [token]"},
	{"bearer opaque lower case", "bearer abc123def456", "bearer [token]"},
	{"bearer long letters only", "Bearer " + strings.Repeat("abcd", 5), "Bearer [token]"},
	{"JWT alone", "token " + jwt + " rejected", "token [token] rejected"},
	{"unsigned JWT", "jwt " + jwtNoSig, "jwt [token]"},
	{"GitHub classic", "using " + ghClassic, "using [token]"},
	{"GitHub OAuth", ghOAuth, "[token]"},
	{"GitHub server", ghServer, "[token]"},
	{"GitHub user", ghUser, "[token]"},
	{"GitHub fine-grained", ghFine + ".", "[token]."},
	{"AWS key ID", "key " + awsKeyID + " denied", "key [token] denied"},
	{"AWS temporary key ID", awsTempID, "[token]"},
	{"URL password", "GET " + labURLAuth, "GET https://deploy:[token]@orders-service.lab/v1/orders"},
	{"URL password without user", "redis://:" + sample + "@cache.lab:6379/0", "redis://:[token]@cache.lab:6379/0"},

	// Secret key=value pairs.
	{"key=value", "password=" + sample + " user=ops", "password=[token] user=ops"},
	{"key: value", "db_password: " + sample, "db_password: [token]"},
	{"JSON", `{"client_secret": "` + sample + `", "x": 1}`, `{"client_secret": "[token]", "x": 1}`},
	{"JSON without spaces", `{"api_key":"` + sample + `"}`, `{"api_key":"[token]"}`},
	{"JSON escaped in a string", `{\"password\":\"` + sample + `\"}`, `{\"password\":\"[token]\"}`},
	{"single quotes", `'token': '` + sample + ` and more'`, `'token': '[token]'`},
	{"quoted logfmt value", `passwd="` + sample + ` x" next=1`, `passwd="[token]" next=1`},
	{"escaped quote inside", `"secret": "a\"` + sample + `"`, `"secret": "[token]"`},
	{"unterminated quote", `"secret": "` + sample + " to the end", `"secret": "[token]`},
	{"apikey upper case", "APIKEY=" + sample, "APIKEY=[token]"},
	{"api-key header", "X-Api-Key: " + sample, "X-Api-Key: [token]"},
	{"access token in a query", "/cb?access_token=" + sample + "&page=2", "/cb?access_token=[token]&page=2"},
	{"value with commas after", "token=" + sample + ",next=1", "token=[token],next=1"},
	{"Go map", "map[password:" + sample + " user:ops]", "map[password:[token] user:ops]"},
	{"Go map, last", "map[user:ops password:" + sample + "]", "map[user:ops password:[token]]"},
	{"value with brackets inside", "token=(" + sample + ")", "token=[token]"},
	{"basic auth header", "Authorization: Basic " + b64("ops:"+sample), "Authorization: Basic [token]"},
	{"auth scheme with nothing after", "authorization: Basic", "authorization: [token]"},
	{"bearer word as value", "Authorization: Bearer token", "Authorization: Bearer [token]"},
	// Regexp matches never overlap: these once hid the token behind a first
	// "Bearer" whose value was the word "Bearer" (found by FuzzString).
	{"bearer twice", "Bearer  Bearer " + sample, "Bearer  Bearer [token]"},
	{"bearer prose then token", "Bearer token expired; retried with Bearer " + sample, "Bearer token expired; retried with Bearer [token]"},
	{"quoted header", `"Authorization": "Bearer ` + sample + `"`, `"Authorization": "Bearer [token]"`},
	{"base64 padding", "secret=" + b64("ops") + "==", "secret=[token]"},
	{"key with prefix", "--token=" + sample, "--token=[token]"},
}

// keepCases must come back unchanged: false positives make the output useless.
var keepCases = []string{
	"2026-11-14T03:05:00Z",
	"2026-11-14T05:47:00.123456789+03:00",
	"2026-11-14 03:05",
	"2026-11-14 03:05:59",
	"14-11-2026 03:05",
	"took 1005ms, timeout after 30s, p99 1.5s",
	"HTTP 503 Service Unavailable",
	"status=401",
	"orders-service v1.4.0, collector 1.47.0, go1.27.4",
	"order ord_000123 charge ch_000042",
	"trace 4bf92f3577b34da6a3ce929d0e0e4736 span 00f067aa0ba902b7",
	"trace_id=12345678901234567890123456789012",
	"192.168.68.57:5555 10.0.0.1 [::1]:8080",
	"42 retries, 12345 rows",
	"amount 2500, total GHS 50,000.00, limit 1,234,567.89",
	"https://orders-service.lab:8443/v1/orders/ord_000123?status=401&trace_id=4bf92f3577b34da6a3ce929d0e0e4736",
	"https://secrets.lab:8200/v1/token/lookup",
	"4111111111111112",
	"epoch 1763089500000 1763089500000000000",
	"ratio 0.4123456789 and 3.4111111111111111",
	"Bearer token expired",
	`WWW-Authenticate: Bearer realm="lab"`,
	"logo@2x.png not found",
	"padded 0000000042 and 0012345678",
	"024 1 2345678", // a one-digit group is not a phone grouping
	"invoice INV-2026-0244123456",
	"[email] [phone] [card] [token]",
	"password=",
	`"password": ""`,
	"no secrets here",
	"",
}

func TestStringRedacts(t *testing.T) {
	r := New()
	for _, tc := range redactCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.String(tc.in); got != tc.want {
				t.Errorf("String(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStringKeeps(t *testing.T) {
	r := New()
	for _, in := range keepCases {
		t.Run(in, func(t *testing.T) {
			if got := r.String(in); got != in {
				t.Errorf("String(%q) = %q, want it unchanged", in, got)
			}
		})
	}
}

func TestStringLogLine(t *testing.T) {
	in := "2026-11-14T03:05:00Z orders-service POST " +
		"https://deploy:" + sample + "@mockpay.lab:8090/v1/charges status=402 duration=1005ms " +
		"trace_id=4bf92f3577b34da6a3ce929d0e0e4736 customer=orders-ops@wallet-service.example " +
		"phone=+233 24 123 4567 card=4111 1111 1111 1111 amount=50,000.00 charge=ch_000042 " +
		"Authorization: Bearer " + jwt
	want := "2026-11-14T03:05:00Z orders-service POST " +
		"https://deploy:[token]@mockpay.lab:8090/v1/charges status=402 duration=1005ms " +
		"trace_id=4bf92f3577b34da6a3ce929d0e0e4736 customer=[email] " +
		"phone=[phone] card=[card] amount=50,000.00 charge=ch_000042 " +
		"Authorization: Bearer [token]"
	if got := New().String(in); got != want {
		t.Errorf("String()\n got %q\nwant %q", got, want)
	}
}

// TestOnePassSettles checks that a single pass already gives the final text
// for every table case: the loop in String is a safety net, not something
// the patterns depend on.
func TestOnePassSettles(t *testing.T) {
	r := New()
	for _, tc := range redactCases {
		once := r.pass(tc.in)
		if twice := r.pass(once); twice != once {
			t.Errorf("%s: second pass changed %q to %q", tc.name, once, twice)
		}
	}
}

func TestPlaceholdersDoNotRematch(t *testing.T) {
	r := New()
	contexts := []string{
		"%s",
		"password=%s",
		`"token": "%s"`,
		`{\"secret\":\"%s\"}`,
		"map[password:%s]",
		"Authorization: Bearer %s",
		"Authorization: Basic %s",
		"https://deploy:%s@orders-service.lab",
		"%s@orders.lab.example",
		"+233 %s",
		"4111 %s 1111",
		"%s%s",
	}
	for _, p := range []string{email, phone, card, token} {
		for i, step := range r.steps {
			if got := step(p); got != p {
				t.Errorf("step %d changed placeholder %q to %q", i, p, got)
			}
		}
		for _, c := range contexts {
			in := strings.ReplaceAll(c, "%s", p)
			if got := r.String(in); got != in {
				t.Errorf("String(%q) = %q, want it unchanged", in, got)
			}
		}
	}
}

func TestMap(t *testing.T) {
	in := map[string]string{
		"http.url":                          labURLAuth,
		"user.email":                        "orders-ops@wallet-service.example",
		"db.password":                       sample,
		"http.request.header.authorization": "Bearer " + jwt,
		"API_KEY":                           "short",
		"payment.card":                      "4111 1111 1111 1111",
		"http.status_code":                  "402",
		"refresh_token":                     "",
	}
	before := maps.Clone(in)
	want := map[string]string{
		"http.url":                          "https://deploy:[token]@orders-service.lab/v1/orders",
		"user.email":                        "[email]",
		"db.password":                       "[token]",
		"http.request.header.authorization": "[token]",
		"API_KEY":                           "[token]",
		"payment.card":                      "[card]",
		"http.status_code":                  "402",
		"refresh_token":                     "",
	}

	got := New().Map(in)
	if !maps.Equal(got, want) {
		t.Errorf("Map()\n got %v\nwant %v", got, want)
	}
	if !maps.Equal(in, before) {
		t.Errorf("Map changed its input: %v", in)
	}
	got["http.status_code"] = "changed"
	if in["http.status_code"] != "402" {
		t.Error("Map returned its input instead of a new map")
	}
}

func TestMapNil(t *testing.T) {
	if got := New().Map(nil); got != nil {
		t.Errorf("Map(nil) = %v, want nil", got)
	}
}

func TestNilRedactor(t *testing.T) {
	var r *Redactor
	in := "password=" + sample + " orders-ops@wallet-service.example"
	if got := r.String(in); got != in {
		t.Errorf("nil Redactor String() = %q, want the input unchanged", got)
	}
	m := map[string]string{"password": sample}
	got := r.Map(m)
	if !maps.Equal(got, m) {
		t.Errorf("nil Redactor Map() = %v, want %v", got, m)
	}
	got["password"] = "changed"
	if m["password"] != sample {
		t.Error("nil Redactor Map returned its input instead of a new map")
	}
}

func TestLuhn(t *testing.T) {
	tests := map[string]bool{
		"4111111111111111": true,
		"4111111111111112": false,
		"378282246310005":  true,
		"79927398713":      true,
		"79927398710":      false,
		"0":                true,
	}
	for in, want := range tests {
		if got := luhn(in); got != want {
			t.Errorf("luhn(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTrimUnbalancedClosers(t *testing.T) {
	tests := map[string]string{
		"abc":       "abc",
		"abc]":      "abc",
		"abc]])}":   "abc",
		"[abc]":     "[abc]",
		"[abc]]":    "[abc]",
		"f(x)":      "f(x)",
		"{a}}":      "{a}",
		"]":         "",
		"":          "",
		"[token]]]": "[token]",
	}
	for in, want := range tests {
		if got := trimUnbalancedClosers(in); got != want {
			t.Errorf("trimUnbalancedClosers(%q) = %q, want %q", in, got, want)
		}
	}
}

func ExampleRedactor_String() {
	r := New()
	fmt.Println(r.String("2026-11-14 03:05 charge ch_000042 for orders-ops@wallet-service.example failed: 402 after 1005ms"))
	fmt.Println(r.String("callback +233 24 123 4567, card 4111 1111 1111 1111"))
	// Output:
	// 2026-11-14 03:05 charge ch_000042 for [email] failed: 402 after 1005ms
	// callback [phone], card [card]
}
