package lab

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace/noop"
)

func quietTelemetry() *Telemetry {
	return &Telemetry{
		Tracer:     noop.NewTracerProvider(),
		Propagator: propagation.TraceContext{},
		Log:        slog.New(slog.DiscardHandler),
	}
}

// certPEM encodes a certificate as the PEM a CA file holds.
func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// testCA makes a throwaway self-signed CA certificate at runtime, so no
// certificate or key is ever committed. Its fixed validity does not depend
// on the clock.
func testCA(tb testing.TB) []byte {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "lab test CA"},
		NotBefore:             time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatal(err)
	}
	return der
}

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCAFileRejectsBadFiles(t *testing.T) {
	ca := certPEM(testCA(t))
	notACert := pem.EncodeToMemory(&pem.Block{Type: "NOT A CERTIFICATE", Bytes: []byte("lab")})
	tests := map[string][]byte{
		"empty":                     {},
		"not PEM":                   []byte("this is not a certificate\n"),
		"junk inside a PEM block":   certPEM([]byte("not DER")),
		"only a non-certificate":    notACert,
		"certificate, then another": append(append([]byte{}, ca...), notACert...),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadCAFile(writeFile(t, data)); err == nil {
				t.Error("LoadCAFile succeeded, want an error")
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		if _, err := LoadCAFile(filepath.Join(t.TempDir(), "absent.pem")); err == nil {
			t.Error("LoadCAFile succeeded, want an error")
		}
	})
}

func TestLoadCAFileKeepsEveryCertificate(t *testing.T) {
	first, second := testCA(t), testCA(t)
	pool, err := LoadCAFile(writeFile(t, append(certPEM(first), certPEM(second)...)))
	if err != nil {
		t.Fatal(err)
	}
	for i, der := range [][]byte{first, second} {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)}); err != nil {
			t.Errorf("certificate %d is not trusted by the pool: %v", i+1, err)
		}
	}
}

func TestClientTrustsExtraCA(t *testing.T) {
	// httptest's certificate is self-signed and valid until 2084.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		WriteJSON(w, http.StatusCreated, map[string]string{"id": "ch_000001"})
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the failed handshake below is expected
	srv.StartTLS()
	t.Cleanup(srv.Close)

	pool, err := LoadCAFile(writeFile(t, certPEM(srv.Certificate().Raw)))
	if err != nil {
		t.Fatal(err)
	}

	var out struct{ ID string }
	err = NewClient(quietTelemetry(), 5*time.Second, WithRootCAs(pool)).
		PostJSON(context.Background(), srv.URL, map[string]int{"amount": 2500}, &out)
	if err != nil || out.ID != "ch_000001" {
		t.Errorf("with the CA: got %q, %v; want ch_000001", out.ID, err)
	}

	// Callers that pass no option keep the system's roots, which do not
	// include the test server's certificate.
	err = NewClient(quietTelemetry(), 5*time.Second).
		PostJSON(context.Background(), srv.URL, map[string]int{"amount": 2500}, &out)
	if err == nil {
		t.Error("without the CA: call succeeded, want a certificate error")
	}
}

func FuzzParseCAs(f *testing.F) {
	ca := certPEM(testCA(f))
	f.Add(ca)
	f.Add(append(append([]byte{}, ca...), ca...))
	f.Add([]byte{})
	f.Add([]byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"))
	f.Add(pem.EncodeToMemory(&pem.Block{Type: "NOT A CERTIFICATE", Bytes: []byte("lab")}))
	f.Fuzz(func(t *testing.T, data []byte) {
		certs, err := parseCAs(data)
		if err == nil && len(certs) == 0 {
			t.Error("no error, but no certificate either")
		}
	})
}
