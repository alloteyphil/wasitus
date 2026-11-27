package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var settings = []string{
	"TRAFFIC_TARGET", "TRAFFIC_RPS", "TRAFFIC_SEED", "TRAFFIC_ADMIN_ADDR",
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
		AdminAddr:    ":8091",
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
		"TRAFFIC_ADMIN_ADDR":    "127.0.0.1:9191",
		"TRAFFIC_MAX_IN_FLIGHT": "7",
		"TRAFFIC_TIMEOUT":       "250ms",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Target != "http://storefront:8080/checkout" || cfg.BaseRPS != 20 || cfg.Seed != 42 ||
		cfg.AdminAddr != "127.0.0.1:9191" || cfg.MaxInFlight != 7 || cfg.Timeout != 250*time.Millisecond {
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

// adminPut changes the multiplier through a running admin API.
func adminPut(t *testing.T, addr, body string) RateState {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, "http://"+addr+"/admin/rate", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got RateState
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s = %d, %v; want 200 with the new rate", body, resp.StatusCode, err)
	}
	return got
}

func TestRunSendsTrafficAndServesTheAdminAPI(t *testing.T) {
	arrived := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case arrived <- struct{}{}:
		default: // the test only waits for the first one
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	setEnv(t, map[string]string{
		"TRAFFIC_TARGET":     srv.URL + "/checkout",
		"TRAFFIC_RPS":        "100",
		"TRAFFIC_ADMIN_ADDR": "127.0.0.1:0", // any free port; the start line says which
	})

	logs := &logBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, slog.New(slog.NewJSONHandler(logs, nil))) }()

	select {
	case <-arrived:
	case <-time.After(failsafe):
		t.Fatal("no checkout reached the server")
	}

	started := logs.lines(t, "trafficgen starting")
	if len(started) != 1 || started[0].AdminAddr == "" {
		t.Fatalf("start lines = %+v, want one naming the admin address", started)
	}
	want := RateState{Multiplier: 3, BaseRPS: 100, TargetRPS: 300}
	if got := adminPut(t, started[0].AdminAddr, `{"multiplier": 3}`); got != want {
		t.Errorf("PUT multiplier 3 = %+v, want %+v", got, want)
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
	if n := len(logs.lines(t, "stopped")); n != 1 {
		t.Errorf("logged %d stopped lines, want 1", n)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	setEnv(t, map[string]string{"TRAFFIC_RPS": "0"})
	err := run(context.Background(), slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "TRAFFIC_RPS") {
		t.Errorf("run = %v, want a TRAFFIC_RPS configuration error", err)
	}
}

func TestRunFailsWhenTheAdminPortIsBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { busy.Close() })
	setEnv(t, map[string]string{"TRAFFIC_ADMIN_ADDR": busy.Addr().String()})

	err = run(context.Background(), slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "admin API") {
		t.Errorf("run = %v, want an admin API error", err)
	}
}
