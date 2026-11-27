package lab

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
)

// ClientOption changes how NewClient builds a Client.
type ClientOption func(*clientConfig)

type clientConfig struct {
	rootCAs *x509.CertPool
}

// WithRootCAs makes the client verify HTTPS servers against pool instead of
// the system's roots. Get a pool that adds a lab CA to the system's roots
// from LoadCAFile.
func WithRootCAs(pool *x509.CertPool) ClientOption {
	return func(c *clientConfig) { c.rootCAs = pool }
}

// LoadCAFile returns the system's trusted roots plus the CA certificates in
// the PEM file at path, such as the lab CA that mockpay writes out. Every PEM
// block in the file must be a certificate: a file that cannot be read, holds
// no certificate, or holds anything else is an error, so a bad setting stops
// the service at startup instead of failing every call later.
func LoadCAFile(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	certs, err := parseCAs(data)
	if err != nil {
		return nil, fmt.Errorf("CA file %s: %w", path, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool() // no system roots here: trust only the file
	}
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool, nil
}

// parseCAs decodes every PEM block in data as a certificate.
func parseCAs(data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("block %d is %q, want only CERTIFICATE blocks", len(certs)+1, block.Type)
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", len(certs)+1, err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, errors.New("no PEM certificate found")
	}
	return certs, nil
}

// transportTrusting returns a copy of http.DefaultTransport that verifies
// servers against pool.
func transportTrusting(pool *x509.CertPool) *http.Transport {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		tr = tr.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return tr
}
