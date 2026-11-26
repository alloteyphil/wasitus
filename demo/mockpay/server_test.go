package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const validCharge = `{"amount": 2500, "currency": "GHS", "reference": "order-1001"}`

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testConfig keeps every delay short so the suite runs in milliseconds.
func testConfig() Config {
	return Config{
		SlowDelay:  20 * time.Millisecond,
		RateLimit:  3,
		RateWindow: time.Minute,
		HangEvery:  0,
		HangMax:    time.Hour,
	}
}

func start(t *testing.T, cfg Config, mode Mode) (*httptest.Server, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(NewServer(cfg, mode, slog.New(slog.DiscardHandler), clock.Now).Handler())
	t.Cleanup(srv.Close)
	return srv, clock
}

type result struct {
	status     int
	code       string // error code; empty on success
	retryAfter string
	charge     charge
}

func post(ctx context.Context, t *testing.T, srv *httptest.Server, path, method, body string) (result, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		return result{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	res := result{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
	if resp.StatusCode >= 400 {
		var e errorBody
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatalf("error body %q is not JSON: %v", data, err)
		}
		res.code = e.Error.Code
	} else if path == "/v1/charges" {
		if err := json.Unmarshal(data, &res.charge); err != nil {
			t.Fatalf("charge body %q is not JSON: %v", data, err)
		}
	}
	return res, nil
}

func postCharge(t *testing.T, srv *httptest.Server) result {
	t.Helper()
	res, err := post(context.Background(), t, srv, "/v1/charges", http.MethodPost, validCharge)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestChargeByMode(t *testing.T) {
	tests := []struct {
		mode       Mode
		wantStatus int
		wantCode   string
	}{
		{ModeHealthy, http.StatusCreated, ""},
		{ModeSlow, http.StatusCreated, ""},
		{ModeSuspended, http.StatusPaymentRequired, "AccountSuspended"},
		{ModeOutage, http.StatusServiceUnavailable, "ServiceUnavailable"},
		{ModeRateLimit, http.StatusCreated, ""}, // first charge is within the quota
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			srv, _ := start(t, testConfig(), tt.mode)
			res := postCharge(t, srv)
			if res.status != tt.wantStatus || res.code != tt.wantCode {
				t.Fatalf("got %d %q, want %d %q", res.status, res.code, tt.wantStatus, tt.wantCode)
			}
			if tt.wantStatus == http.StatusCreated {
				want := charge{ID: "ch_000001", Status: "succeeded", Amount: 2500, Currency: "GHS", Reference: "order-1001"}
				if res.charge != want {
					t.Errorf("charge = %+v, want %+v", res.charge, want)
				}
			}
		})
	}
}

func TestSlowModeDelaysTheAnswer(t *testing.T) {
	srv, _ := start(t, testConfig(), ModeSlow)
	begin := time.Now()
	if res := postCharge(t, srv); res.status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.status)
	}
	if took := time.Since(begin); took < testConfig().SlowDelay {
		t.Errorf("answered after %s, want at least %s", took, testConfig().SlowDelay)
	}
}

func TestRateLimit(t *testing.T) {
	srv, clock := start(t, testConfig(), ModeRateLimit)
	for i := 1; i <= 3; i++ {
		if res := postCharge(t, srv); res.status != http.StatusCreated {
			t.Fatalf("charge %d: status = %d, want 201 (within quota)", i, res.status)
		}
	}

	clock.Advance(15 * time.Second)
	res := postCharge(t, srv)
	if res.status != http.StatusTooManyRequests || res.code != "RateLimited" {
		t.Fatalf("charge 4: got %d %q, want 429 RateLimited", res.status, res.code)
	}
	if res.retryAfter != "45" {
		t.Errorf("Retry-After = %q, want 45 (seconds left in the window)", res.retryAfter)
	}

	clock.Advance(45 * time.Second)
	if res := postCharge(t, srv); res.status != http.StatusCreated {
		t.Errorf("after the window: status = %d, want 201", res.status)
	}
}

func TestOutageHangsEveryNthRequest(t *testing.T) {
	cfg := testConfig()
	cfg.HangEvery = 2
	srv, _ := start(t, cfg, ModeOutage)

	if res := postCharge(t, srv); res.status != http.StatusServiceUnavailable {
		t.Fatalf("charge 1: status = %d, want an immediate 503", res.status)
	}

	// Charge 2 hangs (HangMax is an hour), so the client times out, like a
	// lab service calling a provider that has stopped answering.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := post(ctx, t, srv, "/v1/charges", http.MethodPost, validCharge)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("charge 2: err = %v, want a client timeout", err)
	}

	if res := postCharge(t, srv); res.status != http.StatusServiceUnavailable {
		t.Errorf("charge 3: status = %d, want an immediate 503", res.status)
	}
}

func TestChargeValidation(t *testing.T) {
	tests := map[string]string{
		"not JSON":         `amount=2500`,
		"zero amount":      `{"amount": 0, "currency": "GHS", "reference": "r"}`,
		"lowercase code":   `{"amount": 1, "currency": "ghs", "reference": "r"}`,
		"missing currency": `{"amount": 1, "reference": "r"}`,
		"missing ref":      `{"amount": 1, "currency": "GHS"}`,
	}
	srv, _ := start(t, testConfig(), ModeHealthy)
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			res, err := post(context.Background(), t, srv, "/v1/charges", http.MethodPost, body)
			if err != nil {
				t.Fatal(err)
			}
			if res.status != http.StatusBadRequest || res.code != "InvalidRequest" {
				t.Errorf("got %d %q, want 400 InvalidRequest", res.status, res.code)
			}
		})
	}
}

func TestAdminSwitchesMode(t *testing.T) {
	srv, _ := start(t, testConfig(), ModeHealthy)
	ctx := context.Background()

	if res, err := post(ctx, t, srv, "/admin/mode", http.MethodPut, `{"mode": "suspended"}`); err != nil || res.status != http.StatusOK {
		t.Fatalf("switch to suspended: %+v, %v", res, err)
	}
	if res := postCharge(t, srv); res.code != "AccountSuspended" {
		t.Errorf("after switch: code = %q, want AccountSuspended", res.code)
	}

	res, err := post(ctx, t, srv, "/admin/mode", http.MethodPut, `{"mode": "melted"}`)
	if err != nil || res.status != http.StatusBadRequest || res.code != "InvalidRequest" {
		t.Errorf("unknown mode: %+v, %v; want 400 InvalidRequest", res, err)
	}
}

func TestSwitchingModeResetsRateLimit(t *testing.T) {
	srv, _ := start(t, testConfig(), ModeRateLimit)
	for range 4 {
		postCharge(t, srv)
	}
	for _, mode := range []string{"healthy", "ratelimit"} {
		if res, err := post(context.Background(), t, srv, "/admin/mode", http.MethodPut, `{"mode": "`+mode+`"}`); err != nil || res.status != http.StatusOK {
			t.Fatalf("switch to %s: %+v, %v", mode, res, err)
		}
	}
	if res := postCharge(t, srv); res.status != http.StatusCreated {
		t.Errorf("after re-entering ratelimit: status = %d, want 201 (fresh quota)", res.status)
	}
}

func TestHealthzStaysUpInEveryMode(t *testing.T) {
	for _, mode := range allModes {
		srv, _ := start(t, testConfig(), mode)
		res, err := post(context.Background(), t, srv, "/healthz", http.MethodGet, "")
		if err != nil || res.status != http.StatusOK {
			t.Errorf("%s: /healthz = %+v, %v; want 200", mode, res, err)
		}
	}
}
