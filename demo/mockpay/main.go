// Command mockpay is a fake external payment provider for the demo lab.
//
// It plays a company we do not control. Its failure mode can be switched while
// it runs, to stage incidents that are not the lab's fault: an outage, slow
// responses, a suspended account, a rate limit or an expired certificate.
//
// On purpose, mockpay sends no telemetry to the lab's SigNoz. A real provider
// is a black box, so wasitus must recognise mockpay's failures only from what
// the lab's own services observe when they call it.
//
// mockpay answers the same API over plain HTTP and over HTTPS. At every start
// it creates, in memory, a lab CA and two certificates signed by it: a valid
// one and one that expired 30 days ago. HTTPS serves the expired one while the
// mode is badcert, and the valid one otherwise; plain HTTP works the same in
// every mode. The private keys never leave the process. To let the lab's
// services trust the CA, mockpay can write the CA certificate to a file.
//
// Modes: healthy, outage, slow, suspended, ratelimit, badcert.
//
// Settings come from environment variables:
//
//	MOCKPAY_ADDR        plain HTTP listen address (default ":8090")
//	MOCKPAY_TLS_ADDR    HTTPS listen address (default ":8443")
//	MOCKPAY_TLS_HOSTS   names the certificates are for, comma-separated
//	                    (default "localhost,127.0.0.1,mockpay")
//	MOCKPAY_CA_OUT      if set, write the lab CA certificate (PEM) to this path
//	MOCKPAY_MODE        starting mode (default "healthy")
//	MOCKPAY_SLOW_DELAY  delay in slow mode (default "6s")
//	MOCKPAY_RATE_LIMIT  charges per minute in ratelimit mode (default 60)
//
// Switch modes at runtime:
//
//	curl -X PUT localhost:8090/admin/mode -d '{"mode": "badcert"}'
//
// and see the result over HTTPS:
//
//	MOCKPAY_CA_OUT=/tmp/mockpay-ca.pem go run ./mockpay
//	curl --cacert /tmp/mockpay-ca.pem https://localhost:8443/healthz
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("mockpay stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := DefaultConfig()
	addr := envOr("MOCKPAY_ADDR", ":8090")
	mode, err := parseMode(envOr("MOCKPAY_MODE", string(ModeHealthy)))
	if err != nil {
		return fmt.Errorf("MOCKPAY_MODE: %w", err)
	}
	if v := os.Getenv("MOCKPAY_SLOW_DELAY"); v != "" {
		if cfg.SlowDelay, err = time.ParseDuration(v); err != nil {
			return fmt.Errorf("MOCKPAY_SLOW_DELAY: %w", err)
		}
	}
	if v := os.Getenv("MOCKPAY_RATE_LIMIT"); v != "" {
		if cfg.RateLimit, err = strconv.Atoi(v); err != nil || cfg.RateLimit < 1 {
			return fmt.Errorf("MOCKPAY_RATE_LIMIT: want a positive whole number, got %q", v)
		}
	}

	tlsAddr := envOr("MOCKPAY_TLS_ADDR", ":8443")
	certs, err := setupTLS(time.Now())
	if err != nil {
		return err
	}

	server := NewServer(cfg, mode, log, time.Now)
	handler := server.Handler()
	plain := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	secure := &http.Server{
		Addr:              tlsAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         server.TLSConfig(certs),
		// Failed handshakes (every new connection in badcert mode) are
		// logged as JSON like everything else.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 2)
	go func() { errc <- plain.ListenAndServe() }()
	go func() { errc <- secure.ListenAndServeTLS("", "") }() // certificates come from TLSConfig
	log.Info("mockpay listening", "addr", addr, "tls_addr", tlsAddr, "tls_hosts", certs.hosts, "mode", mode)

	select {
	case err := <-errc:
		// One listener failed (its port is taken, say): stop the other too.
		return errors.Join(err, plain.Close(), secure.Close())
	case <-ctx.Done():
	}

	// Give in-flight requests a moment, then drop the rest: a hanging outage
	// request would otherwise hold up shutdown for up to 30 seconds.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var errs []error
	for _, srv := range []*http.Server{plain, secure} {
		if err := srv.Shutdown(shutdownCtx); errors.Is(err, context.DeadlineExceeded) {
			_ = srv.Close()
		} else if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
