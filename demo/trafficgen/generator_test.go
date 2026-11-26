package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// failsafe bounds every wait on another goroutine, so a broken test fails
// instead of hanging. No test waits this long when it passes.
const failsafe = 5 * time.Second

func testConfig(target string) Config {
	return Config{
		Target:       target,
		BaseRPS:      5,
		Seed:         1,
		MaxInFlight:  100,
		Timeout:      failsafe,
		SummaryEvery: 10 * time.Second,
		Grace:        time.Minute,
	}
}

// logBuffer collects log lines; the generator may log from more than one
// goroutine.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// logLine holds the fields of trafficgen's log lines that the tests check.
type logLine struct {
	Msg             string           `json:"msg"`
	WindowS         float64          `json:"window_s"`
	Sent            int64            `json:"sent"`
	Completed       map[string]int64 `json:"completed"`
	TransportErrors int64            `json:"transport_errors"`
	Dropped         int64            `json:"dropped"`
	InFlight        int              `json:"in_flight"`
	Multiplier      float64          `json:"multiplier"`
	TargetRPS       float64          `json:"target_rps"`
	LastError       string           `json:"last_error"`
	AdminAddr       string           `json:"admin_addr"`
}

// lines returns the logged lines with the given message.
func (b *logBuffer) lines(t *testing.T, msg string) []logLine {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var found []logLine
	sc := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for sc.Scan() {
		var l logLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("log line %q is not JSON: %v", sc.Text(), err)
		}
		if l.Msg == msg {
			found = append(found, l)
		}
	}
	return found
}

func newTestGenerator(t *testing.T, cfg Config) (*Generator, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	g := NewGenerator(cfg, slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() {
		g.stopSends()
		g.inFlight.Wait()
	})
	return g, logs
}

// tickEach ticks g every step, n times after from, and returns the last tick.
func tickEach(g *Generator, from time.Time, step time.Duration, n int) time.Time {
	now := from
	for range n {
		now = now.Add(step)
		g.Tick(now)
	}
	return now
}

func counts(t *testing.T, g *Generator) Counts {
	t.Helper()
	c, _ := g.stats.snapshot()
	return c
}

// received is one request as the fake storefront saw it.
type received struct {
	method, path, contentType, userAgent string
	body                                 checkout
}

// recordingServer answers each request at once, with statusFor(n) for the
// nth request, and records what it received.
func recordingServer(t *testing.T, statusFor func(n int64) int) (*httptest.Server, func() []received) {
	t.Helper()
	var (
		n   atomic.Int64
		mu  sync.Mutex
		got []received
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := received{method: r.Method, path: r.URL.Path,
			contentType: r.Header.Get("Content-Type"), userAgent: r.Header.Get("User-Agent")}
		if err := json.NewDecoder(r.Body).Decode(&rec.body); err != nil {
			t.Errorf("request body is not a JSON checkout: %v", err)
		}
		mu.Lock()
		got = append(got, rec)
		mu.Unlock()
		w.WriteHeader(statusFor(n.Add(1)))
		_, _ = io.WriteString(w, `{"order_id": "ord_000001", "status": "paid"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []received {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

func always(status int) func(int64) int {
	return func(int64) int { return status }
}

// blockingServer holds every request until release is called (or the
// client gives up), and signals each arrival.
func blockingServer(t *testing.T) (srv *httptest.Server, arrived <-chan struct{}, release func()) {
	t.Helper()
	arrivals := make(chan struct{}, 1000)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrivals <- struct{}{}
		select {
		case <-gate:
			w.WriteHeader(http.StatusCreated)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(release) // runs first, so Close does not wait on held requests
	return srv, arrivals, release
}

func waitArrivals(t *testing.T, arrived <-chan struct{}, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-arrived:
		case <-time.After(failsafe):
			t.Fatalf("only %d of %d requests reached the server", i, n)
		}
	}
}

func TestPayloadsAreReproducible(t *testing.T) {
	a, b := newPayloads(42), newPayloads(42)
	for i := range 1000 {
		if pa, pb := a.next(), b.next(); pa != pb {
			t.Fatalf("payload %d differs for the same seed: %+v vs %+v", i, pa, pb)
		}
	}

	// The sequence for a seed must not change between versions either, or
	// a seed noted in an old run would no longer repeat it.
	p := newPayloads(42)
	want := []checkout{
		{CustomerID: "cus_0172", Amount: 48056, Currency: "GHS"},
		{CustomerID: "cus_0028", Amount: 4881, Currency: "GHS"},
		{CustomerID: "cus_0038", Amount: 48337, Currency: "GHS"},
	}
	for i, w := range want {
		if got := p.next(); got != w {
			t.Errorf("seed 42, payload %d = %+v, want %+v", i, got, w)
		}
	}

	other, same := newPayloads(43), newPayloads(42)
	differs := false
	for range 10 {
		if other.next() != same.next() {
			differs = true
		}
	}
	if !differs {
		t.Error("seeds 42 and 43 gave the same 10 payloads")
	}
}

func TestPayloadsStayInRange(t *testing.T) {
	p := newPayloads(7)
	seen := map[string]bool{}
	lo, hi := int64(maxAmount), int64(minAmount)
	for range 20_000 {
		c := p.next()
		if c.Currency != "GHS" || c.Amount < minAmount || c.Amount > maxAmount {
			t.Fatalf("payload out of range: %+v", c)
		}
		if len(c.CustomerID) != len("cus_0001") || c.CustomerID < "cus_0001" || c.CustomerID > "cus_0200" {
			t.Fatalf("customer out of range: %+v", c)
		}
		seen[c.CustomerID] = true
		lo, hi = min(lo, c.Amount), max(hi, c.Amount)
	}
	if len(seen) != customers {
		t.Errorf("saw %d distinct customers, want all %d", len(seen), customers)
	}
	// Each amount is checked against the bounds above; this checks that the
	// whole range is used, not just part of it.
	if lo > minAmount+100 || hi < maxAmount-100 {
		t.Errorf("amounts ranged %d to %d, want close to the full %d to %d", lo, hi, minAmount, maxAmount)
	}
}

func TestRequestsCarryTheSeededPayloads(t *testing.T) {
	srv, got := recordingServer(t, always(http.StatusCreated))
	cfg := testConfig(srv.URL + "/checkout")
	cfg.BaseRPS, cfg.Seed = 20, 7
	g, _ := newTestGenerator(t, cfg)

	g.Tick(t0)
	tickEach(g, t0, 10*time.Millisecond, 100) // one second: 20 requests
	g.inFlight.Wait()

	reqs := got()
	if len(reqs) != 20 {
		t.Fatalf("server got %d requests, want 20", len(reqs))
	}
	var bodies []checkout
	for _, r := range reqs {
		if r.method != http.MethodPost || r.path != "/checkout" ||
			r.contentType != "application/json" || r.userAgent != userAgent {
			t.Errorf("request = %s %s (Content-Type %q, User-Agent %q), want POST /checkout as JSON from %s",
				r.method, r.path, r.contentType, r.userAgent, userAgent)
		}
		bodies = append(bodies, r.body)
	}

	// Requests run concurrently and may arrive in any order, but together
	// they are exactly the seed's first 20 payloads.
	want := newPayloads(7)
	var wantBodies []checkout
	for range 20 {
		wantBodies = append(wantBodies, want.next())
	}
	byFields := func(a, b checkout) int {
		return cmp.Or(strings.Compare(a.CustomerID, b.CustomerID), cmp.Compare(a.Amount, b.Amount))
	}
	slices.SortFunc(bodies, byFields)
	slices.SortFunc(wantBodies, byFields)
	if !slices.Equal(bodies, wantBodies) {
		t.Errorf("bodies = %+v\nwant %+v", bodies, wantBodies)
	}
	if c := counts(t, g); c.Sent != 20 || c.Completed[http.StatusCreated] != 20 {
		t.Errorf("counts = %+v, want 20 sent and 20 completed with 201", c)
	}
}

// The scenarios raise the multiplier (x3 for S4, x10 for S6) and set it back
// to 1; each change applies from the next tick.
func TestTickFollowsTheMultiplier(t *testing.T) {
	srv, _ := recordingServer(t, always(http.StatusCreated))
	g, _ := newTestGenerator(t, testConfig(srv.URL))

	now := t0
	g.Tick(now)
	var before int64
	for _, phase := range []struct {
		multiplier float64
		want       int64
	}{{1, 5}, {3, 15}, {10, 50}, {1, 5}} {
		if _, _, err := g.rate.Set(phase.multiplier); err != nil {
			t.Fatal(err)
		}
		now = tickEach(g, now, 10*time.Millisecond, 100)
		sent := counts(t, g).Sent
		if sent-before != phase.want {
			t.Errorf("one second at x%v: sent %d, want %d", phase.multiplier, sent-before, phase.want)
		}
		before = sent
	}
	g.inFlight.Wait()
	if c := counts(t, g); c.Dropped != 0 || c.Completed[http.StatusCreated] != 75 {
		t.Errorf("counts = %+v, want 75 completed and none dropped", c)
	}
}

func TestSlowAnswersDoNotSlowTheSchedule(t *testing.T) {
	srv, arrived, release := blockingServer(t)
	cfg := testConfig(srv.URL)
	cfg.BaseRPS, cfg.MaxInFlight = 10, 3
	g, _ := newTestGenerator(t, cfg)

	// One second of schedule against a server that answers nothing. If the
	// schedule waited for answers, this would never finish.
	done := make(chan struct{})
	go func() {
		g.Tick(t0)
		tickEach(g, t0, 10*time.Millisecond, 100)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(failsafe):
		t.Fatal("the schedule waited for slow answers")
	}

	waitArrivals(t, arrived, 3)
	c := counts(t, g)
	if c.Sent != 3 || c.Dropped != 7 || len(c.Completed) != 0 || c.Errors != 0 {
		t.Errorf("while the server holds 3 requests: %+v, want 3 sent, 7 dropped, none completed", c)
	}
	if n := len(g.slots); n != 3 {
		t.Errorf("in flight = %d, want 3 (the bound)", n)
	}

	release()
	g.inFlight.Wait()
	if c := counts(t, g); c.Completed[http.StatusCreated] != 3 || len(g.slots) != 0 {
		t.Errorf("after the answers: %+v with %d in flight, want 3 completed and none in flight", c, len(g.slots))
	}

	// With the slots free again, the schedule sends again.
	tickEach(g, t0.Add(time.Second), 10*time.Millisecond, 100)
	c = counts(t, g)
	if c.Sent+c.Dropped != 20 || c.Sent < 6 {
		t.Errorf("after a second second: %+v, want sent+dropped = 20 with at least 6 sent", c)
	}
}

func TestTimeoutIsATransportError(t *testing.T) {
	srv, _, _ := blockingServer(t)
	cfg := testConfig(srv.URL)
	cfg.BaseRPS, cfg.Timeout = 1, 20*time.Millisecond
	g, _ := newTestGenerator(t, cfg)

	g.Tick(t0)
	g.Tick(t0.Add(time.Second))
	g.inFlight.Wait()

	c, lastErr := g.stats.snapshot()
	if c.Sent != 1 || c.Errors != 1 || len(c.Completed) != 0 {
		t.Errorf("counts = %+v, want 1 sent and 1 transport error", c)
	}
	if !strings.Contains(lastErr, "Timeout") {
		t.Errorf("last error = %q, want a client timeout", lastErr)
	}
}

func TestRefusedConnectionIsATransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	target := srv.URL
	srv.Close() // nothing listens there any more

	g, _ := newTestGenerator(t, testConfig(target))
	g.Tick(t0)
	g.Tick(t0.Add(time.Second))
	g.inFlight.Wait()

	if c := counts(t, g); c.Sent != 5 || c.Errors != 5 {
		t.Errorf("counts = %+v, want 5 sent and 5 transport errors", c)
	}
}

func TestSummaryEveryTenSeconds(t *testing.T) {
	// Odd-numbered requests succeed and even-numbered ones fail, so the
	// summary has two status codes to count.
	srv, _ := recordingServer(t, func(n int64) int {
		if n%2 == 1 {
			return http.StatusCreated
		}
		return http.StatusBadGateway
	})
	g, logs := newTestGenerator(t, testConfig(srv.URL))

	g.Tick(t0)
	tickEach(g, t0, time.Second, 9) // 45 requests
	g.inFlight.Wait()
	if n := len(logs.lines(t, "summary")); n != 0 {
		t.Fatalf("%d summaries before 10 seconds, want 0", n)
	}

	// The summary covers the window before this tick; the tick's own five
	// requests count in the next one.
	g.Tick(t0.Add(10 * time.Second))
	g.inFlight.Wait()
	if _, _, err := g.rate.Set(2); err != nil {
		t.Fatal(err)
	}
	g.Tick(t0.Add(15 * time.Second)) // ten more (one second's catch-up at 10 rps)
	g.inFlight.Wait()
	g.Tick(t0.Add(20 * time.Second))

	got := logs.lines(t, "summary")
	want := []logLine{
		{Msg: "summary", WindowS: 10, Sent: 45, Completed: map[string]int64{"201": 23, "502": 22},
			Multiplier: 1, TargetRPS: 5},
		{Msg: "summary", WindowS: 10, Sent: 15, Completed: map[string]int64{"201": 7, "502": 8},
			Multiplier: 2, TargetRPS: 10},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d summaries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !equalLines(got[i], want[i]) {
			t.Errorf("summary %d = %+v\nwant %+v", i+1, got[i], want[i])
		}
	}
}

func TestSummaryShowsDropsErrorsAndInFlight(t *testing.T) {
	srv, arrived, _ := blockingServer(t)
	cfg := testConfig(srv.URL)
	cfg.MaxInFlight = 2
	g, logs := newTestGenerator(t, cfg)

	g.Tick(t0)
	g.Tick(t0.Add(time.Second)) // 5 due: 2 sent, 3 dropped
	waitArrivals(t, arrived, 2)
	g.Tick(t0.Add(10 * time.Second))

	got := logs.lines(t, "summary")
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}
	want := logLine{Msg: "summary", WindowS: 10, Sent: 2, Completed: map[string]int64{},
		Dropped: 3, InFlight: 2, Multiplier: 1, TargetRPS: 5}
	if !equalLines(got[0], want) {
		t.Errorf("summary = %+v\nwant %+v", got[0], want)
	}

	// Cancelling the held requests turns them into transport errors, which
	// the next summary reports with a sample error.
	g.stopSends()
	g.inFlight.Wait()
	g.Tick(t0.Add(20 * time.Second))
	got = logs.lines(t, "summary")
	if len(got) != 2 || got[1].TransportErrors != 2 || got[1].LastError == "" {
		t.Errorf("second summary = %+v, want 2 transport errors with a last_error", got[len(got)-1])
	}
}

func equalLines(a, b logLine) bool {
	return a.Msg == b.Msg && a.WindowS == b.WindowS && a.Sent == b.Sent &&
		maps.Equal(a.Completed, b.Completed) && a.TransportErrors == b.TransportErrors &&
		a.Dropped == b.Dropped && a.InFlight == b.InFlight && a.Multiplier == b.Multiplier &&
		a.TargetRPS == b.TargetRPS && a.LastError == b.LastError
}

func TestCountsSub(t *testing.T) {
	tests := []struct {
		name      string
		now, prev Counts
		want      Counts
	}{
		{
			name: "from nothing",
			now:  Counts{Sent: 3, Completed: map[int]int64{201: 2}, Errors: 1},
			want: Counts{Sent: 3, Completed: map[int]int64{201: 2}, Errors: 1},
		},
		{
			name: "only the window",
			now:  Counts{Sent: 10, Completed: map[int]int64{201: 7, 502: 2}, Errors: 1, Dropped: 4},
			prev: Counts{Sent: 6, Completed: map[int]int64{201: 5, 502: 2}, Errors: 1, Dropped: 1},
			want: Counts{Sent: 4, Completed: map[int]int64{201: 2}, Dropped: 3},
		},
		{
			name: "nothing new",
			now:  Counts{Sent: 2, Completed: map[int]int64{201: 2}},
			prev: Counts{Sent: 2, Completed: map[int]int64{201: 2}},
			want: Counts{Completed: map[int]int64{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.now.Sub(tt.prev)
			if got.Sent != tt.want.Sent || got.Errors != tt.want.Errors || got.Dropped != tt.want.Dropped ||
				!maps.Equal(got.Completed, tt.want.Completed) || got.Completed == nil {
				t.Errorf("Sub = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// runInBackground starts g.Run and returns its ticks and its result.
func runInBackground(ctx context.Context, g *Generator) (chan<- time.Time, <-chan Counts) {
	ticks := make(chan time.Time)
	result := make(chan Counts, 1)
	go func() { result <- g.Run(ctx, ticks) }()
	return ticks, result
}

func waitResult(t *testing.T, result <-chan Counts) Counts {
	t.Helper()
	select {
	case c := <-result:
		return c
	case <-time.After(failsafe):
		t.Fatal("Run did not return")
		return Counts{}
	}
}

func TestRunWaitsForRequestsInFlight(t *testing.T) {
	srv, arrived, release := blockingServer(t)
	cfg := testConfig(srv.URL)
	cfg.BaseRPS = 2
	g, logs := newTestGenerator(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks, result := runInBackground(ctx, g)
	ticks <- t0
	ticks <- t0.Add(time.Second) // 2 requests, held by the server
	waitArrivals(t, arrived, 2)

	cancel()
	select {
	case <-result:
		t.Fatal("Run returned while requests were still in flight")
	default:
	}
	release()

	total := waitResult(t, result)
	if total.Sent != 2 || total.Completed[http.StatusCreated] != 2 || total.Errors != 0 {
		t.Errorf("totals = %+v, want 2 sent and 2 completed", total)
	}
	stopped := logs.lines(t, "stopped")
	if len(stopped) != 1 || stopped[0].Sent != 2 || stopped[0].Completed["201"] != 2 {
		t.Errorf("stopped log = %+v, want one line with the totals", stopped)
	}
}

func TestRunCancelsRequestsAfterTheGrace(t *testing.T) {
	srv, arrived, _ := blockingServer(t) // never answers
	cfg := testConfig(srv.URL)
	cfg.BaseRPS, cfg.Grace = 2, 10*time.Millisecond
	g, logs := newTestGenerator(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	ticks, result := runInBackground(ctx, g)
	ticks <- t0
	ticks <- t0.Add(time.Second)
	waitArrivals(t, arrived, 2)
	cancel()

	total := waitResult(t, result)
	if total.Sent != 2 || total.Errors != 2 || len(total.Completed) != 0 {
		t.Errorf("totals = %+v, want 2 sent, both cancelled as transport errors", total)
	}
	if !errors.Is(g.sendCtx.Err(), context.Canceled) {
		t.Errorf("requests' context: %v, want cancelled", g.sendCtx.Err())
	}
	if n := len(logs.lines(t, "shutdown: cancelling requests still in flight")); n != 1 {
		t.Errorf("logged %d cancellation warnings, want 1", n)
	}
}
