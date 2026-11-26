package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/alloteyphil/wasitus/demo/internal/labtest"
)

const validCheckout = `{"customer_id": "cus_42", "amount": 2500, "currency": "GHS"}`

func fakeOrders(t *testing.T, status int, body string, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckout(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		delay         time.Duration
		wantStatus    int
		wantInBody    string
		wantSpanError bool
	}{
		{"placed", 201, `{"order_id": "ord_000001", "status": "paid", "payment_id": "ch_1"}`, 0,
			201, `"order_id":"ord_000001"`, false},
		{"customer error is passed on", 400, `{"error": {"code": "AmountTooLarge", "message": "too big"}}`, 0,
			400, "AmountTooLarge", false},
		{"orders failed", 502, `{"error": {"code": "PaymentFailed", "message": "payment failed"}}`, 0,
			502, "CheckoutFailed", true},
		{"orders crashed", 500, `{"error": {"code": "InternalError", "message": "Something went wrong."}}`, 0,
			502, "CheckoutFailed", true},
		{"orders too slow", 201, `{}`, time.Second,
			504, "CheckoutTimeout", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orders := fakeOrders(t, tt.status, tt.body, tt.delay)
			tel, rec := labtest.Telemetry()

			resp := httptest.NewRecorder()
			newHandler(tel, orders.URL, 50*time.Millisecond).ServeHTTP(resp,
				httptest.NewRequest(http.MethodPost, "/checkout", strings.NewReader(validCheckout)))

			if resp.Code != tt.wantStatus || !strings.Contains(resp.Body.String(), tt.wantInBody) {
				t.Fatalf("got %d %s, want %d containing %s", resp.Code, resp.Body, tt.wantStatus, tt.wantInBody)
			}
			server := labtest.OnlySpan(t, rec, trace.SpanKindServer)
			if isErr := server.Status().Code == codes.Error; isErr != tt.wantSpanError {
				t.Errorf("server span error = %v, want %v", isErr, tt.wantSpanError)
			}
		})
	}
}

// Health checks run every few seconds; they must not fill SigNoz with spans.
func TestHealthzIsNotTraced(t *testing.T) {
	tel, rec := labtest.Telemetry()
	resp := httptest.NewRecorder()
	newHandler(tel, "http://unused", time.Second).ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if resp.Code != 200 || strings.TrimSpace(resp.Body.String()) != "ok" {
		t.Errorf("got %d %q, want 200 ok", resp.Code, resp.Body)
	}
	if n := len(rec.Ended()); n != 0 {
		t.Errorf("recorded %d spans, want 0", n)
	}
}
