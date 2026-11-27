package fingerprint

import (
	"strings"
	"testing"
)

// labHosts are the demo lab's own services, as the lab's compose file names them.
var labHosts = Options{InternalHosts: []string{"storefront", "orders-service", "wallet-service", "localhost", "127.0.0.1"}}

// mustLoad builds a rule set from user rules and the given built-ins only, so
// engine tests do not depend on what the shipped rules.yaml contains.
func mustLoad(t *testing.T, user, builtin string, opts Options) *RuleSet {
	t.Helper()
	rs, err := load([]byte(user), []byte(builtin), opts)
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	return rs
}

func TestLoadRulesRejectsBadRules(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string // substrings of the error
	}{
		{"not a list", "id: a\nclass: code\n", []string{"user rules: line 1: rules must be a list"}},
		{"syntax error", "- id: [\n", []string{"user rules: yaml:"}},
		{"two documents", "- {id: a, class: code, match: {any: [{http_status: 500}]}}\n---\n- id: b\n", []string{"only one YAML document"}},
		{"rule is not a mapping", "- just-a-string\n", []string{"rule 1 (line 1)", "a rule must be a mapping"}},
		{"null rule", "- {id: a, class: code, match: {any: [{http_status: 500}]}}\n- ~\n", []string{"rule 2 (line 2): a rule must be a mapping"}},
		{"null match", "- id: a\n  class: code\n  match: ~\n", []string{`rule "a"`, "line 3: match has no value"}},
		{"unknown rule key", "- id: a\n  class: code\n  mach: {}\n", []string{`rule "a" (line 1)`, `line 3: unknown key "mach" in a rule`}},
		{"unknown match key", "- id: a\n  class: code\n  match:\n    none: []\n", []string{`rule "a"`, `unknown key "none" in match`}},
		{"unknown condition key", "- id: a\n  class: code\n  match:\n    any:\n      - message_contain: x\n", []string{`rule "a"`, `line 5: unknown key "message_contain" in a condition`}},
		{"condition is not a mapping", "- id: a\n  class: code\n  match:\n    any: [timeout]\n", []string{`rule "a"`, "any entry 1 (line 4): a condition must be a mapping"}},
		{"missing id", "- class: code\n  match: {any: [{http_status: 500}]}\n", []string{"rule 1 (line 1): missing id"}},
		{"empty id", "- id: \"\"\n  class: code\n  match: {any: [{http_status: 500}]}\n", []string{"rule 1 (line 1): missing id"}},
		{"null id", "- id: ~\n  class: code\n", []string{"rule 1 (line 1)", "id has no value"}},
		{"bad id", "- id: has space\n  class: code\n  match: {any: [{http_status: 500}]}\n", []string{`rule "has space"`, "may use only"}},
		{"reserved id", "- id: fallback-ours\n  class: code\n  match: {any: [{http_status: 500}]}\n", []string{`rule "fallback-ours"`, "reserved"}},
		{"missing class", "- id: a\n  match: {any: [{http_status: 500}]}\n", []string{`rule "a" (line 1): missing class`}},
		{"unknown class", "- id: a\n  class: network\n  match: {any: [{http_status: 500}]}\n", []string{`rule "a"`, `unknown class "network"`}},
		{"traffic class", "- id: a\n  class: traffic\n  match: {any: [{http_status: 500}]}\n", []string{`rule "a"`, "request rates"}},
		{"no match", "- id: a\n  class: code\n", []string{`rule "a"`, "match is empty"}},
		{"empty match", "- id: a\n  class: code\n  match: {}\n", []string{`rule "a"`, "match is empty"}},
		{"empty any list", "- id: a\n  class: code\n  match: {any: []}\n", []string{`rule "a"`, "match is empty"}},
		{"any is not a list", "- id: a\n  class: code\n  match:\n    any: {http_status: 500}\n", []string{`rule "a"`, "line 4: any must be a list of conditions"}},
		{"empty condition", "- id: a\n  class: code\n  match:\n    any:\n      - {}\n", []string{`rule "a"`, "any entry 1 (line 5): empty condition"}},
		{"null condition", "- id: a\n  class: code\n  match:\n    all: [~]\n", []string{`rule "a"`, "all entry 1 (line 4): a condition must be a mapping"}},
		{"second condition is bad", "- id: a\n  class: code\n  match:\n    any:\n      - http_status: 500\n      - outbound: true\n        http_status: 99\n", []string{`rule "a"`, "any entry 2 (line 6): http_status 99"}},
		{"null value", "- id: a\n  class: code\n  match: {any: [{message_contains: ~}]}\n", []string{`rule "a"`, "message_contains has no value"}},
		{"empty text", "- id: a\n  class: code\n  match: {any: [{message_contains: \"\"}]}\n", []string{`rule "a"`, "message_contains is empty"}},
		{"blank text", "- id: a\n  class: code\n  match: {any: [{server_address_contains: \"  \"}]}\n", []string{"server_address_contains is empty"}},
		{"empty exception type", "- id: a\n  class: code\n  match: {any: [{exception_type: \"\"}]}\n", []string{"exception_type is empty"}},
		{"empty exception type contains", "- id: a\n  class: code\n  match: {any: [{exception_type_contains: \"\"}]}\n", []string{"exception_type_contains is empty"}},
		{"status out of range", "- id: a\n  class: code\n  match: {any: [{http_status: 1000}]}\n", []string{`rule "a"`, "http_status 1000 is not an HTTP status"}},
		{"status not a number", "- id: a\n  class: code\n  match: {any: [{http_status: abc}]}\n", []string{`rule "a"`, "cannot unmarshal"}},
		{"outbound not a bool", "- id: a\n  class: code\n  match: {any: [{outbound: maybe}]}\n", []string{`rule "a"`, "cannot unmarshal"}},
		{"third_party not a bool", "- id: a\n  class: code\n  match: {all: [{third_party: yes please}]}\n", []string{`rule "a"`, "cannot unmarshal"}},
		{"attr_equals not a mapping", "- id: a\n  class: code\n  match: {all: [{attr_equals: [a, b]}]}\n", []string{`rule "a"`, "cannot unmarshal"}},
		{"empty attr_equals", "- id: a\n  class: code\n  match: {any: [{attr_equals: {}}]}\n", []string{`rule "a"`, "attr_equals is empty"}},
		{"empty attr name", "- id: a\n  class: code\n  match: {any: [{attr_equals: {\"\": x}}]}\n", []string{"empty attribute name"}},
		{"duplicate key", "- id: a\n  id: b\n  class: code\n  match: {any: [{http_status: 500}]}\n", []string{"already defined"}},
		{
			"duplicate id",
			"- {id: a, class: code, match: {any: [{http_status: 500}]}}\n- {id: a, class: limit, match: {any: [{http_status: 429}]}}\n",
			[]string{`duplicate rule id "a": user rules line 1 and user rules line 2`},
		},
		{
			"every bad rule is reported",
			"- id: a\n  class: nope\n  match: {any: [{http_status: 500}]}\n- id: b\n  class: code\n",
			[]string{`rule "a"`, `unknown class "nope"`, `rule "b"`, "match is empty"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs, err := load([]byte(tt.yaml), nil, Options{})
			if err == nil {
				t.Fatalf("load() = %v, want an error", rs)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q\ndoes not contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadRulesRejectsIDsThatClashWithBuiltins(t *testing.T) {
	const builtin = "- {id: shared, class: limit, match: {any: [{http_status: 429}]}}\n"
	const user = "# mine\n- {id: shared, class: code, match: {any: [{http_status: 500}]}}\n"
	_, err := load([]byte(user), []byte(builtin), Options{})
	if err == nil || !strings.Contains(err.Error(), `duplicate rule id "shared": user rules line 2 and built-in rules line 1`) {
		t.Errorf("load() error = %v, want the clash with the built-in rule", err)
	}
}

func TestLoadRulesReportsBadBuiltins(t *testing.T) {
	_, err := load(nil, []byte("- id: a\n"), Options{})
	if err == nil || !strings.Contains(err.Error(), `built-in rules: rule "a"`) {
		t.Errorf("load() error = %v, want it to name the built-in rule", err)
	}
}

func TestLoadRulesAcceptsNoUserRules(t *testing.T) {
	for _, doc := range []string{"", "  \n", "# only a comment\n", "[]", "~", "---\n"} {
		rs, err := load([]byte(doc), nil, Options{})
		if err != nil {
			t.Errorf("load(%q) error = %v", doc, err)
			continue
		}
		if len(rs.rules) != 0 {
			t.Errorf("load(%q) has %d rules, want 0", doc, len(rs.rules))
		}
	}
}

func TestLoadRulesFollowsAliases(t *testing.T) {
	const user = `
- &base
  id: a
  class: limit
  match:
    any:
      - &tooMany {http_status: 429}
- id: b
  class: limit
  match:
    any: [*tooMany]
`
	rs := mustLoad(t, user, "", Options{})
	if len(rs.rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rs.rules))
	}
	if class, id := rs.Classify(Sample{HTTPStatus: 429}); class != ClassLimit || id != "a" {
		t.Errorf("Classify() = %s/%s, want limit/a", class, id)
	}
}

func TestLoadRulesRejectsAliasedDuplicateRule(t *testing.T) {
	const user = "- &r {id: a, class: code, match: {any: [{http_status: 500}]}}\n- *r\n"
	_, err := load([]byte(user), nil, Options{})
	if err == nil || !strings.Contains(err.Error(), `duplicate rule id "a"`) {
		t.Errorf("load() error = %v, want a duplicate id", err)
	}
}

func TestConditions(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		matches   []Sample
		misses    []Sample
	}{
		{
			name:      "message_contains ignores case",
			condition: `{message_contains: "AccountSuspended"}`,
			matches:   []Sample{{Message: "HTTP 402 accountsuspended"}, {Message: "x ACCOUNTSUSPENDED y"}},
			misses:    []Sample{{Message: "account suspended"}, {}},
		},
		{
			name:      "exception_type is exact",
			condition: `{exception_type: "runtime.boundsError"}`,
			matches:   []Sample{{ExceptionType: "runtime.boundsError"}},
			misses:    []Sample{{ExceptionType: "runtime.boundserror"}, {ExceptionType: "*runtime.boundsError"}},
		},
		{
			name:      "exception_type_contains ignores case",
			condition: `{exception_type_contains: "Timeout"}`,
			matches:   []Sample{{ExceptionType: "java.net.SocketTimeoutException"}, {ExceptionType: "TIMEOUT"}},
			misses:    []Sample{{ExceptionType: "IOException"}},
		},
		{
			name:      "http_status",
			condition: `{http_status: 402}`,
			matches:   []Sample{{HTTPStatus: 402}},
			misses:    []Sample{{HTTPStatus: 502}, {}},
		},
		{
			name:      "outbound true",
			condition: `{outbound: true}`,
			matches:   []Sample{{Outbound: true}},
			misses:    []Sample{{Outbound: false}},
		},
		{
			name:      "outbound false",
			condition: `{outbound: false}`,
			matches:   []Sample{{Outbound: false}},
			misses:    []Sample{{Outbound: true}},
		},
		{
			name:      "server_address_contains ignores case",
			condition: `{server_address_contains: "MockPay"}`,
			matches:   []Sample{{Outbound: true, ServerAddress: "api.mockpay.test"}},
			misses:    []Sample{{Outbound: true, ServerAddress: "wallet-service"}},
		},
		{
			name:      "attr_equals needs every pair",
			condition: `{attr_equals: {payment.provider: mockpay, payment.provider.error_code: AccountSuspended}}`,
			matches: []Sample{{Attrs: map[string]string{
				"payment.provider": "mockpay", "payment.provider.error_code": "AccountSuspended", "other": "x",
			}}},
			misses: []Sample{
				{Attrs: map[string]string{"payment.provider": "mockpay"}},
				{Attrs: map[string]string{"payment.provider": "mockpay", "payment.provider.error_code": "accountsuspended"}},
				{},
			},
		},
		{
			name:      "attr_equals with an empty value needs the attribute present",
			condition: `{attr_equals: {note: ""}}`,
			matches:   []Sample{{Attrs: map[string]string{"note": ""}}},
			misses:    []Sample{{}},
		},
		{
			name:      "third_party true",
			condition: `{third_party: true}`,
			matches: []Sample{
				{Outbound: true, ServerAddress: "mockpay"},
				{Message: `mockpay did not answer in time: Post "http://mockpay:8090/v1/charges": context deadline exceeded`},
			},
			misses: []Sample{
				{Outbound: true, ServerAddress: "wallet-service"},
				{Outbound: true},
				{Message: "context deadline exceeded"},
				{Message: `could not reach wallet-service: Post "http://wallet-service:8082/payments": connection refused`},
			},
		},
		{
			name:      "third_party false",
			condition: `{third_party: false}`,
			matches:   []Sample{{Outbound: true, ServerAddress: "orders-service"}, {Message: "no URL here"}},
			misses:    []Sample{{Outbound: true, ServerAddress: "mockpay"}},
		},
		{
			name:      "one entry with several keys needs all of them",
			condition: `{outbound: true, http_status: 503, server_address_contains: mockpay}`,
			matches:   []Sample{{Outbound: true, HTTPStatus: 503, ServerAddress: "mockpay"}},
			misses: []Sample{
				{Outbound: false, HTTPStatus: 503, ServerAddress: "mockpay"},
				{Outbound: true, HTTPStatus: 502, ServerAddress: "mockpay"},
				{Outbound: true, HTTPStatus: 503, ServerAddress: "wallet-service"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := mustLoad(t, "- id: r\n  class: account\n  match:\n    any:\n      - "+tt.condition+"\n", "", labHosts)
			for _, s := range tt.matches {
				if _, id := rs.Classify(s); id != "r" {
					t.Errorf("Classify(%+v) rule = %s, want r", s, id)
				}
			}
			for _, s := range tt.misses {
				if _, id := rs.Classify(s); id == "r" {
					t.Errorf("Classify(%+v) matched, want no match", s)
				}
			}
		})
	}
}

func TestMatchAllAndAny(t *testing.T) {
	const user = `
- id: provider-timeout
  class: dependency
  match:
    all:
      - third_party: true
    any:
      - message_contains: "context deadline exceeded"
      - message_contains: "i/o timeout"
- id: only-all
  class: limit
  match:
    all:
      - outbound: true
      - http_status: 429
`
	rs := mustLoad(t, user, "", labHosts)
	tests := []struct {
		s      Sample
		wantID string
	}{
		{Sample{Outbound: true, ServerAddress: "mockpay", Message: "context deadline exceeded"}, "provider-timeout"},
		{Sample{Outbound: true, ServerAddress: "mockpay", Message: "read: i/o timeout"}, "provider-timeout"},
		{Sample{Outbound: true, ServerAddress: "mockpay", Message: "connection refused"}, FallbackOutbound},
		{Sample{Outbound: true, ServerAddress: "orders-service", Message: "context deadline exceeded"}, FallbackOurs},
		{Sample{Outbound: true, ServerAddress: "mockpay", HTTPStatus: 429}, "only-all"},
		{Sample{Outbound: false, HTTPStatus: 429}, FallbackOurs},
	}
	for _, tt := range tests {
		if _, id := rs.Classify(tt.s); id != tt.wantID {
			t.Errorf("Classify(%+v) rule = %s, want %s", tt.s, id, tt.wantID)
		}
	}
}

func TestFirstMatchingRuleWinsAndUserRulesComeFirst(t *testing.T) {
	const builtin = `
- {id: builtin-suspended, class: account, match: {any: [{message_contains: AccountSuspended}]}}
- {id: builtin-later, class: dependency, match: {any: [{message_contains: mockpay}]}}
`
	const user = `
- {id: user-suspended, class: dependency, match: {any: [{message_contains: AccountSuspended}]}}
`
	msg := "mockpay rejected the charge: HTTP 402 AccountSuspended: overdue"

	without := mustLoad(t, "", builtin, Options{})
	if class, id := without.Classify(Sample{Message: msg}); class != ClassAccount || id != "builtin-suspended" {
		t.Errorf("built-ins only: Classify() = %s/%s, want account/builtin-suspended (the first match)", class, id)
	}
	with := mustLoad(t, user, builtin, Options{})
	if class, id := with.Classify(Sample{Message: msg}); class != ClassDependency || id != "user-suspended" {
		t.Errorf("with user rules: Classify() = %s/%s, want dependency/user-suspended", class, id)
	}
}

func TestFallbacks(t *testing.T) {
	rs := mustLoad(t, "", "", Options{InternalHosts: []string{
		"orders-service", " Wallet-Service:8082 ", "localhost", "127.0.0.1", "::1", ".lab.internal", "", ".",
	}})
	tests := []struct {
		name      string
		s         Sample
		wantClass Class
		wantID    string
	}{
		{"outbound to a third party", Sample{Outbound: true, ServerAddress: "mockpay", HTTPStatus: 500}, ClassDependency, FallbackOutbound},
		{"outbound to a third party with a port", Sample{Outbound: true, ServerAddress: "api.mockpay.test:443"}, ClassDependency, FallbackOutbound},
		{"outbound to our own service", Sample{Outbound: true, ServerAddress: "orders-service", HTTPStatus: 500}, ClassCode, FallbackOurs},
		{"internal host ignores case and port", Sample{Outbound: true, ServerAddress: "WALLET-SERVICE:9999"}, ClassCode, FallbackOurs},
		{"internal host given as a URL", Sample{Outbound: true, ServerAddress: "http://localhost:8081/orders"}, ClassCode, FallbackOurs},
		{"IPv6 loopback", Sample{Outbound: true, ServerAddress: "[::1]:8081"}, ClassCode, FallbackOurs},
		{"internal domain suffix", Sample{Outbound: true, ServerAddress: "ledger.lab.internal"}, ClassCode, FallbackOurs},
		{"internal domain itself", Sample{Outbound: true, ServerAddress: "lab.internal"}, ClassCode, FallbackOurs},
		{"look-alike domain is not ours", Sample{Outbound: true, ServerAddress: "evil-lab.internal"}, ClassDependency, FallbackOutbound},
		{"outbound to an unknown host", Sample{Outbound: true}, ClassDependency, FallbackOutbound},
		{
			"outbound without an address uses the URL in the message",
			Sample{Outbound: true, Message: `Post "http://orders-service:8081/orders": EOF`},
			ClassCode, FallbackOurs,
		},
		{"inbound", Sample{HTTPStatus: 500, ExceptionType: "runtime.boundsError"}, ClassCode, FallbackOurs},
		{
			"inbound naming a third party is still ours without a rule",
			Sample{HTTPStatus: 502, Message: `Post "http://mockpay:8090/v1/charges": EOF`},
			ClassCode, FallbackOurs,
		},
		{"empty sample", Sample{}, ClassCode, FallbackOurs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if class, id := rs.Classify(tt.s); class != tt.wantClass || id != tt.wantID {
				t.Errorf("Classify() = %s/%s, want %s/%s", class, id, tt.wantClass, tt.wantID)
			}
		})
	}
}

func TestPeerOf(t *testing.T) {
	tests := []struct {
		name string
		s    Sample
		want string
	}{
		{"outbound server address", Sample{Outbound: true, ServerAddress: "MockPay:8090"}, "mockpay"},
		{"inbound ignores its own server address", Sample{ServerAddress: "wallet-service"}, ""},
		{"inbound uses the URL in the message", Sample{Message: `Post "http://mockpay:8090/v1/charges": EOF`}, "mockpay"},
		{
			"the last URL is the innermost call",
			Sample{Message: `Post "http://orders-service:8081/orders": HTTP 502: Post "https://api.mockpay.test/v1/charges": timeout`},
			"api.mockpay.test",
		},
		{"user info and IPv6", Sample{Message: `GET http://user:pw@[2001:db8::1]:8443/x failed`}, "2001:db8::1"},
		{"escaped JSON quotes", Sample{Message: `Post \"http://mockpay:8090/v1/charges\": EOF`}, "mockpay"},
		{"no URL", Sample{Message: "dial tcp 10.0.0.1:8090: connection refused"}, ""},
		{"empty authority", Sample{Message: `Post "http:///x"`}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := peerOf(&tt.s); got != tt.want {
				t.Errorf("peerOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassValid(t *testing.T) {
	for _, c := range []Class{ClassCode, ClassDependency, ClassAccount, ClassLimit, ClassExpiry} {
		if !c.Valid() {
			t.Errorf("%q.Valid() = false", c)
		}
	}
	for _, c := range []Class{"", "traffic", "Code"} {
		if c.Valid() {
			t.Errorf("%q.Valid() = true", c)
		}
	}
}
