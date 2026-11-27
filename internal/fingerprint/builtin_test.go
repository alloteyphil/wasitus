package fingerprint

import (
	"strings"
	"testing"
)

func loadBuiltins(t *testing.T) *RuleSet {
	t.Helper()
	rs, err := LoadRules(nil, labHosts)
	if err != nil {
		t.Fatalf("LoadRules(built-ins) error = %v", err)
	}
	return rs
}

// What the demo lab's services report (demo/README.md, demo/services/*).
// Each failure is seen by every hop of storefront -> orders-service ->
// wallet-service -> mockpay, each wrapping the text it got.
const (
	suspendedText = "HTTP 402 AccountSuspended: This account is suspended because an invoice is overdue. Contact billing."
	outageText    = "HTTP 503 ServiceUnavailable: Payments are temporarily unavailable. Try again later."
	rateText      = "HTTP 429 RateLimited: Too many requests. Slow down."
	timeoutText   = `Post "http://mockpay:8090/v1/charges": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`
	certText      = `Post "https://mockpay:8443/v1/charges": tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time 2026-11-26T10:00:00Z is after 2026-11-01T00:00:00Z`
	wrapErr       = "*fmt.wrapError" // the exception type of the lab's wrapped errors
)

// chain wraps the wallet-service message the way orders-service and
// storefront pass it on.
func chain(wallet string) (orders, storefront string) {
	orders = "payment for ord_000123 failed: HTTP 502 PaymentProviderError: " + wallet
	storefront = "could not place the order: HTTP 502 PaymentFailed: " + orders
	return orders, storefront
}

func TestBuiltinRulesClassifyTheLab(t *testing.T) {
	suspended := "mockpay rejected the charge: " + suspendedText
	suspendedOrders, suspendedStorefront := chain(suspended)
	outage := "mockpay rejected the charge: " + outageText
	outageOrders, outageStorefront := chain(outage)
	limited := "mockpay rejected the charge: " + rateText
	limitedOrders, limitedStorefront := chain(limited)
	timeout := "mockpay did not answer in time: " + timeoutText
	timeoutOrders := "payment for ord_000123 failed: HTTP 504 PaymentProviderTimeout: " + timeout
	expired := "could not reach mockpay: " + certText
	expiredOrders, expiredStorefront := chain(expired)
	refused := `could not reach mockpay: Post "http://mockpay:8090/v1/charges": dial tcp 172.18.0.5:8090: connect: connection refused`
	refusedOrders, refusedStorefront := chain(refused)
	noHost := `could not reach mockpay: Post "http://mockpay:8090/v1/charges": dial tcp: lookup mockpay on 127.0.0.11:53: no such host`
	walletDown := `could not reach wallet-service for ord_000123: Post "http://wallet-service:8082/payments": dial tcp 172.18.0.4:8082: connect: connection refused`
	walletSlow := `wallet-service did not answer in time for ord_000123: Post "http://wallet-service:8082/payments": context deadline exceeded`
	providerAttrs := map[string]string{"payment.provider": "mockpay", "payment.provider.error_code": "AccountSuspended"}

	tests := []struct {
		name      string
		s         Sample
		wantClass Class
		wantRule  string
	}{
		// S3 and S7: mockpay suspends the account. Every hop is account, not code.
		{"suspended: wallet calls mockpay", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 402}, ClassAccount, "account-suspended"},
		{"suspended: wallet answers", Sample{Service: "wallet-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: suspended, Attrs: providerAttrs}, ClassAccount, "account-suspended"},
		{"suspended: orders answers", Sample{Service: "orders-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: suspendedOrders}, ClassAccount, "account-suspended"},
		{"suspended: storefront answers", Sample{Service: "storefront", HTTPStatus: 502, ExceptionType: wrapErr, Message: suspendedStorefront}, ClassAccount, "account-suspended"},
		// A client span to our own service carries only the status it got.
		{"suspended: storefront calls orders", Sample{Service: "storefront", Outbound: true, ServerAddress: "orders-service", HTTPStatus: 502}, ClassCode, FallbackOurs},

		// S4: rate limited.
		{"ratelimit: wallet calls mockpay", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 429}, ClassLimit, "limit-rate-limited"},
		{"ratelimit: wallet answers", Sample{Service: "wallet-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: limited}, ClassLimit, "limit-rate-limited"},
		{"ratelimit: orders answers", Sample{Service: "orders-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: limitedOrders}, ClassLimit, "limit-rate-limited"},
		{"ratelimit: storefront answers", Sample{Service: "storefront", HTTPStatus: 502, ExceptionType: wrapErr, Message: limitedStorefront}, ClassLimit, "limit-rate-limited"},
		{"status 429 alone", Sample{Service: "storefront", HTTPStatus: 429}, ClassLimit, "limit-rate-limited"},

		// S2: outage, as 503s and as calls that hang until the caller gives up.
		{"outage: wallet calls mockpay", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay:8090", HTTPStatus: 503}, ClassDependency, "dependency-unavailable"},
		{"outage: wallet answers", Sample{Service: "wallet-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: outage}, ClassDependency, "dependency-unavailable"},
		{"outage: orders answers", Sample{Service: "orders-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: outageOrders}, ClassDependency, "dependency-unavailable"},
		{"outage: storefront answers", Sample{Service: "storefront", HTTPStatus: 502, ExceptionType: wrapErr, Message: outageStorefront}, ClassDependency, "dependency-unavailable"},
		{"outage: mockpay answers 502", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 502}, ClassDependency, "dependency-unavailable"},
		{"outage: mockpay answers 504", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 504}, ClassDependency, "dependency-unavailable"},
		{"timeout: wallet's client span", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", Message: "context deadline exceeded (Client.Timeout exceeded while awaiting headers)"}, ClassDependency, "dependency-timeout"},
		{"timeout: wallet answers", Sample{Service: "wallet-service", HTTPStatus: 504, ExceptionType: wrapErr, Message: timeout}, ClassDependency, "dependency-timeout"},
		{"timeout: orders answers", Sample{Service: "orders-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: timeoutOrders}, ClassDependency, "dependency-timeout"},
		{"timeout: storefront answers", Sample{Service: "storefront", HTTPStatus: 502, ExceptionType: wrapErr, Message: "could not place the order: HTTP 502 PaymentFailed: " + timeoutOrders}, ClassDependency, "dependency-timeout"},
		{"timeout: only the lab's words", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", Message: "mockpay did not answer in time"}, ClassDependency, "dependency-timeout"},
		{"timeout: i/o timeout", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", Message: "read tcp 172.18.0.4:41234->172.18.0.5:8090: i/o timeout"}, ClassDependency, "dependency-timeout"},
		{"unreachable: connection refused", Sample{Service: "wallet-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: refused}, ClassDependency, "dependency-unreachable"},
		{"unreachable: orders passes it on", Sample{Service: "orders-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: refusedOrders}, ClassDependency, "dependency-unreachable"},
		{"unreachable: storefront passes it on", Sample{Service: "storefront", HTTPStatus: 502, ExceptionType: wrapErr, Message: refusedStorefront}, ClassDependency, "dependency-unreachable"},
		{"unreachable: no such host", Sample{Service: "wallet-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: noHost}, ClassDependency, "dependency-unreachable"},
		{"unreachable: wallet's client span", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", Message: "dial tcp 172.18.0.5:8090: connect: connection refused"}, ClassDependency, "dependency-unreachable"},

		// Our own services failing the same ways are not a dependency.
		{"our wallet-service is down", Sample{Service: "orders-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: walletDown}, ClassCode, FallbackOurs},
		{"our wallet-service is slow", Sample{Service: "orders-service", HTTPStatus: 504, ExceptionType: wrapErr, Message: walletSlow}, ClassCode, FallbackOurs},
		{"client span to our own slow service", Sample{Service: "orders-service", Outbound: true, ServerAddress: "wallet-service", Message: "context deadline exceeded"}, ClassCode, FallbackOurs},
		{"our own service answers 503", Sample{Service: "storefront", Outbound: true, ServerAddress: "orders-service", HTTPStatus: 503}, ClassCode, FallbackOurs},

		// S5: mockpay serves an expired certificate.
		{"badcert: wallet's client span", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", Message: "tls: failed to verify certificate: x509: certificate has expired or is not yet valid"}, ClassExpiry, "expiry-certificate"},
		{"badcert: wallet answers", Sample{Service: "wallet-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: expired}, ClassExpiry, "expiry-certificate"},
		{"badcert: orders answers", Sample{Service: "orders-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: expiredOrders}, ClassExpiry, "expiry-certificate"},
		{"badcert: storefront answers", Sample{Service: "storefront", HTTPStatus: 502, ExceptionType: wrapErr, Message: expiredStorefront}, ClassExpiry, "expiry-certificate"},
		// A verification failure is expiry only with expiry wording: an unknown
		// CA or a wrong host name can be our own trust store or configuration
		// (review of PR #15). Without that evidence the fallbacks decide.
		{"tls verify failure without expiry text", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", Message: "tls: failed to verify certificate"}, ClassDependency, FallbackOutbound},
		{"unknown certificate authority", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", Message: "tls: failed to verify certificate: x509: certificate signed by unknown authority"}, ClassDependency, FallbackOutbound},
		{"certificate not yet valid", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", Message: "x509: certificate is not yet valid"}, ClassExpiry, "expiry-certificate"},
		// A panic logged without an exception type stays code even when its
		// text mentions a provider word or a provider URL (review of PR #15).
		{"panic text naming an account word", Sample{Service: "orders-service", HTTPStatus: 500, Message: "runtime error: invalid memory address; PaymentRequired field missing"}, ClassCode, "go-runtime-panic"},
		{"panic text naming a provider call", Sample{Service: "orders-service", HTTPStatus: 500, Message: `runtime error: nil pointer after Get "http://mockpay:8090/x": context deadline exceeded`}, ClassCode, "go-runtime-panic"},

		// S1: the staged bad deploy in orders-service is code at every hop.
		{"bad deploy: orders panics", Sample{Service: "orders-service", HTTPStatus: 500, ExceptionType: "runtime.boundsError", Message: "runtime error: index out of range [1] with length 1"}, ClassCode, "go-runtime-panic"},
		{"bad deploy: storefront calls orders", Sample{Service: "storefront", Outbound: true, ServerAddress: "orders-service", HTTPStatus: 500}, ClassCode, FallbackOurs},
		{"bad deploy: storefront calls orders locally", Sample{Service: "storefront", Outbound: true, ServerAddress: "localhost:8081", HTTPStatus: 500}, ClassCode, FallbackOurs},
		{"bad deploy: storefront answers", Sample{Service: "storefront", HTTPStatus: 502, ExceptionType: wrapErr, Message: "could not place the order: HTTP 500 InternalError: Something went wrong."}, ClassCode, FallbackOurs},
		{"nil pointer panic", Sample{Service: "orders-service", HTTPStatus: 500, ExceptionType: "runtime.errorString", Message: "runtime error: invalid memory address or nil pointer dereference"}, ClassCode, "go-runtime-panic"},

		// Generic cloud error codes (docs/ARCHITECTURE.md section 4).
		{"AccessDenied", Sample{Service: "orders-service", Message: "operation error S3: GetObject, api error AccessDenied: Access Denied"}, ClassAccount, "account-access-denied"},
		{"AccessDeniedException", Sample{Service: "orders-service", Message: "AccessDeniedException: User is not authorized"}, ClassAccount, "account-access-denied"},
		{"SubscriptionRequired", Sample{Service: "orders-service", Message: "SubscriptionRequiredException: The AWS Access Key Id needs a subscription for the service"}, ClassAccount, "account-suspended"},
		{"OptInRequired", Sample{Service: "orders-service", Message: "OptInRequired: You are not subscribed to this service."}, ClassAccount, "account-suspended"},
		{"402 Payment Required", Sample{Service: "orders-service", Message: "unexpected response: 402 Payment Required"}, ClassAccount, "account-suspended"},
		{"ThrottlingException", Sample{Service: "orders-service", Message: "ThrottlingException: Rate exceeded"}, ClassLimit, "limit-rate-limited"},
		{"TooManyRequests", Sample{Service: "orders-service", Message: "TooManyRequestsException: Too many requests"}, ClassLimit, "limit-rate-limited"},
		{"quota exceeded", Sample{Service: "orders-service", Message: "Quota exceeded for quota metric 'Requests' of service"}, ClassLimit, "limit-quota-exceeded"},
		{"LimitExceededException", Sample{Service: "orders-service", Message: "LimitExceededException: Rate of requests exceeds the allowed throughput"}, ClassLimit, "limit-quota-exceeded"},
		{"ExpiredToken", Sample{Service: "orders-service", Message: "ExpiredToken: The security token included in the request is expired"}, ClassExpiry, "expiry-token-or-key"},
		{"token expired", Sample{Service: "orders-service", Message: "oauth2: token expired and refresh token is not set"}, ClassExpiry, "expiry-token-or-key"},
		{"key expired", Sample{Service: "orders-service", Message: "signing failed: key expired"}, ClassExpiry, "expiry-token-or-key"},

		// Fallbacks.
		{"unknown failure calling a third party", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 500}, ClassDependency, FallbackOutbound},
		{"unknown failure in our service", Sample{Service: "orders-service", HTTPStatus: 500, ExceptionType: wrapErr, Message: "decode order: unexpected EOF"}, ClassCode, FallbackOurs},
	}
	rs := loadBuiltins(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if class, id := rs.Classify(tt.s); class != tt.wantClass || id != tt.wantRule {
				t.Errorf("Classify() = %s/%s, want %s/%s", class, id, tt.wantClass, tt.wantRule)
			}
		})
	}

	// Every built-in rule has at least one lab-shaped case above.
	covered := map[string]bool{}
	for _, tt := range tests {
		covered[tt.wantRule] = true
	}
	for _, r := range rs.rules {
		if !covered[r.id] {
			t.Errorf("built-in rule %q has no case in this table", r.id)
		}
	}
}

// TestBuiltinConditionsAreReachable checks that no built-in condition is
// shadowed by an earlier rule: a sample made to fit exactly one condition
// (plus its rule's all: list) must be decided by that rule.
func TestBuiltinConditionsAreReachable(t *testing.T) {
	rs := loadBuiltins(t)
	for _, r := range rs.rules {
		for i, c := range r.any {
			s := Sample{Service: "orders-service"}
			for _, a := range r.all {
				fit(&s, a)
			}
			fit(&s, c)
			if class, id := rs.Classify(s); id != r.id || class != r.class {
				t.Errorf("%s any entry %d: Classify(%+v) = %s/%s, want %s/%s", r.id, i+1, s, class, id, r.class, r.id)
			}
		}
	}
}

// fit changes s so that condition c holds for it.
func fit(s *Sample, c condition) {
	if c.messageContains != "" {
		s.Message = strings.TrimSpace(s.Message + " " + c.messageContains)
	}
	if c.exceptionType != "" {
		s.ExceptionType = c.exceptionType
	}
	if c.exceptionTypeContains != "" {
		s.ExceptionType = "x" + c.exceptionTypeContains + "x"
	}
	if c.serverAddressContains != "" {
		s.ServerAddress = c.serverAddressContains
	}
	if c.httpStatus != 0 {
		s.HTTPStatus = c.httpStatus
	}
	if c.outbound != nil {
		s.Outbound = *c.outbound
	}
	if c.thirdParty != nil && *c.thirdParty {
		s.Outbound, s.ServerAddress = true, "api.provider.example"
	}
	for k, v := range c.attrEquals {
		if s.Attrs == nil {
			s.Attrs = map[string]string{}
		}
		s.Attrs[k] = v
	}
}

func TestUserRulesComeBeforeBuiltins(t *testing.T) {
	// A team whose AccessDenied errors come from their own IAM mistakes, and
	// whose provider reports an unpaid bill as "ACME-4031".
	const user = `
- id: our-iam-mistakes
  class: code
  match:
    any:
      - message_contains: AccessDenied
- id: acme-unpaid
  class: account
  match:
    all:
      - outbound: true
      - server_address_contains: acme
    any:
      - message_contains: ACME-4031
`
	rs, err := LoadRules([]byte(user), labHosts)
	if err != nil {
		t.Fatalf("LoadRules() error = %v", err)
	}
	denied := Sample{Service: "orders-service", Message: "api error AccessDenied: Access Denied"}
	if class, id := rs.Classify(denied); class != ClassCode || id != "our-iam-mistakes" {
		t.Errorf("Classify(AccessDenied) = %s/%s, want code/our-iam-mistakes", class, id)
	}
	if class, id := loadBuiltins(t).Classify(denied); class != ClassAccount || id != "account-access-denied" {
		t.Errorf("built-ins only: Classify(AccessDenied) = %s/%s, want account/account-access-denied", class, id)
	}
	unpaid := Sample{Service: "wallet-service", Outbound: true, ServerAddress: "api.acme.example", HTTPStatus: 400, Message: "ACME-4031 balance overdue"}
	if class, id := rs.Classify(unpaid); class != ClassAccount || id != "acme-unpaid" {
		t.Errorf("Classify(ACME-4031) = %s/%s, want account/acme-unpaid", class, id)
	}
	// The built-ins still apply to what the user rules do not cover.
	if class, id := rs.Classify(Sample{HTTPStatus: 429}); class != ClassLimit || id != "limit-rate-limited" {
		t.Errorf("Classify(429) = %s/%s, want limit/limit-rate-limited", class, id)
	}
}

func TestUserRuleCannotReuseABuiltinID(t *testing.T) {
	const user = "- {id: account-suspended, class: code, match: {any: [{http_status: 500}]}}\n"
	_, err := LoadRules([]byte(user), labHosts)
	if err == nil || !strings.Contains(err.Error(), `duplicate rule id "account-suspended": user rules line 1 and built-in rules line`) {
		t.Errorf("LoadRules() error = %v, want the clash with the built-in rule", err)
	}
}
