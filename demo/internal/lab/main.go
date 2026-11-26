package lab

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// Main runs a lab service and returns its exit code: it sets up telemetry,
// builds the handler, serves on LAB_ADDR (or defaultAddr) until the process
// is stopped, then flushes telemetry. APP_VERSION becomes service.version,
// which is how a staged "deploy" shows up in the traces.
func Main(service, defaultAddr string, build func(*Telemetry) (http.Handler, error)) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	version := Env("APP_VERSION", "dev")
	tel, err := Setup(ctx, service, version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", service, err)
		return 1
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tel.Shutdown(flushCtx); err != nil {
			tel.Log.Error("telemetry shutdown", "error", err)
		}
	}()

	handler, err := build(tel)
	if err != nil {
		tel.Log.Error("bad configuration", "error", err)
		return 1
	}

	addr := Env("LAB_ADDR", defaultAddr)
	tel.Log.Info("starting", "service", service, "version", version, "addr", addr)
	if err := serve(ctx, addr, handler); err != nil {
		tel.Log.Error("stopped", "error", err)
		return 1
	}
	return 0
}

// serve runs handler until ctx is cancelled, then shuts down gracefully.
func serve(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// Env returns the environment variable key, or fallback when it is unset.
func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// EnvDuration reads a duration such as "3s" from the environment.
func EnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: want a positive duration such as 3s, got %q", key, v)
	}
	return d, nil
}

// EnvInt reads a whole number of at least zero from the environment.
func EnvInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: want a whole number of at least 0, got %q", key, v)
	}
	return n, nil
}
