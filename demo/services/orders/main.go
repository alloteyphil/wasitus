// Command orders-service creates orders for the demo lab and pays for them
// through wallet-service.
//
// It is also where the staged bad deploy (scenario S1) lives: with
// ORDERS_BUG_EVERY=N, every Nth order hits a real code bug (see checkLimits).
// That is the lab's "our side" failure, as opposed to mockpay's failures.
//
// Settings: LAB_ADDR (default ":8081"), WALLET_URL (default
// "http://localhost:8082"), ORDERS_WALLET_TIMEOUT (default "5s"),
// ORDERS_BUG_EVERY (default 0, off), APP_VERSION, and the OTEL_* variables.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/alloteyphil/wasitus/demo/internal/lab"
)

func main() {
	os.Exit(lab.Main("orders-service", ":8081", func(t *lab.Telemetry) (http.Handler, error) {
		timeout, err := lab.EnvDuration("ORDERS_WALLET_TIMEOUT", 5*time.Second)
		if err != nil {
			return nil, err
		}
		bugEvery, err := lab.EnvInt("ORDERS_BUG_EVERY", 0)
		if err != nil {
			return nil, err
		}
		if bugEvery > 0 {
			t.Log.Warn("staged bug is ON", "every_nth_order", bugEvery)
		}
		return newHandler(t, lab.Env("WALLET_URL", "http://localhost:8082"), timeout, bugEvery), nil
	}))
}

// maxOrderAmount is the largest order accepted, in minor units (50,000.00).
const maxOrderAmount = 5_000_000

type orders struct {
	wallet    *lab.Client
	walletURL string
	bugEvery  int
	log       *slog.Logger

	mu    sync.Mutex
	count int
}

func newHandler(t *lab.Telemetry, walletURL string, timeout time.Duration, bugEvery int) http.Handler {
	s := &orders{wallet: lab.NewClient(t, timeout), walletURL: walletURL, bugEvery: bugEvery, log: t.Log}
	mux := lab.NewMux(t)
	mux.HandleFunc("POST /orders", s.create)
	return mux
}

type orderRequest struct {
	CustomerID string `json:"customer_id"`
	Amount     int64  `json:"amount"` // minor units
	Currency   string `json:"currency"`
}

type order struct {
	OrderID   string `json:"order_id"`
	Status    string `json:"status"`
	PaymentID string `json:"payment_id"`
}

func (s *orders) create(w http.ResponseWriter, r *http.Request) {
	var req orderRequest
	if err := lab.ReadJSON(w, r, &req); err != nil || req.CustomerID == "" || req.Amount <= 0 || req.Currency == "" {
		lab.WriteError(w, http.StatusBadRequest, "InvalidRequest", "customer_id, amount and currency are required.")
		return
	}

	s.mu.Lock()
	s.count++
	n := s.count
	s.mu.Unlock()
	id := fmt.Sprintf("ord_%06d", n)

	if !s.checkLimits(n, req) {
		lab.WriteError(w, http.StatusBadRequest, "AmountTooLarge", "The order is above the per-order limit.")
		return
	}

	pay := map[string]any{"order_id": id, "amount": req.Amount, "currency": req.Currency}
	var paid struct {
		PaymentID string `json:"payment_id"`
	}
	err := s.wallet.PostJSON(r.Context(), s.walletURL+"/payments", pay, &paid)

	var rejected *lab.APIError
	switch {
	case err == nil:
		lab.WriteJSON(w, http.StatusCreated, order{OrderID: id, Status: "paid", PaymentID: paid.PaymentID})
	case lab.IsTimeout(err):
		lab.Fail(w, r, s.log, http.StatusGatewayTimeout, "PaymentTimeout",
			fmt.Errorf("wallet-service did not answer in time for %s: %w", id, err))
	case errors.As(err, &rejected):
		lab.Fail(w, r, s.log, http.StatusBadGateway, "PaymentFailed",
			fmt.Errorf("payment for %s failed: %w", id, err))
	default:
		lab.Fail(w, r, s.log, http.StatusBadGateway, "PaymentFailed",
			fmt.Errorf("could not reach wallet-service for %s: %w", id, err))
	}
}

// checkLimits reports whether the order is within the per-order limit.
//
// The staged bad deploy lives here. With ORDERS_BUG_EVERY=N, every Nth order
// reads one element past the end of the limits slice, the kind of off-by-one
// a refactor introduces. Go panics with "index out of range"; the lab's
// panic handler records it on the span as a runtime.boundsError exception
// and answers 500. That is what an our-side code bug looks like in SigNoz.
func (s *orders) checkLimits(n int, req orderRequest) bool {
	limits := []int64{maxOrderAmount}
	tier := 0
	if s.bugEvery > 0 && n%s.bugEvery == 0 {
		tier = len(limits) // the bug
	}
	return req.Amount <= limits[tier]
}
