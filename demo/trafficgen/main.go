// Command trafficgen is the demo lab's customers: it sends a steady stream of
// checkouts to storefront, so every staged incident happens under load.
//
// It deliberately sends no telemetry. Real customers do not report to our
// SigNoz, so the lab sees this traffic only through storefront's own spans,
// as it would in production.
//
// Each request is a POST to TRAFFIC_TARGET with a body such as
//
//	{"customer_id": "cus_0042", "amount": 2500, "currency": "GHS"}
//
// for customers cus_0001 to cus_0200 and amounts of 500 to 50000 minor units.
//
// # Usage
//
// From demo/, with storefront running:
//
//	go run ./trafficgen                          # 5 checkouts a second to localhost:8080
//	TRAFFIC_RPS=20 TRAFFIC_SEED=42 go run ./trafficgen
//
// Settings come from environment variables:
//
//	TRAFFIC_TARGET         checkout URL (default "http://localhost:8080/checkout")
//	TRAFFIC_RPS            base requests per second, at least 1 (default 5)
//	TRAFFIC_SEED           payload seed, for the same customers and amounts on
//	                       every run (default: random, and logged at start)
//	TRAFFIC_MAX_IN_FLIGHT  requests waiting for an answer at once (default 100)
//	TRAFFIC_TIMEOUT        per-request timeout (default "10s")
//
// Slow answers never slow the schedule. When TRAFFIC_MAX_IN_FLIGHT requests
// are already waiting, a request that falls due is dropped and counted
// instead of sent late.
//
// # Output
//
// Logs are JSON on stdout. Every 10 seconds one summary line covers the
// window since the previous one:
//
//	{"level":"INFO","msg":"summary","window_s":10,"sent":50,"completed":{"201":48,"502":2},
//	 "transport_errors":0,"dropped":0,"in_flight":0,"multiplier":1,"target_rps":5}
//
// completed counts answers by HTTP status code; transport_errors counts
// requests with no answer at all (timeout, refused connection), with the
// latest one as last_error. On SIGTERM or Ctrl-C trafficgen stops sending,
// waits up to 5 seconds for requests in flight, and logs the run's totals.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alloteyphil/wasitus/demo/internal/lab"
)

// tickEvery is how often the schedule checks for due requests. At 10 ms a
// rate up to 100 rps sends at most one request per tick.
const tickEvery = 10 * time.Millisecond

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, log)
	stop()
	if err != nil {
		log.Error("trafficgen stopped", "error", err)
		os.Exit(1)
	}
}

// run sends traffic until ctx is cancelled.
func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	gen := NewGenerator(cfg, log)

	log.Info("trafficgen starting", "target", cfg.Target, "base_rps", cfg.BaseRPS, "seed", cfg.Seed,
		"max_in_flight", cfg.MaxInFlight, "timeout", cfg.Timeout.String())

	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	gen.Run(ctx, ticker.C)
	return nil
}

// loadConfig reads the settings from the environment.
func loadConfig() (Config, error) {
	cfg := Config{
		Target:       lab.Env("TRAFFIC_TARGET", "http://localhost:8080/checkout"),
		SummaryEvery: 10 * time.Second,
		Grace:        5 * time.Second,
	}
	if u, err := url.Parse(cfg.Target); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("TRAFFIC_TARGET: want an http(s) URL such as http://localhost:8080/checkout, got %q", cfg.Target)
	}

	rps, err := lab.EnvInt("TRAFFIC_RPS", 5)
	if err != nil {
		return Config{}, err
	}
	if rps < 1 {
		return Config{}, fmt.Errorf("TRAFFIC_RPS: want at least 1, got %d", rps)
	}
	cfg.BaseRPS = float64(rps)

	if cfg.MaxInFlight, err = lab.EnvInt("TRAFFIC_MAX_IN_FLIGHT", 100); err != nil {
		return Config{}, err
	}
	if cfg.MaxInFlight < 1 {
		return Config{}, fmt.Errorf("TRAFFIC_MAX_IN_FLIGHT: want at least 1, got %d", cfg.MaxInFlight)
	}

	if cfg.Timeout, err = lab.EnvDuration("TRAFFIC_TIMEOUT", 10*time.Second); err != nil {
		return Config{}, err
	}

	// Without a seed, pick a short one and log it, so a run worth repeating
	// can be repeated.
	seed := rand.IntN(1_000_000)
	if os.Getenv("TRAFFIC_SEED") != "" {
		if seed, err = lab.EnvInt("TRAFFIC_SEED", 0); err != nil {
			return Config{}, err
		}
	}
	cfg.Seed = uint64(seed)
	return cfg, nil
}
