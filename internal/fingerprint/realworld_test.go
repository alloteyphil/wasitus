package fingerprint

import "testing"

// TestOurHostsOutsideCompose covers deployments where our own services are
// not reached by their bare service name: Kubernetes DNS names, pod and VPC
// IPs, and hosts the operator lists in internal_hosts.
func TestOurHostsOutsideCompose(t *testing.T) {
	rs, err := LoadRules(nil, Options{InternalHosts: []string{
		"orders-service", "wallet-service", "storefront", ".corp.example",
	}})
	if err != nil {
		t.Fatal(err)
	}
	k8sSlow := `wallet did not answer: Post "http://wallet-service.payments.svc.cluster.local:8080/charge": context deadline exceeded`

	tests := []struct {
		name      string
		s         Sample
		wantClass Class
	}{
		{"k8s DNS name in a timeout message", Sample{Service: "orders-service", HTTPStatus: 504, Message: k8sSlow}, ClassCode},
		{"k8s DNS name as server address", Sample{Service: "orders-service", Outbound: true, ServerAddress: "wallet-service.payments.svc.cluster.local:8080", Message: "context deadline exceeded"}, ClassCode},
		{"pod IP refused", Sample{Service: "orders-service", Outbound: true, ServerAddress: "10.0.3.7:8080", Message: "dial tcp 10.0.3.7:8080: connect: connection refused"}, ClassCode},
		{"loopback", Sample{Service: "orders-service", Outbound: true, ServerAddress: "127.0.0.1:9000", HTTPStatus: 503}, ClassCode},
		{"operator domain", Sample{Service: "orders-service", Outbound: true, ServerAddress: "billing.corp.example", HTTPStatus: 503}, ClassCode},
		// Still third parties:
		{"public IP refused", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "203.0.113.9:443", Message: "connect: connection refused"}, ClassDependency},
		{"provider name sharing no label", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "api.mockpay.test", HTTPStatus: 503}, ClassDependency},
		// Known limit: any host whose first label is a service name counts as
		// ours, even under someone else's domain.
		{"first label wins even under another domain", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "orders-service.evil.test", HTTPStatus: 503}, ClassCode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, rule := rs.Classify(tc.s); got != tc.wantClass {
				t.Errorf("Classify() = %s/%s, want %s", got, rule, tc.wantClass)
			}
		})
	}
}

// TestNarrowerBuiltins checks wording and statuses that used to be read as
// someone else's fault when they are usually ours.
func TestNarrowerBuiltins(t *testing.T) {
	rs := loadBuiltins(t)
	tests := []struct {
		name      string
		s         Sample
		wantClass Class
	}{
		{"coupon not yet valid is our code", Sample{Service: "orders-service", HTTPStatus: 400, Message: "coupon not yet valid"}, ClassCode},
		{"certificate not yet valid is expiry", Sample{Service: "wallet-service", Message: "x509: certificate is not yet valid"}, ClassExpiry},
		{"provider 400 is our bad request", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 400}, ClassCode},
		{"provider 401 is our bad key", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 401}, ClassCode},
		{"provider 422 is our bad payload", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 422}, ClassCode},
		{"provider 500 is theirs", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 500}, ClassDependency},
		{"provider call with no status is theirs", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay"}, ClassDependency},
		{"provider 402 is still account", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 402}, ClassAccount},
		{"provider 429 is still limit", Sample{Service: "wallet-service", Outbound: true, ServerAddress: "mockpay", HTTPStatus: 429}, ClassLimit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, rule := rs.Classify(tc.s); got != tc.wantClass {
				t.Errorf("Classify() = %s/%s, want %s", got, rule, tc.wantClass)
			}
		})
	}
}
