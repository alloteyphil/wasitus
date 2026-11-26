// Command mockpay is a fake external payment provider for the demo lab.
//
// It plays a company we do not control. Its failure mode can be switched while
// it runs, to stage incidents that are not the lab's fault: an outage, slow
// responses, a suspended account or a rate limit.
//
// On purpose, mockpay sends no telemetry to the lab's SigNoz. A real provider
// is a black box, so wasitus must recognise mockpay's failures only from what
// the lab's own services observe when they call it.
//
// Settings come from environment variables:
//
//	MOCKPAY_ADDR        listen address (default ":8090")
//	MOCKPAY_MODE        starting mode (default "healthy")
//	MOCKPAY_SLOW_DELAY  delay in slow mode (default "6s")
//	MOCKPAY_RATE_LIMIT  charges per minute in ratelimit mode (default 60)
//
// Switch modes at runtime:
//
//	curl -X PUT localhost:8090/admin/mode -d '{"mode": "outage"}'
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

	srv := &http.Server{
		Addr:              addr,
		Handler:           NewServer(cfg, mode, log, time.Now).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Info("mockpay listening", "addr", addr, "mode", mode)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// Give in-flight requests a moment, then drop the rest: a hanging outage
	// request would otherwise hold up shutdown for up to 30 seconds.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); errors.Is(err, context.DeadlineExceeded) {
		_ = srv.Close()
	} else if err != nil {
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
