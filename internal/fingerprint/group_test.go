package fingerprint

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestSignature(t *testing.T) {
	tests := []struct {
		name string
		s    Sample
		want string
	}{
		{
			"the lab's suspended chain",
			Sample{Message: "could not place the order: HTTP 502 PaymentFailed: payment for ord_000123 failed: HTTP 502 PaymentProviderError: mockpay rejected the charge: HTTP 402 AccountSuspended: Contact billing."},
			"could not place the order: HTTP <n> PaymentFailed: payment for <id> failed: HTTP <n> PaymentProviderError: mockpay rejected the charge: HTTP <n> AccountSuspended: Contact billing.",
		},
		{"bounds error", Sample{Message: "runtime error: index out of range [1] with length 1"}, "runtime error: index out of range [<n>] with length <n>"},
		{"URL with a port", Sample{Message: `Post "http://mockpay:8090/v1/charges": context deadline exceeded`}, `Post "http://mockpay:<n>/v1/charges": context deadline exceeded`},
		{"IP address and port", Sample{Message: "dial tcp 172.18.0.5:8090: connect: connection refused"}, "dial tcp <ip>:<n>: connect: connection refused"},
		{"UUID", Sample{Message: "charge 3f2b8c1e-9a4d-4e2f-8b1a-0C9D8E7F6A5B not found"}, "charge <uuid> not found"},
		{"timestamps", Sample{Message: "current time 2026-11-26T10:00:00Z is after 2026-11-01T00:00:00.123+00:00"}, "current time <time> is after <time>"},
		{"date and time with a space", Sample{Message: "at 2026-11-26 10:00:05 retry"}, "at <time> retry"},
		{"prefixed IDs", Sample{Message: "customer cus_42 charge ch_000001 refund re_3MtwBwLkdIwHu7ix"}, "customer <id> charge <id> refund <id>"},
		{"hex trace ID", Sample{Message: "trace 4bf92f3577b34da6a3ce929d0e0e4736 failed"}, "trace <hex> failed"},
		{"0x pointer", Sample{Message: "bad pointer 0xc000123456 and 0x1F"}, "bad pointer <hex> and <hex>"},
		{"long digit run is a number", Sample{Message: "order 12345678 failed"}, "order <n> failed"},
		{"hex-looking words stay", Sample{Message: "deadbeefcafe defaced"}, "deadbeefcafe defaced"},
		{"digits inside words stay", Sample{Message: "x509: http2 v1 utf8 ord1234 1_000"}, "x509: http2 v1 utf8 ord1234 1_000"},
		{"numbers keep their unit", Sample{Message: "took 3.002s, limit 1500ms, 5xx, 64KiB, 123abcd"}, "took <n>s, limit <n>ms, <n>xx, <n>KiB, 123abcd"},
		{"numbers in other words", Sample{Message: "order-1001 v=2 HTTP/1.1 -7"}, "order-<n> v=<n> HTTP/<n> -<n>"},
		{"white space", Sample{Message: "  a\n\tb   c  "}, "a b c"},
		{"no message: exception and status", Sample{ExceptionType: "runtime.boundsError", HTTPStatus: 500}, "runtime.boundsError HTTP 500"},
		{"no message: status", Sample{Message: " \n ", HTTPStatus: 402}, "HTTP 402"},
		{"no message: exception", Sample{ExceptionType: " java.lang.NullPointerException "}, "java.lang.NullPointerException"},
		{"nothing at all", Sample{}, "(no message)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := signature(tt.s); got != tt.want {
				t.Errorf("signature() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

var t0 = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

// suspendedCheckouts returns what the lab reports for n checkouts while
// mockpay is suspended: 5 samples each, 4 account and 1 code (storefront's
// client span to orders-service, which carries only the status it got).
func suspendedCheckouts(n int) []Sample {
	var out []Sample
	for i := 1; i <= n; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		order := fmt.Sprintf("ord_%06d", i)
		wallet := "mockpay rejected the charge: " + suspendedText
		orders := "payment for " + order + " failed: HTTP 502 PaymentProviderError: " + wallet
		out = append(out,
			Sample{Time: at, Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 402},
			Sample{Time: at, Service: "wallet-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: wallet},
			Sample{Time: at.Add(time.Millisecond), Service: "orders-service", HTTPStatus: 502, ExceptionType: wrapErr, Message: orders},
			Sample{Time: at.Add(2 * time.Millisecond), Service: "storefront", Outbound: true, ServerAddress: "orders-service", HTTPStatus: 502},
			Sample{Time: at.Add(2 * time.Millisecond), Service: "storefront", HTTPStatus: 502, ExceptionType: wrapErr, Message: "could not place the order: HTTP 502 PaymentFailed: " + orders},
		)
	}
	return out
}

func TestGroupTheSuspendedScenario(t *testing.T) {
	samples := suspendedCheckouts(4)
	panicAt := time.Date(2026, 11, 26, 11, 0, 0, 0, time.FixedZone("WAT", 3600)) // 10:00 UTC
	samples = append(samples, Sample{
		Time: panicAt, Service: "orders-service", HTTPStatus: 500,
		ExceptionType: "runtime.boundsError", Message: "runtime error: index out of range [1] with length 1",
	})

	r := loadBuiltins(t).Group(samples)

	if r.Total != 21 {
		t.Errorf("Total = %d, want 21", r.Total)
	}
	wantShares := map[Class]float64{ClassAccount: 16.0 / 21, ClassCode: 5.0 / 21}
	if !reflect.DeepEqual(r.Shares, wantShares) {
		t.Errorf("Shares = %v, want %v", r.Shares, wantShares)
	}

	type row struct {
		class     Class
		service   string
		signature string
		rule      string
		count     int
	}
	walletSig := "mockpay rejected the charge: HTTP <n> AccountSuspended: This account is suspended because an invoice is overdue. Contact billing."
	ordersSig := "payment for <id> failed: HTTP <n> PaymentProviderError: " + walletSig
	want := []row{
		{ClassAccount, "orders-service", ordersSig, "account-suspended", 4},
		{ClassAccount, "storefront", "could not place the order: HTTP <n> PaymentFailed: " + ordersSig, "account-suspended", 4},
		{ClassAccount, "wallet-service", "HTTP 402", "account-suspended", 4},
		{ClassAccount, "wallet-service", walletSig, "account-suspended", 4},
		{ClassCode, "storefront", "HTTP 502", FallbackOurs, 4},
		{ClassCode, "orders-service", "runtime error: index out of range [<n>] with length <n>", "go-runtime-panic", 1},
	}
	var got []row
	for _, g := range r.Groups {
		got = append(got, row{g.Class, g.Service, g.Signature, g.RuleID, g.Count})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups =\n%v\nwant\n%v", got, want)
	}

	orders := r.Groups[0]
	if orders.Share != 4.0/21 {
		t.Errorf("orders Share = %v, want %v", orders.Share, 4.0/21)
	}
	if want := t0.Add(time.Second + time.Millisecond); !orders.First.Equal(want) {
		t.Errorf("orders First = %v, want %v", orders.First, want)
	}
	if want := t0.Add(4*time.Second + time.Millisecond); !orders.Last.Equal(want) {
		t.Errorf("orders Last = %v, want %v", orders.Last, want)
	}
	if len(orders.Examples) != 3 || orders.Examples[0] != samples[2].Message || orders.Examples[2] != samples[12].Message {
		t.Errorf("orders Examples = %q, want the first three orders' messages", orders.Examples)
	}
	if ex := r.Groups[2].Examples; ex != nil {
		t.Errorf("status-only group Examples = %q, want none", ex)
	}
	panicGroup := r.Groups[5]
	if panicGroup.First != panicGroup.Last || panicGroup.First.Location() != time.UTC || !panicGroup.First.Equal(panicAt) {
		t.Errorf("panic group First/Last = %v/%v, want %v in UTC", panicGroup.First, panicGroup.Last, panicAt)
	}
}

func TestGroupSortsTiesByClassServiceAndSignature(t *testing.T) {
	samples := []Sample{
		{Service: "wallet-service", Message: "c"},
		{Service: "wallet-service", Message: "b"},
		{Service: "orders-service", HTTPStatus: 429},
		{Service: "wallet-service", Message: "a"},
		{Service: "orders-service", Message: "z"},
		{Service: "storefront", HTTPStatus: 429},
		{Service: "storefront", Message: "y"},
		{Service: "wallet-service", Message: "a"},
	}
	r := mustLoad(t, "", "- {id: rl, class: limit, match: {any: [{http_status: 429}]}}\n", labHosts).Group(samples)
	type row struct {
		class              Class
		service, signature string
		count              int
	}
	want := []row{
		{ClassCode, "wallet-service", "a", 2},
		{ClassCode, "orders-service", "z", 1},
		{ClassCode, "storefront", "y", 1},
		{ClassCode, "wallet-service", "b", 1},
		{ClassCode, "wallet-service", "c", 1},
		{ClassLimit, "orders-service", "HTTP 429", 1},
		{ClassLimit, "storefront", "HTTP 429", 1},
	}
	var got []row
	for _, g := range r.Groups {
		got = append(got, row{g.Class, g.Service, g.Signature, g.Count})
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groups =\n%v\nwant\n%v", got, want)
	}
}

func TestGroupRuleIDIsTheMostCommonRule(t *testing.T) {
	// Two rules give the same class to the same message; which one decided a
	// sample depends on an attribute.
	const rules = `
- {id: b-rule, class: account, match: {all: [{attr_equals: {via: b}}]}}
- {id: a-rule, class: account, match: {all: [{attr_equals: {via: a}}]}}
`
	rs := mustLoad(t, rules, "", labHosts)
	sample := func(via string) Sample {
		return Sample{Service: "wallet-service", Message: "AccountSuspended", Attrs: map[string]string{"via": via}}
	}
	tests := []struct {
		name string
		vias []string
		want string
	}{
		{"majority", []string{"b", "a", "b"}, "b-rule"},
		{"tie goes to the smallest id", []string{"b", "a"}, "a-rule"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var samples []Sample
			for _, v := range tt.vias {
				samples = append(samples, sample(v))
			}
			r := rs.Group(samples)
			if len(r.Groups) != 1 || r.Groups[0].RuleID != tt.want {
				t.Errorf("groups = %+v, want one group decided by %s", r.Groups, tt.want)
			}
		})
	}
}

func TestGroupExamples(t *testing.T) {
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	samples := []Sample{
		{Time: at(5), Service: "orders-service", Message: "payment for ord_000005 failed"},
		{Time: at(3), Service: "orders-service", Message: "payment for ord_000004 failed"},
		{Time: at(9), Service: "orders-service", Message: "payment for ord_000001 failed"},
		{Time: at(1), Service: "orders-service", Message: "payment for ord_000001 failed"}, // the same message, earlier
		{Time: at(3), Service: "orders-service", Message: "  payment for ord_000003 failed\n"},
	}
	r := mustLoad(t, "", "", labHosts).Group(samples)
	if len(r.Groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(r.Groups), r.Groups)
	}
	want := []string{
		"payment for ord_000001 failed", // first seen at 1s
		"payment for ord_000003 failed", // 3s, alphabetically before ord_000004, and trimmed
		"payment for ord_000004 failed",
	}
	if g := r.Groups[0]; !slices.Equal(g.Examples, want) || !g.First.Equal(at(1)) || !g.Last.Equal(at(9)) || g.Count != 5 || g.Share != 1 {
		t.Errorf("group = %+v\nwant Examples %q, First 1s, Last 9s, Count 5, Share 1", g, want)
	}
}

func TestGroupNoSamples(t *testing.T) {
	r := loadBuiltins(t).Group(nil)
	if r.Total != 0 || r.Groups == nil || len(r.Groups) != 0 || r.Shares == nil || len(r.Shares) != 0 {
		t.Errorf("Group(nil) = %+v, want an empty, non-nil report", r)
	}
}

func TestGroupDoesNotDependOnSampleOrder(t *testing.T) {
	samples := suspendedCheckouts(5)
	samples = append(samples,
		Sample{Time: t0, Service: "orders-service", HTTPStatus: 500, ExceptionType: "runtime.boundsError", Message: "runtime error: index out of range [1] with length 1"},
		Sample{Time: t0.Add(time.Hour), Service: "wallet-service", HTTPStatus: 504, Message: "mockpay did not answer in time: " + timeoutText},
	)
	rs := loadBuiltins(t)
	want := rs.Group(samples)
	rng := rand.New(rand.NewPCG(1, 2)) // fixed seed: the same orders on every run
	for range 20 {
		shuffled := slices.Clone(samples)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := rs.Group(shuffled); !reflect.DeepEqual(got, want) {
			t.Fatalf("Group() changed with the sample order:\n%+v\nwant\n%+v", got, want)
		}
	}
}
