package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/alloteyphil/wasitus/demo/internal/labtest"
)

// The client checks certificates against the real clock, so the test
// certificates use fixed dates far from today: the outcome never depends on
// when the test runs. Everything is made at runtime; nothing is committed.
var (
	longAgo  = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	ranOut   = time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	farAhead = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "wallet test CA"},
		NotBefore:             longAgo,
		NotAfter:              farAhead,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf issues a certificate for 127.0.0.1, where httptest listens.
func (ca testCA) leaf(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "mockpay"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// caFile writes the CA certificate where MOCKPAY_CA_FILE can point to it.
func (ca testCA) caFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mockpay-ca.pem")
	if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tlsMockpay is mockpay over HTTPS with the given certificate, charging
// successfully whenever a client gets past the handshake.
func tlsMockpay(t *testing.T, cert tls.Certificate) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id": "ch_000042", "status": "succeeded"}`)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // failed handshakes are expected
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// pay builds wallet-service from its environment, as main does, and sends
// it one payment.
func pay(t *testing.T) (*httptest.ResponseRecorder, *tracetest.SpanRecorder) {
	t.Helper()
	tel, rec := labtest.Telemetry()
	h, err := build(tel)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(validPayment)))
	return resp, rec
}

// Scenario S5: mockpay serves an expired certificate.
func TestExpiredProviderCertificate(t *testing.T) {
	ca := newTestCA(t)
	mockpay := tlsMockpay(t, ca.leaf(t, longAgo, ranOut))
	t.Setenv("MOCKPAY_URL", mockpay.URL)
	t.Setenv("MOCKPAY_CA_FILE", ca.caFile(t))

	resp, rec := pay(t)
	if resp.Code != http.StatusBadGateway || errorCode(t, resp.Body.String()) != "PaymentProviderUnreachable" {
		t.Fatalf("got %d %s, want 502 PaymentProviderUnreachable", resp.Code, resp.Body)
	}

	const expiry = "certificate has expired or is not yet valid"
	server := labtest.OnlySpan(t, rec, trace.SpanKindServer)
	if server.Status().Code != codes.Error {
		t.Errorf("server span status = %v, want Error", server.Status().Code)
	}
	if got := labtest.Attr(server, "payment.provider"); got != "mockpay" {
		t.Errorf("payment.provider = %q, want mockpay", got)
	}
	typ, msg, ok := labtest.Exception(server)
	if !ok || !strings.Contains(msg, expiry) {
		t.Errorf("server span exception = %q (found %v), want it to mention %q", msg, ok, expiry)
	}
	t.Logf("server span exception: %s: %s", typ, msg)

	// The handshake failed, so mockpay never answered: no HTTP status.
	client := labtest.OnlySpan(t, rec, trace.SpanKindClient)
	if got := labtest.Attr(client, "http.response.status_code"); got != "" {
		t.Errorf("client span http.response.status_code = %q, want none", got)
	}
	if client.Status().Code != codes.Error || !strings.Contains(client.Status().Description, expiry) {
		t.Errorf("client span status = %v %q, want Error mentioning %q", client.Status().Code, client.Status().Description, expiry)
	}
}

func TestTrustsProviderCAFromFile(t *testing.T) {
	ca := newTestCA(t)
	mockpay := tlsMockpay(t, ca.leaf(t, longAgo, farAhead))
	t.Setenv("MOCKPAY_URL", mockpay.URL)

	t.Run("with the CA file", func(t *testing.T) {
		t.Setenv("MOCKPAY_CA_FILE", ca.caFile(t))
		resp, _ := pay(t)
		if resp.Code != http.StatusCreated || !strings.Contains(resp.Body.String(), `"payment_id":"ch_000042"`) {
			t.Errorf("got %d %s, want 201 with payment_id ch_000042", resp.Code, resp.Body)
		}
	})

	t.Run("without it", func(t *testing.T) {
		t.Setenv("MOCKPAY_CA_FILE", "")
		resp, _ := pay(t)
		if resp.Code != http.StatusBadGateway || errorCode(t, resp.Body.String()) != "PaymentProviderUnreachable" {
			t.Errorf("got %d %s, want 502 PaymentProviderUnreachable (lab CA not trusted)", resp.Code, resp.Body)
		}
	})
}

func TestBadCAFileStopsStartup(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "not-a-certificate.pem")
	if err := os.WriteFile(notPEM, []byte("this is not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing file": filepath.Join(dir, "absent.pem"),
		"not PEM":      notPEM,
		"a directory":  dir,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MOCKPAY_CA_FILE", path)
			tel, _ := labtest.Telemetry()
			_, err := build(tel)
			if err == nil || !strings.Contains(err.Error(), "MOCKPAY_CA_FILE") {
				t.Errorf("build error = %v, want one naming MOCKPAY_CA_FILE", err)
			}
		})
	}
}
