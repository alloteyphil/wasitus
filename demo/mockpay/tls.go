package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultTLSHosts are the names the certificates are valid for, unless
// MOCKPAY_TLS_HOSTS says otherwise: local runs, and the lab's compose network.
const defaultTLSHosts = "localhost,127.0.0.1,mockpay"

// How long mockpay's certificates are valid, counted from startup.
const (
	clockSkew  = time.Hour            // the valid leaf starts a little early
	leafLife   = 90 * 24 * time.Hour  // like a typical public certificate
	expiredFor = 30 * 24 * time.Hour  // the expired leaf ran out this long ago
	caLife     = 365 * 24 * time.Hour // the CA outlives both leaves
)

// labCerts is mockpay's TLS identity, made in memory at every start: a lab
// CA and two leaf certificates it signed for the same hosts, one valid and
// one expired. The private keys never leave the process; only the CA
// certificate is ever written out, so the lab's services can trust it.
type labCerts struct {
	hosts   []string
	caPEM   []byte // the CA certificate, public part only
	valid   *tls.Certificate
	expired *tls.Certificate
}

// newLabCerts creates the lab CA and both leaves for hosts (DNS names or IP
// addresses), with validity periods counted from now.
func newLabCerts(hosts []string, now time.Time) (*labCerts, error) {
	if len(hosts) == 0 {
		return nil, errors.New("no TLS host names")
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	caTmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "mockpay lab CA", Organization: []string{"wasitus demo lab"}},
		NotBefore:             now.Add(-leafLife - expiredFor - clockSkew),
		NotAfter:              now.Add(caLife),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := sign(caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("CA certificate: %w", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("CA certificate: %w", err)
	}

	valid, err := issueLeaf(hosts, now.Add(-clockSkew), now.Add(leafLife), ca, caKey)
	if err != nil {
		return nil, fmt.Errorf("valid certificate: %w", err)
	}
	expiredAt := now.Add(-expiredFor)
	expired, err := issueLeaf(hosts, expiredAt.Add(-leafLife), expiredAt, ca, caKey)
	if err != nil {
		return nil, fmt.Errorf("expired certificate: %w", err)
	}
	return &labCerts{
		hosts:   hosts,
		caPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		valid:   valid,
		expired: expired,
	}, nil
}

// issueLeaf makes a server certificate for hosts, valid from notBefore to
// notAfter and signed by the CA, with its own new key.
func issueLeaf(hosts []string, notBefore, notAfter time.Time, ca *x509.Certificate, caKey crypto.Signer) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: hosts[0], Organization: []string{"mockpay"}},
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := sign(tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// sign gives tmpl a random serial number and signs it with the parent's key.
func sign(tmpl, parent *x509.Certificate, pub crypto.PublicKey, parentKey crypto.Signer) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial number: %w", err)
	}
	tmpl.SerialNumber = serial
	return x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
}

// TLSConfig chooses the certificate for each new HTTPS connection: the
// expired one while the mode is badcert, the valid one otherwise.
func (s *Server) TLSConfig(certs *labCerts) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if s.Mode() == ModeBadCert {
				return certs.expired, nil
			}
			return certs.valid, nil
		},
	}
}

// setupTLS creates the certificates for MOCKPAY_TLS_HOSTS, and writes the CA
// certificate to MOCKPAY_CA_OUT when that is set.
func setupTLS(now time.Time) (*labCerts, error) {
	hosts := parseHosts(envOr("MOCKPAY_TLS_HOSTS", defaultTLSHosts))
	if len(hosts) == 0 {
		return nil, errors.New("MOCKPAY_TLS_HOSTS: want comma-separated host names or IP addresses")
	}
	certs, err := newLabCerts(hosts, now)
	if err != nil {
		return nil, fmt.Errorf("create TLS certificates: %w", err)
	}
	if path := os.Getenv("MOCKPAY_CA_OUT"); path != "" {
		if err := writeCA(path, certs.caPEM); err != nil {
			return nil, fmt.Errorf("MOCKPAY_CA_OUT: %w", err)
		}
	}
	return certs, nil
}

// parseHosts splits a comma-separated host list, ignoring blanks.
func parseHosts(s string) []string {
	var hosts []string
	for h := range strings.SplitSeq(s, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// writeCA writes the CA certificate where the lab's services can read it.
// It writes a temporary file and renames it into place, so a service that
// starts at the same moment never reads half a certificate.
func writeCA(path string, caPEM []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mockpay-ca-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // nothing left to remove after the rename
	if _, err := tmp.Write(caPEM); err != nil {
		_ = tmp.Close()
		return err
	}
	// Readable by every lab container: it is a public certificate.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
