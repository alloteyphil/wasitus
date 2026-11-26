package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// labNow is the clock the certificates are made with, and the clock the
// tests' TLS clients check them against.
var labNow = time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)

const expiryText = "certificate has expired or is not yet valid"

func testCerts(t *testing.T) *labCerts {
	t.Helper()
	certs, err := newLabCerts(parseHosts(defaultTLSHosts), labNow)
	if err != nil {
		t.Fatal(err)
	}
	return certs
}

// startBoth serves one mockpay over plain HTTP (for the admin calls) and over
// HTTPS, as run does. It returns the plain server and the HTTPS address.
func startBoth(t *testing.T, mode Mode, certs *labCerts) (*httptest.Server, string) {
	t.Helper()
	s := NewServer(testConfig(), mode, slog.New(slog.DiscardHandler), labNowFunc)
	plain := httptest.NewServer(s.Handler())
	t.Cleanup(plain.Close)

	// A real http.Server rather than httptest's StartTLS, which would add its
	// own certificate and serve it to clients that send no server name.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secure := &http.Server{
		Handler:           s.Handler(),
		TLSConfig:         s.TLSConfig(certs),
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0), // failed handshakes are expected
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = secure.ServeTLS(ln, "", "")
	}()
	t.Cleanup(func() {
		_ = secure.Close()
		<-done
	})
	return plain, ln.Addr().String()
}

func labNowFunc() time.Time { return labNow }

// clientTLS trusts only the lab CA, and checks certificates at labNow.
func clientTLS(t *testing.T, certs *labCerts, serverName string) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certs.caPEM) {
		t.Fatal("CA PEM holds no certificate")
	}
	return &tls.Config{RootCAs: pool, ServerName: serverName, Time: labNowFunc, MinVersion: tls.VersionTLS12}
}

func handshake(addr string, cfg *tls.Config) error {
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	return conn.Close()
}

// isExpired reports whether err is a certificate verification failure
// because the certificate is out of date, with the x509 text intact.
func isExpired(err error) bool {
	var invalid x509.CertificateInvalidError
	return errors.As(err, &invalid) && invalid.Reason == x509.Expired &&
		strings.Contains(err.Error(), expiryText)
}

func setMode(t *testing.T, plain *httptest.Server, mode Mode) {
	t.Helper()
	res, err := post(context.Background(), t, plain, "/admin/mode", http.MethodPut, `{"mode": "`+string(mode)+`"}`)
	if err != nil || res.status != http.StatusOK {
		t.Fatalf("switch to %s: %+v, %v", mode, res, err)
	}
}

func TestLabCerts(t *testing.T) {
	certs := testCerts(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certs.caPEM)

	block, rest := pem.Decode(certs.caPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatalf("CA PEM should be exactly one CERTIFICATE block, got %q", certs.caPEM)
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA {
		t.Fatalf("CA certificate: IsCA=%v, %v", ca != nil && ca.IsCA, err)
	}

	for _, host := range []string{"localhost", "127.0.0.1", "mockpay"} {
		valid := x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: labNow}
		if _, err := certs.valid.Leaf.Verify(valid); err != nil {
			t.Errorf("valid leaf for %s: %v", host, err)
		}
		if _, err := certs.expired.Leaf.Verify(valid); !isExpired(err) {
			t.Errorf("expired leaf for %s: err = %v, want the expiry error", host, err)
		}
		// Expired is all that is wrong with it: it was fine while it lasted.
		earlier := valid
		earlier.CurrentTime = certs.expired.Leaf.NotAfter.Add(-time.Hour)
		if _, err := certs.expired.Leaf.Verify(earlier); err != nil {
			t.Errorf("expired leaf for %s, before it ran out: %v", host, err)
		}
	}
	if got, want := certs.expired.Leaf.NotAfter, labNow.Add(-expiredFor); !got.Equal(want) {
		t.Errorf("expired leaf ran out at %s, want %s", got, want)
	}
}

func TestParseHosts(t *testing.T) {
	tests := map[string][]string{
		"localhost,127.0.0.1,mockpay": {"localhost", "127.0.0.1", "mockpay"},
		" mockpay , ,::1 ":            {"mockpay", "::1"},
		" , ":                         nil,
	}
	for in, want := range tests {
		if got := parseHosts(in); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("parseHosts(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandshakeFollowsMode(t *testing.T) {
	certs := testCerts(t)
	plain, addr := startBoth(t, ModeHealthy, certs)

	// Clients that send a server name, and one that connects by IP and sends
	// none: the certificate is chosen by mode either way.
	names := []string{"", "localhost", "mockpay"}
	check := func(wantExpired bool) {
		t.Helper()
		for _, name := range names {
			err := handshake(addr, clientTLS(t, certs, name))
			switch {
			case wantExpired && !isExpired(err):
				t.Errorf("server name %q: err = %v, want the expiry error", name, err)
			case !wantExpired && err != nil:
				t.Errorf("server name %q: %v, want a good handshake", name, err)
			}
		}
	}

	check(false)
	setMode(t, plain, ModeBadCert)
	check(true)
	if res := postCharge(t, plain); res.status != http.StatusCreated {
		t.Errorf("plain HTTP in badcert: status = %d, want 201", res.status)
	}
	setMode(t, plain, ModeHealthy)
	check(false)
}

func TestBadCertStartsWithExpiredCertificate(t *testing.T) {
	certs := testCerts(t)
	_, addr := startBoth(t, ModeBadCert, certs)
	if err := handshake(addr, clientTLS(t, certs, "")); !isExpired(err) {
		t.Errorf("err = %v, want the expiry error", err)
	}
}

// A client keeps reusing a connection opened before the switch, so badcert
// must close it, or the expired certificate would never be seen.
func TestBadCertClosesOpenConnections(t *testing.T) {
	tests := []struct {
		name      string
		http2     bool
		wantProto int
		// HTTP/2 closes the connection with a GOAWAY frame, which races with
		// the next request; that request is then retried, or at worst served
		// on the old connection, so allow one extra.
		maxExtra int
	}{
		{"HTTP/1.1", false, 1, 0},
		{"HTTP/2", true, 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			certs := testCerts(t)
			plain, addr := startBoth(t, ModeHealthy, certs)
			client := &http.Client{Transport: &http.Transport{
				TLSClientConfig:   clientTLS(t, certs, ""),
				ForceAttemptHTTP2: tt.http2,
			}}
			t.Cleanup(client.CloseIdleConnections)
			charge := func() (*http.Response, error) {
				resp, err := client.Post("https://"+addr+"/v1/charges", "application/json", strings.NewReader(validCharge))
				if err != nil {
					return nil, err
				}
				defer resp.Body.Close()
				_, err = io.Copy(io.Discard, resp.Body)
				return resp, err
			}

			resp, err := charge()
			if err != nil || resp.StatusCode != http.StatusCreated {
				t.Fatalf("healthy charge: %v, %v", resp, err)
			}
			if resp.ProtoMajor != tt.wantProto {
				t.Fatalf("protocol HTTP/%d, want %s", resp.ProtoMajor, tt.name)
			}

			setMode(t, plain, ModeBadCert)
			// The connection is already open, so this charge goes through,
			// and the answer tells the client to close the connection.
			if resp, err := charge(); err != nil || resp.StatusCode != http.StatusCreated {
				t.Fatalf("charge on the open connection: %v, %v", resp, err)
			}
			for i := 0; ; i++ {
				_, err := charge()
				if isExpired(err) {
					break
				}
				if err != nil || i >= tt.maxExtra {
					t.Fatalf("charge %d after the switch: err = %v, want the expiry error", i+2, err)
				}
			}
		})
	}
}

func TestSetupTLSWritesCA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mockpay-ca.pem")
	t.Setenv("MOCKPAY_CA_OUT", path)
	t.Setenv("MOCKPAY_TLS_HOSTS", "mockpay")

	certs, err := setupTLS(labNow)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(certs.caPEM) {
		t.Errorf("file holds %q, want the CA certificate", got)
	}
	if strings.Contains(string(got), "PRIVATE") {
		t.Error("the CA file must hold only the public certificate")
	}
	if names := certs.valid.Leaf.DNSNames; len(names) != 1 || names[0] != "mockpay" {
		t.Errorf("certificate names = %q, want [mockpay] from MOCKPAY_TLS_HOSTS", names)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Errorf("directory holds %d files (%v), want only the CA file, no temporary file", len(entries), err)
	}
}

func TestSetupTLSErrors(t *testing.T) {
	t.Run("no hosts", func(t *testing.T) {
		t.Setenv("MOCKPAY_TLS_HOSTS", " , ")
		if _, err := setupTLS(labNow); err == nil {
			t.Error("setupTLS succeeded, want an error")
		}
	})
	t.Run("unwritable CA path", func(t *testing.T) {
		t.Setenv("MOCKPAY_CA_OUT", filepath.Join(t.TempDir(), "missing-dir", "ca.pem"))
		if _, err := setupTLS(labNow); err == nil {
			t.Error("setupTLS succeeded, want an error")
		}
	})
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("MOCKPAY_TLS_HOSTS", "")
		t.Setenv("MOCKPAY_CA_OUT", "")
		certs, err := setupTLS(labNow)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(certs.hosts, ","); got != defaultTLSHosts {
			t.Errorf("hosts = %q, want %q", got, defaultTLSHosts)
		}
	})
}
