package fingerprint

import (
	"strings"
	"testing"
)

// FuzzSignature feeds arbitrary messages (error text comes from outside) to
// the signature normaliser. It must never panic, and its result must be
// stable: normalising it again changes nothing, white space is collapsed and
// trimmed, and no standalone number is left.
func FuzzSignature(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"could not place the order: HTTP 502 PaymentFailed: payment for ord_000123 failed: HTTP 402 AccountSuspended: overdue",
		"runtime error: index out of range [1] with length 1",
		`mockpay did not answer in time: Post "http://mockpay:8090/v1/charges": context deadline exceeded`,
		"dial tcp 172.18.0.5:8090: connect: connection refused",
		"charge 3f2b8c1e-9a4d-4e2f-8b1a-0c9d8e7f6a5b at 2026-11-26T10:00:00.5+00:00",
		"trace 4bf92f3577b34da6a3ce929d0e0e4736 ptr 0xc000123456 cus_42 1x_2",
		"2026-11-26T10:00:00Z2026-11-27 took 3.002s 1500ms 64KiB 5xx 1_000 x509 v1",
		"a b c\td\n",
		"\xff\xfe 12 \xc3",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, msg string) {
		got := normalise(msg)
		if again := normalise(got); again != got {
			t.Fatalf("normalise is not stable:\n%q\n-> %q\n-> %q", msg, got, again)
		}
		if strings.Join(strings.Fields(got), " ") != got {
			t.Fatalf("normalise(%q) = %q: white space not collapsed", msg, got)
		}
		if loc := numberPattern.FindStringIndex(got); loc != nil {
			t.Fatalf("normalise(%q) = %q: number %q left", msg, got, got[loc[0]:loc[1]])
		}
		if sig := signature(Sample{Message: msg}); sig == "" {
			t.Fatalf("signature(%q) is empty", msg)
		}
	})
}

// FuzzLoadRules feeds arbitrary bytes to LoadRules as user rules. It must
// never panic, and a rule set it accepts must classify any sample into a
// valid class.
func FuzzLoadRules(f *testing.F) {
	seeds := []string{
		"",
		"[]",
		"~",
		"# just a comment\n",
		"- id: a\n  class: account\n  match:\n    any:\n      - message_contains: AccountSuspended\n      - http_status: 402\n",
		"- id: b\n  class: dependency\n  match:\n    all:\n      - third_party: true\n    any:\n      - message_contains: timeout\n",
		"- {id: c, class: limit, match: {all: [{outbound: true, http_status: 429, server_address_contains: mockpay}]}}\n",
		"- {id: d, class: code, match: {any: [{exception_type: runtime.boundsError}, {exception_type_contains: panic}]}}\n",
		"- {id: e, class: account, match: {any: [{attr_equals: {payment.provider.error_code: AccountSuspended}}]}}\n",
		"- &r {id: f, class: code, match: {any: [&c {http_status: 500}]}}\n- {id: g, class: code, match: {any: [*c]}}\n",
		"- *missing\n",
		"- id: h\n  class: traffic\n  match: {any: [{http_status: 500}]}\n",
		"- id: i\n  class: code\n  match: {any: [{}]}\n",
		"- id: j\n  class: code\n  match: {any: [~, 1, [x]]}\n",
		"- id: k\n  id: k\n",
		"- {id: fallback-ours, class: code, match: {any: [{http_status: 500}]}}\n",
		"- {id: l, class: code, match: {any: [{http_status: 500}]}}\n---\n- x\n",
		"a: &a [*a]\n",
		"- !!binary aGVsbG8=\n",
		"- {id: m, class: code, match: {any: [{message_contains: !!binary aGVsbG8=}]}}\n",
		"\t- id: [\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	samples := []Sample{
		{},
		{Service: "wallet-service", HTTPStatus: 502, Message: "mockpay rejected the charge: HTTP 402 AccountSuspended: overdue"},
		{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay:8090", HTTPStatus: 429},
		{Service: "orders-service", HTTPStatus: 500, ExceptionType: "runtime.boundsError", Message: "runtime error: index out of range [1] with length 1"},
		{Service: "storefront", Outbound: true, ServerAddress: "orders-service", HTTPStatus: 500, Attrs: map[string]string{"k": "v"}},
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		rs, err := LoadRules(data, labHosts)
		if err != nil {
			if rs != nil {
				t.Fatalf("LoadRules returned a rule set and an error: %v", err)
			}
			return
		}
		for _, r := range rs.rules {
			if r.id == "" || !r.class.Valid() || len(r.all)+len(r.any) == 0 {
				t.Fatalf("accepted an invalid rule: %+v", r)
			}
		}
		for _, s := range samples {
			if class, id := rs.Classify(s); !class.Valid() || id == "" {
				t.Fatalf("Classify(%+v) = %q, %q", s, class, id)
			}
		}
	})
}
