// Command storefront is the demo lab's public edge: customers (the traffic
// generator) check out here, and storefront places the order with
// orders-service.
//
// Settings: LAB_ADDR (default ":8080"), ORDERS_URL (default
// "http://localhost:8081"), STOREFRONT_ORDERS_TIMEOUT (default "8s"),
// APP_VERSION, and the standard OTEL_* variables.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/alloteyphil/wasitus/demo/internal/lab"
)

func main() {
	os.Exit(lab.Main("storefront", ":8080", func(t *lab.Telemetry) (http.Handler, error) {
		timeout, err := lab.EnvDuration("STOREFRONT_ORDERS_TIMEOUT", 8*time.Second)
		if err != nil {
			return nil, err
		}
		return newHandler(t, lab.Env("ORDERS_URL", "http://localhost:8081"), timeout), nil
	}))
}

type storefront struct {
	orders    *lab.Client
	ordersURL string
	log       *slog.Logger
}

func newHandler(t *lab.Telemetry, ordersURL string, timeout time.Duration) http.Handler {
	s := &storefront{orders: lab.NewClient(t, timeout), ordersURL: ordersURL, log: t.Log}
	mux := lab.NewMux(t)
	mux.HandleFunc("POST /checkout", s.checkout)
	return mux
}

type checkoutRequest struct {
	CustomerID string `json:"customer_id"`
	Amount     int64  `json:"amount"` // minor units
	Currency   string `json:"currency"`
}

type checkoutResult struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

func (s *storefront) checkout(w http.ResponseWriter, r *http.Request) {
	var req checkoutRequest
	if err := lab.ReadJSON(w, r, &req); err != nil || req.CustomerID == "" || req.Amount <= 0 || req.Currency == "" {
		lab.WriteError(w, http.StatusBadRequest, "InvalidRequest", "customer_id, amount and currency are required.")
		return
	}

	var placed checkoutResult
	err := s.orders.PostJSON(r.Context(), s.ordersURL+"/orders", req, &placed)

	var rejected *lab.APIError
	switch {
	case err == nil:
		lab.WriteJSON(w, http.StatusCreated, placed)
	case errors.As(err, &rejected) && rejected.Status < 500:
		// The customer's mistake (for example, too large an order): pass it on.
		lab.WriteError(w, rejected.Status, rejected.Code, rejected.Message)
	case lab.IsTimeout(err):
		lab.Fail(w, r, s.log, http.StatusGatewayTimeout, "CheckoutTimeout",
			fmt.Errorf("orders-service did not answer in time: %w", err))
	default:
		lab.Fail(w, r, s.log, http.StatusBadGateway, "CheckoutFailed",
			fmt.Errorf("could not place the order: %w", err))
	}
}
