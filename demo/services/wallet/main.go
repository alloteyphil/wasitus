// Command wallet-service takes payments for the demo lab by charging mockpay,
// the lab's external payment provider.
//
// It is the only lab service that talks to mockpay, so its spans are where
// provider failures show up: the client span carries mockpay's HTTP status,
// and the server span records the provider's error code. That is the evidence
// wasitus needs to say "external".
//
// Settings: LAB_ADDR (default ":8082"), MOCKPAY_URL (default
// "http://localhost:8090"), WALLET_PROVIDER_TIMEOUT (default "3s"),
// APP_VERSION, and the standard OTEL_* variables.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/alloteyphil/wasitus/demo/internal/lab"
)

func main() {
	os.Exit(lab.Main("wallet-service", ":8082", func(t *lab.Telemetry) (http.Handler, error) {
		timeout, err := lab.EnvDuration("WALLET_PROVIDER_TIMEOUT", 3*time.Second)
		if err != nil {
			return nil, err
		}
		return newHandler(t, lab.Env("MOCKPAY_URL", "http://localhost:8090"), timeout), nil
	}))
}

var providerAttr = attribute.String("payment.provider", "mockpay")

type wallet struct {
	mockpay    *lab.Client
	mockpayURL string
	log        *slog.Logger
}

func newHandler(t *lab.Telemetry, mockpayURL string, timeout time.Duration) http.Handler {
	s := &wallet{mockpay: lab.NewClient(t, timeout), mockpayURL: mockpayURL, log: t.Log}
	mux := lab.NewMux(t)
	mux.HandleFunc("POST /payments", s.pay)
	return mux
}

type paymentRequest struct {
	OrderID  string `json:"order_id"`
	Amount   int64  `json:"amount"` // minor units
	Currency string `json:"currency"`
}

type payment struct {
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"`
}

func (s *wallet) pay(w http.ResponseWriter, r *http.Request) {
	var req paymentRequest
	if err := lab.ReadJSON(w, r, &req); err != nil || req.OrderID == "" || req.Amount <= 0 || req.Currency == "" {
		lab.WriteError(w, http.StatusBadRequest, "InvalidRequest", "order_id, amount and currency are required.")
		return
	}

	charge := map[string]any{"amount": req.Amount, "currency": req.Currency, "reference": req.OrderID}
	var created struct {
		ID string `json:"id"`
	}
	err := s.mockpay.PostJSON(r.Context(), s.mockpayURL+"/v1/charges", charge, &created)

	var rejected *lab.APIError
	switch {
	case err == nil:
		lab.WriteJSON(w, http.StatusCreated, payment{PaymentID: created.ID, Status: "paid"})
	case lab.IsTimeout(err):
		lab.Fail(w, r, s.log, http.StatusGatewayTimeout, "PaymentProviderTimeout",
			fmt.Errorf("mockpay did not answer in time: %w", err), providerAttr)
	case errors.As(err, &rejected):
		lab.Fail(w, r, s.log, http.StatusBadGateway, "PaymentProviderError",
			fmt.Errorf("mockpay rejected the charge: %w", err),
			providerAttr, attribute.String("payment.provider.error_code", rejected.Code))
	default:
		lab.Fail(w, r, s.log, http.StatusBadGateway, "PaymentProviderUnreachable",
			fmt.Errorf("could not reach mockpay: %w", err), providerAttr)
	}
}
