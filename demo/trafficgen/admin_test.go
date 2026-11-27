package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type adminResult struct {
	status int
	rate   RateState
	code   string // error code; empty on success
}

// call sends one request straight to the admin handler, with no network.
func call(t *testing.T, h http.Handler, method, body string) adminResult {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/admin/rate", strings.NewReader(body)))

	res := adminResult{status: rec.Code}
	var err error
	switch rec.Code {
	case http.StatusOK:
		err = json.Unmarshal(rec.Body.Bytes(), &res.rate)
	case http.StatusBadRequest:
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		err = json.Unmarshal(rec.Body.Bytes(), &e)
		res.code = e.Error.Code
	}
	if err != nil {
		t.Errorf("%s /admin/rate: body %q is not the expected JSON: %v", method, rec.Body, err)
	}
	return res
}

func newTestAdmin() (http.Handler, *Rate, *logBuffer) {
	rate, logs := NewRate(5), &logBuffer{}
	return newAdmin(rate, slog.New(slog.NewJSONHandler(logs, nil))), rate, logs
}

func TestAdminGetsTheRate(t *testing.T) {
	h, _, _ := newTestAdmin()
	want := adminResult{status: http.StatusOK, rate: RateState{Multiplier: 1, BaseRPS: 5, TargetRPS: 5}}
	if got := call(t, h, http.MethodGet, ""); got != want {
		t.Errorf("GET = %+v, want %+v", got, want)
	}
}

func TestAdminSetsTheMultiplier(t *testing.T) {
	tests := []struct {
		body string
		want RateState
	}{
		{`{"multiplier": 3}`, RateState{Multiplier: 3, BaseRPS: 5, TargetRPS: 15}},   // S4
		{`{"multiplier": 10}`, RateState{Multiplier: 10, BaseRPS: 5, TargetRPS: 50}}, // S6
		{`{"multiplier": 1}`, RateState{Multiplier: 1, BaseRPS: 5, TargetRPS: 5}},    // reset
		{`{"multiplier": 0.5}`, RateState{Multiplier: 0.5, BaseRPS: 5, TargetRPS: 2.5}},
		{`{"multiplier": 50}`, RateState{Multiplier: 50, BaseRPS: 5, TargetRPS: 250}},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			h, rate, logs := newTestAdmin()
			want := adminResult{status: http.StatusOK, rate: tt.want}
			if got := call(t, h, http.MethodPut, tt.body); got != want {
				t.Errorf("PUT = %+v, want %+v", got, want)
			}
			if got := call(t, h, http.MethodGet, ""); got != want {
				t.Errorf("GET after PUT = %+v, want %+v", got, want)
			}
			if got := rate.Get(); got != tt.want {
				t.Errorf("the generator's rate = %+v, want %+v", got, tt.want)
			}
			changes := logs.lines(t, "rate changed")
			if len(changes) != 1 || changes[0].TargetRPS != tt.want.TargetRPS {
				t.Errorf("logged %+v, want one rate change to %v rps", changes, tt.want.TargetRPS)
			}
		})
	}
}

func TestAdminRejectsBadMultipliers(t *testing.T) {
	tests := map[string]string{
		"zero":              `{"multiplier": 0}`,
		"negative":          `{"multiplier": -3}`,
		"above 50":          `{"multiplier": 51}`,
		"just above 50":     `{"multiplier": 50.0001}`,
		"too large a float": `{"multiplier": 1e400}`,
		"a string":          `{"multiplier": "3"}`,
		"null":              `{"multiplier": null}`,
		"missing":           `{}`,
		"wrong field":       `{"rps": 15}`,
		"not JSON":          `multiplier=3`,
		"empty":             ``,
		"JSON null":         `null`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			h, rate, logs := newTestAdmin()
			want := adminResult{status: http.StatusBadRequest, code: "InvalidRequest"}
			if got := call(t, h, http.MethodPut, body); got != want {
				t.Errorf("PUT %s = %+v, want %+v", body, got, want)
			}
			if m := rate.Get().Multiplier; m != 1 {
				t.Errorf("multiplier = %v after a rejected PUT, want 1 (unchanged)", m)
			}
			if n := len(logs.lines(t, "rate changed")); n != 0 {
				t.Errorf("logged %d rate changes, want 0", n)
			}
		})
	}
}

func TestAdminRejectsOtherMethods(t *testing.T) {
	h, _, _ := newTestAdmin()
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if got := call(t, h, method, `{"multiplier": 3}`); got.status != http.StatusMethodNotAllowed {
			t.Errorf("%s /admin/rate = %d, want 405", method, got.status)
		}
	}
}

func TestAdminHealthz(t *testing.T) {
	h, _, _ := newTestAdmin()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Errorf("GET /healthz = %d %q, want 200 ok", rec.Code, rec.Body)
	}
}

// Scenario scripts and people can change the rate at the same moment, while
// the schedule reads it. Every answer must be consistent, and the race
// detector must stay quiet.
func TestAdminConcurrentUpdates(t *testing.T) {
	h, rate, _ := newTestAdmin()
	var wg sync.WaitGroup
	for m := 1; m <= maxMultiplier; m++ {
		wg.Go(func() {
			body := fmt.Sprintf(`{"multiplier": %d}`, m)
			if got := call(t, h, http.MethodPut, body); got.status != http.StatusOK ||
				got.rate.Multiplier != float64(m) || got.rate.TargetRPS != 5*float64(m) {
				t.Errorf("PUT %s = %+v, want 200 with multiplier %d", body, got, m)
			}
		})
		wg.Go(func() {
			if got := call(t, h, http.MethodGet, ""); got.status != http.StatusOK ||
				got.rate.TargetRPS != got.rate.BaseRPS*got.rate.Multiplier {
				t.Errorf("GET = %+v, want 200 with target = base x multiplier", got)
			}
		})
		wg.Go(func() { // the schedule reading the rate
			if s := rate.Get(); s.TargetRPS != s.BaseRPS*s.Multiplier {
				t.Errorf("rate = %+v, want target = base x multiplier", s)
			}
		})
	}
	wg.Wait()

	got := rate.Get()
	if got.Multiplier < 1 || got.Multiplier > maxMultiplier || got.Multiplier != math.Trunc(got.Multiplier) {
		t.Errorf("final multiplier = %v, want one of the values sent (1 to %d)", got.Multiplier, maxMultiplier)
	}
}

// The PUT body comes from outside, so any body must get a 200 or a 400, and
// never a panic or a multiplier out of range.
func FuzzAdminSetRate(f *testing.F) {
	for _, seed := range []string{
		`{"multiplier": 3}`, `{"multiplier": 0}`, `{"multiplier": -1}`, `{"multiplier": 50}`,
		`{"multiplier": 50.5}`, `{"multiplier": 1e400}`, `{"multiplier": "3"}`, `{"multiplier": null}`,
		`{}`, `null`, `[]`, ``, `{"multiplier": 3} trailing`, `{"multiplier": 3, "multiplier": 99}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		h, rate, _ := newTestAdmin()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/rate", strings.NewReader(body)))

		m := rate.Get().Multiplier
		switch rec.Code {
		case http.StatusOK:
			if math.IsNaN(m) || m <= 0 || m > maxMultiplier {
				t.Errorf("PUT %q accepted multiplier %v, outside (0, %d]", body, m, maxMultiplier)
			}
		case http.StatusBadRequest:
			if m != 1 {
				t.Errorf("PUT %q was rejected but changed the multiplier to %v", body, m)
			}
		default:
			t.Errorf("PUT %q = %d, want 200 or 400", body, rec.Code)
		}
	})
}
