package fingerprint

import (
	"testing"
)

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
