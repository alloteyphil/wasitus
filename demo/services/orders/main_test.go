package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/alloteyphil/wasitus/demo/internal/labtest"
)

const validOrder = `{"customer_id": "cus_42", "amount": 2500, "currency": "GHS"}`

// fakeWallet answers every payment with status and body after delay. It
// counts calls and keeps the last traceparent header it received.
type fakeWallet struct {
	*httptest.Server
	calls       atomic.Int32
	traceparent atomic.Value
}

func newFakeWallet(t *testing.T, status int, body string, delay time.Duration) *fakeWallet {
	t.Helper()
	f := &fakeWallet{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.traceparent.Store(r.Header.Get("traceparent"))
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func placeOrder(h http.Handler, body string) *httptest.ResponseRecorder {
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body)))
	return resp
}

func TestOrderPaid(t *testing.T) {
	wallet := newFakeWallet(t, 201, `{"payment_id": "ch_000042", "status": "paid"}`, 0)
	tel, rec := labtest.Telemetry()

	resp := placeOrder(newHandler(tel, wallet.URL, time.Second, 0), validOrder)

	var got order
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil || resp.Code != 201 {
		t.Fatalf("got %d %s, want 201 and an order", resp.Code, resp.Body)
	}
	if want := (order{OrderID: "ord_000001", Status: "paid", PaymentID: "ch_000042"}); got != want {
		t.Errorf("order = %+v, want %+v", got, want)
	}

	// The trace continues into wallet-service: the call carries a traceparent
	// header from the same trace as orders-service's own span.
	server := labtest.OnlySpan(t, rec, trace.SpanKindServer)
	tp, _ := wallet.traceparent.Load().(string)
	if !strings.Contains(tp, server.SpanContext().TraceID().String()) {
		t.Errorf("traceparent %q does not carry trace %s", tp, server.SpanContext().TraceID())
	}
}

func TestPaymentProblems(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		delay      time.Duration
		wantStatus int
		wantCode   string
	}{
		{"wallet reports provider error", 502, `{"error": {"code": "PaymentProviderError", "message": "mockpay rejected the charge"}}`, 0, 502, "PaymentFailed"},
		{"wallet too slow", 201, `{"payment_id": "late"}`, time.Second, 504, "PaymentTimeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wallet := newFakeWallet(t, tt.status, tt.body, tt.delay)
			tel, rec := labtest.Telemetry()

			resp := placeOrder(newHandler(tel, wallet.URL, 50*time.Millisecond, 0), validOrder)

			if resp.Code != tt.wantStatus || !strings.Contains(resp.Body.String(), tt.wantCode) {
				t.Fatalf("got %d %s, want %d %s", resp.Code, resp.Body, tt.wantStatus, tt.wantCode)
			}
			if server := labtest.OnlySpan(t, rec, trace.SpanKindServer); server.Status().Code != codes.Error {
				t.Error("server span is not marked as an error")
			}
		})
	}
}

// TestStagedBug checks scenario S1's signal: with ORDERS_BUG_EVERY=2 the
// second order hits the off-by-one, and the span records a Go runtime panic,
// which is what an our-side code bug looks like.
func TestStagedBug(t *testing.T) {
	wallet := newFakeWallet(t, 201, `{"payment_id": "ch_1", "status": "paid"}`, 0)
	tel, rec := labtest.Telemetry()
	h := newHandler(tel, wallet.URL, time.Second, 2)

	if resp := placeOrder(h, validOrder); resp.Code != 201 {
		t.Fatalf("order 1: status = %d, want 201", resp.Code)
	}
	rec.Reset()

	resp := placeOrder(h, validOrder)
	if resp.Code != 500 || !strings.Contains(resp.Body.String(), "InternalError") {
		t.Fatalf("order 2: got %d %s, want 500 InternalError", resp.Code, resp.Body)
	}
	server := labtest.OnlySpan(t, rec, trace.SpanKindServer)
	if server.Status().Code != codes.Error {
		t.Error("server span is not marked as an error")
	}
	typ, msg, ok := labtest.Exception(server)
	if !ok || typ != "runtime.boundsError" || !strings.Contains(msg, "index out of range") {
		t.Errorf("exception = %q %q, want runtime.boundsError, index out of range", typ, msg)
	}
	if n := wallet.calls.Load(); n != 1 {
		t.Errorf("wallet called %d times, want 1 (the buggy order must not be charged)", n)
	}
}

func TestOrderRejected(t *testing.T) {
	tests := map[string]struct {
		body string
		code string
	}{
		"missing customer": {`{"amount": 2500, "currency": "GHS"}`, "InvalidRequest"},
		"above the limit":  {`{"customer_id": "cus_42", "amount": 5000001, "currency": "GHS"}`, "AmountTooLarge"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tel, _ := labtest.Telemetry()
			resp := placeOrder(newHandler(tel, "http://unused", time.Second, 0), tt.body)
			if resp.Code != 400 || !strings.Contains(resp.Body.String(), tt.code) {
				t.Errorf("got %d %s, want 400 %s", resp.Code, resp.Body, tt.code)
			}
		})
	}
}
