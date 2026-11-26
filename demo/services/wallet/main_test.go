package main

import (
	"encoding/json"
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

const validPayment = `{"order_id": "ord_000001", "amount": 2500, "currency": "GHS"}`

// fakeMockpay answers every charge with status and body, after delay.
func fakeMockpay(t *testing.T, status int, body string, delay time.Duration) *httptest.Server {
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

func suspended() string {
	return `{"error": {"code": "AccountSuspended", "message": "invoice overdue"}}`
}

func TestPaymentOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		delay         time.Duration
		wantStatus    int
		wantCode      string
		wantProvider  string // payment.provider.error_code on the server span
		wantUpstream  string // http.response.status_code on the client span
		wantSpanError bool
	}{
		{"charged", 201, `{"id": "ch_000042", "status": "succeeded"}`, 0, 201, "", "", "201", false},
		{"account suspended", 402, suspended(), 0, 502, "PaymentProviderError", "AccountSuspended", "402", true},
		{"rate limited", 429, `{"error": {"code": "RateLimited", "message": "slow down"}}`, 0, 502, "PaymentProviderError", "RateLimited", "429", true},
		{"provider outage", 503, `{"error": {"code": "ServiceUnavailable", "message": "down"}}`, 0, 502, "PaymentProviderError", "ServiceUnavailable", "503", true},
		{"provider too slow", 201, `{"id": "ch_late"}`, time.Second, 504, "PaymentProviderTimeout", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockpay := fakeMockpay(t, tt.status, tt.body, tt.delay)
			tel, rec := labtest.Telemetry()
			h := newHandler(tel, mockpay.URL, 50*time.Millisecond)

			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(validPayment)))

			if resp.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", resp.Code, tt.wantStatus, resp.Body)
			}
			if tt.wantCode != "" {
				if got := errorCode(t, resp.Body.String()); got != tt.wantCode {
					t.Errorf("error code = %q, want %q", got, tt.wantCode)
				}
			} else if !strings.Contains(resp.Body.String(), `"payment_id":"ch_000042"`) {
				t.Errorf("body = %s, want payment_id ch_000042", resp.Body)
			}

			server := labtest.OnlySpan(t, rec, trace.SpanKindServer)
			if server.Name() != "POST /payments" {
				t.Errorf("server span name = %q", server.Name())
			}
			if isErr := server.Status().Code == codes.Error; isErr != tt.wantSpanError {
				t.Errorf("server span error = %v, want %v", isErr, tt.wantSpanError)
			}
			if got := labtest.Attr(server, "payment.provider.error_code"); got != tt.wantProvider {
				t.Errorf("payment.provider.error_code = %q, want %q", got, tt.wantProvider)
			}
			if tt.wantSpanError {
				if _, _, ok := labtest.Exception(server); !ok {
					t.Error("server span has no exception event")
				}
			}

			client := labtest.OnlySpan(t, rec, trace.SpanKindClient)
			if got := labtest.Attr(client, "http.response.status_code"); got != tt.wantUpstream {
				t.Errorf("client span http.response.status_code = %q, want %q", got, tt.wantUpstream)
			}
		})
	}
}

func TestProviderUnreachable(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close() // nothing listens at this address any more

	tel, _ := labtest.Telemetry()
	resp := httptest.NewRecorder()
	newHandler(tel, gone.URL, time.Second).ServeHTTP(resp,
		httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(validPayment)))

	if resp.Code != 502 || errorCode(t, resp.Body.String()) != "PaymentProviderUnreachable" {
		t.Errorf("got %d %s, want 502 PaymentProviderUnreachable", resp.Code, resp.Body)
	}
}

func TestInvalidPayment(t *testing.T) {
	tel, _ := labtest.Telemetry()
	resp := httptest.NewRecorder()
	newHandler(tel, "http://unused", time.Second).ServeHTTP(resp,
		httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(`{"amount": 2500}`)))
	if resp.Code != 400 {
		t.Errorf("status = %d, want 400", resp.Code)
	}
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("error body %q: %v", body, err)
	}
	return e.Error.Code
}
