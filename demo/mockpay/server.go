package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Mode is how mockpay currently behaves.
type Mode string

const (
	ModeHealthy   Mode = "healthy"   // every valid charge succeeds
	ModeOutage    Mode = "outage"    // 503s, and every Nth request hangs (client timeouts)
	ModeSlow      Mode = "slow"      // charges succeed, but only after a long delay
	ModeSuspended Mode = "suspended" // 402 AccountSuspended: the lab's account is unpaid
	ModeRateLimit Mode = "ratelimit" // 429 once the per-window quota is used up
)

var allModes = []Mode{ModeHealthy, ModeOutage, ModeSlow, ModeSuspended, ModeRateLimit}

func parseMode(s string) (Mode, error) {
	for _, m := range allModes {
		if string(m) == s {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown mode %q (valid: %v)", s, allModes)
}

// Config tunes the failure modes.
type Config struct {
	SlowDelay  time.Duration // how long a charge takes in slow mode
	RateLimit  int           // charges allowed per RateWindow in ratelimit mode
	RateWindow time.Duration
	HangEvery  int           // in outage mode, every HangEvery-th charge hangs (0: never)
	HangMax    time.Duration // how long a hanging charge waits before answering 503
}

// DefaultConfig matches the scenarios in docs/SCENARIOS.md.
func DefaultConfig() Config {
	return Config{
		SlowDelay:  6 * time.Second, // above the 5s p99 the slow scenario expects
		RateLimit:  60,
		RateWindow: time.Minute,
		HangEvery:  5,
		HangMax:    30 * time.Second,
	}
}

// Server is mockpay's HTTP API. It is safe for concurrent use.
type Server struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu          sync.Mutex
	mode        Mode
	windowStart time.Time // ratelimit: start of the current window
	windowCount int       // ratelimit: charges seen in the current window
	outageCount int       // outage: charges seen, to pick which ones hang
	charges     int       // successful charges, for IDs
}

// NewServer returns a server in the given mode. now is the clock used for
// rate limiting; pass time.Now outside tests.
func NewServer(cfg Config, mode Mode, log *slog.Logger, now func() time.Time) *Server {
	return &Server{cfg: cfg, mode: mode, log: log, now: now}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/charges", s.createCharge)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /admin/mode", s.getMode)
	mux.HandleFunc("PUT /admin/mode", s.setMode)
	return s.logRequests(mux)
}

// apiError is the error format clients see, modelled on real payment APIs:
// a stable machine-readable code plus a human message.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorBody struct {
	Error apiError `json:"error"`
}

type chargeRequest struct {
	Amount    int64  `json:"amount"`   // minor units, e.g. pesewas
	Currency  string `json:"currency"` // ISO 4217, e.g. GHS
	Reference string `json:"reference"`
}

type charge struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Reference string `json:"reference"`
}

// failure is what the current mode does to a charge request.
type failure struct {
	status     int
	err        apiError
	retryAfter int // seconds; 0 means no Retry-After header
}

// decide applies the current mode to one incoming charge: how long to wait
// first, and whether to fail. It updates the mode's counters.
func (s *Server) decide() (delay time.Duration, fail *failure) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.mode {
	case ModeSlow:
		return s.cfg.SlowDelay, nil

	case ModeSuspended:
		return 0, &failure{status: http.StatusPaymentRequired, err: apiError{
			Code:    "AccountSuspended",
			Message: "This account is suspended because an invoice is overdue. Contact billing.",
		}}

	case ModeOutage:
		s.outageCount++
		unavailable := &failure{status: http.StatusServiceUnavailable, err: apiError{
			Code:    "ServiceUnavailable",
			Message: "Payments are temporarily unavailable. Try again later.",
		}}
		if s.cfg.HangEvery > 0 && s.outageCount%s.cfg.HangEvery == 0 {
			return s.cfg.HangMax, unavailable
		}
		return 0, unavailable

	case ModeRateLimit:
		now := s.now()
		if now.Sub(s.windowStart) >= s.cfg.RateWindow {
			s.windowStart, s.windowCount = now, 0
		}
		s.windowCount++
		if s.windowCount > s.cfg.RateLimit {
			reset := s.windowStart.Add(s.cfg.RateWindow).Sub(now)
			return 0, &failure{
				status:     http.StatusTooManyRequests,
				err:        apiError{Code: "RateLimited", Message: "Too many requests. Slow down."},
				retryAfter: max(1, int(math.Ceil(reset.Seconds()))),
			}
		}
	}
	return 0, nil
}

func (s *Server) createCharge(w http.ResponseWriter, r *http.Request) {
	// Read the whole body before waiting. Go's server only notices that a
	// client has hung up once the body has been read, so without this a
	// hanging request would never see the client give up.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeInvalid(w, "Request body is too large or could not be read.")
		return
	}

	delay, fail := s.decide()
	if !wait(r, delay) {
		return // the client gave up; nobody is listening for an answer
	}
	if fail != nil {
		if fail.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(fail.retryAfter))
		}
		writeJSON(w, fail.status, errorBody{Error: fail.err})
		return
	}

	var req chargeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeInvalid(w, "Request body must be a JSON charge.")
		return
	}
	if msg := validate(req); msg != "" {
		writeInvalid(w, msg)
		return
	}

	s.mu.Lock()
	s.charges++
	id := s.charges
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, charge{
		ID:        fmt.Sprintf("ch_%06d", id),
		Status:    "succeeded",
		Amount:    req.Amount,
		Currency:  req.Currency,
		Reference: req.Reference,
	})
}

func validate(req chargeRequest) string {
	switch {
	case req.Amount <= 0:
		return "amount must be a positive number of minor units."
	case !isCurrencyCode(req.Currency):
		return "currency must be a 3-letter ISO 4217 code such as GHS."
	case req.Reference == "":
		return "reference is required."
	}
	return ""
}

func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// healthz reports whether the mockpay process is up. It stays 200 in every
// mode, so Docker does not restart mockpay in the middle of a staged outage.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

type modeBody struct {
	Mode Mode `json:"mode"`
}

func (s *Server) getMode(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, modeBody{Mode: mode})
}

// setMode switches the failure mode and resets its counters, so every staged
// scenario starts from the same state.
func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeInvalid(w, `Body must be JSON such as {"mode": "outage"}.`)
		return
	}
	mode, err := parseMode(body.Mode)
	if err != nil {
		writeInvalid(w, err.Error())
		return
	}

	s.mu.Lock()
	previous := s.mode
	s.mode = mode
	s.windowStart, s.windowCount, s.outageCount = time.Time{}, 0, 0
	s.mu.Unlock()

	s.log.Info("mode changed", "from", previous, "to", mode)
	writeJSON(w, http.StatusOK, modeBody{Mode: mode})
}

// wait pauses for d, or until the client gives up. It reports whether the
// whole delay passed.
func wait(r *http.Request, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

func writeInvalid(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: apiError{Code: "InvalidRequest", Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// statusRecorder remembers the status code a handler wrote, for the log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logRequests logs one line per request. A request the client abandoned
// before getting an answer is logged as 499 (the nginx convention).
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		status := rec.status
		switch {
		case status == 0 && r.Context().Err() != nil:
			status = 499
		case status == 0:
			status = http.StatusOK
		}
		s.log.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", status, "duration_ms", time.Since(start).Milliseconds())
	})
}
