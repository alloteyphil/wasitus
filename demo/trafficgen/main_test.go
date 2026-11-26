package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var settings = []string{
	"TRAFFIC_TARGET", "TRAFFIC_RPS", "TRAFFIC_SEED",
	"TRAFFIC_MAX_IN_FLIGHT", "TRAFFIC_TIMEOUT",
}

// setEnv clears every setting, then applies env.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range settings {
		t.Setenv(k, env[k])
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	setEnv(t, nil)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Seed = 0 // random when unset
	want := Config{
		Target:       "http://localhost:8080/checkout",
		BaseRPS:      5,
		MaxInFlight:  100,
		Timeout:      10 * time.Second,
		SummaryEvery: 10 * time.Second,
		Grace:        5 * time.Second,
	}
	if cfg != want {
		t.Errorf("defaults = %+v\nwant %+v", cfg, want)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	setEnv(t, map[string]string{
		"TRAFFIC_TARGET":        "http://storefront:8080/checkout",
		"TRAFFIC_RPS":           "20",
		"TRAFFIC_SEED":          "42",
		"TRAFFIC_MAX_IN_FLIGHT": "7",
		"TRAFFIC_TIMEOUT":       "250ms",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Target != "http://storefront:8080/checkout" || cfg.BaseRPS != 20 || cfg.Seed != 42 ||
		cfg.MaxInFlight != 7 || cfg.Timeout != 250*time.Millisecond {
		t.Errorf("config = %+v, want the values from the environment", cfg)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	tests := []struct{ key, value string }{
		{"TRAFFIC_TARGET", "storefront:8080/checkout"},
		{"TRAFFIC_TARGET", "ftp://storefront/checkout"},
		{"TRAFFIC_TARGET", "http:///checkout"},
		{"TRAFFIC_TARGET", "http://%zz"},
		{"TRAFFIC_RPS", "0"},
		{"TRAFFIC_RPS", "-5"},
		{"TRAFFIC_RPS", "fast"},
		{"TRAFFIC_MAX_IN_FLIGHT", "0"},
		{"TRAFFIC_MAX_IN_FLIGHT", "lots"},
		{"TRAFFIC_TIMEOUT", "10"},
		{"TRAFFIC_TIMEOUT", "-1s"},
		{"TRAFFIC_SEED", "-1"},
		{"TRAFFIC_SEED", "lucky"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			setEnv(t, map[string]string{tt.key: tt.value})
			_, err := loadConfig()
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error = %v, want one naming %s", err, tt.key)
			}
		})
	}
}

func TestRunSendsTrafficUntilCancelled(t *testing.T) {
	arrived := make(chan struct{}, 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	setEnv(t, map[string]string{"TRAFFIC_TARGET": srv.URL + "/checkout", "TRAFFIC_RPS": "100"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, slog.New(slog.DiscardHandler)) }()

	select {
	case <-arrived:
	case <-time.After(failsafe):
		t.Fatal("no checkout reached the server")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v, want nil after a clean stop", err)
		}
	case <-time.After(failsafe):
		t.Fatal("run did not stop after cancel")
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	setEnv(t, map[string]string{"TRAFFIC_RPS": "0"})
	err := run(context.Background(), slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "TRAFFIC_RPS") {
		t.Errorf("run = %v, want a TRAFFIC_RPS configuration error", err)
	}
}
