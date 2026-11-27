package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/alloteyphil/wasitus/demo/internal/lab"
)

// newAdmin returns the admin API, which the scenario scripts use to change
// the traffic while it runs:
//
//	GET /admin/rate  the multiplier, base rate and target rate
//	PUT /admin/rate  {"multiplier": 3} sets the multiplier (above 0, at most 50)
//	GET /healthz     "ok"
//
// Errors use the lab's format: 400 {"error": {"code": "InvalidRequest", ...}}.
func newAdmin(rate *Rate, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/rate", func(w http.ResponseWriter, _ *http.Request) {
		lab.WriteJSON(w, http.StatusOK, rate.Get())
	})
	mux.HandleFunc("PUT /admin/rate", func(w http.ResponseWriter, r *http.Request) {
		setRate(w, r, rate, log)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}

func setRate(w http.ResponseWriter, r *http.Request, rate *Rate, log *slog.Logger) {
	var body struct {
		Multiplier *float64 `json:"multiplier"`
	}
	if err := lab.ReadJSON(w, r, &body); err != nil || body.Multiplier == nil {
		lab.WriteError(w, http.StatusBadRequest, "InvalidRequest", `Body must be JSON such as {"multiplier": 3}.`)
		return
	}
	from, to, err := rate.Set(*body.Multiplier)
	if err != nil {
		lab.WriteError(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	log.Info("rate changed", "from", from.Multiplier, "to", to.Multiplier, "target_rps", to.TargetRPS)
	lab.WriteJSON(w, http.StatusOK, to)
}
