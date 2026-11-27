package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// What a customer sends: one of customers cus_0001 to cus_0200, paying
// between 5.00 and 500.00 GHS (in minor units).
const (
	customers = 200
	minAmount = 500
	maxAmount = 50_000
	currency  = "GHS"
	userAgent = "lab-trafficgen"
)

// Config is how the generator behaves. loadConfig fills it from the
// environment.
type Config struct {
	Target       string        // checkout URL
	BaseRPS      float64       // requests per second at multiplier 1
	Seed         uint64        // the same seed sends the same customers and amounts
	MaxInFlight  int           // requests waiting for an answer at once; more are dropped
	Timeout      time.Duration // per request, including reading the answer
	AdminAddr    string        // where the admin API listens
	SummaryEvery time.Duration // how often to log a summary line
	Grace        time.Duration // how long shutdown waits for requests in flight
}

// checkout is the body of one request to storefront.
type checkout struct {
	CustomerID string `json:"customer_id"`
	Amount     int64  `json:"amount"` // minor units
	Currency   string `json:"currency"`
}

// payloads makes checkouts from a seeded random source, so a seed always
// gives the same sequence of customers and amounts.
type payloads struct {
	rng *rand.Rand
}

func newPayloads(seed uint64) *payloads {
	return &payloads{rng: rand.New(rand.NewPCG(seed, 0))}
}

func (p *payloads) next() checkout {
	return checkout{
		CustomerID: fmt.Sprintf("cus_%04d", 1+p.rng.IntN(customers)),
		Amount:     minAmount + p.rng.Int64N(maxAmount-minAmount+1),
		Currency:   currency,
	}
}

// Counts are the generator's request counters.
type Counts struct {
	Sent      int64         // requests started
	Completed map[int]int64 // answers, by HTTP status code
	Errors    int64         // requests with no answer: timeout, refused connection
	Dropped   int64         // due requests skipped because MaxInFlight were waiting
}

// Sub returns the counts since prev, an earlier snapshot of the same counters.
func (c Counts) Sub(prev Counts) Counts {
	d := Counts{
		Sent:      c.Sent - prev.Sent,
		Completed: map[int]int64{},
		Errors:    c.Errors - prev.Errors,
		Dropped:   c.Dropped - prev.Dropped,
	}
	for code, n := range c.Completed {
		if n -= prev.Completed[code]; n != 0 {
			d.Completed[code] = n
		}
	}
	return d
}

// stats holds the counters. Request goroutines update them while the
// schedule reads them, so a mutex guards them all.
type stats struct {
	mu      sync.Mutex
	c       Counts
	lastErr string // the latest transport error, as a sample for the summary
}

func newStats() *stats {
	return &stats{c: Counts{Completed: map[int]int64{}}}
}

func (s *stats) sent() {
	s.mu.Lock()
	s.c.Sent++
	s.mu.Unlock()
}

func (s *stats) dropped() {
	s.mu.Lock()
	s.c.Dropped++
	s.mu.Unlock()
}

func (s *stats) failed(err error) {
	s.mu.Lock()
	s.c.Errors++
	s.lastErr = err.Error()
	s.mu.Unlock()
}

func (s *stats) completed(status int) {
	s.mu.Lock()
	s.c.Completed[status]++
	s.mu.Unlock()
}

// snapshot returns a copy of the counters and the latest transport error.
func (s *stats) snapshot() (Counts, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.c
	c.Completed = maps.Clone(s.c.Completed)
	return c, s.lastErr
}

// Generator sends checkouts to storefront on a schedule.
//
// The schedule runs on one goroutine (Run, or a test calling Tick), and
// never waits for an answer: each request runs on its own goroutine, and a
// request that falls due while MaxInFlight are already waiting is dropped and
// counted. So a slow storefront cannot slow the schedule down.
type Generator struct {
	cfg      Config
	rate     *Rate
	client   *http.Client
	log      *slog.Logger
	payloads *payloads
	stats    *stats

	slots     chan struct{} // holds one token per request in flight
	inFlight  sync.WaitGroup
	sendCtx   context.Context // requests' context: cancelled when shutdown stops waiting
	stopSends context.CancelFunc

	// Used only by the goroutine running the schedule.
	pacer       pacer
	lastSummary time.Time // zero before the first tick
	reported    Counts    // the totals at the last summary
}

// NewGenerator returns a generator at multiplier 1. It sends nothing until
// the schedule ticks.
func NewGenerator(cfg Config, log *slog.Logger) *Generator {
	sendCtx, stopSends := context.WithCancel(context.Background())
	return &Generator{
		cfg:  cfg,
		rate: NewRate(cfg.BaseRPS),
		client: &http.Client{
			Timeout: cfg.Timeout,
			// Keep enough idle connections to reuse one per request in
			// flight, instead of opening a new one for most requests.
			Transport: &http.Transport{
				MaxIdleConns:        cfg.MaxInFlight,
				MaxIdleConnsPerHost: cfg.MaxInFlight,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		log:       log,
		payloads:  newPayloads(cfg.Seed),
		stats:     newStats(),
		slots:     make(chan struct{}, cfg.MaxInFlight),
		sendCtx:   sendCtx,
		stopSends: stopSends,
	}
}

// Run calls Tick for every tick until ctx is cancelled. Then it waits up to
// Grace for the requests in flight, logs the run's totals and returns them.
func (g *Generator) Run(ctx context.Context, ticks <-chan time.Time) Counts {
	for {
		select {
		case now := <-ticks:
			g.Tick(now)
		case <-ctx.Done():
			g.drain()
			total, _ := g.stats.snapshot()
			g.log.Info("stopped", "sent", total.Sent, "completed", total.Completed,
				"transport_errors", total.Errors, "dropped", total.Dropped)
			return total
		}
	}
}

// Tick sends the requests due at now. It logs a summary first when one is
// due, so the summary covers exactly the requests sent before now.
func (g *Generator) Tick(now time.Time) {
	if g.lastSummary.IsZero() {
		g.lastSummary = now
	}
	if now.Sub(g.lastSummary) >= g.cfg.SummaryEvery {
		g.logSummary(now)
	}
	for range g.pacer.due(now, g.rate.Get().TargetRPS) {
		g.fire()
	}
}

// fire starts one request, or drops it when MaxInFlight are already waiting.
// The payload is drawn either way, so the requests sent at each point in
// the schedule do not depend on how fast earlier ones were answered.
func (g *Generator) fire() {
	p := g.payloads.next()
	select {
	case g.slots <- struct{}{}:
	default:
		g.stats.dropped()
		return
	}
	g.stats.sent()
	g.inFlight.Go(func() {
		defer func() { <-g.slots }()
		g.send(p)
	})
}

func (g *Generator) send(p checkout) {
	body, err := json.Marshal(p)
	if err != nil {
		g.stats.failed(fmt.Errorf("encode checkout: %w", err))
		return
	}
	req, err := http.NewRequestWithContext(g.sendCtx, http.MethodPost, g.cfg.Target, bytes.NewReader(body))
	if err != nil {
		g.stats.failed(fmt.Errorf("build request: %w", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := g.client.Do(req)
	if err != nil {
		g.stats.failed(err)
		return
	}
	defer resp.Body.Close()
	// Only the status matters. Reading the rest of the answer lets the
	// connection be reused; a failure to read it does not change the status.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	g.stats.completed(resp.StatusCode)
}

// logSummary logs one line for the window since the previous summary.
func (g *Generator) logSummary(now time.Time) {
	total, lastErr := g.stats.snapshot()
	window := total.Sub(g.reported)
	rate := g.rate.Get()
	attrs := []any{
		"window_s", now.Sub(g.lastSummary).Round(time.Millisecond).Seconds(),
		"sent", window.Sent,
		"completed", window.Completed,
		"transport_errors", window.Errors,
		"dropped", window.Dropped,
		"in_flight", len(g.slots),
		"multiplier", rate.Multiplier,
		"target_rps", rate.TargetRPS,
	}
	if window.Errors > 0 {
		attrs = append(attrs, "last_error", lastErr)
	}
	g.log.Info("summary", attrs...)
	g.reported, g.lastSummary = total, now
}

// drain waits for the requests in flight. After Grace it cancels the ones
// still waiting, which then count as transport errors.
func (g *Generator) drain() {
	defer g.stopSends()
	done := make(chan struct{})
	go func() {
		g.inFlight.Wait()
		close(done)
	}()

	grace := time.NewTimer(g.cfg.Grace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		g.log.Warn("shutdown: cancelling requests still in flight", "in_flight", len(g.slots))
		g.stopSends()
		<-done
	}
}
